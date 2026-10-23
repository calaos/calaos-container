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

install -Dm644 "$here/../lib/calaos-common.sh" /usr/lib/calaos/calaos-common.sh
id olad >/dev/null 2>&1 || useradd --system --user-group olad
id intruder >/dev/null 2>&1 || useradd --system --user-group intruder

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
        c=
        { IFS= read -r c || :; } 2>/dev/null <"$token" || continue
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
    # A box whose olad config lives in a real /etc/ola directory.
    rm -rf /etc/ola /mnt/calaos/ola
    mkdir -p /etc/ola /mnt/calaos/ola
    echo dmx >/etc/ola/ola-universe.conf
    echo etc >/etc/ola/keep.conf
    echo mnt >/mnt/calaos/ola/keep.conf

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
    [ "$(cat /mnt/calaos/ola/ola-universe.conf 2>/dev/null)" = dmx ] ||
        fail "round $round: the olad config in /etc/ola was lost"
    [ "$(cat /mnt/calaos/ola/keep.conf)" = mnt ] ||
        fail "round $round: /mnt/calaos/ola/keep.conf was overwritten"
done

[ "$(printf '%s\n' "${seen[@]}" | sort -u | wc -l)" -eq "$rounds" ] ||
    fail "the same token was generated twice"

# An upgraded box keeps its old weak token until the reboot, now 0600.
printf '1729000000-123456789-12345\n' >"$token"
chmod 644 "$token"
run_parallel
[ "$(stat -c '%a' "$token") $(cat "$token")" = "600 1729000000-123456789-12345" ] ||
    fail "the token of an upgraded box was rewritten or left 0644"

# A token nobody but root could have written properly is replaced, never
# followed: a symlink, a file owned by another user, an empty file.
rm -f /tmp/evil
echo attacker-known-token >/tmp/evil
chown intruder: /tmp/evil
for kind in symlink foreign empty; do
    rm -f "$token"
    case $kind in
        symlink) ln -s /tmp/evil "$token" ;;
        foreign) cp /tmp/evil "$token" && chown intruder: "$token" ;;
        empty) : >"$token" ;;
    esac
    run_parallel
    check_token "untrusted $kind token"
    [ "$(stat -c '%U' /tmp/evil) $(cat /tmp/evil)" = "intruder attacker-known-token" ] ||
        fail "untrusted $kind token: the symlink target was touched"
done

[ "$(find /run/calaos -mindepth 1 -maxdepth 1 -name '.*' | wc -l)" -eq 0 ] ||
    fail "hidden files left in /run/calaos: $(ls -a /run/calaos)"

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK: $rounds rounds of $runs concurrent runs"
