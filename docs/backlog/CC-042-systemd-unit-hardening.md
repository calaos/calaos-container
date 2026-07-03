# CC-042: systemd unit hardening

- **Phase:** P4
- **Priority:** P2
- **Size:** S
- **Status:** Backlog
- **Depends on:** CC-014 (reliable graceful shutdown makes `Restart=`/`TimeoutStopSec=` meaningful). Runs in parallel with CC-040 and CC-041 (disjoint files).
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `debian/calaos-container.service`
  - `debian/calaos-boot.service`
  - `debian/btrfs-scrub@.service`
  - `debian/grub-btrfsd.service`
  - `debian/usb-serial-touchscreen@.service`

## Context

systemd unit hardening is essentially absent:

- `debian/calaos-container.service` — `Type=simple`, `Restart=always`, runs as **root**, **zero** sandboxing directives (`NoNewPrivileges`, `ProtectSystem`, `ProtectHome`, `PrivateTmp`, `ReadWritePaths`, `CapabilityBoundingSet` all absent). It legitimately needs broad privilege (it drives `apt`, `podman`, `systemctl`, `dd` via the installer), so full confinement is not possible — but there are currently **no** directives at all, and no `TimeoutStopSec`.
- `debian/calaos-boot.service` — oneshot on tty2, no hardening.
- `debian/grub-btrfsd.service:20` — hardcoded `ExecStart` path, no hardening.
- `debian/usb-serial-touchscreen@.service` — `Restart=always`, no hardening.
- `debian/btrfs-scrub@.service` — the only unit with any resource care (`Nice=19`, `IOSchedulingClass=idle`).

## Work items

- [ ] For `calaos-container.service`: add the directives that are **compatible** with its need to run privileged operations, and explicitly document (as unit comments) why the aggressive ones are omitted. Reasonable candidates: `Restart=on-failure` (instead of `always`), `RestartSec=`, `TimeoutStopSec=`, `ProtectHome=read-only`, `ProtectKernelTunables=`, `ProtectControlGroups=`, `RestrictSUIDSGID=`, `NoNewPrivileges=no` (must stay no — it shells out to privileged tools), keeping `ReadWritePaths` broad. Test each on a real target: any directive that breaks apt/podman/dd/systemctl must be reverted and its omission noted.
- [ ] Add `Restart=on-failure` + `TimeoutStopSec=` to the long-running units where appropriate (`usb-serial-touchscreen@`, `grub-btrfsd`).
- [ ] Leave `btrfs-scrub@` largely as-is (already reasonable); add hardening only if it does not interfere.
- [ ] Do not change what the units execute or their install wiring.

## Out of scope

- Go daemon shutdown behavior — CC-014.
- Packaging/version/Makefile — CC-041.
- Unit installation paths / `debian/*.install` — not needed here.

## Breaking changes

None expected, but each directive must be validated on real hardware/VM because over-restriction would break the daemon's privileged operations. The ticket's verification notes must record what was tested and what was omitted and why.

## Acceptance criteria

- [ ] `systemd-analyze verify` passes for each changed unit.
- [ ] `calaos-container.service` still successfully runs an apt upgrade, a podman operation, and (in a VM) an install, with the new directives applied.
- [ ] `Restart=on-failure` + `TimeoutStopSec` present; a clean `systemctl stop` completes within the timeout.
- [ ] Omitted directives are documented with rationale.

## Verification notes

_(filled by the implementing agent)_
