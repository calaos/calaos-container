# CC-001: PR CI pipeline (build, vet, lint, race tests, shellcheck)

- **Phase:** P0
- **Priority:** P0
- **Size:** M
- **Status:** Backlog
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

_(filled by the implementing agent)_
