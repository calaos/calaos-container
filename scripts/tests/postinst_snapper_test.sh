#!/bin/bash
#
# postinst_snapper_test.sh - runs debian/calaos-container.postinst "configure"
# against a temporary DPKG_ROOT and checks the snapper root config handling.
# findmnt is a stub in PATH so the filesystem type can be chosen.

set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
postinst=${1:-$here/../../debian/calaos-container.postinst}
fails=0

fail() {
    echo "FAIL: $*"
    fails=$((fails + 1))
}

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
bin=$root/bin
mkdir -p "$bin"
cat >"$bin/findmnt" <<'EOS'
#!/bin/bash
echo "${FAKE_FSTYPE:-btrfs}"
EOS
chmod +x "$bin/findmnt"

sroot=$root/sys
cfg=$sroot/etc/snapper/configs/root

reset() {
    rm -rf "$sroot"
    mkdir -p "$sroot/etc/snapper/configs" "$sroot/usr/share/snapper/config-templates" "$sroot/etc/default" "$sroot/.snapshots"
    printf 'SUBVOLUME="/"\nFSTYPE="btrfs"\nNUMBER_LIMIT="50"\nTIMELINE_CREATE="yes"\nTIMELINE_LIMIT_HOURLY="10"\n' \
        >"$sroot/usr/share/snapper/config-templates/default"
    printf 'SNAPPER_CONFIGS=""\n' >"$sroot/etc/default/snapper"
}

run() {
    PATH=$bin:$PATH DPKG_ROOT=$sroot bash "$postinst" configure 2>"$root/err"
}

reset
run || fail "absent config: rc=$?"
[ -f "$cfg" ] || fail "config not created"
for kv in 'SUBVOLUME="/"' 'FSTYPE="btrfs"' 'NUMBER_LIMIT="10"' 'NUMBER_MIN_AGE="5400"' \
    'TIMELINE_LIMIT_DAILY="14"' 'TIMELINE_LIMIT_WEEKLY="4"' 'TIMELINE_LIMIT_MONTHLY="6"' \
    'TIMELINE_LIMIT_YEARLY="2"' 'TIMELINE_CREATE="yes"'; do
    grep -qx "$kv" "$cfg" || fail "missing $kv"
done
grep -q '^SNAPPER_CONFIGS="root"$' "$sroot/etc/default/snapper" || fail "SNAPPER_CONFIGS: $(cat "$sroot/etc/default/snapper")"
[ "$(stat -c %a "$cfg")" = 640 ] || fail "mode $(stat -c %a "$cfg")"
[ -z "$(ls "$sroot/etc/snapper/configs")" ] || [ "$(ls "$sroot/etc/snapper/configs")" = root ] || fail "temp file left"
run || fail "second pass: rc=$?"
[ "$(grep -c '^SNAPPER_CONFIGS="root"$' "$sroot/etc/default/snapper")" = 1 ] || fail "SNAPPER_CONFIGS changed on second pass"

reset
printf 'SUBVOLUME="/"\nNUMBER_LIMIT="3"\n' >"$cfg"
before=$(cat "$cfg")
run || fail "existing config: rc=$?"
[ "$(cat "$cfg")" = "$before" ] || fail "existing config modified"

reset
FAKE_FSTYPE=ext4 run || fail "ext4: rc=$?"
[ ! -e "$cfg" ] || fail "config created on ext4"

reset
FAKE_FSTYPE=overlay run || fail "overlay: rc=$?"
[ ! -e "$cfg" ] || fail "config created on overlay"

reset
rmdir "$sroot/.snapshots"
run || fail "no .snapshots: rc=$?"
[ ! -e "$cfg" ] || fail "config created without /.snapshots"

reset
rm "$sroot/usr/share/snapper/config-templates/default"
run || fail "no template: rc=$?"
[ ! -e "$cfg" ] || fail "config created without template"
grep -q "template\|missing" "$root/err" || fail "no message without template"

reset
mkdir -p "$sroot/etc/snapper/config-templates"
mv "$sroot/usr/share/snapper/config-templates/default" "$sroot/etc/snapper/config-templates/default"
run || fail "etc template: rc=$?"
[ -f "$cfg" ] || fail "config not created from the /etc template"

reset
printf 'SNAPPER_CONFIGS="home"\n' >"$sroot/etc/default/snapper"
run || fail "home: rc=$?"
grep -qx 'SNAPPER_CONFIGS="home root"' "$sroot/etc/default/snapper" || fail "home: $(cat "$sroot/etc/default/snapper")"
rm "$cfg"
run || fail "home second pass: rc=$?"
grep -qx 'SNAPPER_CONFIGS="home root"' "$sroot/etc/default/snapper" || fail "home second pass: $(cat "$sroot/etc/default/snapper")"

reset
printf 'SNAPPER_CONFIGS="root"\n' >"$sroot/etc/default/snapper"
run || fail "root already listed: rc=$?"
grep -qx 'SNAPPER_CONFIGS="root"' "$sroot/etc/default/snapper" || fail "root duplicated: $(cat "$sroot/etc/default/snapper")"

reset
rm "$sroot/etc/default/snapper"
run || fail "no default file: rc=$?"
[ -f "$cfg" ] || fail "config not created without default file"
grep -q 'SNAPPER_CONFIGS' "$root/err" || fail "no message without default file"

reset
printf '# nothing\n' >"$sroot/etc/default/snapper"
run || fail "no SNAPPER_CONFIGS line: rc=$?"
grep -q 'SNAPPER_CONFIGS' "$root/err" || fail "no message without SNAPPER_CONFIGS line"

reset
rm -rf "$sroot/etc/snapper/configs"
run || fail "no configs dir: rc=$?"
[ ! -e "$cfg" ] || fail "config created without configs dir"

reset
chmod 555 "$sroot/etc/snapper/configs"
if [ "$(id -u)" != 0 ]; then
    run || fail "read-only configs dir: rc=$?"
    [ ! -e "$cfg" ] || fail "config created in read-only dir"
fi
chmod 755 "$sroot/etc/snapper/configs"

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK"
