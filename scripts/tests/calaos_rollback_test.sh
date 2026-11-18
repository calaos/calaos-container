#!/bin/bash
# Runs scripts/calaos_rollback against stubbed findmnt, mount, umount,
# mountpoint, arch-chroot and a fake /proc/cmdline, and checks the device it
# hands to mount.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/lib"

# The script hardcodes the installed library and /proc/cmdline.
sed "s#cat /proc/cmdline#cat $tmp/cmdline#" "$here/../lib/calaos-common.sh" >"$tmp/lib/calaos-common.sh"
sed "s#/usr/lib/calaos/calaos-common.sh#$tmp/lib/calaos-common.sh#" "$here/../calaos_rollback" >"$tmp/calaos_rollback"
echo 'ro rootflags=subvol=@/.snapshots/5/snapshot' >"$tmp/cmdline"

cat >"$tmp/bin/findmnt" <<'STUB'
#!/bin/sh
case "$*" in
*FSTYPE*) echo overlay ;;
*SOURCE*) printf '%s\n' "$FAKE_SOURCE" ;;
esac
STUB
for c in mount umount arch-chroot; do
    printf '#!/bin/sh\necho "%s $*" >>"%s/calls"\n' "$c" "$tmp" >"$tmp/bin/$c"
done
printf '#!/bin/sh\nexit 1\n' >"$tmp/bin/mountpoint"
chmod +x "$tmp"/bin/*

failures=0
fail() {
    echo "FAIL: $*"
    failures=$((failures + 1))
}

# check <name> <findmnt source> <expected device>
check() {
    local rc=0
    : >"$tmp/calls"
    FAKE_SOURCE=$2 PATH="$tmp/bin:$PATH" bash "$tmp/calaos_rollback" >/dev/null 2>"$tmp/err" || rc=$?
    [ "$rc" -eq 0 ] || fail "$1: rc=$rc: $(cat "$tmp/err")"
    grep -qx "mount -o noatime,compress=zstd $3 /mnt" "$tmp/calls" || fail "$1: root mount: $(cat "$tmp/calls")"
    grep -qx "mount -o noatime,compress=zstd,subvol=@/.snapshots $3 /mnt/.snapshots" "$tmp/calls" ||
        fail "$1: snapshots mount: $(cat "$tmp/calls")"
}

check "subvolume source" "/dev/vda3[/@/.snapshots]" /dev/vda3
check "plain source" /dev/vda3 /dev/vda3

[ "$failures" -eq 0 ] && echo "calaos_rollback_test: ok"
exit "$failures"
