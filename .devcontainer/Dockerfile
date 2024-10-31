FROM debian:12-slim as dev

RUN apt -y update && \
    apt -y upgrade && \
    apt-get install -yq --no-install-recommends devscripts debhelper dpkg-dev fakeroot lintian sudo \
        pkgconf golang libbtrfs-dev libdevmapper-dev libgpgme-dev git python3-semver python3-git \
        pkgconf golang libbtrfs-dev libdevmapper-dev libgpgme-dev libglib2.0-dev libapt-pkg-dev meson \
        bash openssl coreutils util-linux gawk btrfs-progs parted dosfstools snapper rsync sed \
        inotify-tools curl wget ifupdown2 jq build-essential git \
        unzip zip cmake automake autoconf libtool autopoint gettext \
        tar gzip zsh vim nano sudo zstd less gnupg ripgrep gdb cgdb locales \
        systemd systemd-resolved systemd-sysv

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