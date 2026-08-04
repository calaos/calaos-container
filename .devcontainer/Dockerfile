FROM debian:12-slim

RUN apt -y update && \
    apt -y upgrade && \
    apt-get install -yq --no-install-recommends devscripts debhelper dpkg-dev fakeroot lintian sudo \
        pkgconf libbtrfs-dev libdevmapper-dev libgpgme-dev git python3-semver python3-git \
        libglib2.0-dev libapt-pkg-dev meson \
        bash openssl coreutils util-linux gawk btrfs-progs parted dosfstools snapper rsync sed \
        inotify-tools curl wget ifupdown2 jq build-essential git ssh \
        unzip zip cmake automake autoconf libtool autopoint gettext \
        tar gzip zsh vim nano sudo zstd less gnupg ripgrep gdb cgdb locales shellcheck \
        systemd systemd-resolved systemd-sysv dbus iproute2 iputils-ping

# Go toolchain from go.dev (Debian bookworm only ships golang 1.19, go.mod requires >= 1.20).
# Keep GO_VERSION in sync with docker/Dockerfile (the CI deb build image).
ARG GO_VERSION=1.26.5
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH

# GitHub CLI (official apt repo)
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -yq --no-install-recommends gh

# golangci-lint (binary release)
RUN curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b /usr/local/bin v2.12.2

RUN sed -i -e 's/# en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen && \
    sed -i -e 's/# fr_FR.UTF-8 UTF-8/fr_FR.UTF-8 UTF-8/' /etc/locale.gen && \
    dpkg-reconfigure --frontend=noninteractive locales && \
    update-locale LANG=en_US.UTF-8
ENV LANG en_US.UTF-8
ENV LANGUAGE en_US:en
ENV LC_ALL en_US.UTF-8

RUN echo "export EDITOR=nano \n\
    export VISUAL=nano \n\
    export LS_OPTIONS='--color=auto' \n\
    eval "$(dircolors)" \n\
    alias ls='ls $LS_OPTIONS' \n\
    alias ll='ls $LS_OPTIONS -l' \n\
    " >> /etc/bash.bashrc

RUN curl -fsSL https://starship.rs/install.sh | sh -s -- -y
RUN echo 'eval "$(starship init bash)"' >> /etc/bash.bashrc

#better bash history search with up/down arrows
RUN echo "\n\
## arrow up \n\
\"\e[A\":history-search-backward \n\
## arrow down \n\
\"\e[B\":history-search-forward \n\
\n\
set show-all-if-ambiguous on \n\
set completion-ignore-case on \n\
" >> /etc/inputrc

RUN echo "\n\
[Match]\n\
Name=calaos-*\n\
\n\
[Network]\n\
DHCP=yes\n\
" > /etc/systemd/network/calaos.network

COPY setup_veth.sh /usr/local/bin/setup_veth.sh
RUN chmod +x /usr/local/bin/setup_veth.sh
COPY setup-veth.service /etc/systemd/system/setup-veth.service
RUN systemctl enable setup-veth.service

RUN systemctl enable setup-veth.service
RUN systemctl enable systemd-resolved.service
RUN systemctl enable systemd-networkd.service

ENV container docker
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd", "--system", "--unit=basic.target"]
