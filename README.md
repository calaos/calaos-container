# calaos-container

System management daemon for [Calaos-OS](https://calaos.fr). It runs on the box as root and is
the only component allowed to touch the host: package and container image upgrades, btrfs
snapshots and rollback, network configuration, disk installation, reboot.

The Calaos applications themselves (calaos_server, the Calaos Home kiosk, InfluxDB, Mosquitto,
zigbee2mqtt…) run in podman containers and never get these privileges: they ask the daemon
through its local HTTP API.

The repository ships, as one Debian package:

- `calaos-container`, the Go daemon serving the HTTP/WebSocket API;
- `calaos-os`, a command line client of that API for a shell on the box;
- `libcalaos-apt`, a small C++ wrapper around libapt-pkg, linked into the daemon through cgo;
- the boot, install and rollback shell scripts of Calaos-OS, and their systemd units;
- the grub-btrfs integration that lets GRUB boot a snapshot.

## How it works

At boot, `calaos-container.service` first runs `init_calaosfs`, which lays out the data tree in
`/mnt/calaos` and writes a random API token to `/run/calaos/calaos-ct.token`. The daemon then
listens on `127.0.0.1:8000`. Every client (the kiosk, calaos_server, the CLI) authenticates with
that token as `Authorization: Bearer <token>`; only processes that can read `/run/calaos` can talk
to it.

Updates are plain Debian packages: every `calaos-*` package of the system is an update unit, and
container packages pull their image with `pull_calaos_image`. The daemon checks for new versions
every `general.update_time` and, on upgrade:

1. takes a snapper *pre* snapshot of the root filesystem;
2. runs the upgrade through libapt-pkg, publishing its progress on the `/api/events` WebSocket;
3. takes the matching *post* snapshot.

grub-btrfs lists these snapshots in the GRUB menu ("Calaos-OS snapshots"). After booting one,
`POST /api/system/rollback_snapshot` (`calaos_rollback`) makes it the new root and reboots.

`calaos-boot.service` runs `config_calaos-boot` on tty2: on a live medium it offers to install
Calaos-OS to a disk (`calaos_install`), otherwise it loads the container images cached in the OS
image and lets the boot continue.

## API

The current API is the unprefixed `/api/...` set:

| Area | Routes |
|---|---|
| Updates | `GET /api/update/check`, `available`, `installed`, `status`; `POST /api/update/upgrade`, `upgrade-all` |
| System | `POST /api/system/reboot`, `restart` (restarts the kiosk), `rollback_snapshot`; `GET /api/system/info`, `fs_status` |
| Install | `GET /api/system/install/list_devices`, `status`; `POST /api/system/install/start` |
| Network | `GET /api/network/list`; `POST /api/network/{intf}` |
| Events | `GET /api/events` (WebSocket, upgrade progress) |

[docs/API.md](docs/API.md) is the contract of the versioned `/api/v2/` API (common envelope,
error codes, admin password and sessions, persistent upgrade jobs). It is being implemented; the
routes above stay frozen for the clients already deployed.

## The `calaos-os` CLI

```sh
calaos-os list                       # installed calaos-* packages and available updates
calaos-os check-update               # ask the mirrors for new versions
calaos-os upgrade [PKG]              # upgrade one package, or all of them, with live progress
calaos-os network list
calaos-os network state              # interfaces and the pending change, if any
calaos-os network apply --intf eth0 --ipv4 192.168.1.10/24 [--gateway IP] [--dns IP]... [--search DOMAIN]...
calaos-os network apply --intf eth0 --dhcp
calaos-os network confirm [CHANGE_ID]   # keep an applied change
calaos-os network revert [CHANGE_ID]    # undo it now
calaos-os network configure dhcp INTERFACE
calaos-os network configure static INTERFACE IPV4 NETMASK [GATEWAY] [DNS...]
```

It reads the token from `/run/calaos/calaos-ct.token`, so it has to run as root on the box. Each
command has its own `--help`.

The package also installs `/etc/profile.d/calaos.sh`, which gives a shell on the box aliases to the
tools living in the containers: `calaos_config`, `calaos_mail`, `calaos_1wire`, `wago_test`,
`xinput_calibrator`…

## Configuration

`/etc/calaos-container.toml`, or another file with `-c`:

```toml
[general]
port = 8000
address = "127.0.0.1"
update_time = "12h"
# cors_origins = "http://localhost:3000"   # optional, comma separated

[log]
default = "info"
```

Every key can be overridden from the environment: `CALAOS_GENERAL_ADDRESS` sets `general.address`.
The daemon refuses to start with an empty `address`, which would expose the API, and with it
reboot and disk wipe, on every interface.

Exit codes: 1 bad command line, 2 invalid configuration, 3 and 4 initialisation failure, 5 the HTTP
listener failed.

## Repository layout

| Path | Content |
|---|---|
| `main.go`, `app/` | daemon entry point, Fiber routes, authentication, WebSocket events |
| `models/` | what the routes do: updates, network (ifupdown2), system info, snapper, command execution |
| `apt/` | `libcalaos-apt` (C++, Meson) and its cgo binding |
| `events/` | in-process event hub feeding the WebSocket |
| `config/` | configuration loading and validation |
| `cmd/calaos-os/` | the CLI |
| `scripts/` | `calaos_install`, `calaos_rollback`, `config_calaos-boot`, `init_calaosfs`, `pull_calaos_image`, `load_containers_cache`, shared `lib/calaos-common.sh` |
| `grub-btrfs/`, `initramfs/` | snapshot boot entries and the initramfs overlay that makes a read-only snapshot bootable |
| `debian/` | packaging and systemd units (daemon, boot TUI, grub-btrfsd, btrfs scrub, serial touchscreen) |
| `env/` | `/etc/profile.d/calaos.sh` |
| `docker/` | image and script used by CI to build the .deb |
| `docs/` | API contract, vendored code ([VENDORED.md](docs/VENDORED.md)), planning board and backlog |

`genfstab` and `arch-chroot` are vendored from Arch Linux's arch-install-scripts, see
[docs/VENDORED.md](docs/VENDORED.md).

## Building

Dependencies (Debian bookworm): Go, Meson, `libapt-pkg-dev`, `pkgconf`, plus `libbtrfs-dev`,
`libdevmapper-dev`, `libgpgme-dev`, `libglib2.0-dev` for the package build. The devcontainer in
`.devcontainer/` has all of them, golangci-lint and shellcheck, and runs systemd so the daemon can
be started as a real service.

```sh
make help     # list the targets
make          # libcalaos-apt, then the daemon and the CLI, into bin/
make test     # go test -race ./...
```

`libcalaos-apt` must be built before the daemon links against it, which `make` handles. Note that
`make build-lib` also runs `meson install`, which installs `libcalaos-apt.so` into `/usr`; to run
`bin/calaos-container` from the tree against the freshly built library, set `LD_LIBRARY_PATH`.

Lint:

```sh
CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run
shellcheck scripts/calaos_install    # and the other Calaos-owned scripts
```

## CI and releases

Pull requests run `.github/workflows/ci.yml`: build, `go vet`, golangci-lint, `go test -race`,
shellcheck as a blocking gate, and a dry-run build of the Debian package.

Every push to `main` bumps the version, builds the .deb in
`ghcr.io/calaos/debian_deb_build_base` (`docker/build-deb.sh`), tags it, publishes a GitHub release
and refreshes the package repository. Only push to `main` what is ready to reach the boxes.

## License

GPLv3, see [LICENSE](LICENSE).
