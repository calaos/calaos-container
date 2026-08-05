# CC-041: Build & release engineering

- **Phase:** P4
- **Priority:** P2
- **Size:** M
- **Status:** Backlog
- **Depends on:** P3 complete (the Makefile was last touched by CC-030; wait for it to merge). Runs in parallel with CC-040 and CC-042 (disjoint files).
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `Makefile` (everything except the `test` target owned by CC-001 and the lib-install rule owned by CC-030 — both merged by now)
  - `debian/rules`
  - `debian/changelog`
  - `debian/compat`
  - `debian/control`
  - `.github/workflows/build-deb-push.yml`

## Context

Build and release plumbing is fragile and duplicated:

- **Version in ≥4 places:** `Makefile:6`, `debian/rules:4` (`VERSION = 1.0.0`), `debian/changelog:1`, plus the Go default. `docker/build-deb.sh:17-20` sed-rewrites two of them at build time; CI also runs `calaos/action-bump-version`.
- **Broken Makefile var:** `SERVER_LDFLAGS := -L$(pwd)/bin …` — `$(pwd)` is not a Make function (should be `$(CURDIR)` or `$(shell pwd)`), expands empty → `-L/bin`; only the `-L./bin` fallback saves the link.
- **No PREFIX default:** a bare `make install` targets `/bin` and `/sbin`; correct paths depend on debhelper injecting `PREFIX=/usr`.
- **Inverted prerelease flag:** `build-deb-push.yml:59` `prerelease: ${{ github.event.inputs.vincrement == '' }}` — every push (empty input) publishes a *prerelease*, while manual dispatch is a full release.
- **Stale changelog:** `debian/changelog` still says `UNRELEASED` / "Initial release" 2023-07-01.
- **debhelper compat 11** (`debian/compat`) while 13 is current.
- Leftover `clean` targets remove report files no target produces (`Makefile:55-58`).
- Possible new build flag from CC-020's apt ABI change — check that ticket's verification notes.
- **Note (2026-08-05, post CC-001):** `build-deb-push.yml` was already modified as a CC-001 follow-up: its `test` job was replaced by a `ci` job that calls `ci.yml` as a reusable workflow (`workflow_call`), and `build-and-push-deb-pkg` now has `needs: ci` so the release only runs when all CI gates are green. Preserve this gating when reworking the file; the `prerelease` fix at line ~64 is still pending here.

## Work items

- [ ] `$(pwd)` → `$(CURDIR)` in `SERVER_LDFLAGS`.
- [ ] `PREFIX ?= /usr` default in the Makefile.
- [ ] Single-source the version: pick one authority (a `VERSION` file or the git tag) and have `Makefile`, `debian/rules`, and the changelog/CI bump read from it; remove the sed hacks in `docker/build-deb.sh` where possible (that file is not owned here — if it must change, note it for a follow-up; the Makefile/rules side is owned here).
- [ ] Fix the inverted `prerelease` logic in `build-deb-push.yml:59` so pushes are not published as prereleases (or make the intent explicit and correct).
- [ ] Bump `debian/compat` 11 → 13 (or switch to `debhelper-compat (= 13)` in `debian/control` Build-Depends and remove the `compat` file).
- [ ] Refresh `debian/changelog` to a real release entry.
- [ ] Remove the dead `clean` report-file removals (`Makefile:55-58`).
- [ ] Pick up any RPATH / `LD_LIBRARY_PATH` requirement for `libcalaos-apt.so` if CC-020 flagged one.

## Out of scope

- CI test/lint/build pipeline (`ci.yml`) — CC-001; shellcheck gate — CC-035.
- systemd units — CC-042.
- The apt ABI itself — CC-020 (only consume its build-flag note here).

## Breaking changes

None to runtime; changes how versions are sourced and how releases are tagged/published.

## Acceptance criteria

- [ ] `make build-server` links without relying on the `$(pwd)` accident (grep the Makefile: no bare `$(pwd)`).
- [ ] `make install DESTDIR=/tmp/stage` with no explicit PREFIX installs under `/tmp/stage/usr`, not `/tmp/stage/bin`.
- [ ] Version appears in exactly one authoritative place; the deb builds with the correct version.
- [ ] A push to `main` produces a normal release (not a prerelease); manual dispatch behaves as intended.
- [ ] `dpkg-buildpackage`/CI deb build succeeds with compat 13.

## Verification notes

_(filled by the implementing agent)_
