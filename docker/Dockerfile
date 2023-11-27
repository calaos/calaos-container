#Docker image used to build the deb

FROM debian:bookworm

RUN apt -y update && \
    apt -y upgrade && \
    apt-get install -yq --no-install-recommends devscripts debhelper dpkg-dev fakeroot lintian sudo \
        pkgconf golang libbtrfs-dev libdevmapper-dev libgpgme-dev git python3-semver python3-git

COPY build-deb.sh /build-deb.sh
COPY  get_last_tag.py /get_last_tag.py
