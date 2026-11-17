#!/usr/bin/env bash
# Runs initramfs/scripts/local-bottom/grub-btrfs-overlay against stubbed
# btrfs/modprobe/mkdir/mount/umount and checks the exact command sequence.
# PATH holds only the stubs, so no real mount can happen.
# TEST_SH picks the shell running the script (default sh; the initrd uses
# /usr/lib/klibc/bin/sh).
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script="$here/../../initramfs/scripts/local-bottom/grub-btrfs-overlay"
test_sh=${TEST_SH:-sh}
test_sh=$(command -v "$test_sh") || { echo "shell not found: ${TEST_SH:-sh}"; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
root="$tmp/root"
mkdir -p "$root"
log="$tmp/log"

failures=0
fail() {
    echo "FAIL: $*"
    failures=$((failures + 1))
}

# Each stub logs its command line and exits 1 when that line matches the
# shell pattern in $STUB_FAIL or $STUB_FAIL2.
make_stubs() {
    rm -rf "${tmp:?}/bin"
    mkdir -p "$tmp/bin"
    local cmd
    for cmd in "$@"; do
        cat >"$tmp/bin/$cmd" <<EOF
#!/bin/sh
line="$cmd \$*"
echo "\$line" >>"$log"
case "\$line" in
    \$STUB_FAIL | \$STUB_FAIL2) exit 1 ;;
esac
if [ "$cmd" = btrfs ]; then
    [ -n "\${BTRFS_OUT:-}" ] && echo "\$BTRFS_OUT"
    exit "\${BTRFS_RC:-0}"
fi
exit 0
EOF
        chmod +x "$tmp/bin/$cmd"
    done
}

# run <rootmnt> [args...]: runs the script, stores its rc in $rc.
run() {
    local mnt=$1
    shift
    : >"$log"
    env -i PATH="$tmp/bin" rootmnt="$mnt" STUB_FAIL="${STUB_FAIL:-__never__}" \
        STUB_FAIL2="${STUB_FAIL2:-__never__}" \
        BTRFS_OUT="${BTRFS_OUT:-}" BTRFS_RC="${BTRFS_RC:-0}" \
        "$test_sh" "$script" "$@" >"$tmp/out" 2>"$tmp/err"
    rc=$?
}

expect_rc0() {
    [ "$rc" -eq 0 ] || fail "$1: rc=$rc, want 0"
}

expect_log() {
    local name=$1 want=$2
    if [ "$(cat "$log")" != "$want" ]; then
        fail "$name: unexpected commands"
        echo "--- want"
        echo "$want"
        echo "--- got"
        cat "$log"
    fi
}

ovl=/run/calaos-overlay
probe="btrfs property get -ts $root ro"
modp="modprobe overlay"
mk1="mkdir -p $ovl/lower $ovl/ram"
tmpfs="mount -t tmpfs -o mode=0755,size=25% cowspace $ovl/ram"
mk2="mkdir -m 0755 $ovl/ram/upper $ovl/ram/work"
move="mount -o move $root $ovl/lower"
overlay="mount -t overlay -o lowerdir=$ovl/lower,upperdir=$ovl/ram/upper,workdir=$ovl/ram/work rootfs $root"
back="mount -o move $ovl/lower $root"
umnt="umount $ovl/ram"
all_stubs="btrfs modprobe mkdir mount umount"

# shellcheck disable=SC2086
make_stubs $all_stubs

run "$root" prereqs
expect_rc0 prereqs
[ "$(cat "$tmp/out")" = "" ] || fail "prereqs: printed '$(cat "$tmp/out")'"
expect_log prereqs ""

run ""
expect_rc0 "rootmnt unset"
expect_log "rootmnt unset" ""

run "$tmp/missing"
expect_rc0 "rootmnt missing"
expect_log "rootmnt missing" ""

BTRFS_OUT="ro=false" run "$root"
expect_rc0 "rw root"
expect_log "rw root" "$probe"

BTRFS_RC=1 run "$root"
expect_rc0 "not btrfs"
expect_log "not btrfs" "$probe"

BTRFS_OUT="ro=true" BTRFS_RC=1 run "$root"
expect_rc0 "btrfs error"
expect_log "btrfs error" "$probe"

BTRFS_OUT="ro=TRUE" run "$root"
expect_rc0 "odd output"
expect_log "odd output" "$probe"

BTRFS_OUT="ro=true" run "$root"
expect_rc0 "ro root"
expect_log "ro root" "$probe
$modp
$mk1
$tmpfs
$mk2
$move
$overlay"
[ -s "$tmp/err" ] || fail "ro root: no message on stderr"

BTRFS_OUT="ro=true" STUB_FAIL="modprobe*" run "$root"
expect_rc0 "modprobe fails"
expect_log "modprobe fails" "$probe
$modp
$mk1
$tmpfs
$mk2
$move
$overlay"

BTRFS_OUT="ro=true" STUB_FAIL="mkdir -p*" run "$root"
expect_rc0 "mkdir run fails"
expect_log "mkdir run fails" "$probe
$modp
$mk1"

BTRFS_OUT="ro=true" STUB_FAIL="mount -t tmpfs*" run "$root"
expect_rc0 "tmpfs fails"
expect_log "tmpfs fails" "$probe
$modp
$mk1
$tmpfs"

BTRFS_OUT="ro=true" STUB_FAIL="mkdir -m*" run "$root"
expect_rc0 "upper fails"
expect_log "upper fails" "$probe
$modp
$mk1
$tmpfs
$mk2
$umnt"

BTRFS_OUT="ro=true" STUB_FAIL="$move" run "$root"
expect_rc0 "move fails"
expect_log "move fails" "$probe
$modp
$mk1
$tmpfs
$mk2
$move
$umnt"

BTRFS_OUT="ro=true" STUB_FAIL="mount -t overlay*" run "$root"
expect_rc0 "overlay fails"
expect_log "overlay fails" "$probe
$modp
$mk1
$tmpfs
$mk2
$move
$overlay
$back
$umnt"

# The root could not be put back: leave the tmpfs mounted, nothing else to try.
BTRFS_OUT="ro=true" STUB_FAIL="mount -t overlay*" STUB_FAIL2="$back" run "$root"
expect_rc0 "move back fails"
expect_log "move back fails" "$probe
$modp
$mk1
$tmpfs
$mk2
$move
$overlay
$back"

make_stubs modprobe mkdir mount umount
BTRFS_OUT="ro=true" run "$root"
expect_rc0 "no btrfs"
expect_log "no btrfs" ""

if [ "$failures" -ne 0 ]; then
    echo "$failures failure(s) ($test_sh)"
    exit 1
fi
echo "OK ($test_sh)"
