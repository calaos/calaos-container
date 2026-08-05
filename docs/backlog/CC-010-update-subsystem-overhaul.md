# CC-010: Update subsystem overhaul (loop, race, lifecycle)

- **Phase:** P1
- **Priority:** P0
- **Size:** L
- **Status:** Done
- **Depends on:** CC-002
- **Blocks:** CC-020, CC-050
- **Files owned (exclusive for this phase):**
  - `models/update.go`
  - `models/models.go`
  - `app/update.go`

## Context

The update subsystem carries the highest-severity runtime bugs in the daemon:

1. **The periodic check runs at most once.** `checkForUpdatesLoop` (`models/update.go:15-39`) ends its `case <-time.After(updateTime):` branch with an unconditional `return` (`:36`), so the goroutine exits permanently after the first tick. The advertised "every 12h" check never repeats. Additionally `defer muCheck.Unlock()` (`:32`) is function-scoped inside a loop — it would never release per-iteration if the loop actually looped.
2. **Data race / crash on `NewVersions`.** The global map (`models/models.go:22`) is written by `checkForUpdates` (`models/update.go:58,70`) and read with **no lock** by `apiUpdateAvail`/`apiUpdateCheck` (`app/update.go:19,23` — JSON-marshalled directly) and by `Upgrade`/`UpgradeAll` (`models/update.go:113,127,157,174`). A `GET /update/available` concurrent with a check is a concurrent map read/write → Go runtime panic. `upgradeLock` does not cover the HTTP read paths.
3. **`Fatalf` from a goroutine.** A missing/malformed `general.update_time` (no default exists — see CC-014) makes `time.ParseDuration` fail and `logging.Fatalf` (`models/update.go:19-22`) calls `os.Exit(1)` from the goroutine, bypassing graceful shutdown. The `return` after `Fatalf` is unreachable.
4. **WaitGroup ordering race.** `models.Init` (`models/models.go:30-34`) does `wgDone.Add(1)` **after** `go checkForUpdatesLoop()`; the goroutine's `defer wgDone.Done()` can theoretically fire first → negative-counter panic.
5. **Ignored apt failure.** `out, _ := RunCommand("apt-get", "update", "-qq")` (`models/update.go:63`) — network-down or repo errors are silently ignored and the stale cache is presented as the truth.
6. Minor: `Upgrade` (`:111-125`) linear-scans the map to build a `found` bool instead of `if _, ok := NewVersions[pkg]; ok`; the two-lock scheme (`muCheck` mutex + `upgradeLock` semaphore) is asymmetric and easy to misuse.

## Work items

- [x] Rewrite `checkForUpdatesLoop` with `time.NewTicker` + `for { select { case <-quit: return; case <-ticker.C: ... } }`; no `return` on tick, no `defer` inside the loop (use explicit lock/unlock or a small helper func per tick).
- [x] Make the map private (`newVersions`), guard it with a `sync.RWMutex`, and expose `GetNewVersions() structs.ImageMap` returning a **copy**. Update all readers: `app/update.go:19,23` and `Upgrade`/`UpgradeAll` internals.
- [x] Simplify the locking story: document (in code) that `upgradeLock` serializes check/upgrade operations and the new RWMutex protects the map; remove `muCheck` if it becomes redundant after the loop rewrite.
- [x] `wgDone.Add(1)` **before** `go checkForUpdatesLoop()` in `Init()` (`models/models.go:30-34`).
- [x] Parse `general.update_time` inside `models.Init()` and **return the error** — `main.go:82-84` already propagates `models.Init()` errors, so no `main.go` change is needed. Remove the `Fatalf` from the goroutine.
- [x] Propagate the `apt-get update` error from `checkForUpdates` (`:63`): the check should record/return failure (surfaced by `GET /update/check`) instead of silently reporting a possibly-stale cache.
- [x] Replace the linear `found` scan in `Upgrade` with a map lookup.

## Out of scope

- Do NOT change `apt.GetCachePackages()`'s signature or the cgo binding — owned by CC-020 (it will adjust the single call site at `models/update.go:66` afterwards).
- Do NOT touch response envelopes / HTTP status codes (e.g. 409 for "upgrade in progress") — owned by CC-040.
- Do NOT touch `models/command.go` — owned by CC-021.
- `LoadFromDisk` is already gone (CC-002).

## Breaking changes

None at the HTTP level (same routes, same JSON shapes). `models.NewVersions` disappears as an exported symbol — in-repo callers only.

## Acceptance criteria

- [x] With `update_time = "5s"` in a test config, debug logs show the check firing repeatedly (≥3 ticks).
- [x] `go build ./...` green; `go vet` green; no exported mutable package-level map remains in `models`.
- [x] Hammering `GET /update/available` in a loop while a check runs (e.g. `for i in $(seq 200); do curl -s ...& done`) does not crash the daemon (was: concurrent-map panic).
- [x] Daemon started with an invalid `update_time` exits at startup with a clear error (no goroutine `Fatalf`).
- [x] `kill -TERM` still shuts down cleanly (WaitGroup pairs balanced).

## Verification notes

### Audit vs. the described state

The line references in the Context section predate the apt-progress commit
(`f3f51c5`) and CC-002 (`8a50c87`). Re-audited against the current tree, all 6
bugs were still present; two had moved:

| # | Bug | Status at audit |
| - | --- | --- |
| 1 | One-shot check loop (`return` on tick, `defer` in loop) | Still present, same place |
| 2 | Unguarded `NewVersions` | Still present. Writers moved into the new `refreshNewVersions()` helper added by the apt-progress commit. Readers: `app/update.go:18,22` and `Upgrade:252,266`. `UpgradeAll` no longer reads the map at all (apt-progress rewrote it to call `apt.UpgradeAll`), so the ticket's `:157,:174` reader refs are stale |
| 3 | `Fatalf` from the goroutine | Still present |
| 4 | `wgDone.Add(1)` after `go` | Still present |
| 5 | Ignored `apt-get update` error | Still present, moved `:63` → inside `refreshNewVersions()` |
| 6 | Linear scan in `Upgrade` | Still present, moved `:111-125` → `:250-266` |

New shared state introduced by the apt-progress work was reviewed: `upgradeStatus`
(`models/status.go`) is already correctly guarded — `SafeStatus` holds its own
mutex, deep-copies via `cloneStatus` on both read and publish, and publishes
outside the lock. Its readers (`app/events.go:35` WS snapshot, `app/update.go:68`)
need no change, so it was left alone.

### Locking design

- `upgradeLock` (semaphore, cap 1) serializes the long operations —
  `checkForUpdates`, `Upgrade`, `UpgradeAll` — with `TryAcquire`, so a second
  caller is rejected rather than queued.
- `newVersionsMu` (RWMutex) protects the `newVersions` map **only**, and is
  never held across an `apt-get` run, a cgo call or an `upgradeLock` acquisition:
  `refreshNewVersions` builds the new map into a local and swaps it in at the
  end, so readers never see a half-rebuilt map.
- Lock ordering is one-way (`upgradeLock` → `newVersionsMu`), so the two cannot
  deadlock.
- `muCheck` was removed: it only serialized `CheckUpdates`, which `upgradeLock`
  already covers. Consequence: two concurrent `GET /update/check` no longer
  block, the second gets the existing "upgrade already in progress" error —
  same JSON shape and status code as today's concurrent-upgrade path.
- `lifecycleMu` + a nil'd-out `quitCheckUpdate` make `Shutdown()` idempotent and
  safe after a failed `Init()`; the loop goroutine receives its interval and
  quit channel as parameters, so it never reads mutable package state.

`apt-get update` failures are now returned (wrapped) by `refreshNewVersions`,
but the map is still rebuilt from the local apt cache — the failure is surfaced
to `GET /update/check` (500, unchanged envelope) instead of being silent, while
`/update/available` keeps serving the best-known local state.

### Commands run

Host `bin/` and `apt/build` were left root-owned by an earlier container run, so
build/test/lint ran in `ghcr.io/calaos/debian_deb_build_base:latest` with the
repo bind-mounted and `apt-get build-dep -y .` (same as CI); artifacts were
chowned back to the host user afterwards.

- `make` — green (C++ apt lib + daemon + CLI).
- `make test` (`go test -race ./...`) — all packages ok.
- `CGO_LDFLAGS="-L./bin -lcalaos-apt" go vet ./...` — clean.
- `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` (v2.12.2) — 0 issues.
- `gofmt -l` clean on all touched files. (Pre-existing, untouched:
  `models/structs/network.go` is not gofmt-clean.)

### Acceptance criteria

- **≥3 ticks with `update_time = "5s"`**: 4 ticks in a ~35 s controlled run
  ("Periodic update check firing"); an earlier longer run logged 64 consecutive
  ticks with no drift or exit — the loop no longer stops after the first tick.
- **`go build`/`go vet` green, no exported mutable package-level map**: yes;
  `models` has no exported package-level vars left, `grep` for `models.NewVersions`
  and `muCheck` returns nothing.
- **Hammer `GET /update/available`**: 3 rounds of 200 concurrent authenticated
  curls (600 total) overlapping the 5 s checks → 600 × HTTP 200, daemon still
  running, zero `panic`/`concurrent map`/`DATA RACE` in the log.
- **Invalid `update_time`**: exits at startup, code 1, with
  `invalid general.update_time "not-a-duration": time: invalid duration "not-a-duration"`.
  (Note: a *missing* `update_time` also fails startup — same as before this
  change, where the goroutine `Fatalf`'d — until CC-014 adds the `12h` default.)
- **`kill -TERM`**: process exits cleanly, logs `Exit checkForUpdates goroutine`,
  no WaitGroup panic.

### Tests

No existing test referenced the renamed symbols, so nothing needed fixing.
Added a small focused regression file `models/update_test.go` (the comprehensive
suite stays with CC-050): `parseUpdateTime` table (valid/empty/malformed/zero/
negative), `GetNewVersions` returns a copy, a concurrent readers-vs-writers race
test for the map, and Init/Shutdown lifecycle tests (WaitGroup balance, double
`Shutdown`, `Shutdown` after a failed `Init`). All pass under `-race`.
