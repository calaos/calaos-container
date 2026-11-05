#!/bin/bash
#
# postinst_speedup_test.sh - runs debian/calaos-container.postinst "configure"
# against a temporary DPKG_ROOT and checks the docker-apt-speedup handling.
# DPKG_ROOT also switches off the postinst's policy-rc.d and restart steps,
# so nothing outside the temporary root is touched.

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
dir=$root/etc/dpkg/dpkg.cfg.d
f=$dir/docker-apt-speedup

run() {
    DPKG_ROOT=$root bash "$postinst" configure 2>"$root/err"
}

reset() {
    rm -rf "${root:?}/etc"
    mkdir -p "$dir"
}

reset
printf '# For most Docker users, "apt-get install" only happens during "docker build",\n\n# Since docker does not allow the image to be written to a different place\nforce-unsafe-io\n' >"$f"
run || fail "stock file: rc=$?"
[ ! -e "$f" ] || fail "stock file was not removed"
run || fail "second pass: rc=$?"
[ ! -e "$f" ] || fail "second pass recreated the file"

reset
printf 'force-unsafe-io\nno-debsig\n' >"$f"
run || fail "modified file: rc=$?"
[ -f "$f" ] || fail "modified file was removed"
grep -q "was modified" "$root/err" || fail "modified file: no message"
run || fail "modified file, second pass: rc=$?"
[ -f "$f" ] || fail "modified file removed on second pass"

reset
run || fail "absent file: rc=$?"

rm -rf "${root:?}/etc"
run || fail "no dpkg.cfg.d directory: rc=$?"

if [ "$fails" -ne 0 ]; then
    echo "$fails failure(s)"
    exit 1
fi
echo "OK"
