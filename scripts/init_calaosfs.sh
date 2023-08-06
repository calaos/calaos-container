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

mkdir -p /run/calaos

#Create a unique token
if [ ! -e /run/calaos-ct.token ]
then
    echo "$(date +%s-%N)-$RANDOM" > /run/calaos-ct.token
fi

if [ -e /.calaos-live ]
then
    touch /run/calaos/calaos-live
fi
