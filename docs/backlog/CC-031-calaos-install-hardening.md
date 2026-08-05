# CC-031: calaos_install hardening (destructive path)

- **Phase:** P3
- **Priority:** P0
- **Size:** L
- **Status:** Done
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

- [x] Source `scripts/lib/calaos-common.sh`; adopt `set -euo pipefail`.
- [x] Quote every variable expansion and command substitution.
- [x] Replace `eval` cmdline parsing (`:47`) with the library's eval-free parser.
- [x] Install a cleanup `trap` (on `EXIT`/`ERR`) that unmounts everything under `$dst`, `swapoff` any swap this script enabled, and detaches loop devices — each guarded with `|| true` so teardown never aborts mid-cleanup.
- [x] Validate `get_dev_part` output is non-empty and the resulting path is a block device before use (`:126-128,142-144`).
- [x] Validate `uuid_rootfs`/`blkid` results are non-empty before interpolation (`:151,341`).
- [x] Make `genfstab` idempotent: write to a temp file and move into place, or generate the base fstab then append once — never append to an existing populated fstab on re-run (`:253`).
- [x] Guard the `sed -i` grub edits with existence checks (`:261-263`).
- [x] Replace the broken podman detection (`:276,289`) with the library helper (exact `Repository:Tag` match); make a failed `podman pull` a clear, handled error, not a silent skip that continues without the image (`:287,293`).
- [x] ~~Add a `bash -n` syntax check target usable by CI~~; add a `--dry-run`/no-op guard for CI smoke if feasible.
  - `--dry-run` **done** (runs every safety check, prints the plan, exits 0 before
    the first destructive command).
  - The CI/Makefile `bash -n` target is **deferred**: this ticket owns
    `scripts/calaos_install` exclusively, and `Makefile` / `.github/workflows/`
    belong to CC-041 / CC-035. `bash -n scripts/calaos_install` passes (see
    below) and CC-035, which flips shellcheck to a hard gate right after this,
    is the natural home for wiring it into CI.

## Out of scope

- The API-side device validation / confirm flag — done in CC-013 (Go).
- The shared library internals — CC-030.
- `config_calaos-boot`, `calaos_rollback`, etc. — their own tickets (CC-032/033).

## Breaking changes

None to the interface (`calaos_install <disk>`), but behavior is stricter: it aborts earlier and more loudly on malformed input, and cleans up on failure.

## Acceptance criteria

- [x] `shellcheck scripts/calaos_install` is clean (needed for CC-035).
- [x] `bash -n scripts/calaos_install` passes.
- [x] An induced failure mid-install leaves **no** stray mounts or swap.
      Verified with a stubbed mount table rather than a VM / loop device (see
      the safety note below), which is why the loop-device part of this
      criterion does not apply: the script never attaches a loop device, so
      there is none to detach.
- [x] Re-running the installer does not produce duplicate fstab entries.
- [x] A missing container image produces a clear failure, not a silent continue.

## Verification notes

### Safety constraint on the verification

`calaos_install` was **never executed against any device** — not on the host,
not in a container, not against a loop device, and no device node was created
anywhere. Verification is static analysis plus function-level tests: the script
is now sourceable (its `main` is guarded by `BASH_SOURCE[0] == $0`), so the test
harness sources it — which defines the functions and runs nothing — and calls
individual functions with every dangerous command replaced by a logging stub on
`PATH`. The only real executions of the script are `--help`, a missing argument,
an unknown option, and a non-device path, all of which exit during argument
handling/validation; that test additionally strips `dd`/`parted`/`mkfs.*` from
`PATH` as a second net.

Harness: `stubs.sh` (logging stubs for `dd parted mkfs.vfat mkfs.btrfs mkswap
blkid lsblk mount umount swapon swapoff mountpoint btrfs chattr rsync genfstab
arch-chroot podman systemd-escape udevadm`, with a simulated mount table so
teardown assertions are meaningful, plus `STUB_FAIL_CMD` failure injection) and
`tests.sh`, run in a throwaway `ghcr.io/calaos/debian_deb_build_base` container
with the library installed at `/usr/lib/calaos/calaos-common.sh` (mode 0644, the
CC-030 contract path). **76 checks, 0 failures.**

### Static checks

- `shellcheck scripts/calaos_install` → **0 findings** (shellcheck 0.9.0). Also
  0 findings when invoked the way CI does (all shell files as inputs) and with
  `-x`. The file-level `# shellcheck disable=SC2034,SC2086` blanket suppression
  is gone — nothing is suppressed any more except two `SC2016` lines where the
  literal `${rootsubvol}` must reach `sed` unexpanded, and `source=/dev/null` on
  the two dynamic `source` sites (the library, and the `*.source` image files).
- `bash -n scripts/calaos_install` → clean.

### Command sequence: before vs after (nominal UEFI install)

"Before" is a static transcription of the pre-CC-031 script's UEFI path (the old
script was **not** executed), cross-checked against a mechanical
`grep -nE '^\s*(dd|parted|mkfs\.|...)'` extraction so no command line is missed.
"After" is the real stub log of the hardened step functions called in `main`'s
order, normalised back to the `${destination}`/`${dst}`/`${src}` placeholders.
The two sequences are identical except for four intentional groups:

```diff
 parted -s ${destination} print
+udevadm settle                      # let udev create the partition nodes
+lsblk --json ${destination}         # partition-count sanity check (3 for GPT, 2 for MBR)
 lsblk --json ${destination}         # x3, one per partition, now validated
...
-chattr +C ${dst}/@/var/cache ${dst}/@/var/log ${dst}/@/var/spool ${dst}/@/var/tmp
+chattr +C ${dst}/@/var/cache        # unquoted brace expansion replaced by a
+chattr +C ${dst}/@/var/log          # quoted loop - same four paths
+chattr +C ${dst}/@/var/spool
+chattr +C ${dst}/@/var/tmp
...
-podman images                       # piped into two greps that never matched
+podman images --format {{.Repository}}:{{.Tag}}   # exact match via calaos_image_exists
...
-umount ${dst}/.snapshots            # 14 bare umounts, no error handling
-umount ${dst}/boot/grub
-...                                 # (9 more)
-umount ${dst}/efi
-umount ${dst}
-umount ${src}
+swapoff ${destination_swap}         # single idempotent teardown, shared with
+mountpoint -q ${src}                # the cleanup trap
+umount -R ${src}
+mountpoint -q ${dst}
+umount -R ${dst}
+mountpoint -q ${src}                # EXIT trap re-runs teardown and finds
+mountpoint -q ${dst}                # nothing left to unmount
```

Everything else — the `dd`, the six `parted` calls, `mkfs.vfat`/`mkswap`/
`mkfs.btrfs`, `blkid`, all 13 `btrfs subvolume create`, `set-default`/
`get-default`, the remount, the 11 subvolume mounts, the EFI mount, `swapon`,
the `rsync` with its 13 excludes, `genfstab`, every `arch-chroot` call
(`update-initramfs`, snapper create-config, `grub-install`, `update-grub`,
`systemctl enable` with all five units, the six `snapper set-config`, the
`/var/run` symlink) — is byte-identical in content and order. The BIOS path was
checked the same way (`mklabel msdos`, two `mkpart`, `set 2 boot on`, no
`mkfs.vfat`, `grub-install --target=i386-pc ${destination}`).

### Failure behaviour (stub failure injection)

- `mkfs.btrfs` fails → aborts with "Failed to format the root partition"; the log
  contains **no** subsequent `mount -o noatime`, `rsync` or `grub-install`.
- `dd` fails → aborts before any `parted` runs.
- `rsync` fails mid-install (after all mounts + `swapon`) → aborts with a clear
  message, and the cleanup trap emits `swapoff ${destination_swap}`,
  `umount -R ${src}`, `umount -R ${dst}`; the simulated mount table is empty
  afterwards and nothing past the failure runs. This is the "no stray mounts,
  swap" acceptance criterion.
- A successful run leaves the simulated mount table empty too, and the EXIT trap
  after `finish` is a no-op (teardown is idempotent).

### Guards (each one aborts with a specific message)

`resolve_part` on a null lsblk result ("lsblk reported no such partition") and on
a non-block path ("is not a block device") — the old `/dev/$(get_dev_part …)`
could yield a bare `/dev/` and hand it to `mkfs`; unexpected partition count
("Expected 3 partitions"); empty `blkid` PARTUUID ("Could not read the PARTUUID")
— previously interpolated unchecked into the `systemd-escape` unit name; a
non-block argument (original message "No valid disk argument given" preserved for
the daemon); a target that is not a whole disk; a target that holds the running
rootfs (now also when it is the *parent disk* of `/`, which the old exact-string
compare missed); a target with anything mounted from it (protects the live
medium); a missing tool at preflight ("Missing required command(s): genfstab") —
this fires **before** the disk is touched, instead of wiping it and then failing
half way; a `.source` file without `IMAGE_SRC`; a missing `/etc/default/grub`.

### fstab idempotency

`generate_fstab` writes a marker line and regenerates everything below it into a
temp file next to the target, then `mv`s it into place. Running it twice
produced a byte-identical file with exactly one generated root entry, while the
pre-existing (live medium) fstab content above the marker was preserved:

```
# live medium fstab
LABEL=live-root / ext4 defaults 0 1
# --- calaos_install: generated by genfstab, do not edit below ---
# /dev/sdz3 LABEL=calaos-os
UUID=fake-root-uuid / btrfs rw,noatime,compress=zstd,subvol=/@/.snapshots/1/snapshot 0 0
UUID=fake-swap-uuid none swap defaults 0 0
```

### Container images

With a store containing only the look-alike `ghcr.io/calaos/calaos_base_extra:1.2.3`
and a wanted `ghcr.io/calaos/calaos_base:1.2.3`, the old
`podman images | grep "$base" | grep "$tag"` reports the image **present**
(false positive) while `calaos_image_exists` correctly reports it absent. A
failed `podman pull` now aborts with "Failed to pull container image … required
by /usr/share/calaos/<name>.source" instead of `err`-ing and continuing without
the image; an image still absent after a *successful* pull is also an error. The
cache-copy branch now creates `${dst}/var/lib/cache/containers` before the
`rsync` into it (it previously relied on a `mkdir -p` that only ran in the other
branch).

### cmdline parsing

The library parser was compared against the old `eval` parser by feeding both a
synthetic `/proc/cmdline` through a `cat` shim (neither implementation was
modified — both read it via `cat /proc/cmdline`). Identical results for
`LABEL=live-efi`, `LABEL=live-bios`, a cmdline with no `LABEL`, and one carrying
`rootflags=subvol=@/.snapshots/3/snapshot`. With the injected single token
`LABEL=live-efi;>/tmp/pwned`, the old parser **executed** it (file created) and
the library parser did not. `detect_install_mode` maps `live-efi` → UEFI and
everything else → BIOS.

**Deliberately preserved behaviour:** a *missing* `LABEL` selects BIOS mode. The
old `[ $LABEL == "live-efi" ]` with `LABEL` unset does not abort — it prints
`[: ==: unary operator expected` to stderr and falls into the `else` branch — so
BIOS is the established behaviour and dying there would have risked breaking
BIOS installs. The new code reaches the same decision without the bash error and
warns that it is assuming BIOS.

### Other changes worth noting for review

- The script is now `main` + one function per install step, called in the
  original order; this is what makes function-level testing possible without
  running the installer.
- `err_report` is kept (ERR trap, `set -E` so it fires inside functions too) and
  now also reports the failing command. Commands with an explicit `|| die` do not
  trigger it, so each failure produces exactly one actionable message.
- `parted … print` is now `|| true`: a failing informational print should not
  abort an install.
- `destination_esp` is no longer set to the *rootfs* partition in BIOS mode (it
  was dead in that branch — only ever used under the UEFI guard).
- The `btrfs subvolume list | grep -oP '(?<=ID )[0-9]+'` PCRE parse is replaced
  by `awk '$NF == "@/.snapshots/1/snapshot" { print $2 }'` (exact path field
  match, no PCRE dependency), with a non-empty check before `set-default`.
- `20_linux_xen` is now skipped with a warning when absent instead of aborting
  the install — it legitimately does not exist on systems without Xen.
- Interface unchanged for both callers: `calaos_install <disk>` (daemon,
  `app/system.go` → `models.RunCommandReader("calaos_install", …, n.Device)`;
  boot TUI, `config_calaos-boot` → `calaos_install "$disk"`), exit code 1 on
  validation failure, same "No valid disk argument given" message.
