# CC-043: grub-btrfs-overlay initramfs hook never runs

- **Phase:** P4
- **Priority:** P2
- **Size:** S
- **Status:** Backlog
- **Depends on:** CC-041 (`/usr` install paths, version handling)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `debian/calaos-container.install`
  - new `debian/calaos-container.preinst` (if that variant of the fix is chosen)

## Context

`debian/calaos-container.install` gives a destination that dh_install treats as a directory, so the deb ships:

- `usr/share/initramfs-tools/hooks/grub-btrfs-overlay/grub-btrfs-overlay`
- `usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay/grub-btrfs-overlay`

initramfs-tools only looks one level deep and `set_initlist` skips directories, so neither the hook nor the local-bottom script ever runs: the overlay needed to boot read-only btrfs snapshots is missing from the initramfs. Present in every published deb (seen on 1.8.1~dev.1), not introduced by CC-041.

## Work items

- [ ] Install to the directories: `usr/share/initramfs-tools/hooks/` and `usr/share/initramfs-tools/scripts/local-bottom/`.
- [ ] Make the directory → file swap at the same path upgrade-safe (dpkg can fail to unpack a file over an existing directory). Either:
  - add `debian/calaos-container.preinst` that `rm -rf`s the two old directories when `$1 = upgrade` (keep `#DEBHELPER#`), or
  - install under a new name (e.g. `calaos-grub-btrfs-overlay`) so no path changes type.

## Acceptance criteria

- [ ] Upgrade from the published `1.8.1~dev.1` deb in a `debian:bookworm` container: state `ii`, every file of the new deb present, `dpkg --verify` clean.
- [ ] `lsinitramfs` of a regenerated initramfs contains the overlay hook output and `scripts/local-bottom/<name>`.
