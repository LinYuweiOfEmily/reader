# Windows ZCode integration

**Goal:** Add the installed Z.ai ZCode harness as a Windows Reader provider, alongside Codex/Kimi, preserving the previous Codex fix.

**Approved scope:** User explicitly requested implementing the Codex/Kimi integration pattern against E:/Program Files/ZCode. Reuse ZCode's configured account; never read credential files. Keep development and build assets on E:.

**Architecture:** Discover ZCode Desktop's bundled resources/glm/zcode.cjs and run it with the desktop Electron executable in Node mode. Use its official NDJSON app-server session API, isolated work directories, empty tool allowlist, no MCP servers, and denied unsolicited host requests. Forward text deltas only, require a successful completion event and matching final response, and stop the session on cancellation. Model discovery uses the protocol's settings snapshot. Add provider identity/types/settings and release notes; preserve existing SSE semantics.

**Alternatives:** Direct GLM API bypasses ZCode's harness and is already supported by API Agent. Headless prompt mode has a simpler output but weaker per-session model/tool control. Prefer app-server after verifying the installed 0.16.9 protocol.

**Tech:** Go provider adapter and Windows installation discovery, React settings, existing tests and local Windows packaging cache.

- [x] Probe installed app-server: protocol, model list, account readiness, event format and images.
- [x] Add failing protocol/discovery tests (invalid/truncated output, foreign session, failed/cancelled completion, matching response).
- [x] Implement Windows discovery/startup and Go adapter/model catalog/provider detection.
- [x] Extend shared/OpenAPI types, provider settings and UI identity.
- [x] Run scoped tests, typecheck, full test attempt and build; document remaining unrelated failures.
- [x] Validate real ZCode text/image calls and cancellation without touching user documents.
- [x] Review coherent diffs and commit; update release metadata and package Windows EXE using local caches. Do not push.

**Compatibility boundary:** The installed app exposes session/create, session/subscribe, session/send and session/stop. Official main is moving towards a v4 transport; fail clearly if these methods disappear rather than interpreting incompatible messages as success. Login/account choices remain managed in ZCode.

## Verified findings and final scope

- Desktop free account produced an empty model catalog. Configuring an API Key in ZCode made GLM-5.3 and GLM-5.3-Flash available through the official app-server, without Reader reading credentials.
- Installed 0.16.9 streams `model.streaming` with `text_delta`; ignore reasoning and require a matching successful terminal response. Support `part.delta` as a separate stream source without duplicating both.
- Real Reader JSONL translation and bounded cancellation passed. Local-path and inline image probes did not pass the random color challenge. This intermediate text-only scope was superseded by the Flash investigation below.
- Added model identity encoding, discovery, provider settings/types, and protocol/replay/error tests. Type checking passed before release preparation.
- Full Go test attempt failed in existing diagnostic path handling and streaming/cancellation fixtures, timed out at 120 seconds. Targeted ZCode/generation/model tests passed. Full Vitest attempt and final package verification are recorded in local E:/agent_demo/reader/work logs.
- Release target: 0.8.6-zcode.1, retaining prior Codex fixes. No remote push.

### Vision root cause resolved

The prior text-only scope is superseded. The user requested continued investigation. Official GLM docs confirm GLM-5.3 is text-only whereas GLM-5.3-Flash is multimodal; ZCode's builtin metadata misleadingly marks both as vision-capable. A controlled image sent via the same native protocol to Flash returned every color correctly. Restore image attachments and add an independent image model selector/default, include it in capability fingerprints, and reject known text-only GLM-5.3 for image tasks. Final live regression includes random colors, not merely the model claiming it saw an image.

## Validation evidence

- Live Go adapter: Reader JSONL translation, randomized eight-color image recognition using Flash, and bounded cancellation passed.
- Compiled Reader HTTP API: ZCode installed, both models returned, `/api/ai/test/zcode` returned `text: true, vision: true`. A required-default-reasoning regression found here was fixed and unit-tested.
- Final typecheck and full application build completed. Windows targeted desktop suite: 48 passed, 6 skipped.
- Full Go suite remains failing in existing diagnostic path and stream/cancellation fixtures (120-second package timeout). Full Vitest reported an existing macOS-update fixture failure; focused renderer tests did not finish and were interrupted. Do not claim all tests passed.
- Installation packaging uses cached Electron/NSIS/processor assets on E:, with no downloads. Manual UI acceptance remains for the user; no browser automation used.

- Final packaging succeeded: bundled PDF.js/canvas/layout-model inference and Go/SQLite/authentication/CLI/web smoke checks passed. The packaged server hash matches the compiled server that passed the real ZCode HTTP capability check. Installer copied to E:/agent_demo/reader/release.
