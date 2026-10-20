# Windows Codex desktop discovery

**Goal:** Recognize the Codex CLI bundled with the Windows desktop app without changing the user's PATH.

**Architecture:** Keep existing agent search paths first. Append directories under LOCALAPPDATA/OpenAI/Codex/bin that contain a non-empty regular codex.exe, newest executable first. Use the home-based AppData/Local location when LOCALAPPDATA is absent. Missing/unreadable directories are optional and do not prevent Reader from starting. Do not read credentials or execute programs during discovery.

**Scope approved:** Codex discovery, regression tests, local Windows installer. Preserve existing CLI preference and non-Windows behavior. No remote push or release publication.

## Tasks

- [x] Install pinned pnpm 10.30.3 and Go 1.26.5 under the E-drive workspace; install frozen dependencies.
- [x] Add real-filesystem Windows tests to agent-path.test.ts: desktop bundle discovery, newest-first order, empty/missing binaries, PATH precedence, missing LOCALAPPDATA fallback, nonexistent directory.
- [x] Verify discovery fails before implementation using a direct Node assertion against the installed Windows bundle; then run the Vitest regressions after dependencies are installed.
- [x] Add a small private discovery helper in `agent-path.ts` using asynchronous readdir/lstat; append results after existing entries only on Windows. Keep the helper with its sole caller rather than adding another module.
- [x] Verify real installed CLI lookup and `codex login status` with the generated PATH; never read credential files. Verified App Server initialize and model/list (7 models).
- [x] Set root/desktop version to 0.8.6-codex.1; copy accumulated 0.8.5 release notes and append the discovery fix.
- [x] Run typecheck, full Go/frontend test attempts, build and release metadata validation; record full-suite failures separately from passing scoped checks.
- [x] Review diff and commit explicit files. Run `pnpm package` and packaged-runtime smoke checks, then expose installer in the main workspace release directory.

## Verification boundary

File existence establishes discoverability, not login or service availability. Reader retains its existing login and capability checks. Desktop app updates may leave several bundles; order by executable mtime and deterministic path tie-breaker. Reject relative LOCALAPPDATA and do not traverse arbitrary subdirectories or symlink directories.

## Execution evidence

Before implementation, a Node assertion against agentPath using the real Windows installation failed because the Codex bundle was absent. After implementation, the same assertion passed and `where.exe codex` plus `codex --version` resolved the bundled executable. Full Vitest execution initially waits on dependency installation; package downloads have intermittent connection resets.

Typecheck, full production build and release metadata validation passed. Scoped Windows/provider tests passed (40 tests, 6 platform skips). After adapting Python invocation and symlink fixtures for Windows, the expanded suite passed (48 tests, 6 skips). The packaged backend reports Codex installed and authenticated. Isolated packaged PDF.js/canvas/model inference and Go/SQLite/authentication/CLI/web smoke checks passed.

Full suites are not green: the initial frontend run encountered macOS path assumptions and UI/bootstrap timeouts; Go diagnostics had a platform expectation failure and reader cancellation/streaming tests failed or timed out. The Python and privileged symlink fixture failures were subsequently fixed and their tests rerun successfully. Do not infer full-suite success from the scoped build gate.

Network transport failures affected npm, Go and installer-tool downloads, independently of the Codex PATH discovery bug. Official Electron/NSIS archives were downloaded and SHA256 verified. `scripts/build-windows.ps1` now reuses a prepared local cache and `docs/windows-local-build.md` documents repeat builds and cache upgrade boundaries.

The complete Windows cache-based rebuild finished with exit code 0: frozen offline install, typecheck, 48 passing scoped tests, full build, NSIS packaging, and isolated runtime smoke. Processor deployment reused all 20 packages with zero downloads; Electron/NSIS/7-Zip were explicit local inputs. The rebuild log is retained at `E:/agent_demo/reader/work/windows-rebuild.log`.
