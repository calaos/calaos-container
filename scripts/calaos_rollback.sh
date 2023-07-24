#!/bin/bash

set -e

rootdev=$(mount | grep /.snapshots | awk '{print $1}')

NOCOLOR='\033[0m'
CYAN='\033[0;36m'
RED='\033[0;31m'
GREEN='\033[0;32m'
ORANGE='\033[0;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
LIGHTGRAY='\033[0;37m'
DARKGRAY='\033[1;30m'
LIGHTRED='\033[1;31m'
LIGHTGREEN='\033[1;32m'
YELLOW='\033[1;33m'
LIGHTBLUE='\033[1;34m'
LIGHTPURPLE='\033[1;35m'
LIGHTCYAN='\033[1;36m'
WHITE='\033[1;37m'

info()
{
    echo -e "${CYAN}$*${NOCOLOR}"
}

green()
{
    echo -e "${GREEN}$*${NOCOLOR}"
}

err()
{
    echo -e "${RED}$*${NOCOLOR}"
}

if [ -z "$rootdev" ]
then
    err "Snapshot btrfs device not found"
    exit 1
fi

#check if we are on an overlayfs (booted from a snapshot entry in grub)
if ! mount | grep "rootfs.*overlay" > /dev/null
then
    err "Not booted from a readonly btrfs snapshot"
    exit 1
fi

dst="/mnt"

cmdline=$(cat /proc/cmdline)
reg="subvol=@\/.snapshots\/([0-9]+)\/snapshot"
if [[ $cmdline =~ $reg ]]
then
    snapNumber="${BASH_REMATCH[1]}"
fi

#mount temporary rootfs and snapshot subvolume to be able to use snapper
mount -o noatime,compress=zstd ${rootdev} ${dst}
mount -o noatime,compress=zstd,subvol=@/.snapshots ${rootdev} ${dst}/.snapshots

info "--> Rollback rootfs to snapshot #$snapNumber"
arch-chroot ${dst} snapper --no-dbus rollback $snapNumber
arch-chroot ${dst} snapper --no-dbus list

umount ${dst}/.snapshots
umount ${dst}