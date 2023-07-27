#!/bin/bash

set -e

# Init script for Mosquitto for calaos-OS
# This script will initialize with a default config

fs="/mnt/calaos/mosquitto"

mkdir -p ${fs}

if [ ! -e "${fs}/mosquitto.conf" ]
then
cat > ${fs}/mosquitto.conf <<- EOF
persistence true
persistence_location /mosquitto/data/
log_dest stdout
EOF

echo "Initial mosquitto.conf file created."
fi
