#!/bin/bash
# Runs scripts/calaos_rollback against stubbed findmnt, mount, umount,
# mountpoint, btrfs, timeout, arch-chroot, mv and a fake /proc/cmdline: checks the
# device it hands to mount, that grub.cfg must follow the new default subvolume,
# and that a failure from snapper on puts the previous default subvolume and
# grub.cfg back and deletes the writable copy snapper made.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
fixtures="$here/../../app/testdata/grubcfg"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/lib"
dst="$tmp/run/calaos-rollback.private"
mkdir -p "$tmp/run"

# The script hardcodes the installed library, /proc/cmdline and /run.
sed "s#cat /proc/cmdline#cat $tmp/cmdline#" "$here/../lib/calaos-common.sh" >"$tmp/lib/calaos-common.sh"
sed -e "s#/usr/lib/calaos/calaos-common.sh#$tmp/lib/calaos-common.sh#" \
    -e "s#^run_dir=\"/run\"#run_dir=\"$tmp/run\"#" "$here/../calaos_rollback" >"$tmp/calaos_rollback"
grep -q "^run_dir=\"$tmp/run\"" "$tmp/calaos_rollback" || { echo "FAIL: run_dir not redirected"; exit 1; }
echo 'ro rootflags=subvol=@/.snapshots/5/snapshot' >"$tmp/cmdline"

cat >"$tmp/bin/findmnt" <<'STUB'
#!/bin/sh
case "$*" in
*FSTYPE*) echo overlay ;;
*SOURCE*) printf '%s\n' "$FAKE_SOURCE" ;;
esac
STUB
# FAKE_MOUNT_FAIL: fail the mounts whose arguments contain it;
# FAKE_MOUNT_FAIL_AT=N: fail the Nth mount call.
cat >"$tmp/bin/mount" <<STUB
#!/bin/sh
n=\$((\$(cat "$tmp/mounts" 2>/dev/null || echo 0) + 1))
echo "\$n" >"$tmp/mounts"
[ "\$n" != "\${FAKE_MOUNT_FAIL_AT:-}" ] || exit 32
case "\$*" in *"\${FAKE_MOUNT_FAIL:-^}"*) exit 32 ;; esac
echo "mount \$*" >>"$tmp/calls"
STUB
printf '#!/bin/sh\necho "umount $*" >>"%s/calls"\n' "$tmp" >"$tmp/bin/umount"
# timeout <kill-after> <duration> <command...>: reports an expiry on demand.
cat >"$tmp/bin/timeout" <<STUB
#!/bin/sh
echo "timeout \$1 \$2" >>"$tmp/calls"
[ -z "\$FAKE_TIMEOUT" ] || exit "\$FAKE_TIMEOUT"
shift 2
exec "\$@"
STUB
# The default subvolume lives in $tmp/default: 307 is the previous root, 320
# the writable copy snapper rollback makes the default. Every call needs $dst
# mounted, as on a real box.
prev_default='ID 307 gen 300 top level 257 path @/.snapshots/39/snapshot'
copy_default='ID 320 gen 310 top level 257 path @/.snapshots/40/snapshot'
cat >"$tmp/bin/btrfs" <<STUB
#!/bin/sh
m=\$(grep -c "^mount .* $dst\$" "$tmp/calls")
u=\$(grep -c "^umount $dst\$" "$tmp/calls")
[ "\$m" -gt "\$u" ] || { echo "btrfs: $dst not mounted" >&2; exit 1; }
case "\$*" in
"subvolume list"*) printf 'ID 258 gen 10 top level 256 path %s\n' \$FAKE_SUBVOLS ;;
"subvolume get-default"*) cat "$tmp/default" ;;
"subvolume set-default"*)
    echo "btrfs \$*" >>"$tmp/calls"
    [ -z "\$FAKE_SETDEFAULT_FAIL" ] || exit 1
    [ "\$3" != 307 ] || echo '$prev_default' >"$tmp/default"
    ;;
esac
STUB
# snapper rollback makes the copy the default, failing before or after it on
# demand; update-grub "writes" grub.cfg from a fixture, or fails when none is given.
cat >"$tmp/bin/arch-chroot" <<STUB
#!/bin/sh
echo "arch-chroot \$*" >>"$tmp/calls"
case "\$*" in
*"snapper --no-dbus rollback"*)
    [ "\$FAKE_SNAPPER_FAIL" != early ] || exit 1
    echo '$copy_default' >"$tmp/default"
    [ "\$FAKE_SNAPPER_FAIL" != late ] || exit 1
    exit 0
    ;;
*"snapper --no-dbus delete"*) [ -z "\$FAKE_DELETE_FAIL" ] || exit 1 ;;
esac
[ "\$2" = update-grub ] || exit 0
echo "GRUB_BTRFS_DISABLE=\${GRUB_BTRFS_DISABLE:-}" >>"$tmp/calls"
# fd 9 holds the rollback lock: update-grub must inherit it.
{ flock -n 9 && echo "update-grub holds fd 9" >>"$tmp/calls"; } 2>/dev/null
[ -z "\$FAKE_NO_MARKER" ] || { mkdir -p "\$1/var/lib" && : >"\$1/var/lib/calaos-container"; }
[ -n "\$FAKE_GRUBCFG" ] || exit 1
mkdir -p "\$1/boot/grub" && cp "\$FAKE_GRUBCFG" "\$1/boot/grub/grub.cfg"
STUB
cat >"$tmp/bin/mountpoint" <<STUB
#!/bin/sh
case "\$2" in
/efi) exit "\${FAKE_EFI:-1}" ;;
*stale*) [ -z "\$FAKE_STALE" ] || exit 0; exit 1 ;;
*) exit 1 ;;
esac
STUB
# mktemp -d <template>: hands out the fixed private mount point and records it.
realmktemp=$(command -v mktemp)
cat >"$tmp/bin/mktemp" <<STUB
#!/bin/sh
case "\$*" in
"-d $tmp/run/calaos-rollback.XXXXXX") echo "mktemp \$*" >>"$tmp/calls"; mkdir -p "$dst"; echo "$dst" ;;
*) exec "$realmktemp" "\$@" ;;
esac
STUB
cat >"$tmp/bin/rmdir" <<STUB
#!/bin/sh
echo "rmdir \$*" >>"$tmp/calls"
STUB
realmv=$(command -v mv)
cat >"$tmp/bin/mv" <<STUB
#!/bin/sh
echo "mv \$*" >>"$tmp/calls"
[ -z "\$FAKE_MV_FAIL" ] || exit 1
exec "$realmv" "\$@"
STUB
chmod +x "$tmp"/bin/*

failures=0
fail() {
    echo "FAIL: $*"
    failures=$((failures + 1))
}

marker="$dst/var/lib/calaos-container/grub-refresh"

# run <findmnt source> <grub.cfg written by update-grub> [subvolumes]
run() {
    rm -rf "$dst"
    mkdir -p "$dst/.snapshots/5/snapshot/usr/sbin" "$dst/.snapshots/39"
    [ -z "${FAKE_EXISTING:-}" ] || mkdir -p "$dst/.snapshots/$FAKE_EXISTING"
    [ "${FAKE_EFI:-1}" != 0 ] || mkdir -p "$dst/efi"
    printf '#!/bin/sh\n' >"$dst/.snapshots/5/snapshot/usr/sbin/update-grub"
    chmod +x "$dst/.snapshots/5/snapshot/usr/sbin/update-grub"
    mkdir -p "$dst/boot/grub"
    echo '# previous' >"$dst/boot/grub/grub.cfg"
    : >"$tmp/calls"
    rm -f "$tmp/mounts"
    echo "$prev_default" >"$tmp/default"
    rc=0
    FAKE_SOURCE=$1 FAKE_GRUBCFG=$2 FAKE_SUBVOLS=${3:-"@ @/.snapshots @/boot/grub"} PATH="$tmp/bin:$PATH" \
        bash "$tmp/calaos_rollback" >/dev/null 2>"$tmp/err" || rc=$?
}

# private_mounts <name>: /mnt is never touched, every mount sits in the private
# directory made by mktemp, and that directory is removed on the way out.
private_mounts() {
    ! grep -qE '^(mount|umount) .*(^| )/mnt($|/)' "$tmp/calls" || fail "$1: touched /mnt: $(cat "$tmp/calls")"
    grep -qx "mktemp -d $tmp/run/calaos-rollback.XXXXXX" "$tmp/calls" || fail "$1: no private mount point: $(cat "$tmp/calls")"
    ! grep '^mount ' "$tmp/calls" | awk -v d="$dst" '{ t = $NF; if (t != d && index(t, d "/") != 1 && t != "/efi") bad = 1 } END { exit !bad }' ||
        fail "$1: mount outside the private directory: $(cat "$tmp/calls")"
    grep -qx "rmdir $dst" "$tmp/calls" || fail "$1: private directory not removed: $(cat "$tmp/calls")"
    [ "$(grep -c '^umount ' "$tmp/calls")" -eq "$(grep -c '^mount ' "$tmp/calls")" ] || fail "$1: mounts left"
}

# check_ok <name> <findmnt source> <expected device>
check_ok() {
    run "$2" "$fixtures/current.cfg"
    [ "$rc" -eq 0 ] || fail "$1: rc=$rc: $(cat "$tmp/err")"
    grep -qx "mount -o noatime,compress=zstd $3 $dst" "$tmp/calls" || fail "$1: root mount: $(cat "$tmp/calls")"
    grep -qx "mount -o noatime,compress=zstd,subvol=@/.snapshots $3 $dst/.snapshots" "$tmp/calls" ||
        fail "$1: snapshots mount: $(cat "$tmp/calls")"
    grep -qx "mount -o noatime,compress=zstd,subvol=@/boot/grub $3 $dst/boot/grub" "$tmp/calls" ||
        fail "$1: grub mount: $(cat "$tmp/calls")"
    grep -qx "arch-chroot $dst update-grub" "$tmp/calls" || fail "$1: no update-grub: $(cat "$tmp/calls")"
    grep -qx "timeout --kill-after=30s 10m" "$tmp/calls" || fail "$1: update-grub not bounded: $(cat "$tmp/calls")"
    grep -qx "GRUB_BTRFS_DISABLE=true" "$tmp/calls" || fail "$1: grub-btrfs not disabled: $(cat "$tmp/calls")"
    grep -qx "update-grub holds fd 9" "$tmp/calls" || fail "$1: update-grub lost the lock fd: $(cat "$tmp/calls")"
    [ -f "$marker" ] || fail "$1: no refresh marker"
    private_mounts "$1"
    ! grep -qE "set-default|snapper --no-dbus delete" "$tmp/calls" || fail "$1: undone: $(cat "$tmp/calls")"
}

# copy_deleted: the writable copy went, once the default was back.
copy_deleted() {
    local set del
    set=$(grep -nx "btrfs subvolume set-default 307 $dst" "$tmp/calls" | head -n 1 | cut -d: -f1)
    del=$(grep -nx "arch-chroot $dst snapper --no-dbus delete 40" "$tmp/calls" | cut -d: -f1)
    [ -n "$del" ] && [ -n "$set" ] && [ "$set" -lt "$del" ]
}

# check_fails <name> <expected stderr> <snapper must have run: yes|no> <run args...>
check_fails() {
    local name=$1 msg=$2 snapper=$3
    shift 3
    run "$@"
    [ "$rc" -ne 0 ] || fail "$name: rc=0"
    grep -qF "$msg" "$tmp/err" || fail "$name: stderr: $(cat "$tmp/err")"
    [ ! -f "$marker" ] || fail "$name: refresh marker written"
    if grep -q "snapper --no-dbus rollback" "$tmp/calls"; then
        [ "$snapper" = yes ] || fail "$name: snapper ran"
    else
        [ "$snapper" = no ] || fail "$name: snapper did not run"
    fi
    if [ "$snapper" = yes ]; then
        grep -qx "btrfs subvolume set-default 307 $dst" "$tmp/calls" || fail "$name: default not restored: $(cat "$tmp/calls")"
        grep -qF "the default subvolume is back to @/.snapshots/39/snapshot" "$tmp/err" || fail "$name: stderr: $(cat "$tmp/err")"
        grep -qx '# previous' "$dst/boot/grub/grub.cfg" || fail "$name: grub.cfg not put back"
        copy_deleted || fail "$name: writable copy not deleted: $(cat "$tmp/calls")"
    else
        ! grep -qE "set-default|snapper --no-dbus delete" "$tmp/calls" || fail "$name: default changed: $(cat "$tmp/calls")"
    fi
    # Every mount must be undone, the last one first.
    local mounts umounts
    mounts=$(grep -c '^mount ' "$tmp/calls")
    umounts=$(grep -c '^umount ' "$tmp/calls")
    [ "$mounts" -eq "$umounts" ] || fail "$name: $mounts mounts, $umounts umounts"
    ! grep -q "Error on line" "$tmp/err" || fail "$name: generic error: $(cat "$tmp/err")"
    private_mounts "$name"
}

check_ok "subvolume source" "/dev/vda3[/@/.snapshots]" /dev/vda3
check_ok "plain source" /dev/vda3 /dev/vda3

check_fails "stale grub.cfg" "the default grub entry loads /@/.snapshots/31/snapshot/boot/vmlinuz" yes \
    /dev/vda3 "$fixtures/stale.cfg"
# grub.cfg comes back through a rename in its own directory, never rewritten in place.
grep -qx "mv -f $dst/boot/grub/grub.cfg.calaos-rollback $dst/boot/grub/grub.cfg" "$tmp/calls" ||
    fail "stale grub.cfg: not renamed into place: $(cat "$tmp/calls")"
check_fails "update-grub fails" "update-grub failed" yes /dev/vda3 ""
printf 'set default=0\n' >"$tmp/empty.cfg"
check_fails "no boot entry" "grub.cfg has no boot entry" yes /dev/vda3 "$tmp/empty.cfg"
check_fails "no @/boot/grub" "No @/boot/grub subvolume" no /dev/vda3 "$fixtures/current.cfg" "@ @/.snapshots"
after="Rollback undone, grub.cfg could not follow it"
FAKE_MOUNT_FAIL="subvol=@/boot/grub" check_fails "grub mount fails" "$after (command failed at line" yes \
    /dev/vda3 "$fixtures/current.cfg"
FAKE_TIMEOUT=124 check_fails "update-grub times out" "$after (update-grub timed out after 10m)" yes \
    /dev/vda3 "$fixtures/current.cfg"
FAKE_TIMEOUT=137 check_fails "update-grub killed after the timeout" "$after (update-grub timed out after 10m)" yes \
    /dev/vda3 "$fixtures/current.cfg"
# Unmounted to remount the new default, $dst is mounted again to undo.
FAKE_MOUNT_FAIL_AT=3 check_fails "new root mount fails" "$after (command failed at line" yes \
    /dev/vda3 "$fixtures/current.cfg"
FAKE_SNAPPER_FAIL=late check_fails "snapper fails after its set-default" \
    "Rollback undone, snapper rollback failed: the default subvolume is back to" yes /dev/vda3 "$fixtures/current.cfg"

# Failing before its set-default, snapper made no copy: nothing to delete.
FAKE_SNAPPER_FAIL=early run /dev/vda3 "$fixtures/current.cfg"
[ "$rc" -ne 0 ] || fail "snapper fails early: rc=0"
grep -qF "Rollback undone, snapper rollback failed" "$tmp/err" || fail "snapper fails early: stderr: $(cat "$tmp/err")"
! grep -q "snapper --no-dbus delete" "$tmp/calls" || fail "snapper fails early: deleted: $(cat "$tmp/calls")"
[ "$(grep -c '^mount ' "$tmp/calls")" -eq "$(grep -c '^umount ' "$tmp/calls")" ] || fail "snapper fails early: mounts left"

# A new default that existed before the rollback is never deleted.
FAKE_EXISTING=40 run /dev/vda3 ""
[ "$rc" -ne 0 ] || fail "existing default: rc=0"
grep -qx "btrfs subvolume set-default 307 $dst" "$tmp/calls" || fail "existing default: not undone: $(cat "$tmp/calls")"
! grep -q "snapper --no-dbus delete" "$tmp/calls" || fail "existing default: deleted: $(cat "$tmp/calls")"

# The copy could not be deleted: the rollback is still undone, the copy named.
FAKE_DELETE_FAIL=1 run /dev/vda3 ""
[ "$rc" -ne 0 ] || fail "delete fails: rc=0"
grep -qF "Snapshot #40, the writable copy of #5, is left: delete it with snapper delete 40" "$tmp/err" ||
    fail "delete fails: stderr: $(cat "$tmp/err")"
case "$(tail -n 1 "$tmp/err")" in
*"Rollback undone, grub.cfg could not follow it (update-grub failed)"*) ;;
*) fail "delete fails: last line: $(cat "$tmp/err")" ;;
esac
[ "$(grep -c '^mount ' "$tmp/calls")" -eq "$(grep -c '^umount ' "$tmp/calls")" ] || fail "delete fails: mounts left"

# grub.cfg could not be put back: the default is, the copy stays (grub.cfg
# loads its kernel), and grub.cfg is whole, never half copied.
FAKE_MV_FAIL=1 run /dev/vda3 "$fixtures/stale.cfg"
[ "$rc" -ne 0 ] || fail "grub.cfg not put back: rc=0"
case "$(tail -n 1 "$tmp/err")" in
*"Rollback undone but grub.cfg could not be put back, do not reboot: grub.cfg could not follow it (the default grub entry"*) ;;
*) fail "grub.cfg not put back: stderr: $(cat "$tmp/err")" ;;
esac
grep -qx "btrfs subvolume set-default 307 $dst" "$tmp/calls" || fail "grub.cfg not put back: default: $(cat "$tmp/calls")"
! grep -q "snapper --no-dbus delete" "$tmp/calls" || fail "grub.cfg not put back: copy deleted: $(cat "$tmp/calls")"
cmp -s "$fixtures/stale.cfg" "$dst/boot/grub/grub.cfg" || fail "grub.cfg not put back: grub.cfg changed"
[ ! -e "$dst/boot/grub/grub.cfg.calaos-rollback" ] || fail "grub.cfg not put back: temporary file left"
[ "$(grep -c '^mount ' "$tmp/calls")" -eq "$(grep -c '^umount ' "$tmp/calls")" ] || fail "grub.cfg not put back: mounts left"

# The default could not be put back: the message says so and gives the id
# first, since the API keeps only the first 200 characters of that line.
FAKE_SETDEFAULT_FAIL=1 run /dev/vda3 "$fixtures/stale.cfg"
[ "$rc" -ne 0 ] || fail "restore fails: rc=0"
grep -qx "btrfs subvolume set-default 307 $dst" "$tmp/calls" || fail "restore fails: not tried: $(cat "$tmp/calls")"
last=$(tail -n 1 "$tmp/err" | sed 's/\x1b\[[0-9;]*m//g')
case "${last:0:200}" in
"Could not undo the rollback, do not reboot: setting the default subvolume back to 307 failed"*) ;;
*) fail "restore fails: stderr: $(cat "$tmp/err")" ;;
esac
grep -qF "after: grub.cfg could not follow it (the default grub entry loads" "$tmp/err" ||
    fail "restore fails: no cause: $(cat "$tmp/err")"
! grep -q "Rollback undone" "$tmp/err" || fail "restore fails: claims undone: $(cat "$tmp/err")"
! grep -q "snapper --no-dbus delete" "$tmp/calls" || fail "restore fails: copy deleted: $(cat "$tmp/calls")"
[ "$(grep -c '^mount ' "$tmp/calls")" -eq "$(grep -c '^umount ' "$tmp/calls")" ] || fail "restore fails: mounts left"

# A rollback holds the lock, possibly through an orphaned update-grub: nothing
# is mounted, made or removed, orphans included.
mkdir -p "$tmp/run/calaos-rollback.stale"
exec 8>"$tmp/run/calaos-rollback.lock"
flock -n 8
FAKE_STALE=1 run /dev/vda3 "$fixtures/current.cfg"
exec 8>&-
rm -rf "$tmp/run/calaos-rollback.stale"
[ "$rc" -ne 0 ] || fail "locked: rc=0"
grep -qF "Another calaos_rollback is running" "$tmp/err" || fail "locked: stderr: $(cat "$tmp/err")"
! grep -qE '^(mount|umount|rmdir|mktemp|arch-chroot|btrfs)' "$tmp/calls" || fail "locked: acted: $(cat "$tmp/calls")"

# Mounts of a killed rollback are released once its lock is free.
mkdir -p "$tmp/run/calaos-rollback.stale"
FAKE_STALE=1 run /dev/vda3 "$fixtures/current.cfg"
[ "$rc" -eq 0 ] || fail "stale: rc=$rc: $(cat "$tmp/err")"
grep -qx "umount -R $tmp/run/calaos-rollback.stale/" "$tmp/calls" || fail "stale: not unmounted: $(cat "$tmp/calls")"
grep -qx "rmdir $tmp/run/calaos-rollback.stale/" "$tmp/calls" || fail "stale: not removed: $(cat "$tmp/calls")"
rm -rf "$tmp/run/calaos-rollback.stale"

# A bare linux line is not an entry, as in app/grubrepair.go.
{ printf 'linux\n'; cat "$fixtures/current.cfg"; } >"$tmp/bare.cfg"
run /dev/vda3 "$tmp/bare.cfg"
[ "$rc" -eq 0 ] || fail "bare linux line: rc=$rc: $(cat "$tmp/err")"

# Once grub.cfg is right the rollback has succeeded, marker or not.
FAKE_NO_MARKER=1 run /dev/vda3 "$fixtures/current.cfg"
[ "$rc" -eq 0 ] || fail "marker not written: rc=$rc: $(cat "$tmp/err")"
grep -qF "Could not write $marker" "$tmp/err" || fail "marker not written: stderr: $(cat "$tmp/err")"

# UEFI: the ESP is bound into the chroot for the one-shot block, then released.
FAKE_EFI=0 run /dev/vda3 "$fixtures/current.cfg"
[ "$rc" -eq 0 ] || fail "efi: rc=$rc: $(cat "$tmp/err")"
grep -qx "mount --bind /efi $dst/efi" "$tmp/calls" || fail "efi: no bind: $(cat "$tmp/calls")"
grep -qx "umount $dst/efi" "$tmp/calls" || fail "efi: not unmounted: $(cat "$tmp/calls")"

# The grub-btrfs config is overridden by a bind mount, never edited.
run /dev/vda3 "$fixtures/current.cfg"
mkdir -p "$dst/etc/default/grub-btrfs"
echo 'GRUB_BTRFS_DISABLE="false"' >"$dst/etc/default/grub-btrfs/config"
: >"$tmp/calls"
echo "$prev_default" >"$tmp/default"
FAKE_SOURCE=/dev/vda3 FAKE_GRUBCFG="$fixtures/current.cfg" FAKE_SUBVOLS="@ @/boot/grub" PATH="$tmp/bin:$PATH" \
    bash "$tmp/calaos_rollback" >/dev/null 2>"$tmp/err" || fail "grub-btrfs: $(cat "$tmp/err")"
grep -q "^mount --bind .* $dst/etc/default/grub-btrfs/config$" "$tmp/calls" || fail "grub-btrfs: no bind: $(cat "$tmp/calls")"
grep -qx 'GRUB_BTRFS_DISABLE="false"' "$dst/etc/default/grub-btrfs/config" || fail "grub-btrfs: config edited"

[ "$failures" -eq 0 ] && echo "calaos_rollback_test: ok"
exit "$failures"
