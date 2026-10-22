# CC-042: systemd unit hardening

> **Amended by the admin plan** ([`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)): `Restart=always` is kept (a self-upgrade exits 0 and must be relaunched); the ticket absorbs CC4 and owns the new postinst. Where the text below says otherwise, this note wins.

- **Phase:** P4
- **Priority:** P2
- **Size:** S
- **Status:** Backlog
- **Depends on:** CC-014 (reliable graceful shutdown makes `Restart=`/`TimeoutStopSec=` meaningful). Runs in parallel with CC-040 and CC-041 (disjoint files).
- **Blocks:** ADM-CC2
- **Files owned (exclusive for this phase):**
  - `debian/calaos-container.service`
  - `debian/calaos-container.postinst` (new)
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

## Admin plan amendments

- **`Restart=always`, not `on-failure`.** `Restart=on-failure` would not restart after a clean exit 0 following a self-upgrade. Drop every `Restart=on-failure` item below for `calaos-container.service`.
- **Unit.** `ExecStartPre=-/usr/bin/env CALAOS_CT_STARTPRE=1 /usr/bin/dpkg --configure -a` (leading `-`: a broken dpkg must not prevent the daemon from starting), `KillMode=process`, `TimeoutStopSec`, and a long `TimeoutStartSec` (30min) so the pre-start `dpkg --configure` is not killed.
- **Postinst with deferred restart.** New `debian/calaos-container.postinst` (`--no-stop-on-upgrade` stays in `debian/rules`, owned by CC-041). It starts a deferred restart in a `systemd-run` transient unit. That unit waits until the dpkg lock is free, then until `GET /api/update/status` returns `status: "idle"`. It retries every 5 s while the daemon is unreachable and gives up with a log entry after 2 h. It restarts only if `/proc/<pid>/exe` is marked deleted. The postinst never waits for calaos-container to start: it uses `--no-block`, or does nothing when `$CALAOS_CT_STARTPRE` is set. The ExecStartPre exports that variable, e.g. `ExecStartPre=-/usr/bin/env CALAOS_CT_STARTPRE=1 /usr/bin/dpkg --configure -a`.
- Absorbs CC4 of the admin plan; CC5 (apt in a transient unit) only if this proves insufficient at acceptance.
- Blocks ADM-CC2.

## Work items

- [ ] For `calaos-container.service`: add the directives that are **compatible** with its need to run privileged operations, and explicitly document (as unit comments) why the aggressive ones are omitted. Reasonable candidates: `RestartSec=`, `TimeoutStopSec=`, `ProtectHome=read-only`, `ProtectKernelTunables=`, `ProtectControlGroups=`, `RestrictSUIDSGID=`, `NoNewPrivileges=no` (must stay no — it shells out to privileged tools), keeping `ReadWritePaths` broad. Test each on a real target: any directive that breaks apt/podman/dd/systemctl must be reverted and its omission noted.
- [ ] Add `TimeoutStopSec=` to the other long-running units where appropriate (`usb-serial-touchscreen@`, `grub-btrfsd`); keep their existing `Restart=` values.
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
- [ ] `Restart=always`, `KillMode=process`, `TimeoutStopSec` and `TimeoutStartSec=30min` present; a clean `systemctl stop` completes within the timeout.
- [ ] Postinst: no restart while the dpkg lock is held or a job is running; never blocks startup.
- [ ] `ExecStartPre=-/usr/bin/env CALAOS_CT_STARTPRE=1 /usr/bin/dpkg --configure -a` does not prevent startup when dpkg is broken.
- [ ] `dpkg --configure -a` run from ExecStartPre with a pending calaos-container postinst completes without waiting on the unit.
- [ ] Omitted directives are documented with rationale.

## Verification notes

_(filled by the implementing agent)_
