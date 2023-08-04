#!/bin/bash

set -e

cidfile=$1
[ -z "$cidfile" ] && {
    echo "Missing arg"
    exit 1
}

# print list of GPU devices
devicelist_gpu()
{
  find /dev/dri/* /dev/nvidia* /dev/vga_arbiter /dev/nvhost* /dev/nvmap -maxdepth 0 -type c 2>/dev/null ||:
}

# print list of input devices
devicelist_input()
{
    find /dev/input/* -maxdepth 0 -type c 2>/dev/null ||:
}

append_gpu_opts()
{
    local gpudevice='' cmd=''

    while read -r gpudevice ; do
        echo " --device $gpudevice:$gpudevice "
    done < <(devicelist_gpu)
}

append_input_opts()
{
    local inputdevice='' cmd=''

    while read -r inputdevice ; do
        echo " --device=$inputdevice "
    done < <(devicelist_input)
}

# shellcheck disable=SC2046

podman run \
        --name=calaos-home \
        --cidfile="${cidfile}" --replace --rm --tz=local \
        --sdnotify=conmon -d \
        -v /mnt/calaos/cache:/root/.cache/calaos/ \
        -v /mnt/calaos/config:/etc/calaos \
        --tty \
        --tmpfs /run:exec \
        --tmpfs /run/lock \
        --tmpfs /tmp \
        --network=host \
        --mount type=bind,source=/var/run/dbus,target=/var/run/dbus \
        --mount type=bind,source=/run/udev/data,target=/run/udev/data,readonly \
        $(append_gpu_opts) \
        $(append_input_opts) \
        ghcr.io/calaos/calaos_home:latest \
        /usr/bin/start.sh