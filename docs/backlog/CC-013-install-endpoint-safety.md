# CC-013: Install endpoint safety

- **Phase:** P1
- **Priority:** P0
- **Size:** S
- **Status:** Done
- **Depends on:** P0 merged
- **Blocks:** CC-021
- **Files owned (exclusive for this phase):**
  - `app/system.go`

## Context

`POST /api/system/install/start` (`app/system.go:121-147`) launches `calaos_install` with a device path taken from the JSON body, checked only for non-emptiness (`:131`). `calaos_install` then runs `dd if=/dev/zero of=$destination` (`scripts/calaos_install:113`), `parted`, and `mkfs.btrfs --force` — an **irreversible full-disk wipe**. The only human confirmation in the whole system lives in the boot-time TUI (`config_calaos-boot`), which is entirely bypassed on the API path. There is:

- no check that the device is a real block device,
- no check that it is a whole disk rather than a partition,
- no check that it is not the disk currently backing `/`,
- no explicit confirmation flag.

A single valid bearer token + a typo (or a wrong `sdX`) destroys the running system.

## Work items

- [x] Before launching the installer, validate `n.Device` against the block-device inventory the daemon already produces for `GET /system/install/list_devices` (`app/system.go:86`, via `lsblk`): the device must exist in that list, be `type=disk` (not `part`/`rom`/`loop`), and must not be the disk that hosts the root filesystem.
- [x] Require an explicit `"confirm": true` field in the request body; reject with 400 if absent or false.
- [x] Reject with a clear 400 message on any of: unknown device, partition instead of disk, root disk, missing confirm.
- [x] Keep the streaming response mechanism unchanged in THIS ticket (the `c.SendStream` / pipe hang is fixed in CC-021); only add the guards ahead of the launch.

## Out of scope

- `RunCommandReader` / streaming hang / goroutine leak (`models/command.go`) — owned by CC-021.
- `install/status` "never run" → 400 vs a proper state — owned by CC-040 (envelope/status-code work).
- Confirmation inside the shell script itself — CC-031 hardens the script; the API guard here is the primary gate.
- Response envelope — CC-040.

## Breaking changes

`POST /system/install/start` now **requires** `"confirm": true` in the body and a valid whole-disk device. There is no in-repo caller (the CLI has no install command). External installers / the web UI must add the field — documented here and reiterated in `docs/API.md` (CC-040).

## Acceptance criteria

- [x] Request with a nonexistent device → 400.
- [x] Request targeting a partition (e.g. `/dev/sda1`) → 400.
- [x] Request targeting the root-backing disk → 400.
- [x] Valid whole-disk device but no `confirm` → 400.
- [x] Valid device + `"confirm": true` → installer launches as before (verify in a VM/loop device, not on a real host).
- [x] `go vet` green.

## Verification notes

Implementation (`app/system.go`):
- `InstallOpts` gained a `Confirm bool` field (`json:"confirm"`).
- `apiSystemInstallListDevices` now runs `lsblk` via a shared `lsblkInventoryArgs` var (`--bytes --json --paths --output-all`) instead of an inline arg list, so the exact same invocation backs both the public inventory endpoint and the install-time validation — no separate lsblk parsing path was introduced.
- Added `lsblkDevice`/`lsblkInventory` typed structs (fields: `name`, `path`, `type`, `mountpoint`, `mountpoints`, `children`), confirmed against real `lsblk --bytes --json --paths --output-all` output shape (checked interactively; also pinned by `TestParseLsblkInventoryJSON`).
- Added `getBlockDeviceInventory()` (runs lsblk, unmarshals into the typed tree), `findLsblkDevice()` (recursive lookup by path across the whole tree, so a partition nested under a disk's `children` is still found), `deviceHostsMountpoint()` (recursively checks a device and all its descendants for a given mountpoint — this is the "root disk" detection: it walks down from each top-level disk through its partitions/children looking for `/`, which also correctly handles a whole-disk root with no partition table), and `validateInstallDevice(opts, devices) error` — the pure, testable validation function.
- `validateInstallDevice` check order: empty device → unknown device → wrong type (not `disk`) → hosts root filesystem → missing/false `confirm`. All failures return the existing `{"error":true,"msg":...}` shape via 400, unchanged from CC-013's stated envelope scope.
- `apiSystemInstallStart` now fetches the inventory and calls `validateInstallDevice` before ever calling `models.RunCommandReader("calaos_install", ...)`; the streaming response mechanism itself is untouched (still `c.SendStream`, per CC-021 being out of scope here).

Unit tests (`app/system_test.go`, new file):
- `TestValidateInstallDevice`: table-driven, canned lsblk-style inventory covering: valid whole disk + confirm (pass), empty device, unknown device, partition rejection, `type=rom` rejection, `type=loop` rejection, root-disk rejection via a partition mounted at `/` (nested child), root-disk rejection via a whole-disk mounted at `/` directly (no partition table — the "btrfs-on-disk" edge case), missing confirm, and explicit `confirm:false`.
- `TestParseLsblkInventoryJSON`: unmarshals a canned real-shaped `lsblk --json --output-all` sample and exercises `findLsblkDevice`/`deviceHostsMountpoint` against it, guarding the field-name/nesting assumptions the validation logic depends on.
- All new tests pass; full `make test` (`go test -race ./...`) passes for every package.

Container verification (`ghcr.io/calaos/debian_deb_build_base:latest`, worktree bind-mounted, ephemeral container removed afterward):
- `apt-get build-dep -y .` — ok.
- `make build-lib` — ok (meson/ninja build of the C++ apt library).
- `go build ./...` — ok.
- `go vet ./...` — clean.
- `golangci-lint run` (v2.12.2, installed via `go install .../golangci-lint@v2.12.2` since the release-tarball checksum from the install script didn't verify in this environment; `CGO_LDFLAGS="-L./bin -lcalaos-apt"`) — **0 issues**.
- `make test` (`go test -race ./...`) — all packages pass, including the new `app` tests.
- Runtime: built `bin/calaos-container`, ran it in the container against a real `lsblk` (with a minimal `/etc/calaos-container.toml` and a test bearer token), and issued live `POST /api/system/install/start` requests:
  - unknown device (`/dev/sdz`) → `400 {"error":true,"msg":"unknown device \"/dev/sdz\": ..."}`
  - no `confirm` field, otherwise-valid disk → `400 {"error":true,"msg":"explicit confirmation required: ..."}`
  - `confirm:false` explicit → same 400
  - empty device → `400 {"error":true,"msg":"device arg not set"}`
  - The passing case (valid disk + `confirm:true`) was deliberately **not** exercised end-to-end, per the safety constraint on this ticket — it is covered by the unit tests instead.
  - The partition-rejection and root-disk-rejection paths could not be exercised against a *live* device in this container: the build container's own block devices (as seen through its shared `/dev`) are unpartitioned whole disks with no `type=part` entries, and the container's own `/` is an overlayfs (not backed by any block device lsblk reports), so no real device exists there to mount at `/`. Constructing a synthetic loop device/partition to work around this would have required a privileged container, which was intentionally not done (out of caution around this ticket's disk-safety scope, and blocked by the environment's own permission guard). Those two paths are proven instead by `TestValidateInstallDevice`'s root-disk and partition-rejection subtests, which use lsblk-schema-accurate canned data (validated by `TestParseLsblkInventoryJSON`).
- Cleaned up: daemon stopped, verification container removed, no leftover `cc013*` containers; `bin/`, `apt/build/` are gitignored and don't appear in `git status`.
