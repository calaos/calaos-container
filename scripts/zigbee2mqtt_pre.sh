#!/bin/bash

set -e

# Init script for Zigbee2mqtt for calaos-OS
# This script will initialize with a default config

fs="/mnt/calaos/zigbee2mqtt"

mkdir -p ${fs}

if [ ! -e "${fs}/configuration.yaml" ]
then
cat > ${fs}/configuration.yaml <<- EOF
# Home Assistant integration (MQTT discovery)
homeassistant: false

# allow new devices to join
permit_join: true

# MQTT settings
mqtt:
  # MQTT base topic for zigbee2mqtt MQTT messages
  base_topic: zigbee2mqtt
  # MQTT server URL
  server: 'mqtt://127.0.0.1'
  # MQTT server authentication, uncomment if required:
  # user: my_user
  # password: my_password

# Serial settings
serial:
  # Location of CC2531 USB sniffer
  port: /dev/ttyACM0

EOF

echo "Initial Zigbee2mqtt config file created."
fi
