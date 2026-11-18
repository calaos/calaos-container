#!/usr/bin/env bash
# Runs grub/41_calaos-snapshot-boot against stubbed mountpoint and grub-probe
# and checks the grub.cfg fragment it prints. When grub-script-check is
# installed, every fragment is also parsed by GRUB's own script parser.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script="$here/../../grub/41_calaos-snapshot-boot"
uuid=4A3B-1C2D

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

failures=0
fail() {
    echo "FAIL: $*"
    failures=$((failures + 1))
}

# stubs <mountpoint rc> <fs> <fs_uuid> [probe rc]
stubs() {
    cat >"$tmp/bin/mountpoint" <<EOF
#!/bin/sh
exit $1
EOF
    cat >"$tmp/bin/grub-probe" <<EOF
#!/bin/sh
[ "\$2" = /efi ] || exit 9
case "\$1" in
--target=fs) echo '$2' ;;
--target=fs_uuid) echo '$3' ;;
*) exit 9 ;;
esac
exit ${4:-0}
EOF
    chmod +x "$tmp/bin/mountpoint" "$tmp/bin/grub-probe"
}

# run <name>: the fragment goes to $tmp/out, and must always start with the
# version marker and the exports, whatever the ESP.
run() {
    local rc=0
    env -u grub_probe PATH="$tmp/bin:$PATH" sh "$script" >"$tmp/out" 2>"$tmp/err" || rc=$?
    [ "$rc" -eq 0 ] || fail "$1: rc=$rc"
    [ ! -s "$tmp/err" ] || fail "$1: stderr $(cat "$tmp/err")"
    [ "$(head -n 3 "$tmp/out")" = "$(printf '# calaos-snapshot-boot 4\nexport default\nexport timeout')" ] ||
        fail "$1: header $(head -n 3 "$tmp/out")"
    if command -v grub-script-check >/dev/null 2>&1; then
        grub-script-check "$tmp/out" >/dev/null 2>&1 || fail "$1: grub-script-check rejects the fragment"
    fi
}

no_esp_block() {
    ! grep -q calaos_oneshot "$tmp/out" || fail "$1: ESP block emitted"
    [ "$(grep -c 'set timeout=' "$tmp/out")" -eq 1 ] || fail "$1: timeout set outside the BIOS boot_once block"
    bios_block "$1"
}

# BIOS one-shot: next_entry consumed by 00_header leaves boot_once=true; the
# timeout is zeroed for the nested menus, on the pc platform only, and before
# any early exit of the ESP probing.
bios_block() {
    local want
    # shellcheck disable=SC2016 # GRUB variables, matched literally
    want=$(printf '%s\n' 'if [ "${grub_platform}" = "pc" -a "${boot_once}" = "true" ]; then' \
        '  set timeout=0' '  export timeout' 'fi')
    [ "$(sed -n 4,7p "$tmp/out")" = "$want" ] || fail "$1: BIOS boot_once block missing or moved: $(sed -n 4,7p "$tmp/out")"
}

stubs 0 fat "$uuid"
run "FAT ESP"
grep -q "search --no-floppy --fs-uuid --set=calaos_esp $uuid; then" "$tmp/out" || fail "FAT ESP: no search on the ESP UUID"
# shellcheck disable=SC2016 # GRUB variables, matched literally
grep -q 'if \[ "${grub_platform}" = "efi" \]; then' "$tmp/out" || fail "FAT ESP: block not limited to EFI"
# shellcheck disable=SC2016 # GRUB variables, matched literally
grep -q 'load_env --file "(${calaos_esp})/calaos/grubenv" calaos_oneshot' "$tmp/out" || fail "FAT ESP: no load_env"
# default may only be set once the one-shot is erased on disk.
# shellcheck disable=SC2016 # GRUB variables, matched literally
save=$(grep -n 'if save_env --file "(${calaos_esp})/calaos/grubenv" calaos_oneshot; then' "$tmp/out" | cut -d: -f1)
clear=$(grep -n '^ *set calaos_oneshot=$' "$tmp/out" | cut -d: -f1)
# shellcheck disable=SC2016 # GRUB variables, matched literally
default=$(grep -n 'set default="${calaos_target}"' "$tmp/out" | cut -d: -f1)
if [ -z "$save" ] || [ -z "$clear" ] || [ -z "$default" ] || [ "$clear" -ge "$save" ] || [ "$save" -ge "$default" ]; then
    fail "FAT ESP: clear $clear, save_env $save, set default $default out of order"
fi
# The nested snapshot menu has no timeout of its own: it is zeroed and
# exported right after set default, inside the save_env branch (its fi follows).
after=$(sed -n "$((default + 1)),$((default + 3))p" "$tmp/out")
want=$(printf '          set timeout=0\n          export timeout\n        fi')
[ "$after" = "$want" ] || fail "FAT ESP: set timeout/export timeout not inside the save_env branch after set default: $after"
bios_block "FAT ESP"
[ "$(grep -c 'set timeout=' "$tmp/out")" -eq 2 ] || fail "FAT ESP: timeout set outside the BIOS and one-shot branches"
[ "$(grep -c 'set default=' "$tmp/out")" -eq 1 ] || fail "FAT ESP: default set outside the save_env branch"

stubs 1 fat "$uuid"
run "no ESP mounted"
no_esp_block "no ESP mounted"

stubs 0 btrfs "$uuid"
run "ESP not FAT"
no_esp_block "ESP not FAT"

stubs 0 fat "$uuid" 1
run "grub-probe fails"
no_esp_block "grub-probe fails"

stubs 0 fat ''
run "empty UUID"
no_esp_block "empty UUID"

stubs 0 fat '4A3B; reboot'
run "UUID with shell characters"
no_esp_block "UUID with shell characters"

# grub-mkconfig passes its own grub-probe in $grub_probe.
stubs 0 fat "$uuid"
mv "$tmp/bin/grub-probe" "$tmp/probe"
rc=0
grub_probe="$tmp/probe" PATH="$tmp/bin:$PATH" sh "$script" >"$tmp/out" 2>/dev/null || rc=$?
if [ "$rc" -ne 0 ] || ! grep -q "fs-uuid --set=calaos_esp $uuid;" "$tmp/out"; then
    fail "\$grub_probe ignored (rc=$rc)"
fi

if [ "$failures" -ne 0 ]; then
    echo "$failures failure(s)"
    exit 1
fi
echo "grub_snapshot_oneshot_test: OK"
