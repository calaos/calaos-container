#!/bin/bash

set -e

fs="/mnt/calaos"

for d in cache \
    haproxy \
    influxdb \
    zigbee2mqtt \
    mosquitto/data \
    config
do
    mkdir -p ${fs}/${d}
done
