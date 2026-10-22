# CC-050: Update subsystem tests

> **Absorbed by ADM-CC3** (admin plan): the cases below become part of that ticket's test checklist, written against the new behaviour. Not scheduled on its own.

- **Phase:** P5
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-010, CC-020, CC-021 (needs the ticker loop, the RWMutex accessor, the apt error return, and the `Executor` seam)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `models/update_test.go` (new)
  - `models/models_test.go` (new)

## Context

The update subsystem was the highest-risk area (loop-runs-once, `NewVersions` data race, apt errors swallowed). CC-010/020/021 fixed the behavior and introduced the seams (private map + accessor, `Executor` interface, `GetCachePackages() ([]*Pkg, error)`). This ticket locks that behavior in with tests, including a race-detector run.

## Work items

- [ ] Using the `Executor` seam (CC-021), fake `apt-get update`/`install`/`dist-upgrade` and the apt cache so tests need no real apt.
- [ ] Test the ticker loop fires repeatedly (inject a short interval; assert ≥N checks) and exits cleanly on the quit channel.
- [ ] Test concurrent readers of the versions accessor against a running check under `-race` (the old concurrent-map panic must not reproduce).
- [ ] Test apt error propagation: a faked locked-cache error surfaces as a check failure, not "up to date".
- [ ] Test `Init`/`Shutdown` WaitGroup balance (no negative-counter panic; clean shutdown).
- [ ] Test `Upgrade`/`UpgradeAll` with a package present vs absent in the versions map.

## Out of scope

- Changing production code — if a test reveals a bug, file a follow-up; only test files are owned here.
- HTTP-layer tests — CC-051/052/055.

## Acceptance criteria

- [ ] `go test -race ./models/ -run 'Update|Models'` passes.
- [ ] Tests fail if the loop-returns-once regression is reintroduced (guard the fix).
- [ ] Coverage exercises the error-propagation path.

## Verification notes

_(filled by the implementing agent)_
