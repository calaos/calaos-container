# CC-020: apt cgo error propagation & bounds fix

- **Phase:** P2
- **Priority:** P1
- **Size:** L
- **Status:** Done
- **Depends on:** CC-010 (stabilizes `models/update.go`)
- **Blocks:** CC-050
- **Files owned (exclusive for this phase):**
  - `apt/apt.cpp`
  - `apt/apt.h`
  - `apt/apt.go`
  - `apt/apt-cache-file.cpp`
  - `apt/apt-cache-file.h`
  - `models/update.go` (the single `apt.GetCachePackages()` call site only, ~`:66`)

## Context

The cgo binding to libapt swallows every error, so failures masquerade as success:

1. **No error channel.** Every failure path in `aptCacheGetArray` returns `nullptr` with a literal `//TODO: return error` (`apt/apt.cpp:39-42` cache lock/Open fail, `:52-59` CheckDeps, `:61-68` DistUpgrade). `aptInit` ignores `pkgInitConfig`/`pkgInitSystem` (`apt.cpp:17-25`). On the Go side, `nullptr` → `aptCacheArrayCount` returns 0 (`apt.cpp:100-103`) → `GetCachePackages` returns an empty slice (`apt/apt.go:19-36`) → `checkForUpdates` records **zero updates and returns nil** (`models/update.go:66-77`). So a locked apt cache, broken dpkg state, or a failed dist-upgrade computation all surface to the user as **"Already up to date."** The ABI has no error out-param (`apt.h:14-18`).
2. **Off-by-one → undefined behavior.** `aptCacheArrayGet` bounds check is `if (idx < 0 || (size_t)idx > plist->size())` (`apt.cpp:111`) — should be `>=`. At `idx == size()` it calls `plist->at(idx)` (`:114`) which throws `std::out_of_range`; a C++ exception crossing the cgo boundary is UB. Latent today (Go iterates `i < count`) but a landmine.
3. **Debug noise.** Unconditional `cout << ...` on every check (`apt.cpp:35,52,61,72,82`) writes to the daemon's stdout.
4. **Global-state hazards.** `AptCacheFile::Close` calls `_error->Discard()` (`apt-cache-file.cpp:26-33`), globally discarding apt errors and masking failures for anything else on the shared apt-pkg globals.

## Work items

- [x] Extend the C ABI (`apt.h`) so `aptCacheGetArray` (or a wrapper) reports failure — e.g. a `char** errmsg` out-parameter populated from `_error` via `strdup`, or a status enum. Free the string on the Go side.
- [x] Replace every `return nullptr //TODO` in `apt.cpp` with a path that sets the error message from libapt's `_error` stack before returning.
- [x] Restore `aptInit` error handling (`apt.cpp:17-25`) — do not silently ignore `pkgInitConfig`/`pkgInitSystem`.
- [x] Fix the bounds check to `>=` (`apt.cpp:111`).
- [x] Remove the unconditional `cout` debug lines (`apt.cpp:35,52,61,72,82`) — **already done before this ticket** by the apt-progress work; verified no `cout`/`iostream` use remains in `apt/` (the now-unused `#include <iostream>` in `apt-cache-file.cpp` was dropped).
- [x] Change the Go binding to `GetCachePackages() ([]*Pkg, error)` (`apt/apt.go`) and translate the C error into a Go error.
- [x] Update the single call site in `checkForUpdates` (`models/update.go:66`) so a cache error is surfaced (check reports failure) rather than reported as "no updates". Coordinate with the check-status field CC-010 introduced.

## Out of scope

- The update loop / locking / `NewVersions` — already done in CC-010; touch ONLY the `GetCachePackages` call line.
- The `libcalaos-apt.so` build wiring (`Makefile` `$(pwd)`/RPATH) — owned by CC-041. If the ABI change requires a rebuild, CC-041 already touches the Makefile in P4; note any new build flag needed in this ticket's verification notes for CC-041 to pick up.
- Removing the unused `glib-2.0` meson dep — CC-041.

## Breaking changes

Internal ABI only (`apt.h`); no Go public API beyond `GetCachePackages` gaining an error return (in-repo caller only). No HTTP contract change.

## Acceptance criteria

- [x] With the apt lists locked (`sudo apt-get update` running in parallel, or a held `/var/lib/apt/lists/lock`), `GET /update/check` reports an **error**, not "up to date".
- [x] Normal case still lists available upgrades correctly.
- [x] No `cout` output on the daemon's stdout during a check.
- [x] `apt/` compiles via `make -C apt`; `go build ./...` links against the rebuilt lib.
- [x] Bounds check is `>=`.

## Verification notes

### Design: how the error crosses the boundary

The ABI reuses the contract `aptUpgradeAll`/`aptInstallPackage` already had
(added by the apt-progress work, `apt-install.cpp`) instead of inventing a
status enum, so the whole C surface now reports failures the same way:

```c
int   aptInit(char **errorMsg);            /* was: void aptInit(void)   */
void *aptCacheGetArray(char **errorMsg);   /* was: void *aptCacheGetArray(void) */
```

`*errorMsg` is NULL on entry, and on failure only is set to a `strdup()`ed
message the caller frees. `apt.h` documents this as an ABI-wide convention.
A NULL return from `aptCacheGetArray` is now *always* an error: an empty
upgrade set is a valid non-NULL handle with count 0, so "no updates" and
"could not tell" are no longer the same value.

Ordering constraint that made this non-trivial: `~AptCacheFile()` calls
`Close()` which calls `_error->Discard()`, i.e. destroying the cache throws
away the very messages describing why it failed. Every failure path therefore
drains `_error` (`setError()`) *before* the cache goes out of scope; the cache
is now a stack object so RAII still releases it on every path, including the
thrown-exception one. The constraint is written down in `apt-cache-file.cpp`
next to the `Discard()` call.

`setError()` is deliberately a separate helper from `fail()` in
`apt-install.cpp`: `fail()` also emits an `"error"` progress event, which is
meaningless for a cache listing (not a transaction, no callback registered).
It also drains with `while (_error->empty() == false)` rather than
`while (_error->PopMessage(msg))`: **`PopMessage()` returns false for a
warning**, so the `fail()` idiom stops draining as soon as apt warns before it
fails. The lock-failure message captured below is 3 messages long and would
have been truncated to 1 by that idiom — worth fixing in `apt-install.cpp`
(not owned by this ticket, no behaviour change there today beyond shorter
messages).

C++ exceptions can no longer reach cgo: `aptInit` and `aptCacheGetArray` wrap
their whole body in `try/catch(...)` and convert to an error return, and
`aptCacheArrayGet` uses `operator[]` behind the (now correct) bounds check
instead of the throwing `at()`.

### Memory ownership

- `char **errorMsg`: C allocates with `strdup`, Go frees. Centralised in
  `cErrString()` (converts *and* frees, used by both `GetCachePackages` and
  `runTransaction`) and `freeCErr()` (success paths, so a message set despite
  success cannot leak).
- `PkgListBuilder` (RAII) owns the `strdup`ed strings while the list is being
  built: if the vector throws mid-fill the strings are freed instead of leaked,
  and `release()` moves the content out so the destructor then frees nothing.
  `add()` frees its three strings if `push_back` throws.
- Entries handed out by `aptCacheArrayGet` stay owned by the handle
  (documented in `apt.h`); `aptCacheArrayFree` remains the single owner-side
  free and is NULL-safe.
- The old code called `aptCacheArrayFree` even on the NULL handle; the new Go
  code only defers the free once the handle is known good.

### Interplay with the progress bridge (`callbacks.go`)

`callbacks.go` is **unchanged** — the `//export goAptProgress` bridge and the
`progressBridge` shim needed nothing. Two related changes were made in
`apt.go` (owned):

- `GetCachePackages` now takes `transactionMu`. libapt's `_config`/`_error`/
  `_system` are process-global and the cache is opened *with* the dpkg locks,
  so draining the global error stack must not race with a transaction
  reporting its own failure through `fail()`. No progress callback calls back
  into the apt package, so this cannot deadlock a running transaction, and
  `models` already serialises check vs upgrade through `upgradeLock`
  (`checkForUpdates` fails fast with "upgrade already in progress").
- `init()` can't return an error, so a failed `aptInit` is recorded in
  `initErr` and returned by `GetCachePackages` and `runTransaction` rather
  than being ignored (previously: every call would then quietly return an
  empty list).

### Call site (`models/update.go`)

`refreshNewVersions` consumes the new error. A failed cache read now returns
early *before* `setNewVersions`, so the previously known updates are kept — a
transient dpkg lock no longer erases them — and the error is joined with the
`apt-get update` error when both failed. `GET /api/update/check` turns that
into HTTP 500 (`app/update.go`, unchanged).

### Verification

All in `ghcr.io/calaos/debian_deb_build_base:latest` + `apt-get build-dep`
(mirrors `.github/workflows/ci.yml`); the host has no `libapt-pkg-dev`.

- `make build-lib` (meson): clean compile, no warnings, no ABI/link errors.
- `go build ./...`, `cd cmd/calaos-os && go build .`, `go vet ./...`: OK.
- `golangci-lint run` v2.12.2 with `CGO_LDFLAGS="-L./bin -lcalaos-apt"`:
  **0 issues**. `gofmt -l .`: clean.
- `make test` (`go test -race ./...`): all packages pass.

New tests in `apt/apt_test.go` (signature update was mandatory there):

- `TestGetCachePackagesError` — the real regression test. libapt reads
  `APT_CONFIG` once, in `pkgInitConfig()` during package `init()`, so the test
  re-executes the test binary with `APT_CONFIG` pointing at a non-existent apt
  tree. Child output: `CHILD-ERROR: unable to open the apt cache: Unable to
  read .../apt.conf.d/ - DirectoryExists (2: ...)` + 2 more drained messages,
  and the returned slice is nil (never an empty non-nil list).
- `TestCacheArrayBounds` — probes `aptCacheArrayGet` at `-1`, `count`,
  `count+1` (all NULL) and `count-1` (non-NULL), plus the NULL handle on
  count/get/free. `idx == count` is exactly the case the old `>` check let
  through into `std::vector::at()`.
- `TestGetCachePackagesSmoke` — updated for the new signature; skips (rather
  than silently passing) when the environment has no usable apt state.

### Error-path demonstration (daemon on port 8020)

Same daemon process asked to check while healthy, check while broken, then
list what it knows. HEAD build vs this branch, same container:

| failure mode | build | broken `GET /api/update/check` | known versions after |
|---|---|---|---|
| dpkg frontend lock held by another process | HEAD | **HTTP 200 `{}`** ("up to date") | wiped to `{}` |
| dpkg frontend lock held by another process | CC-020 | **HTTP 500** `failed to read the apt cache: unable to open the apt cache: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 1034 (python3) ...` | preserved |
| corrupt `/var/lib/dpkg/status` | HEAD | HTTP 500 (from `apt-get update` only) | wiped to `{}` |
| corrupt `/var/lib/dpkg/status` | CC-020 | HTTP 500, both causes joined (`apt-get update failed: ... ` + `failed to read the apt cache: ...`) | preserved |

The lock row is the ticket's acceptance criterion: `apt-get update` itself
succeeds (it takes the *lists* lock, not the frontend lock), so before this
ticket the failure was invisible — exactly the "Already up to date" bug.
The lock was held with a real `fcntl` lock (`python3 fcntl.lockf`); note
`flock(1)` would *not* reproduce it, as apt uses `fcntl` locks and the two
families do not conflict.

Normal case unaffected: healthy check answers HTTP 200 with
`{"linux-libc-dev":{"version":"6.1.180-1","current_version":"6.1.177-1"}}`.
Daemon stdout during checks is structured logrus output only — no `cout` noise.

### Note for CC-041 (build wiring)

No new build flag is needed. But `aptInit`/`aptCacheGetArray` changed
signature without a soname bump (`apt/meson.build` still `version = '1.0.0'`,
not owned here): `libcalaos-apt.so` and the Go daemon must be rebuilt
together. A stale `.so` would not crash (the extra register is ignored on
x86-64) but would leave `*errorMsg` NULL, degrading every failure to the
generic fallback message. Bumping the library version alongside the packaging
work would make that mismatch impossible.
