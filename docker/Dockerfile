#Docker image used to build the deb

FROM ghcr.io/calaos/debian_deb_build_base:latest

RUN apt -y update && \
    apt -y upgrade && \
    apt-get install -yq --no-install-recommends devscripts debhelper dpkg-dev fakeroot lintian sudo \
        pkgconf golang libbtrfs-dev libdevmapper-dev libgpgme-dev
