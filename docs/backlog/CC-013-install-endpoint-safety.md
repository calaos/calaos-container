# CC-013: Install endpoint safety

- **Phase:** P1
- **Priority:** P0
- **Size:** S
- **Status:** Backlog
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

- [ ] Before launching the installer, validate `n.Device` against the block-device inventory the daemon already produces for `GET /system/install/list_devices` (`app/system.go:86`, via `lsblk`): the device must exist in that list, be `type=disk` (not `part`/`rom`/`loop`), and must not be the disk that hosts the root filesystem.
- [ ] Require an explicit `"confirm": true` field in the request body; reject with 400 if absent or false.
- [ ] Reject with a clear 400 message on any of: unknown device, partition instead of disk, root disk, missing confirm.
- [ ] Keep the streaming response mechanism unchanged in THIS ticket (the `c.SendStream` / pipe hang is fixed in CC-021); only add the guards ahead of the launch.

## Out of scope

- `RunCommandReader` / streaming hang / goroutine leak (`models/command.go`) — owned by CC-021.
- `install/status` "never run" → 400 vs a proper state — owned by CC-040 (envelope/status-code work).
- Confirmation inside the shell script itself — CC-031 hardens the script; the API guard here is the primary gate.
- Response envelope — CC-040.

## Breaking changes

`POST /system/install/start` now **requires** `"confirm": true` in the body and a valid whole-disk device. There is no in-repo caller (the CLI has no install command). External installers / the web UI must add the field — documented here and reiterated in `docs/API.md` (CC-040).

## Acceptance criteria

- [ ] Request with a nonexistent device → 400.
- [ ] Request targeting a partition (e.g. `/dev/sda1`) → 400.
- [ ] Request targeting the root-backing disk → 400.
- [ ] Valid whole-disk device but no `confirm` → 400.
- [ ] Valid device + `"confirm": true` → installer launches as before (verify in a VM/loop device, not on a real host).
- [ ] `go vet` green.

## Verification notes

_(filled by the implementing agent)_
