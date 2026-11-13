# CC-043: grub-btrfs-overlay initramfs hook never runs

> **Absorbed by removal** (CONTAINER-OVERLAY-FIX): the overlay feature is abandoned. The hook and the local-bottom script are no longer shipped, so there is nothing left to install in the right place.

- **Phase:** P4
- **Priority:** P2
- **Size:** S
- **Status:** Absorbed
- **Depends on:** CC-041 (`/usr` install paths, version handling)
- **Blocks:** —

## Context

`debian/calaos-container.install` gave a destination that dh_install treated as a directory, so the deb shipped:

- `usr/share/initramfs-tools/hooks/grub-btrfs-overlay/grub-btrfs-overlay`
- `usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay/grub-btrfs-overlay`

initramfs-tools only looks one level deep and `set_initlist` skips directories, so neither the hook nor the local-bottom script ever ran. Present in every published deb (seen on 1.8.1~dev.1), not introduced by CC-041.

## Resolution

Option (a): the files are removed from the package (`.install` lines and `initramfs/` sources). Installing them at working paths was rejected: `dh_installinitramfs` would add an `update-initramfs` trigger and activate a hook on every box, and the local-bottom script uses `trap ... ERR`, which fails under the klibc `sh`.

On upgrade dpkg removes the old files and the then-empty directories itself; no preinst is needed. The previous package already triggered initramfs-tools on each update.
