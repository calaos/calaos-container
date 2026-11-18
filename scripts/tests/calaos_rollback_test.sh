#!/bin/bash
# Runs scripts/calaos_rollback against stubbed findmnt, mount, umount,
# mountpoint, btrfs, arch-chroot and a fake /proc/cmdline: checks the device it
# hands to mount and that grub.cfg must follow the new default subvolume.
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
for c in mount umount; do
    printf '#!/bin/sh\necho "%s $*" >>"%s/calls"\n' "$c" "$tmp" >"$tmp/bin/$c"
done
cat >"$tmp/bin/btrfs" <<'STUB'
#!/bin/sh
case "$*" in
"subvolume list"*) printf 'ID 258 gen 10 top level 256 path %s\n' $FAKE_SUBVOLS ;;
"subvolume get-default"*) echo 'ID 307 gen 300 top level 257 path @/.snapshots/40/snapshot' ;;
esac
STUB
# update-grub "writes" grub.cfg from a fixture, or fails when none is given.
cat >"$tmp/bin/arch-chroot" <<STUB
#!/bin/sh
echo "arch-chroot \$*" >>"$tmp/calls"
[ "\$2" = update-grub ] || exit 0
[ -n "\$FAKE_GRUBCFG" ] || exit 1
mkdir -p "\$1/boot/grub" && cp "\$FAKE_GRUBCFG" "\$1/boot/grub/grub.cfg"
STUB
printf '#!/bin/sh\nexit 1\n' >"$tmp/bin/mountpoint"
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
    printf '#!/bin/sh\n' >"$dst/.snapshots/5/snapshot/usr/sbin/update-grub"
    chmod +x "$dst/.snapshots/5/snapshot/usr/sbin/update-grub"
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
    # Every mount must be undone, the last one first.
    local mounts umounts
    mounts=$(grep -c '^mount ' "$tmp/calls")
    umounts=$(grep -c '^umount ' "$tmp/calls")
    [ "$mounts" -eq "$umounts" ] || fail "$name: $mounts mounts, $umounts umounts"
}

check_ok "subvolume source" "/dev/vda3[/@/.snapshots]" /dev/vda3
check_ok "plain source" /dev/vda3 /dev/vda3

check_fails "stale grub.cfg" "the default grub entry loads /@/.snapshots/31/snapshot/boot/vmlinuz" yes \
    /dev/vda3 "$fixtures/stale.cfg"
check_fails "update-grub fails" "update-grub failed" yes /dev/vda3 ""
printf 'set default=0\n' >"$tmp/empty.cfg"
check_fails "no boot entry" "grub.cfg has no boot entry" yes /dev/vda3 "$tmp/empty.cfg"
check_fails "no @/boot/grub" "No @/boot/grub subvolume" no /dev/vda3 "$fixtures/current.cfg" "@ @/.snapshots"

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
