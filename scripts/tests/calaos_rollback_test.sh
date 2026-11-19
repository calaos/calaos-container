#!/bin/bash
# Runs scripts/calaos_rollback against stubbed findmnt, mount, umount,
# mountpoint, btrfs, timeout, arch-chroot and a fake /proc/cmdline: checks the device it
# hands to mount, that grub.cfg must follow the new default subvolume, and that
# a failure after snapper puts the previous default subvolume and grub.cfg back.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
fixtures="$here/../../app/testdata/grubcfg"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/lib"
dst="$tmp/mnt"

# The script hardcodes the installed library, /proc/cmdline and /mnt.
sed "s#cat /proc/cmdline#cat $tmp/cmdline#" "$here/../lib/calaos-common.sh" >"$tmp/lib/calaos-common.sh"
sed -e "s#/usr/lib/calaos/calaos-common.sh#$tmp/lib/calaos-common.sh#" \
    -e "s#^dst=\"/mnt\"#dst=\"$dst\"#" "$here/../calaos_rollback" >"$tmp/calaos_rollback"
echo 'ro rootflags=subvol=@/.snapshots/5/snapshot' >"$tmp/cmdline"

cat >"$tmp/bin/findmnt" <<'STUB'
#!/bin/sh
case "$*" in
*FSTYPE*) echo overlay ;;
*SOURCE*) printf '%s\n' "$FAKE_SOURCE" ;;
esac
STUB
cat >"$tmp/bin/mount" <<STUB
#!/bin/sh
case "\$*" in *"\${FAKE_MOUNT_FAIL:-^}"*) exit 32 ;; esac
echo "mount \$*" >>"$tmp/calls"
STUB
printf '#!/bin/sh\necho "umount $*" >>"%s/calls"\n' "$tmp" >"$tmp/bin/umount"
# timeout <kill-after> <duration> <command...>: reports an expiry on demand.
cat >"$tmp/bin/timeout" <<STUB
#!/bin/sh
echo "timeout \$1 \$2" >>"$tmp/calls"
[ -z "\$FAKE_TIMEOUT" ] || exit 124
shift 2
exec "\$@"
STUB
cat >"$tmp/bin/btrfs" <<STUB
#!/bin/sh
case "\$*" in
"subvolume list"*) printf 'ID 258 gen 10 top level 256 path %s\n' \$FAKE_SUBVOLS ;;
"subvolume get-default"*) echo 'ID 307 gen 300 top level 257 path @/.snapshots/40/snapshot' ;;
"subvolume set-default"*)
    echo "btrfs \$*" >>"$tmp/calls"
    [ -z "\$FAKE_SETDEFAULT_FAIL" ] || exit 1
    ;;
esac
STUB
# update-grub "writes" grub.cfg from a fixture, or fails when none is given.
cat >"$tmp/bin/arch-chroot" <<STUB
#!/bin/sh
echo "arch-chroot \$*" >>"$tmp/calls"
[ "\$2" = update-grub ] || exit 0
echo "GRUB_BTRFS_DISABLE=\${GRUB_BTRFS_DISABLE:-}" >>"$tmp/calls"
[ -z "\$FAKE_NO_MARKER" ] || { mkdir -p "\$1/var/lib" && : >"\$1/var/lib/calaos-container"; }
[ -n "\$FAKE_GRUBCFG" ] || exit 1
mkdir -p "\$1/boot/grub" && cp "\$FAKE_GRUBCFG" "\$1/boot/grub/grub.cfg"
STUB
cat >"$tmp/bin/mountpoint" <<'STUB'
#!/bin/sh
exit "${FAKE_EFI:-1}"
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
    mkdir -p "$dst/.snapshots/5/snapshot/usr/sbin"
    [ "${FAKE_EFI:-1}" != 0 ] || mkdir -p "$dst/efi"
    printf '#!/bin/sh\n' >"$dst/.snapshots/5/snapshot/usr/sbin/update-grub"
    chmod +x "$dst/.snapshots/5/snapshot/usr/sbin/update-grub"
    mkdir -p "$dst/boot/grub"
    echo '# previous' >"$dst/boot/grub/grub.cfg"
    : >"$tmp/calls"
    rc=0
    FAKE_SOURCE=$1 FAKE_GRUBCFG=$2 FAKE_SUBVOLS=${3:-"@ @/.snapshots @/boot/grub"} PATH="$tmp/bin:$PATH" \
        bash "$tmp/calaos_rollback" >/dev/null 2>"$tmp/err" || rc=$?
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
    [ -f "$marker" ] || fail "$1: no refresh marker"
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
        grep -qF "the default subvolume is back to @/.snapshots/40/snapshot" "$tmp/err" || fail "$name: stderr: $(cat "$tmp/err")"
        grep -qx '# previous' "$dst/boot/grub/grub.cfg" || fail "$name: grub.cfg not put back"
    else
        ! grep -q "set-default" "$tmp/calls" || fail "$name: default changed: $(cat "$tmp/calls")"
    fi
    # Every mount must be undone, the last one first.
    local mounts umounts
    mounts=$(grep -c '^mount ' "$tmp/calls")
    umounts=$(grep -c '^umount ' "$tmp/calls")
    [ "$mounts" -eq "$umounts" ] || fail "$name: $mounts mounts, $umounts umounts"
    ! grep -q "Error on line" "$tmp/err" || fail "$name: generic error: $(cat "$tmp/err")"
}

check_ok "subvolume source" "/dev/vda3[/@/.snapshots]" /dev/vda3
check_ok "plain source" /dev/vda3 /dev/vda3

check_fails "stale grub.cfg" "the default grub entry loads /@/.snapshots/31/snapshot/boot/vmlinuz" yes \
    /dev/vda3 "$fixtures/stale.cfg"
check_fails "update-grub fails" "update-grub failed" yes /dev/vda3 ""
printf 'set default=0\n' >"$tmp/empty.cfg"
check_fails "no boot entry" "grub.cfg has no boot entry" yes /dev/vda3 "$tmp/empty.cfg"
check_fails "no @/boot/grub" "No @/boot/grub subvolume" no /dev/vda3 "$fixtures/current.cfg" "@ @/.snapshots"
after="Rollback undone, grub.cfg could not follow it"
FAKE_MOUNT_FAIL="subvol=@/boot/grub" check_fails "grub mount fails" "$after (command failed at line" yes \
    /dev/vda3 "$fixtures/current.cfg"
FAKE_TIMEOUT=1 check_fails "update-grub times out" "$after (update-grub timed out after 10m)" yes \
    /dev/vda3 "$fixtures/current.cfg"

# The default could not be put back: the message says so and comes first,
# since the API keeps only the first 200 characters of that line.
FAKE_SETDEFAULT_FAIL=1 run /dev/vda3 ""
[ "$rc" -ne 0 ] || fail "restore fails: rc=0"
grep -qx "btrfs subvolume set-default 307 $dst" "$tmp/calls" || fail "restore fails: not tried: $(cat "$tmp/calls")"
last=$(tail -n 1 "$tmp/err")
case "$last" in
*"Could not undo the rollback, do not reboot: grub.cfg not updated (update-grub failed)"*) ;;
*) fail "restore fails: stderr: $(cat "$tmp/err")" ;;
esac
grep -qF "restoring the default subvolume 307 failed" "$tmp/err" || fail "restore fails: stderr: $(cat "$tmp/err")"
! grep -q "Rollback undone" "$tmp/err" || fail "restore fails: claims undone: $(cat "$tmp/err")"
[ "$(grep -c '^mount ' "$tmp/calls")" -eq "$(grep -c '^umount ' "$tmp/calls")" ] || fail "restore fails: mounts left"

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
FAKE_SOURCE=/dev/vda3 FAKE_GRUBCFG="$fixtures/current.cfg" FAKE_SUBVOLS="@ @/boot/grub" PATH="$tmp/bin:$PATH" \
    bash "$tmp/calaos_rollback" >/dev/null 2>"$tmp/err" || fail "grub-btrfs: $(cat "$tmp/err")"
grep -q "^mount --bind .* $dst/etc/default/grub-btrfs/config$" "$tmp/calls" || fail "grub-btrfs: no bind: $(cat "$tmp/calls")"
grep -qx 'GRUB_BTRFS_DISABLE="false"' "$dst/etc/default/grub-btrfs/config" || fail "grub-btrfs: config edited"

[ "$failures" -eq 0 ] && echo "calaos_rollback_test: ok"
exit "$failures"
