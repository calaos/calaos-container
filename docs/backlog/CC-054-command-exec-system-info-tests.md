# CC-054: Command exec & system info tests

- **Phase:** P5
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-021 (Executor seam + non-blocking streaming), CC-022 (CPU/sysinfo fixes)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `models/command_test.go` (new)
  - `models/system_test.go` (new)

## Context

CC-021 decoupled the install stream from the HTTP client (client disconnect must not hang the subprocess) and introduced the `Executor` seam; CC-022 fixed the CPU-usage math and the `/proc` parse error handling. Both need tests — the disconnect-no-hang behavior especially, since it was a real goroutine/process leak.

## Work items

- [ ] `command_test.go`:
  - Using the `Executor` seam, verify `RunCommand` delegates and surfaces exit codes/errors.
  - Streaming: simulate a consumer that stops reading mid-stream and assert the subprocess still completes and the exit-code file is written; assert `runtime.NumGoroutine()` returns to baseline (no leak) after completion.
  - Guard the `logfile == ""` / `exitfile == ""` edge cases (no nil-deref, no write to empty path).
- [ ] `system_test.go`:
  - Feed fixture `/proc/stat` / `/proc/meminfo` content (via injected readers or temp files) and assert CPU% excludes idle (idle-heavy sample → low usage) and memory% is correct.
  - Malformed `/proc` line → returned error, not silent 0.
  - `GetSystemInfo` contract: full success or hard error, never partial-with-nil-error.

## Out of scope

- Production code — test files only. If the code lacks an injection point for `/proc` paths, note it; prefer testing the pure calculation helpers rather than forcing a refactor here.
- HTTP handlers — CC-040-covered elsewhere.

## Acceptance criteria

- [ ] `go test -race ./models/ -run 'Command|System'` passes.
- [ ] The disconnect-no-hang test fails if CC-021's decoupling is reverted.
- [ ] CPU calculation asserted against a known fixture.

## Verification notes

_(filled by the implementing agent)_
