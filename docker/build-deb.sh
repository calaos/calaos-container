#!/bin/bash

set -e

workdir="/work"
version="$1"

cd $workdir || exit

if [ -z "$version" ]
then
    version=$(git describe --long --tags --always)
fi

sed -i "s/VERSION = 1.0.0/VERSION = $version/g" debian/rules
sed -i "s/1.0-0/$version/g" debian/changelog

apt-get build-dep -y .
dpkg-buildpackage -us -uc -b

mkdir $workdir/pkg
mv ../calaos-container_*.deb $workdir/pkg
