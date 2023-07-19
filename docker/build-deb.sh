#!/bin/bash

set -e

workdir="/work"
version="$1"

cd $workdir || exit

git config --global --add safe.directory $workdir

if [ -z "$version" ]
then
    version=$(git describe --long --tags --always)
fi

sed -i "s/VERSION = 1.0.0/VERSION = $version/g" debian/rules
sed -i "s/1.0-0/$version/g" debian/changelog

pkg_name=$(grep "Package: " debian/control | sed 's/Package: //')

apt-get build-dep -y .
dpkg-buildpackage -us -uc -b

mkdir $workdir/pkg
mv ../"${pkg_name}"_*.deb $workdir/pkg
