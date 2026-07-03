# CC-010: Update subsystem overhaul (loop, race, lifecycle)

- **Phase:** P1
- **Priority:** P0
- **Size:** L
- **Status:** Backlog
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

- [ ] Rewrite `checkForUpdatesLoop` with `time.NewTicker` + `for { select { case <-quit: return; case <-ticker.C: ... } }`; no `return` on tick, no `defer` inside the loop (use explicit lock/unlock or a small helper func per tick).
- [ ] Make the map private (`newVersions`), guard it with a `sync.RWMutex`, and expose `GetNewVersions() structs.ImageMap` returning a **copy**. Update all readers: `app/update.go:19,23` and `Upgrade`/`UpgradeAll` internals.
- [ ] Simplify the locking story: document (in code) that `upgradeLock` serializes check/upgrade operations and the new RWMutex protects the map; remove `muCheck` if it becomes redundant after the loop rewrite.
- [ ] `wgDone.Add(1)` **before** `go checkForUpdatesLoop()` in `Init()` (`models/models.go:30-34`).
- [ ] Parse `general.update_time` inside `models.Init()` and **return the error** — `main.go:82-84` already propagates `models.Init()` errors, so no `main.go` change is needed. Remove the `Fatalf` from the goroutine.
- [ ] Propagate the `apt-get update` error from `checkForUpdates` (`:63`): the check should record/return failure (surfaced by `GET /update/check`) instead of silently reporting a possibly-stale cache.
- [ ] Replace the linear `found` scan in `Upgrade` with a map lookup.

## Out of scope

- Do NOT change `apt.GetCachePackages()`'s signature or the cgo binding — owned by CC-020 (it will adjust the single call site at `models/update.go:66` afterwards).
- Do NOT touch response envelopes / HTTP status codes (e.g. 409 for "upgrade in progress") — owned by CC-040.
- Do NOT touch `models/command.go` — owned by CC-021.
- `LoadFromDisk` is already gone (CC-002).

## Breaking changes

None at the HTTP level (same routes, same JSON shapes). `models.NewVersions` disappears as an exported symbol — in-repo callers only.

## Acceptance criteria

- [ ] With `update_time = "5s"` in a test config, debug logs show the check firing repeatedly (≥3 ticks).
- [ ] `go build ./...` green; `go vet` green; no exported mutable package-level map remains in `models`.
- [ ] Hammering `GET /update/available` in a loop while a check runs (e.g. `for i in $(seq 200); do curl -s ...& done`) does not crash the daemon (was: concurrent-map panic).
- [ ] Daemon started with an invalid `update_time` exits at startup with a clear error (no goroutine `Fatalf`).
- [ ] `kill -TERM` still shuts down cleanly (WaitGroup pairs balanced).

## Verification notes

_(filled by the implementing agent)_
