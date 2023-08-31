#!/bin/bash

set -e

fs="/mnt/calaos"
rundir="/run/calaos"

for d in cache \
    haproxy \
    influxdb/data \
    influxdb/config \
    zigbee2mqtt \
    mosquitto/data \
    mosquitto/config \
    grafana/data \
    config
do
    mkdir -p ${fs}/${d}
done

mkdir -p $rundir

#Create a unique token
if [ ! -e $rundir/calaos-ct.token ]
then
    echo "$(date +%s-%N)-$RANDOM" > $rundir/calaos-ct.token
fi

if [ -e /.calaos-live ]
then
    touch $rundir/calaos-live
fi
