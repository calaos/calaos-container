# CC-021: Command execution rewrite + mockable executor

- **Phase:** P2
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-013 (stabilizes `app/system.go`)
- **Blocks:** CC-054
- **Files owned (exclusive for this phase):**
  - `models/command.go`
  - `models/executor.go` (new)
  - `app/system.go` (the single `RunCommandReader` call site only, ~`:138`)

## Context

1. **Install stream hangs the subprocess on client disconnect.** `RunCommandReader` (`models/command.go:55-89`) returns an `io.Pipe` reader handed to `c.SendStream(r)` (`app/system.go:146`). `io.Pipe` is synchronous: if the HTTP client disconnects and stops reading, `pipeWriter.Write` blocks, which blocks `cmd.Stdout`, which hangs the `calaos_install` subprocess **and** the goroutine indefinitely. There is no context/cancellation.
2. **Latent nil-deref.** `log.Close()` (`models/command.go:70`) runs unconditionally in the goroutine, but `log` is only assigned when `logfile != ""` (`:46-53`). An empty logfile panics the goroutine.
3. **Write to empty path.** `os.WriteFile(exitfile, ...)` when `exitfile == ""` (`:81`) writes to path `""` and only `fmt.Printf`s the error.
4. **No test seam.** All exec goes through hardcoded `exec.Command`; there is no interface to fake subprocesses, blocking every unit test of `models`.

## Work items

- [ ] Introduce an `Executor` interface in `models/executor.go` (e.g. `Run(ctx, name, args...) (string, error)` and a streaming variant) with a package-level default implementation and a swap point for tests. **Preserve the existing `RunCommand` signature** so the many call sites (`models/snapper.go`, `models/network.go`, `models/update.go`, `app/system.go`) do not change — `RunCommand` delegates to the default executor.
- [ ] Redesign the install streaming path so the subprocess is **decoupled from the HTTP client**:
  - The subprocess writes its output to the log file (`/run/calaos/calaos_install.log`) independently; the exit code is written to `/run/calaos/calaos_install.code` when it finishes, regardless of client state.
  - The HTTP handler streams by tailing the log file bounded by the request `Context()` (`c.Context()` / `c.UserContext()`); a client disconnect cancels the tail only — it must never block or kill the install.
- [ ] Plumb a `context.Context` for cancellation where appropriate; guard the `logfile == ""` and `exitfile == ""` cases so no nil-deref / empty-path write occurs.
- [ ] Update the single `RunCommandReader` call site in `app/system.go:138` to the new streaming API.

## Out of scope

- Device validation / confirm flag — already done in CC-013 (do not re-touch that logic).
- Response envelope / status codes / "never run" state — CC-040.
- Adding the actual tests — CC-054 (this ticket only provides the seam and the non-blocking behavior).

## Breaking changes

None at the HTTP contract level (same endpoint, still streams install output). Internal `models` API gains an `Executor` seam; `RunCommandReader` is replaced by the new tailing mechanism.

## Acceptance criteria

- [ ] Start an install, disconnect the client mid-stream (`curl ... | head -c 100`): the `calaos_install` process **runs to completion** and `/run/calaos/calaos_install.code` is written (was: subprocess hung).
- [ ] No goroutine leak after disconnect (spot-check with a `runtime.NumGoroutine()` log or `pprof`; formal test in CC-054).
- [ ] `RunCommand` callers compile unchanged.
- [ ] The default executor is swappable for a fake in a `models` test (proven by CC-054 later).
- [ ] `go vet` green.

## Verification notes

_(filled by the implementing agent)_
