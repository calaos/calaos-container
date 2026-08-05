# CC-001: PR CI pipeline (build, vet, lint, race tests, shellcheck)

- **Phase:** P0
- **Priority:** P0
- **Size:** M
- **Status:** Done
- **Depends on:** CC-002 (merge order only — develop in parallel, merge after, so the pipeline is green on main)
- **Blocks:** CC-030, CC-034, CC-035, and effectively every later ticket (they all rely on the CI gate)
- **Files owned (exclusive for this phase):**
  - `.github/workflows/ci.yml` (new)
  - `.golangci.yml` (new)
  - `.shellcheckrc` (new)
  - `Makefile` (the `test` target ONLY)

## Context

There is currently **no pre-merge quality gate**:

- Zero `*_test.go` files exist; the Makefile `test` target is stubbed: `@echo test disabled` (`Makefile:63`).
- `.github/workflows/` only contains `build-deb-push.yml` (runs on push to `main` — after merge, not before) and `build-docker-base.yml`.
- No `go vet`, no `golangci-lint`, no `gofmt` check, no shellcheck despite ~1,500 lines of shell driving destructive disk operations.
- The server binary needs cgo + libapt (`apt/apt.go:3` `#cgo pkg-config: apt-pkg`) and links the meson-built `libcalaos-apt.so` — the CI job must install `libapt-pkg-dev`, `meson`, `ninja-build` and run the apt lib build before `go build` (see top-level `Makefile` targets `build-server` and the `apt/Makefile` meson wrapper).

## Work items

- [ ] New `.github/workflows/ci.yml` triggered on `pull_request` and `push` to `main`:
  - Debian-based job (or the existing `docker/` build-base image) installing `golang`, `libapt-pkg-dev`, `meson`, `ninja-build`, `pkg-config`.
  - Build the apt shared lib (`make -C apt`), then `go build ./...` and `cd cmd/calaos-os && go build`.
  - `go vet ./...` (blocking).
  - `golangci-lint run` (blocking) with curated `.golangci.yml`: `govet`, `staticcheck`, `errcheck`, `unused`, `gosec`, `ineffassign`. Keep the initial set small enough to be green on post-CC-002 main.
  - `go test -race ./...` (blocking — trivially green until P5 adds tests).
  - Debian package dry-run build (reuse `docker/build-deb.sh` logic or `dpkg-buildpackage -us -uc -b`) — blocking.
  - `shellcheck` on Calaos-owned scripts with `continue-on-error: true` (**non-blocking** until CC-035 flips it).
- [ ] `.shellcheckrc` (or an explicit path list in the workflow) excluding vendored third-party files: `scripts/genfstab`, `scripts/arch-chroot`, `grub-btrfs/**`, `initramfs/**`.
- [ ] Replace the Makefile `test` target body with `go test -race ./...` (and keep `cd cmd/calaos-os && go test -race ./...` if it stays a separate module).

## Out of scope

- Do NOT fix other Makefile issues (`$(pwd)`, `PREFIX`, version) — owned by CC-041.
- Do NOT touch `build-deb-push.yml` (prerelease flag) — owned by CC-041.
- Do NOT make shellcheck blocking — owned by CC-035.
- Do NOT add tests — owned by P5 tickets.

## Breaking changes

None (additive; PRs now require a green pipeline).

## Acceptance criteria

- [ ] Opening a PR triggers the workflow; all Go gates pass on a branch based on post-CC-002 main.
- [ ] The cgo server binary builds in CI (proves `libapt-pkg-dev` + meson wiring).
- [ ] Shellcheck step runs, reports findings on `scripts/calaos_install` etc., but does not fail the pipeline.
- [ ] Vendored scripts produce no shellcheck annotations (excluded).
- [ ] `make test` runs `go test -race ./...` locally.

## Verification notes

**Ticket text was stale at implementation time.** Pre-existing state (commits `93701d6`
"CI: install build deps in test job" and `70bde9b`) already had: `*_test.go` files in
`app/`, `apt/`, `models/`, `models/structs/`, `cmd/calaos-os/api/`; a working
`make test` running `go test ./...` (not stubbed); and `build-deb-push.yml`'s `test`
job already running `make test` inside `ghcr.io/calaos/debian_deb_build_base` — but
only on `push` to `main`, i.e. post-merge, not a PR gate. A stub `.golangci.yml`
already existed (no linters configured). `.shellcheckrc` and `.github/workflows/ci.yml`
did not exist. Implemented only the gap: added `-race`, curated `.golangci.yml`,
new `.shellcheckrc`, and the new PR-triggered `ci.yml`. `build-deb-push.yml` was not
touched.

**What was built:**
- `.github/workflows/ci.yml` (new) — triggers on `pull_request` and `push` to `main`.
  Three jobs: `build-vet-lint-test` (build apt lib, `go build ./...`, `cmd/calaos-os`
  build, `go vet`, `golangci-lint run`, `make test`, all inside
  `ghcr.io/calaos/debian_deb_build_base`), `shellcheck` (non-blocking,
  `continue-on-error: true`, on ubuntu-latest), `deb-build-dry-run`
  (`dpkg-buildpackage -us -uc -b` inside the same base image, blocking).
- `.golangci.yml` — `linters.default: standard` (govet, staticcheck, errcheck,
  unused, ineffassign) + `gosec` enabled, matching the ticket's curated set exactly.
  29 pre-existing findings on main (6 errcheck, 13 gosec incl. 3 gosec/errcheck
  overlap unmasked once errcheck was excluded on the same lines, 4 staticcheck,
  9 unused) are grandfathered via `linters.exclusions.rules`, scoped per
  (linter, file, specific message/source) — not blanket per-file suppression —
  so new issues in those same files are still caught. Fixing the underlying
  application code is out of scope for this ticket; narrowed the config instead,
  per the ticket's own guidance.
- `.shellcheckrc` (new) — shellcheck has no file-path-exclusion directive, so it
  only sets `source-path=SCRIPTDIR`; the vendored-file exclusion
  (`scripts/genfstab`, `scripts/arch-chroot`, `grub-btrfs/**`, `initramfs/**`) is
  implemented in `ci.yml`'s file-discovery step instead (documented in both files).
- `Makefile` `test` target — changed `$(GOTEST) ./...` to `$(GOTEST) -race ./...`.
  Single Go module (one `go.mod` at repo root; `cmd/calaos-os` is a package in it,
  not a separate module), so no second `cd cmd/calaos-os && go test` was needed.

**Verification performed** (host lacks `libapt-pkg-dev`/`golangci-lint`/passwordless
sudo, so verification ran inside a container from
`ghcr.io/calaos/debian_deb_build_base:latest`, the same image `ci.yml` uses, with the
repo bind-mounted — i.e. the actual CI environment, not an approximation):
- `apt-get build-dep -y .` then `make build-lib` then `go build ./...` then
  `(cd cmd/calaos-os && go build .)` — all pass.
- `go vet ./...` — clean, 0 issues.
- `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` (v2.12.2, pinned to match
  `.devcontainer/Dockerfile`) with the new `.golangci.yml` — `0 issues`.
  `golangci-lint config verify` — passes (schema-valid).
- `make test` — runs `CGO_LDFLAGS=... go test -race ./...`, all packages pass
  (`app`, `apt`, `cmd/calaos-os/api`, `events`, `models`, `models/structs`).
- `shellcheck` on the discovered file list (`.devcontainer/setup_veth.sh`,
  `docker/build-deb.sh`, `env/calaos.sh`, and 7 files under `scripts/`, vendored
  files excluded) — runs and reports findings (e.g. `scripts/calaos_install`
  SC2046/SC1090), exit code 123 as expected for a non-blocking step; the excluded
  vendored files (`scripts/genfstab`, `scripts/arch-chroot`, `grub-btrfs/grub-btrfsd`,
  `initramfs/**`) are never passed to shellcheck, so they produce no annotations.
- `dpkg-buildpackage -us -uc -b` (after `apt-get build-dep -y .`) — builds
  `calaos-container_1.0.0-0~bookworm0_amd64.deb` successfully.
- `.github/workflows/ci.yml` validated with `python3 -c "import yaml,sys;
  yaml.safe_load(open(...))"` (valid) and with `actionlint` v1.7.12
  (`go install github.com/rhysd/actionlint/cmd/actionlint@latest`) — 0 findings.
- All build artifacts (`bin/`, `apt/build/`, `debian/calaos-container`, etc.)
  cleaned up after verification; nothing left uncommitted beyond the intended
  file changes.
