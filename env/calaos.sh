#!/bin/bash
#This file is installed in /etc/profile.d and will allow shell users to use calaos tools directly

# calaos_ddns
export CALAOS_HAPROXY_PATH="/mnt/calaos/haproxy"
export CALAOS_CERT_FILE="/mnt/calaos/haproxy/server.pem"
export CALAOS_CONFIG="/mnt/calaos/config"
export CALAOS_HAPROXY_TEMPLATE_PATH="/usr/share/calaos-ddns"
export CALAOSDNS_CACHE_DIR="/mnt/calaos/calaos-ddns"

function calaosserver() {
    local cmd
    cmd="$1"
    shift

    #check if calaos-server container is running
    if podman ps | grep calaos-server > /dev/null
    then
        podman exec -it calaos-server ${cmd} "$@"
    else
        #if not, we need run the container using calaos_1wire instead of calaos_server
        local img
        img=$(grep IMAGE_SRC /usr/share/calaos/calaos-server.source | cut -d= -f2)
        podman run --rm -it \
            -v /mnt/calaos/config:/root/.config/calaos \
            -v /run/calaos:/run/calaos \
            --privileged -v /dev/bus/usb:/dev/bus/usb \
            --entrypoint ${cmd} "$img" "$@"
    fi
}

alias calaos_1wire='calaosserver /opt/bin/calaos_1wire'
alias calaos_config='calaosserver /opt/bin/calaos_config'
alias wago_test='calaosserver /opt/bin/wago_test'
alias calaos_mail='calaosserver /opt/bin/calaos_mail'
alias xinput_calibrator='podman exec -it -e DISPLAY=:0 calaos-home xinput_calibrator --output-filename /etc/X11/xorg.conf.d/99-calibration.conf'
alias envoy='podman exec -it -e "ENVOY_CACHE_PATH=/config/cache" envoy /app/bin/envoy'