#!/bin/bash

set -e

workdir="/work"


cd $workdir || exit

apt-get build-dep -y .
dpkg-buildpackage -us -uc -b

mkdir $workdir/pkg
mv ../calaos-container_*.deb $workdir/pkg
