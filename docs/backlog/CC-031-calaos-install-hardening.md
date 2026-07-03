# CC-031: calaos_install hardening (destructive path)

- **Phase:** P3
- **Priority:** P0
- **Size:** L
- **Status:** Backlog
- **Depends on:** CC-030
- **Blocks:** CC-035
- **Files owned (exclusive for this phase):**
  - `scripts/calaos_install`

## Context

`calaos_install` (375 lines) is the most dangerous script in the repo: it partitions, `dd`-wipes, and `mkfs`-formats a whole disk. It is reachable from the HTTP API (`app/system.go:138`, guarded by CC-013) and from the boot TUI. Current weaknesses:

- `:5` only `set -e`; no `-u`, no `-o pipefail`, despite pipeline-based image detection and `dd`/`mkfs`/`parted`.
- `:47` `eval "${x}"` on tokens from `/proc/cmdline`.
- `:95,116,219,313` `[ $LABEL == "live-efi" ]` — `$LABEL` unquoted and unset-unsafe (no `-u`).
- `:113` `dd if=/dev/zero of=${destination} bs=512 count=35` — unquoted destination; no confirmation inside this script (the only confirmation is in `config_calaos-boot`).
- `:118-148` `parted`/`mkfs.vfat`/`mkswap`/`mkfs.btrfs --force` — no explicit success checks, unquoted device vars, `--force` silently overwrites.
- `:126-128,142-144` `destination_esp="/dev/$(get_dev_part …)"` — `get_dev_part` (`:60-64`) output unvalidated; a null result yields `/dev/`.
- `:151` `blkid` result `uuid_rootfs` unchecked, later interpolated unquoted (`:341`).
- `:191` fragile PCRE parse of `btrfs subvolume` text output.
- `:195` `chattr +C ${dst}/@/var/{cache,log,spool,tmp}` — brace expansion + unquoted path.
- `:253` `genfstab -U ${dst} >> ${dst}/etc/fstab` — appends, non-idempotent (re-run duplicates entries).
- `:261-263` `sed -i` on `/etc/grub.d/*` with no existence check.
- `:276,289` broken podman image detection (colon-grep never matches columns).
- `:287` `podman pull` under `set -e` aborts the whole install on transient failure.
- **No cleanup trap:** `err_report` (`:66-68`, hooked `:84`) only prints the failing line; on failure the script leaves partitions mounted, swap enabled (`:225`), loop/chroot state dangling. Final teardown (`:356-373`) is bare `umount` with no `|| true`.

## Work items

- [ ] Source `scripts/lib/calaos-common.sh`; adopt `set -euo pipefail`.
- [ ] Quote every variable expansion and command substitution.
- [ ] Replace `eval` cmdline parsing (`:47`) with the library's eval-free parser.
- [ ] Install a cleanup `trap` (on `EXIT`/`ERR`) that unmounts everything under `$dst`, `swapoff` any swap this script enabled, and detaches loop devices — each guarded with `|| true` so teardown never aborts mid-cleanup.
- [ ] Validate `get_dev_part` output is non-empty and the resulting path is a block device before use (`:126-128,142-144`).
- [ ] Validate `uuid_rootfs`/`blkid` results are non-empty before interpolation (`:151,341`).
- [ ] Make `genfstab` idempotent: write to a temp file and move into place, or generate the base fstab then append once — never append to an existing populated fstab on re-run (`:253`).
- [ ] Guard the `sed -i` grub edits with existence checks (`:261-263`).
- [ ] Replace the broken podman detection (`:276,289`) with the library helper (exact `Repository:Tag` match); make a failed `podman pull` a clear, handled error, not a silent skip that continues without the image (`:287,293`).
- [ ] Add a `bash -n` syntax check target usable by CI; add a `--dry-run`/no-op guard for CI smoke if feasible.

## Out of scope

- The API-side device validation / confirm flag — done in CC-013 (Go).
- The shared library internals — CC-030.
- `config_calaos-boot`, `calaos_rollback`, etc. — their own tickets (CC-032/033).

## Breaking changes

None to the interface (`calaos_install <disk>`), but behavior is stricter: it aborts earlier and more loudly on malformed input, and cleans up on failure.

## Acceptance criteria

- [ ] `shellcheck scripts/calaos_install` is clean (needed for CC-035).
- [ ] `bash -n scripts/calaos_install` passes.
- [ ] In a VM / loop-device test: an induced failure mid-install leaves **no** stray mounts, swap, or loop devices (verify with `mount`, `swapon --show`, `losetup -a`).
- [ ] Re-running the installer does not produce duplicate fstab entries.
- [ ] A missing container image produces a clear failure, not a silent continue.

## Verification notes

_(filled by the implementing agent)_
