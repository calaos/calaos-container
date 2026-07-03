# CC-020: apt cgo error propagation & bounds fix

- **Phase:** P2
- **Priority:** P1
- **Size:** L
- **Status:** Backlog
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

- [ ] Extend the C ABI (`apt.h`) so `aptCacheGetArray` (or a wrapper) reports failure — e.g. a `char** errmsg` out-parameter populated from `_error` via `strdup`, or a status enum. Free the string on the Go side.
- [ ] Replace every `return nullptr //TODO` in `apt.cpp` with a path that sets the error message from libapt's `_error` stack before returning.
- [ ] Restore `aptInit` error handling (`apt.cpp:17-25`) — do not silently ignore `pkgInitConfig`/`pkgInitSystem`.
- [ ] Fix the bounds check to `>=` (`apt.cpp:111`).
- [ ] Remove the unconditional `cout` debug lines (`apt.cpp:35,52,61,72,82`).
- [ ] Change the Go binding to `GetCachePackages() ([]*Pkg, error)` (`apt/apt.go`) and translate the C error into a Go error.
- [ ] Update the single call site in `checkForUpdates` (`models/update.go:66`) so a cache error is surfaced (check reports failure) rather than reported as "no updates". Coordinate with the check-status field CC-010 introduced.

## Out of scope

- The update loop / locking / `NewVersions` — already done in CC-010; touch ONLY the `GetCachePackages` call line.
- The `libcalaos-apt.so` build wiring (`Makefile` `$(pwd)`/RPATH) — owned by CC-041. If the ABI change requires a rebuild, CC-041 already touches the Makefile in P4; note any new build flag needed in this ticket's verification notes for CC-041 to pick up.
- Removing the unused `glib-2.0` meson dep — CC-041.

## Breaking changes

Internal ABI only (`apt.h`); no Go public API beyond `GetCachePackages` gaining an error return (in-repo caller only). No HTTP contract change.

## Acceptance criteria

- [ ] With the apt lists locked (`sudo apt-get update` running in parallel, or a held `/var/lib/apt/lists/lock`), `GET /update/check` reports an **error**, not "up to date".
- [ ] Normal case still lists available upgrades correctly.
- [ ] No `cout` output on the daemon's stdout during a check.
- [ ] `apt/` compiles via `make -C apt`; `go build ./...` links against the rebuilt lib.
- [ ] Bounds check is `>=`.

## Verification notes

_(filled by the implementing agent)_
