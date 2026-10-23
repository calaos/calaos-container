# AGENTS.md

Guidance for coding agents working in this repository.

Container/system management daemon for Calaos-OS (home automation). Go daemon exposing a REST API (Fiber, default `127.0.0.1:8000`) plus a `calaos-os` CLI, with a C++ APT wrapper library linked via CGO.

## Build

- `make` builds everything into `bin/`. Order matters: the C++ apt library (`apt/`, Meson) must build before the Go daemon — `make build-server` depends on it and links with `CGO_LDFLAGS="-L./bin -L. -lcalaos-apt"`.
- Gotcha: `make build-lib` runs `meson install` and installs `libcalaos-apt.so` system-wide (to `/usr`). To run `bin/calaos-container` without installing, use `LD_LIBRARY_PATH`.
- The CLI tool builds from `cmd/calaos-os/` (`make build-tools`), no CGO needed.
- Debian packaging lives in `debian/`; CI builds the .deb inside `ghcr.io/calaos/debian_deb_build_base` running `docker/build-deb.sh`.

## Verification

- The bar for a change is: the full build passes AND `make test` passes (`go test ./...`, needs the apt lib built first — the target depends on `build-lib`). CI runs `make test` as a gate before building/releasing the .deb. Add Go tests alongside new features/code when it makes sense for robustness.
- Lint Go code with `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` (build the apt lib first so typecheck works). Lint shell scripts with `shellcheck`. Both tools come from the devcontainer image; if missing, the container needs a rebuild.
- Standard `gofmt` formatting applies; run it on every Go file you touch (for Claude Code, a PostToolUse hook in `.claude/settings.json` does it on edits).

## Workflow

- Commit directly to `main` — no PRs. Note: every push to `main` triggers CI to bump the version, build the .deb, and publish a GitHub release, so only push working code.
- `docs/BOARD.md` and `docs/backlog/` are planning documents only; changes don't need to map to CC-* tickets.

## Environment

- The devcontainer is privileged with full systemd — running and restarting the daemon locally (even via systemd) is fine.
- Daemon config: `/etc/calaos-container.toml` (override with `-c`, or `CALAOS_*` env vars); defaults: `127.0.0.1:8000`, update check every 12h. Updates are the `calaos-*` Debian packages, upgraded through libapt with snapper pre/post snapshots; there is no image release URL anymore.

## API

- The routes served today are the legacy unprefixed `/api/...` ones (`app/app.go`); their paths and bodies are frozen for deployed kiosks and CLIs.
- `docs/API.md` is the contract of `/api/v2/` (envelope, error codes, admin sessions, upgrade jobs). It is not implemented yet: check the code before assuming a v2 route exists.
