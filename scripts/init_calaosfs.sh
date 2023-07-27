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
