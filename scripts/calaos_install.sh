#!/bin/bash
# shellcheck disable=SC2034,SC2086

# Usage : install.sh [destination_disk]
set -e

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

parse_cmdline()
{
    # Parse command line and LABEL variable
    set -- $(cat /proc/cmdline)
    for x in "$@"; do
        case "$x" in
            LABEL=*)
                eval "${x}"
            ;;
        esac
    done
}

#args: /dev/sda
get_dev_part_count()
{
    lsblk --json $1 | jq -r ".blockdevices[0].children | length"
}

#args: /dev/sda <partnum>
get_dev_part()
{
    part=$2
    lsblk --json $1 | jq -r ".blockdevices[0].children[$((part - 1))].name"
}

err_report() {
    err "Error on line $1"
}

trap 'err_report $LINENO' ERR

destination=$1
if [ -z "$destination" ]
then
    err "No disk argument given. Usage: $0 /dev/xxx"
    exit 1
fi

parse_cmdline

if [ $LABEL == "live-efi" ]; then
    info "--> Install in UEFI mode"
else
    info "--> Install in BIOS mode"
fi

info "--> Installing on disk $destination"

origin_rootfs=$(mount | grep "on \/ " | cut -d ' ' -f1)

if [ "$origin_rootfs" == "$destination" ]
then
    err "Can't install on same disk as source"
    exit 1
fi

# Deleting partition table
info "--> Deleting partition table on ${destination}"
dd if=/dev/zero of=${destination} bs=512 count=35 > /dev/null


if [ $LABEL == "live-efi" ]; then
    info "--> Creating GPT partition table"
    parted -s ${destination} mklabel gpt > /dev/null
    parted -s ${destination} mkpart "efi" fat32 1MiB 513MiB > /dev/null
    parted -s ${destination} mkpart "swap" linux-swap 513MiB 2.5GiB > /dev/null
    parted -s ${destination} mkpart "calaos" ext4 2.5GiB 100% > /dev/null
    parted -s ${destination} set 1 esp on > /dev/null
    parted -s ${destination} set 2 boot on > /dev/null
    parted -s ${destination} print

    destination_esp="/dev/$(get_dev_part ${destination} 1)"
    destination_swap="/dev/$(get_dev_part ${destination} 2)"
    destination_rootfs="/dev/$(get_dev_part ${destination} 3)"

    info "--> Formating partitions"
    mkfs.vfat -F32 ${destination_esp} > /dev/null
    mkswap ${destination_swap}
    mkfs.btrfs --force --label "calaos-os" ${destination_rootfs}
else
    info "--> Creating Bios partition table"
    parted -s ${destination} mklabel msdos  > /dev/null
    parted -s ${destination} mkpart primary linux-swap 1MiB 2049MiB > /dev/null
    parted -s ${destination} mkpart primary ext4 2049MiB 100% > /dev/null
    parted -s ${destination} set 2 boot on > /dev/null
    parted -s ${destination} print

    destination_esp="/dev/$(get_dev_part ${destination} 2)"
    destination_swap="/dev/$(get_dev_part ${destination} 1)"
    destination_rootfs="/dev/$(get_dev_part ${destination} 2)"

    info "--> Formating partitions"
    mkswap ${destination_swap}
    mkfs.btrfs --force --label "calaos-os" ${destination_rootfs}
fi

uuid_rootfs=$(blkid -s PARTUUID -o value ${destination_rootfs})

info "--> Create BTRFS filesystem"
dst="/mnt/destination_rootfs"
mkdir -p ${dst}
mount ${destination_rootfs} ${dst}
btrfs subvolume create ${dst}/@
btrfs subvolume create ${dst}/@/.snapshots

#Create a subvolume for the initial snapshot which will be the target of the installation
mkdir ${dst}/@/.snapshots/1

mkdir ${dst}/@/boot \
      ${dst}/@/usr \
      ${dst}/@/var

btrfs subvolume create ${dst}/@/.snapshots/1/snapshot
btrfs subvolume create ${dst}/@/boot/grub
btrfs subvolume create ${dst}/@/opt
btrfs subvolume create ${dst}/@/root
btrfs subvolume create ${dst}/@/srv
btrfs subvolume create ${dst}/@/tmp
btrfs subvolume create ${dst}/@/usr/local
btrfs subvolume create ${dst}/@/var/cache
btrfs subvolume create ${dst}/@/var/log
btrfs subvolume create ${dst}/@/var/spool
btrfs subvolume create ${dst}/@/var/tmp

#Snapper stores metadata for each snapshot in the snapshot's directory /@/.snapshots/# where "#" represents the snapshot number in an .xml file.
cat > ${dst}/@/.snapshots/1/info.xml <<EOF
<?xml version="1.0"?>
<snapshot>
	<type>single</type>
	<num>1</num>
	<date>$(date +"%Y-%m-%d %H:%M:%S")</date>
	<description>First Root Filesystem Created at Installation</description>
</snapshot>
EOF

#make the initial snapshot subvolume
btrfs subvolume set-default $(btrfs subvolume list ${dst} | grep "@/.snapshots/1/snapshot" | grep -oP '(?<=ID )[0-9]+') ${dst}
btrfs subvolume get-default ${dst}

#disable CoW for /var
chattr +C ${dst}/@/var/{cache,log,spool,tmp}

btrfs subvolume list ${dst}

#umount and remount properly the rootfs
umount ${dst}
mount -o noatime,compress=zstd ${destination_rootfs} ${dst}

#make mountpoints and mount subvolumes
mkdir -p ${dst}/{.snapshots,boot/grub,opt,root,srv,tmp,usr/local,var/cache,var/log,var/spool,var/tmp}

mount -o noatime,compress=zstd,subvol=@/.snapshots ${destination_rootfs} ${dst}/.snapshots
mount -o noatime,compress=zstd,subvol=@/boot/grub ${destination_rootfs} ${dst}/boot/grub
mount -o noatime,compress=zstd,subvol=@/opt ${destination_rootfs} ${dst}/opt
mount -o noatime,compress=zstd,subvol=@/root ${destination_rootfs} ${dst}/root
mount -o noatime,compress=zstd,subvol=@/srv ${destination_rootfs} ${dst}/srv
mount -o noatime,compress=zstd,subvol=@/tmp ${destination_rootfs} ${dst}/tmp
mount -o noatime,compress=zstd,subvol=@/usr/local ${destination_rootfs} ${dst}/usr/local
mount -o noatime,compress=zstd,subvol=@/var/cache ${destination_rootfs} ${dst}/var/cache
mount -o noatime,compress=zstd,subvol=@/var/log ${destination_rootfs} ${dst}/var/log
mount -o noatime,compress=zstd,subvol=@/var/spool ${destination_rootfs} ${dst}/var/spool
mount -o noatime,compress=zstd,subvol=@/var/tmp ${destination_rootfs} ${dst}/var/tmp

#mount EFI
if [ $LABEL == "live-efi" ]; then
    mkdir -p ${dst}/efi
    mount ${destination_esp} ${dst}/efi
fi

#enable swap
swapon ${destination_swap}

info "--> Copy rootfs from live usb"
src="/mnt/origin_rootfs"
mkdir -p ${src}
mount ${origin_rootfs} ${src}

rsync -avh ${src}/ ${dst} \
    --exclude /.calaos-live \
    --exclude /tmp \
    --exclude /run \
    --exclude /dev \
    --exclude /proc \
    --exclude /sys \
    --exclude /var/run/containers \
    --exclude /media \
    --exclude /mnt/destination_rootfs \
    --exclude /mnt/origin_rootfs

#Cleanup initramfs scrits not needed for installed rootfs
rm -rf ${dst}/.calaos-live \
    ${dst}/etc/calaos-resize \
    ${dst}/usr/share/initramfs-tools/hooks/resizeroot \
    ${dst}/usr/share/initramfs-tools/scripts/local-bottom/resizeroot

genfstab -U ${dst} >> ${dst}/etc/fstab

mkdir -p ${dst}/{tmp,run,dev,proc,sys,var/run/containers,media}

cat ${dst}/etc/fstab

#Fix grub with BTRFS to use /@/.snapshots/1/snapshot/boot instead of /@/boot
# shellcheck disable=SC2016
sed -i 's/rootflags=subvol=${rootsubvol}//g' ${dst}/etc/grub.d/10_linux
# shellcheck disable=SC2016
sed -i 's/rootflags=subvol=${rootsubvol}//g' ${dst}/etc/grub.d/20_linux_xen

#regen mkinitcpio in rootfs
arch-chroot ${dst} update-initramfs -u

#Initialize Snapper. Unmount our predefined .snapshot folder, let snapper recreate it (it fails otherwise)
#remove the snapshot created by snapper, remount our .snapshot
info "--> Initializing Snapper"
umount ${dst}/.snapshots
rm -r ${dst}/.snapshots
arch-chroot ${dst} snapper --no-dbus -c root create-config /
btrfs subvolume list ${dst}
btrfs subvolume delete ${dst}/.snapshots
mkdir -p ${dst}/.snapshots
mount -o noatime,compress=zstd,subvol=@/.snapshots ${destination_rootfs} ${dst}/.snapshots
chmod 750 ${dst}/.snapshots

info "--> Install Bootloader"

if [ $LABEL == "live-efi" ]; then
    # --removable Needed to install uefi payload in /EFI/BOOT/BOOTX64.EFI for buggy bios
    arch-chroot ${dst} grub-install --target=x86_64-efi \
                                    --efi-directory=/efi \
                                    --bootloader-id=Calaos-OS \
                                    --removable \
                                    --modules="normal test efi_gop efi_uga search echo linux all_video gfxmenu gfxterm_background gfxterm_menu gfxterm loadenv configfile gzio part_gpt btrfs"
else
    arch-chroot ${dst} grub-install --target=i386-pc ${destination}
fi

#keep default 5s boot menu timeout for now. To allow user to choose a snapshot if any
#sed -i 's/GRUB_TIMEOUT=[0-9]/GRUB_TIMEOUT=1/g' ${dst}/etc/default/grub
sed -i 's/^GRUB_DISTRIBUTOR=.*$/GRUB_DISTRIBUTOR="Calaos OS"/g' ${dst}/etc/default/grub
sed -i 's/^.*GRUB_COLOR_NORMAL=.*$/GRUB_COLOR_NORMAL="light-blue\/black"/g' ${dst}/etc/default/grub
sed -i 's/^.*GRUB_COLOR_HIGHLIGHT=.*$/GRUB_COLOR_HIGHLIGHT="white\/blue"/g' ${dst}/etc/default/grub

sed -i 's/^#*GRUB_BTRFS_DISABLE=.*$/GRUB_BTRFS_DISABLE="true"/g' ${dst}/etc/default/grub-btrfs/config

arch-chroot ${dst} update-grub

#Only enable grub-btrfs after installation. It does not work in chroot
sed -i 's/^#*GRUB_BTRFS_DISABLE=.*$/GRUB_BTRFS_DISABLE="false"/g' ${dst}/etc/default/grub-btrfs/config

info "--> Enable services"

arch-chroot ${dst} systemctl enable \
        fstrim.timer \
        $(systemd-escape --template btrfs-scrub@.timer --path /dev/disk/by-partuuid/$uuid_rootfs) \
        snapper-timeline.timer \
        snapper-cleanup.timer \
        grub-btrfsd.service

arch-chroot ${dst} snapper --no-dbus -c root set-config "NUMBER_LIMIT=10"
arch-chroot ${dst} snapper --no-dbus -c root set-config "NUMBER_MIN_AGE=5400"
arch-chroot ${dst} snapper --no-dbus -c root set-config "TIMELINE_LIMIT_DAILY=14"
arch-chroot ${dst} snapper --no-dbus -c root set-config "TIMELINE_LIMIT_WEEKLY=4"
arch-chroot ${dst} snapper --no-dbus -c root set-config "TIMELINE_LIMIT_MONTHLY=6"
arch-chroot ${dst} snapper --no-dbus -c root set-config "TIMELINE_LIMIT_YEARLY=2"

info "--> Unmouting all partitions"

umount ${dst}/.snapshots
umount ${dst}/boot/grub
umount ${dst}/opt
umount ${dst}/root
umount ${dst}/srv
umount ${dst}/tmp
umount ${dst}/usr/local
umount ${dst}/var/cache
umount ${dst}/var/log
umount ${dst}/var/spool
umount ${dst}/var/tmp
if [ $LABEL == "live-efi" ]; then
    umount ${dst}/efi
fi
umount ${dst}
umount ${src}

green "--> Installation successful, you can now reboot"