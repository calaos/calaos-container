# CC-043: grub-btrfs-overlay initramfs hook never runs

- **Phase:** P4
- **Priority:** P2
- **Size:** S
- **Status:** Review (branch `fix/snapshot-rescue-overlay`), boot of a snapshot in a VM still to validate
- **Depends on:** CC-041 (`/usr` install paths, version handling)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `debian/calaos-container.install`
  - `initramfs/`

## Context

Up to 1.9.x, `debian/calaos-container.install` gave a destination that dh_install treats as a directory, so the deb shipped:

- `usr/share/initramfs-tools/hooks/grub-btrfs-overlay/grub-btrfs-overlay`
- `usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay/grub-btrfs-overlay`

initramfs-tools only looks one level deep and `set_initlist` skips directories, so neither the hook nor the local-bottom script ever ran: a grub-btrfs snapshot entry booted a read-only root. The script itself could not have run either: it used `trap ... ERR` and `mktemp`, which the klibc `sh` of the Debian initramfs does not have.

## Work items

- [x] Install to the directories: `usr/share/initramfs-tools/hooks/` and `usr/share/initramfs-tools/scripts/local-bottom/`.
- [x] Directory → file swap on upgrade: dpkg replaces the old directories by the files without a preinst (checked with real dpkg, upgrade from 1.9.1).
- [x] Rewrite the local-bottom script in POSIX sh for klibc `sh`: fixed directories under `/run/calaos-overlay`, tmpfs bounded to 25% of RAM, overlay source `rootfs`, no-op unless the root is a read-only btrfs subvolume, rollback and exit 0 on any failure.
- [x] Hook ships `overlay` and `btrfs`, never fails.
- [x] initramfs regeneration through the `update-initramfs` trigger added by `dh_installinitramfs` (no postinst).
- [x] shellcheck on `initramfs/` in CI, stubbed shell test `scripts/tests/grub_btrfs_overlay_test.sh`.

## Acceptance criteria

- [x] Upgrade from the published 1.9.1 deb in a `debian:bookworm` container: state `ii`, every file of the new deb present, `dpkg --verify` clean.
- [x] `lsinitramfs` of the regenerated initramfs contains `scripts/local-bottom/grub-btrfs-overlay`, its `ORDER` entry, `overlay.ko` and `usr/bin/btrfs`.
- [ ] Boot of a grub-btrfs snapshot entry in a VM: `/` is `overlay` from `rootfs`, the system is writable, a normal boot is unchanged.
