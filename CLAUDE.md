# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Container/system management daemon for Calaos-OS (home automation). Go daemon exposing a REST API (Fiber, default `127.0.0.1:8000`) plus a `calaos-os` CLI, with a C++ APT wrapper library linked via CGO.

## Build

- `make` builds everything into `bin/`. Order matters: the C++ apt library (`apt/`, Meson) must build before the Go daemon — `make build-server` depends on it and links with `CGO_LDFLAGS="-L./bin -L. -lcalaos-apt"`.
- Gotcha: `make build-lib` runs `meson install` and installs `libcalaos-apt.so` system-wide (to `/usr`). To run `bin/calaos-container` without installing, use `LD_LIBRARY_PATH`.
- The CLI tool builds from `cmd/calaos-os/` (`make build-tools`), no CGO needed.
- Debian packaging lives in `debian/`; CI builds the .deb inside `ghcr.io/calaos/debian_deb_build_base` running `docker/build-deb.sh`.

## Verification

- There are no tests yet (`make test` is intentionally disabled). The bar for a change is: the full build passes. Add Go tests alongside new features/code when it makes sense for robustness.
- Lint Go code with `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` (build the apt lib first so typecheck works). Lint shell scripts with `shellcheck`. Both tools come from the devcontainer image; if missing, the container needs a rebuild.
- Standard `gofmt` formatting applies (a PostToolUse hook in `.claude/settings.json` enforces it on edits).

## Workflow

- Commit directly to `main` — no PRs. Note: every push to `main` triggers CI to bump the version, build the .deb, and publish a GitHub release, so only push working code.
- `docs/BOARD.md` and `docs/backlog/` are planning documents only; changes don't need to map to CC-* tickets.

## Environment

- The devcontainer is privileged with full systemd — running and restarting the daemon locally (even via systemd) is fine.
- Daemon config: `/etc/calaos-container.toml` (override with `-c`); defaults: port 8000, update check every 12h, images from `https://releases.calaos.fr/v4/images`.
