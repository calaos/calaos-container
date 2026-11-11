#!/bin/bash
#
# preinst_overlay_test.sh - runs debian/calaos-container.preinst against a
# temporary DPKG_ROOT and checks the removal of the misplaced
# grub-btrfs-overlay directories.

set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
preinst=${1:-$here/../../debian/calaos-container.preinst}
fails=0

fail() {
    echo "FAIL: $*"
    fails=$((fails + 1))
}

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
hooks=$root/usr/share/initramfs-tools/hooks
bottom=$root/usr/share/initramfs-tools/scripts/local-bottom

run() {
    DPKG_ROOT=$root bash "$preinst" "$@" 2>"$root/err"
}

reset() {
    rm -rf "${root:?}/usr"
    mkdir -p "$hooks" "$bottom"
}

# Old layout: directory containing the file.
reset
for d in "$hooks" "$bottom"; do
    mkdir "$d/grub-btrfs-overlay"
    echo old >"$d/grub-btrfs-overlay/grub-btrfs-overlay"
done
echo keep >"$hooks/other"
run upgrade 4.4.0 || fail "upgrade: rc=$?"
for d in "$hooks" "$bottom"; do
    [ ! -e "$d/grub-btrfs-overlay" ] || fail "$d: misplaced dir still there"
done
[ -f "$hooks/other" ] || fail "unrelated hook removed"
run upgrade 4.4.0 || fail "second pass: rc=$?"

# Already a file: untouched.
reset
echo good >"$hooks/grub-btrfs-overlay"
run upgrade 4.5.0 || fail "file case: rc=$?"
[ "$(cat "$hooks/grub-btrfs-overlay")" = good ] || fail "regular file modified"

# Directory with extra content: untouched, message.
reset
mkdir "$hooks/grub-btrfs-overlay"
echo old >"$hooks/grub-btrfs-overlay/grub-btrfs-overlay"
echo mine >"$hooks/grub-btrfs-overlay/extra"
run upgrade 4.4.0 || fail "extra content: rc=$?"
[ -f "$hooks/grub-btrfs-overlay/extra" ] || fail "dir with extra content removed"
grep -q "unexpected content" "$root/err" || fail "extra content: no message"

# Symlink to a directory: untouched.
reset
mkdir "$root/target"
ln -s "$root/target" "$hooks/grub-btrfs-overlay"
run upgrade 4.4.0 || fail "symlink: rc=$?"
[ -L "$hooks/grub-btrfs-overlay" ] || fail "symlink removed"

# Other actions do nothing.
reset
mkdir "$hooks/grub-btrfs-overlay"
echo old >"$hooks/grub-btrfs-overlay/grub-btrfs-overlay"
run configure || fail "configure: rc=$?"
[ -d "$hooks/grub-btrfs-overlay" ] || fail "configure removed the dir"

# Fresh install, nothing there.
rm -rf "${root:?}/usr"
run install || fail "fresh install: rc=$?"

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK"
