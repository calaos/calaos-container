# CC-021: Command execution rewrite + mockable executor

- **Phase:** P2
- **Priority:** P1
- **Size:** M
- **Status:** Done
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

- [x] Introduce an `Executor` interface in `models/executor.go` (e.g. `Run(ctx, name, args...) (string, error)` and a streaming variant) with a package-level default implementation and a swap point for tests. **Preserve the existing `RunCommand` signature** so the many call sites (`models/snapper.go`, `models/network.go`, `models/update.go`, `app/system.go`) do not change — `RunCommand` delegates to the default executor.
- [x] Redesign the install streaming path so the subprocess is **decoupled from the HTTP client**:
  - The subprocess writes its output to the log file (`/run/calaos/calaos_install.log`) independently; the exit code is written to `/run/calaos/calaos_install.code` when it finishes, regardless of client state.
  - The HTTP handler streams by tailing the log file bounded by the request `Context()` (`c.Context()` / `c.UserContext()`); a client disconnect cancels the tail only — it must never block or kill the install.
- [x] Plumb a `context.Context` for cancellation where appropriate; guard the `logfile == ""` and `exitfile == ""` cases so no nil-deref / empty-path write occurs.
- [x] Update the single `RunCommandReader` call site in `app/system.go:138` to the new streaming API.

## Out of scope

- Device validation / confirm flag — already done in CC-013 (do not re-touch that logic).
- Response envelope / status codes / "never run" state — CC-040.
- Adding the actual tests — CC-054 (this ticket only provides the seam and the non-blocking behavior).

## Breaking changes

None at the HTTP contract level (same endpoint, still streams install output). Internal `models` API gains an `Executor` seam; `RunCommandReader` is replaced by the new tailing mechanism.

## Acceptance criteria

- [x] Start an install, disconnect the client mid-stream (`curl ... | head -c 100`): the `calaos_install` process **runs to completion** and `/run/calaos/calaos_install.code` is written (was: subprocess hung).
- [x] No goroutine leak after disconnect (spot-check with a `runtime.NumGoroutine()` log or `pprof`; formal test in CC-054).
- [x] `RunCommand` callers compile unchanged.
- [x] The default executor is swappable for a fake in a `models` test (proven by CC-054 later).
- [x] `go vet` green.

## Verification notes

### What the new API looks like

`models/executor.go` (new) holds the seam:

- `Executor` interface — `Run(ctx, name, args...) (string, error)` for short synchronous
  commands, `StartLogged(spec LoggedCommand) (Process, error)` for long-running ones.
  `StartLogged` deliberately takes **no** context: tying an install's lifetime to a caller
  is exactly the bug being fixed.
- `OSExecutor` is the default implementation; `CurrentExecutor()` / `SetExecutor(e) (restore func())`
  are the swap point (`defer SetExecutor(fake)()` in tests).
- `Process` = `Pid() int`, `Done() <-chan struct{}`, `ExitCode() (code int, ok bool)`; `ok` is
  false while running, so a signal-killed `-1` is never confused with "still running".

`models/command.go` keeps the public helpers: `RunCommand` (unchanged signature, delegates),
the new `RunCommandContext`, `StartLoggedCommand`, and `TailFile(ctx, path, w, TailOptions{Until, PollInterval})`.
`RunCommandReader` and its `io.Pipe` are gone — with them the nil-`log.Close()` deref, the
`os.WriteFile("")` empty-path write and the `fmt.Printf` error reporting.

### File contract (why the client can no longer hurt the install)

`StartLogged` opens the log file (0600, truncated, parent dir created) and hands the **file
descriptor itself** to the child as stdout+stderr, then closes the parent's copy right after
`Start`. The daemon is not in the data path at all: there is no pipe to fill and no reader to
stall, so nothing a client does can throttle or hang the installer. Stdin is `/dev/null`, and
`SysProcAttr{Setsid: true}` puts the child in its own session so it does not share the daemon's
signal fate. A reaper goroutine `Wait`s, writes the exit status **atomically** (temp file +
rename in the same dir) and only then closes `Done()`, so a reader woken by `Done()` always
finds the file. A stale `.code` file is removed at start, so "file present" means "this run
finished".

### Context semantics

- `Run` is bounded by its context, plus `WaitDelay = 2s`. Without that, cancellation was
  ineffective in practice: killing `sh -c "sleep 10"` leaves `sleep` holding the stdout pipe,
  and `cmd.Run` blocked for the full 10s (observed, then fixed — see the cancellation test).
- `TailFile` stops on: EOF + `Until` closed (returns nil, after draining, so the last lines
  written before the command exited are never lost), ctx cancellation, or a **write error** —
  which is the disconnect signal that actually matters. Verified in the fasthttp source:
  `RequestCtx.Done()` is closed on *server shutdown only* (`ctx.s.done`), never on client
  disconnect, and `SetBodyStreamWriter` runs the writer on a `PipeConns` whose writes fail once
  the peer closes. The handler therefore captures `c.Context().Done()` (a plain channel owned by
  the server, safe to retain past the handler, unlike the pooled `RequestCtx`) and converts it
  to a context, so shutdown ends the stream too.
- `TailFile` flushes after every chunk when the writer supports it, so output reaches the client
  as it is produced.

### Verified in `ghcr.io/calaos/debian_deb_build_base:latest`

`go build ./...`, `go vet ./...`, `golangci-lint v2.12.2` (**0 issues**), `make test` (`-race`)
all green. The new `models/executor_test.go` (8 tests, stub shell script — the real
`calaos_install` was never invoked) covers the file contract, stale-code clearing, tail-follows,
ctx cancellation, disconnect-does-not-kill, replay, the executor seam and `Run` cancellation.

Runtime, daemon on port 8021 with `calaos_install` **and** `lsblk` shadowed by stubs (canned
inventory, so no real device could ever be named):

- `curl -sN -X POST .../system/install/start | head -c 100` → client gone at `14:08:34`; the
  stub kept running unattended and logged through `14:08:42`, `/run/calaos/calaos_install.code`
  then contained `0`. Before the rewrite this hung the subprocess.
- The installer runs in its own session: `pid=25141 sid=25141 pgrp=25141` vs daemon `pid/sid=25029`.
- Re-tail works both mid-run (the log keeps growing with nobody attached) and after completion
  (`GET /system/install/status` returns the full log + `exit_code: 0`).
- Connected client: line-by-line delivery one second apart (per-chunk flush), stream closes when
  the install ends, `curl rc=0`, `Content-Type: text/plain; charset=utf-8`, chunked.
- No leak: daemon fds back to exactly 6 after 5 disconnect cycles, and no zombie child (the
  zombies in the container belong to its PID 1, not the daemon).
- CC-013 validation untouched and still enforced: `/dev/fakeroot` (hosts `/`) rejected with 400.

### Notes / follow-ups

- The `.golangci.yml` exclusions for `models/command.go` (errcheck `pipeWriter|log.Close`,
  gosec `G104/G204/G304/G306`) are now dead: linting with a copy of the config that drops those
  two blocks reports 0 issues. Left in place for the central cleanup at merge, as instructed.
  The two remaining `#nosec` annotations in the new code (G204 on exec, G304 on opening the
  daemon-constant log path) are justified in comments.
- Concurrent installs still overwrite the same log file, and a daemon restart mid-install loses
  the reaper (no `.code` file written). Both belong to the "already running / never run" state
  handling owned by **CC-040**, not here.
