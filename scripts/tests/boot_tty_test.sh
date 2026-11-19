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
    if grep -nE '^\s*echo( .*)?$' "$repo/scripts/$f" | grep -vE '\|\| true|\| systemd-cat|usage|Usage|ex:|exit'; then
        fail "$f: unguarded echo"
    fi
done

grep -q 'trap .chvt 1 || true; die' "$repo/scripts/config_calaos-boot" \
    || fail "ERR trap does not give the VT back"

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
