#!/bin/bash
#
# postinst_initramfs_test.sh - runs refresh_initramfs_all from
# debian/calaos-container.postinst against a temporary root. update-initramfs
# and findmnt are stubs in PATH; the stub logs the kernels it rebuilds and
# fails for the one named in FAKE_FAIL.

set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
postinst=${1:-$here/../../debian/calaos-container.postinst}
fails=0

fail() {
    echo "FAIL: $*"
    fails=$((fails + 1))
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
bin=$tmp/bin
mkdir -p "$bin"
cat >"$bin/findmnt" <<'EOS'
#!/bin/bash
echo "${FAKE_FSTYPE:-btrfs}"
EOS
cat >"$bin/update-initramfs" <<'EOS'
#!/bin/bash
[ "$1 $2" = "-u -k" ] && [ $# -eq 3 ] || { echo "bad args: $*" >>"$FAKE_LOG"; exit 2; }
echo "$3" >>"$FAKE_LOG"
[ "$3" != "${FAKE_FAIL:-}" ] || { echo "mkinitramfs failed" >&2; exit 1; }
EOS
chmod +x "$bin/findmnt" "$bin/update-initramfs"

fn=$(sed -n '/^refresh_initramfs_all() {$/,/^}$/p' "$postinst")
[ -n "$fn" ] || { echo "FAIL: refresh_initramfs_all not found in $postinst"; exit 1; }
grep -qx '    refresh_initramfs_all || true' "$postinst" || fail "configure does not call refresh_initramfs_all"

root=$tmp/root
log=$tmp/log
stamp=$root/var/lib/calaos-container/initramfs-overlay.sum

reset() {
    rm -rf "$root"
    mkdir -p "$root/boot" "$root/usr/share/initramfs-tools/hooks" \
        "$root/usr/share/initramfs-tools/scripts/local-bottom"
    echo hook >"$root/usr/share/initramfs-tools/hooks/grub-btrfs-overlay"
    echo script >"$root/usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay"
    for v in 6.1.0-54-amd64 6.1.0-56-amd64 6.1.0-9-amd64; do
        : >"$root/boot/initrd.img-$v"
        mkdir -p "$root/lib/modules/$v"
    done
    # A leftover initrd whose kernel is gone.
    : >"$root/boot/initrd.img-5.10.0-1-amd64"
}

run() {
    : >"$log"
    env -u DPKG_ROOT PATH="$bin:$PATH" FAKE_LOG="$log" "$@" \
        bash -c "$fn"'
refresh_initramfs_all "$1"' _ "$root" 2>"$tmp/err"
}

rebuilt() { sort "$log" | tr '\n' ' '; }

all="6.1.0-54-amd64 6.1.0-56-amd64 6.1.0-9-amd64 "

reset
run || fail "first run: rc=$?"
[ "$(rebuilt)" = "$all" ] || fail "first run rebuilt: $(rebuilt)"
[ -s "$stamp" ] || fail "stamp not written"
run || fail "second run: rc=$?"
[ -z "$(rebuilt)" ] || fail "second run rebuilt again: $(rebuilt)"

echo "script v2" >"$root/usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay"
run || fail "script changed: rc=$?"
[ "$(rebuilt)" = "$all" ] || fail "script changed, rebuilt: $(rebuilt)"
echo "hook v2" >"$root/usr/share/initramfs-tools/hooks/grub-btrfs-overlay"
run || fail "hook changed: rc=$?"
[ "$(rebuilt)" = "$all" ] || fail "hook changed, rebuilt: $(rebuilt)"

# A failing kernel neither stops the others nor fails the postinst, and the
# next run retries.
reset
run FAKE_FAIL=6.1.0-54-amd64 || fail "failing kernel: rc=$?"
[ "$(rebuilt)" = "$all" ] || fail "failing kernel stopped the others: $(rebuilt)"
[ ! -e "$stamp" ] || fail "stamp written despite a failure"
grep -q '6.1.0-54-amd64' "$tmp/err" || fail "failure not reported: $(cat "$tmp/err")"
run || fail "retry: rc=$?"
[ "$(rebuilt)" = "$all" ] || fail "retry rebuilt: $(rebuilt)"
[ -s "$stamp" ] || fail "stamp not written on retry"

reset
run DPKG_ROOT="$root" || fail "DPKG_ROOT: rc=$?"
[ -z "$(rebuilt)" ] && [ ! -e "$stamp" ] || fail "ran under DPKG_ROOT: $(rebuilt)"

reset
run FAKE_FSTYPE=overlay || fail "overlay: rc=$?"
[ -z "$(rebuilt)" ] && [ ! -e "$stamp" ] || fail "ran on the rescue overlay: $(rebuilt)"

reset
rm "$root/usr/share/initramfs-tools/hooks/grub-btrfs-overlay"
run || fail "no hook: rc=$?"
[ -z "$(rebuilt)" ] || fail "ran without the hook: $(rebuilt)"

reset
rm "$root"/boot/initrd.img-*
run || fail "no initrd: rc=$?"
[ -z "$(rebuilt)" ] || fail "no initrd, rebuilt: $(rebuilt)"

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK"
