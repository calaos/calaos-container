#!/bin/bash
#
# boot_tty_test.sh - calaos-boot must survive EIO on its console (tty2 hangup by
# a getty) and hand the VT back on failure; the deb must mask getty/autovt on tty2.

set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$here/../..
lib=${1:-$repo/scripts/lib/calaos-common.sh}
fails=0

fail() {
    echo "FAIL: $*"
    fails=$((fails + 1))
}

# A stdout/stderr that always fails with EIO, like a hung-up tty.
eio_run() {
    bash -c "set -e; . '$lib'; $1" >/dev/full 2>/dev/full
}

for fn in info warn err green; do
    eio_run "$fn hello; exit 0" || fail "$fn aborts under set -e when the console fails"
done
eio_run "color \"\$RED\" hello; exit 0" || fail "color aborts under set -e when the console fails"

# Every console write of the boot scripts is guarded.
for f in config_calaos-boot load_containers_cache; do
    if grep -nE '^\s*echo( .*)?$' "$repo/scripts/$f" | grep -vE '\|\| true|\| systemd-cat|"Usage:|:\s*echo "  ex:'; then
        fail "$f: unguarded echo"
    fi
done

# Behaviour: any failing exit must end with chvt 1. Script copy with the lib
# and source dir redirected, stubbed commands on PATH.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/src"
for c in udevadm systemd-cat clear dialog sleep; do
    printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/$c"
done
printf '#!/bin/sh\necho "$*" >> "%s/chvt.log"\n' "$tmp" > "$tmp/bin/chvt"
printf '#!/bin/sh\nexit 1\n' > "$tmp/bin/lsblk"
chmod +x "$tmp"/bin/*
sed -e "s#/usr/lib/calaos/calaos-common.sh#$lib#" \
    -e "s#/usr/share/calaos/#$tmp/src/#" \
    "$repo/scripts/config_calaos-boot" > "$tmp/boot"

run_boot() {
    : > "$tmp/chvt.log"
    env -u IMAGE_SRC PATH="$tmp/bin:$PATH" bash "$tmp/boot" "$@" >/dev/null 2>&1
}

# failure inside a function (set -e, no ERR trap there)
run_boot --calaos-install; rc=$?
[ "$rc" -ne 0 ] || fail "function failure: rc=0"
[ "$(tail -n1 "$tmp/chvt.log")" = "1" ] || fail "function failure: VT not given back"

# set -u error (no ERR trap at all)
echo 'IMAGE_SRC_TYPO=x' > "$tmp/src/x.source"
run_boot; rc=$?
[ "$rc" -ne 0 ] || fail "set -u failure: rc=0"
[ "$(tail -n1 "$tmp/chvt.log")" = "1" ] || fail "set -u failure: VT not given back"
rm -f "$tmp/src/x.source"

links=$repo/debian/calaos-container.links
for u in autovt getty; do
    grep -qx "dev/null etc/systemd/system/$u@tty2.service" "$links" \
        || fail "$u@tty2.service is not masked"
done

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK"
