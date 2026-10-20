# Windows Codex desktop discovery

**Goal:** Recognize the Codex CLI bundled with the Windows desktop app without changing the user's PATH.

**Architecture:** Keep existing agent search paths first. Append directories under LOCALAPPDATA/OpenAI/Codex/bin that contain a non-empty regular codex.exe, newest executable first. Use the home-based AppData/Local location when LOCALAPPDATA is absent. Missing/unreadable directories are optional and do not prevent Reader from starting. Do not read credentials or execute programs during discovery.

**Scope approved:** Codex discovery, regression tests, local Windows installer. Preserve existing CLI preference and non-Windows behavior. No remote push or release publication.

## Tasks

- [ ] Install pinned pnpm 10.30.3 and Go 1.26.5 under the E-drive workspace; install frozen dependencies.
- [x] Add real-filesystem Windows tests to agent-path.test.ts: desktop bundle discovery, newest-first order, empty/missing binaries, PATH precedence, missing LOCALAPPDATA fallback, nonexistent directory.
- [ ] Run `pnpm exec vitest run apps/desktop/main/agent-path.test.ts` and verify discovery assertions fail before implementation.
- [x] Add a small private discovery helper in `agent-path.ts` using asynchronous readdir/lstat; append results after existing entries only on Windows. Keep the helper with its sole caller rather than adding another module.
- [x] Verify real installed CLI lookup and `codex login status` with the generated PATH; never read credential files. Verified App Server initialize and model/list (7 models).
- [x] Set root/desktop version to 0.8.6-codex.1; copy accumulated 0.8.5 release notes and append the discovery fix.
- [ ] Run `pnpm typecheck`, `pnpm test`, `pnpm build`, `node scripts/release-source.mjs`; investigate failures before packaging.
- [ ] Review diff and commit explicit files. Run `pnpm package` and packaged-runtime smoke checks, then expose installer in the main workspace release directory.

## Verification boundary

File existence establishes discoverability, not login or service availability. Reader retains its existing login and capability checks. Desktop app updates may leave several bundles; order by executable mtime and deterministic path tie-breaker. Reject relative LOCALAPPDATA and do not traverse arbitrary subdirectories or symlink directories.

## Execution evidence

Before implementation, a Node assertion against agentPath using the real Windows installation failed because the Codex bundle was absent. After implementation, the same assertion passed and `where.exe codex` plus `codex --version` resolved the bundled executable. Full Vitest execution initially waits on dependency installation; package downloads have intermittent connection resets.
