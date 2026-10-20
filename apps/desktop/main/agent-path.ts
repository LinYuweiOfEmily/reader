import { execFile } from "node:child_process";
import { lstat, readdir } from "node:fs/promises";
import { posix, win32 } from "node:path";

const marker = "__READER_AGENT_PATH__";
type Run = (
  file: string,
  args: string[],
  env: NodeJS.ProcessEnv,
) => Promise<string>;
const run: Run = (file, args, env) =>
  new Promise((resolve, reject) => {
    execFile(
      file,
      args,
      {
        env,
        timeout: args.includes("-ilc") ? 8000 : 3000,
        maxBuffer: 256 * 1024,
      },
      (error, stdout) => (error ? reject(error) : resolve(stdout)),
    );
  });

// Desktop updates leave versioned bundles beside other runtimes. Discover only
// non-empty CLI files, without executing candidates or reading account data.
async function windowsCodexPaths(env: NodeJS.ProcessEnv, home: string) {
  const local =
    env.LOCALAPPDATA && win32.isAbsolute(env.LOCALAPPDATA)
      ? env.LOCALAPPDATA
      : win32.join(home, "AppData", "Local");
  const root = win32.join(local, "OpenAI", "Codex", "bin");
  try {
    const bundles = await readdir(root, { withFileTypes: true });
    const candidates = await Promise.all(
      bundles
        .filter((entry) => entry.isDirectory())
        .map(async (entry) => {
          const directory = win32.join(root, entry.name);
          if (directory.includes(";")) return undefined;
          try {
            const executable = await lstat(win32.join(directory, "codex.exe"));
            return executable.isFile() && executable.size > 0
              ? { directory, modified: executable.mtimeMs }
              : undefined;
          } catch {
            return undefined;
          }
        }),
    );
    return candidates
      .filter((entry) => entry !== undefined)
      .sort(
        (a, b) =>
          b.modified - a.modified || a.directory.localeCompare(b.directory),
      )
      .map((entry) => entry.directory);
  } catch {
    // The desktop app is optional; a missing/unreadable install is harmless.
    return [];
  }
}

/** Shared by provider detection and every CLI child of the Go server. */
export async function agentPath(
  env: NodeJS.ProcessEnv,
  home: string,
  platform: NodeJS.Platform,
  execute: Run = run,
): Promise<string> {
  const windows = platform === "win32";
  const path = windows ? win32 : posix;
  const separator = windows ? ";" : ":";
  const inherited =
    Object.entries(env).find(([key]) => key.toUpperCase() === "PATH")?.[1] ??
    "";
  const entries = [
    path.join(home, ".local", "bin"),
    path.join(home, ".kimi-code", "bin"),
    ...inherited.split(separator),
  ];
  if (windows) {
    if (env.APPDATA) entries.push(path.join(env.APPDATA, "npm"));
  } else {
    // Finder launches do not inherit interactive shell initialization (e.g. nvm).
    try {
      const output = await execute(
        env.SHELL || "/bin/sh",
        ["-ilc", `printf '\\n${marker}%s\\n' "$PATH"`],
        env,
      );
      const shellPath = output
        .split(/\r?\n/)
        .find((line) => line.startsWith(marker))
        ?.slice(marker.length);
      if (shellPath) entries.splice(2, 0, ...shellPath.split(separator));
    } catch {
      // A broken or slow shell must not prevent Reader from starting.
    }
    entries.push("/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin");
  }
  entries.push(
    path.join(home, ".npm-global", "bin"),
    path.join(home, ".npm", "bin"),
  );
  const merge = () =>
    [...new Set(entries.filter((entry) => path.isAbsolute(entry)))].join(
      separator,
    );
  const explicitPrefix = env.NPM_CONFIG_PREFIX || env.npm_config_prefix;
  if (explicitPrefix && path.isAbsolute(explicitPrefix)) {
    entries.push(windows ? explicitPrefix : path.join(explicitPrefix, "bin"));
  }
  if (!windows) {
    try {
      // npm resolves custom prefixes itself; never read the user's npmrc/auth data.
      const prefix = (
        await execute("npm", ["prefix", "-g"], { ...env, PATH: merge() })
      ).trim();
      if (path.isAbsolute(prefix) && !prefix.includes("\n"))
        entries.push(path.join(prefix, "bin"));
    } catch {
      // npm need not be installed for native CLI distributions.
    }
  }
  if (windows) entries.push(...(await windowsCodexPaths(env, home)));
  return merge();
}
