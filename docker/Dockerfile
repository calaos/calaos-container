#Docker image used to build the deb

FROM debian:bookworm

RUN apt -y update && \
    apt -y upgrade && \
    apt-get install -yq --no-install-recommends devscripts debhelper dpkg-dev fakeroot lintian sudo \
        pkgconf golang libbtrfs-dev libdevmapper-dev libgpgme-dev git golang

RUN go install github.com/maykonlf/semver-cli/cmd/semver@latest

COPY build-deb.sh /build-deb.sh
