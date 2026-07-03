# CC-052: Auth middleware tests

- **Phase:** P5
- **Priority:** P1
- **Size:** S
- **Status:** Backlog
- **Depends on:** CC-011 (hardened middleware), CC-040 (final 401 envelope)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `app/context_test.go` (new)

## Context

CC-011 made the token middleware fail-closed, constant-time, mutex-guarded, and rotation-aware. Auth is the only gate in front of destructive endpoints, so it needs regression tests — most importantly the empty-file-bypass fix.

## Work items

- [ ] Point the middleware at a temp token file (parameterize the path or inject it) and cover:
  - Missing file → all requests 401.
  - Empty / whitespace-only file → request **without** Authorization header → 401 (the critical bypass fix).
  - Correct token → 200 (via a trivial protected route on a test Fiber app using `app.Test`).
  - Wrong token → 401.
  - Rotation: write a new token to the file → the next request with the new token succeeds without restarting.
- [ ] Concurrency: hit the protected route from many goroutines on first load under `-race` (guards the old unsynchronized-cache race).
- [ ] Assert the 401 body matches the CC-040 envelope and leaks no OS error text.

## Out of scope

- Production code — test file only.
- CLI token handling — CC-055.

## Acceptance criteria

- [ ] `go test -race ./app/ -run 'Token|Auth'` passes.
- [ ] The empty-file-bypass case is explicitly asserted (request with no header → 401).
- [ ] Rotation test passes without a restart.

## Verification notes

_(filled by the implementing agent)_
