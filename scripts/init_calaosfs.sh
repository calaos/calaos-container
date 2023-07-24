#!/bin/bash

set -e

fs="/mnt/calaos"

for d in cache \
    haproxy \
    influxdb \
    zigbee2mqtt \
    config
do
    mkdir -p ${fs}/${d}
done
