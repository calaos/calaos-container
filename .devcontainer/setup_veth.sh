#!/bin/bash
# Create 4 pairs of veth interfaces

for i in {0..1}; do
    veth_in="calaos-${i}-0"
    veth_out="calaos-${i}-1"

    ip link add "$veth_in" type veth peer name "$veth_out"
    ip link set "$veth_in" up
    ip link set "$veth_out" up
done
