#!/bin/bash

set -e

fs="/mnt/calaos"

for d in cache \
    haproxy \
    influxdb/data \
    influxdb/config \
    zigbee2mqtt \
    mosquitto/data \
    grafana/data \
    config
do
    mkdir -p ${fs}/${d}
done

#Create a unique token
[ ! -e /run/calaos-ct.token ] && {
    echo "$(date +%s-%N)-$RANDOM" > /run/calaos-ct.token
}
