#Docker image used to build the deb

FROM debian:bookworm

RUN apt -y update && \
    apt -y upgrade && \
    apt-get install -yq --no-install-recommends devscripts debhelper dpkg-dev fakeroot lintian sudo \
        pkgconf libbtrfs-dev libdevmapper-dev libgpgme-dev git python3-semver python3-git \
        curl ca-certificates

# Go toolchain from go.dev — keep GO_VERSION in sync with .devcontainer/Dockerfile.
# NOTE: `apt-get build-dep` (run by build-deb.sh) may still install Debian's golang
# because debian/control lists it in Build-Depends; PATH below makes this Go win.
ARG GO_VERSION=1.26.5
RUN arch="$(dpkg --print-architecture)" && \
    case "$arch" in \
        amd64) goarch=amd64 ;; \
        arm64) goarch=arm64 ;; \
        armhf) goarch=armv6l ;; \
        *) echo "unsupported architecture: $arch" && exit 1 ;; \
    esac && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH

COPY build-deb.sh /build-deb.sh
COPY  get_last_tag.py /get_last_tag.py
