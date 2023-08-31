#!/bin/bash

set -e

cidfile=$1
[ -z "$cidfile" ] && {
    echo "Missing arg"
    exit 1
}

fs="/mnt/calaos/zigbee2mqtt"

devlist()
{
    grep '/dev/' ${fs}/configuration.yaml | awk '{print $2}'
}

append_dev_opts()
{
    local device=''

    while read -r device ; do
        echo " --device $device "
    done < <(devlist)
}

# shellcheck disable=SC2046

podman run \
        --name=zigbee2mqtt \
        --cidfile="${cidfile}" --replace --rm --tz=local \
        --sdnotify=conmon -d \
        -v /mnt/calaos/zigbee2mqtt:/app/data \
        --network=host \
        $(append_dev_opts) \
        docker.io/koenkk/zigbee2mqtt
