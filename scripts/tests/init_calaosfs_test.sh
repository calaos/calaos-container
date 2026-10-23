#!/bin/bash
#
# init_calaosfs_test.sh [path/to/init_calaosfs] - runs init_calaosfs as 10
# concurrent units would at boot and checks the token it leaves behind.
# It writes to /mnt/calaos, /etc/ola and /run/calaos, so it only runs as root
# inside a throwaway container.

set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=${1:-$here/../init_calaosfs}
token=/run/calaos/calaos-ct.token
runs=10
rounds=20
fails=0

if [ "$(id -u)" -ne 0 ] || { [ ! -e /.dockerenv ] && [ ! -e /run/.containerenv ]; }; then
    echo "refusing to run: needs root inside a disposable container" >&2
    exit 2
fi

if [ ! -e /usr/lib/calaos/calaos-common.sh ]; then
    install -Dm644 "$here/../lib/calaos-common.sh" /usr/lib/calaos/calaos-common.sh
fi
id olad >/dev/null 2>&1 || useradd --system --user-group olad

fail() {
    echo "FAIL: $*"
    fails=$((fails + 1))
}

# Starts $runs copies at once and fails unless every one exits 0.
run_parallel() {
    local i pids=() rc=0
    for i in $(seq "$runs"); do
        "$script" >"/tmp/init_calaosfs.$i.log" 2>&1 &
        pids+=($!)
    done
    for i in "${!pids[@]}"; do
        if ! wait "${pids[$i]}"; then
            rc=1
            fail "run $((i + 1)) exited non-zero: $(tail -3 "/tmp/init_calaosfs.$((i + 1)).log")"
        fi
    done
    return $rc
}

check_token() {
    local what=$1 stat
    if [ ! -f "$token" ] || [ -L "$token" ]; then
        fail "$what: no regular token file"
        return
    fi
    stat=$(stat -c '%a %U %G %h %s' "$token")
    [ "$stat" = "600 root root 1 65" ] || fail "$what: mode/owner/links/size '$stat', want '600 root root 1 65'"
    grep -qxE '[0-9a-f]{64}' "$token" || fail "$what: content '$(cat "$token")' is not 64 hex chars"
    [ "$(find /run/calaos -maxdepth 1 -name '.calaos-ct.token.*' | wc -l)" -eq 0 ] ||
        fail "$what: temp files left: $(ls -a /run/calaos)"
}

# Flags a partially written token, or a token replaced by a later run, while
# the runs race.
watch_token() {
    local c first=
    while [ ! -e /tmp/stop-watch ]; do
        c=$(cat "$token" 2>/dev/null) || continue
        if ! [[ $c =~ ^[0-9a-f]{64}$ ]]; then
            echo "partial token read: '$c'" >>/tmp/watch.log
        elif [ -z "$first" ]; then
            first=$c
        elif [ "$c" != "$first" ]; then
            echo "token replaced while racing" >>/tmp/watch.log
        fi
    done
}

seen=()
for round in $(seq "$rounds"); do
    rm -f "$token" /tmp/watch.log /tmp/stop-watch
    rm -rf /etc/ola
    mkdir -p /etc/ola

    watch_token &
    watcher=$!
    run_parallel
    touch /tmp/stop-watch
    wait "$watcher"
    [ -s /tmp/watch.log ] && fail "round $round: $(head -1 /tmp/watch.log)"

    check_token "round $round"
    first=$(stat -c '%i' "$token")$(cat "$token")
    seen+=("$first")

    run_parallel
    [ "$(stat -c '%i' "$token")$(cat "$token")" = "$first" ] ||
        fail "round $round: an existing token was replaced"

    [ "$(readlink /etc/ola)" = /mnt/calaos/ola ] || fail "round $round: /etc/ola is not the OLA symlink"
done

[ "$(printf '%s\n' "${seen[@]}" | sort -u | wc -l)" -eq "$rounds" ] ||
    fail "the same token was generated twice"

# An upgraded box keeps its old weak token, mode included, until the reboot.
printf '1729000000-123456789-12345\n' >"$token"
chmod 644 "$token"
run_parallel
[ "$(stat -c '%a' "$token") $(cat "$token")" = "644 1729000000-123456789-12345" ] ||
    fail "the token of an upgraded box was rewritten"

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK: $rounds rounds of $runs concurrent runs"
