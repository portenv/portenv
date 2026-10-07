#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Phase 0 server setup (milestone 0.5). Run as root on a fresh Ubuntu 24.04
# server (arm64 or amd64):
#
#   sudo ./setup.sh --portenv-bin ./portenv [--homes-device /dev/X | --homes-size 8G]
#                   [--storage-key "ssh-ed25519 AAAA..."]
#
# Installs Docker Engine from Docker's repository (signing key pinned), an
# encrypted LUKS volume for box homes unlocked at boot from a root-only key
# file, a chrooted SFTP-only storage account for repositories, and the
# portenv CLI. Opens no port: everything goes through the server's existing
# SSH. Safe to run again.
set -euo pipefail

# Docker's release signing key, as published in Docker's install
# documentation. A different key is refused.
DOCKER_KEY_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88

portenv_bin=""
homes_size=8G
homes_device=""
storage_key=""
only=""
while (($#)); do
	case $1 in
		--portenv-bin) portenv_bin=$2; shift 2 ;;
		--homes-size) homes_size=$2; shift 2 ;;
		--homes-device) homes_device=$2; shift 2 ;;
		--storage-key) storage_key=$2; shift 2 ;;
		--only) only=$2; shift 2 ;;
		*) echo "unknown option $1" >&2; exit 2 ;;
	esac
done

say() { printf '\n== %s\n' "$*"; }
die() { printf 'setup: %s\n' "$*" >&2; exit 1; }

setup_storage_account() {
	say "SFTP storage account (chrooted, SFTP only)"
	# Every box, including boxes on this server, reaches repositories over SFTP
	# as this account, so all repository files have one owner whatever its uid.
	getent group portenv-storage >/dev/null || groupadd --system portenv-storage
	id portenv-storage >/dev/null 2>&1 || useradd --system --gid portenv-storage --no-create-home \
		--home-dir / --shell /usr/sbin/nologin portenv-storage
	install -d -m 0755 -o root -g root /srv/portenv /srv/portenv/storage-root
	install -d -m 0700 -o portenv-storage -g portenv-storage /srv/portenv/storage-root/storage
	# sshd reads authorized keys as the target user, so the file must be
	# readable by portenv-storage: root-owned, 0644, outside root-only /etc/portenv.
	keys=/etc/ssh/portenv-storage.authorized_keys
	touch "$keys" && chown root:root "$keys" && chmod 0644 "$keys"
	if [[ -n $storage_key ]]; then
		line="restrict $storage_key"
		grep -qxF "$line" "$keys" || echo "$line" >> "$keys"
	fi
	cat > /etc/ssh/sshd_config.d/50-portenv-storage.conf <<'CONF'
Match User portenv-storage
	ChrootDirectory /srv/portenv/storage-root
	ForceCommand internal-sftp
	AuthorizedKeysFile /etc/ssh/portenv-storage.authorized_keys
	AllowTcpForwarding no
	AllowAgentForwarding no
	X11Forwarding no
	PermitTTY no
	PasswordAuthentication no
CONF
	sshd -t || die "sshd rejected the storage account configuration"
	systemctl reload ssh 2>/dev/null || kill -HUP "$(cat /run/sshd.pid 2>/dev/null)" 2>/dev/null || true
	echo "repositories: sftp:portenv-storage@<this server>:/storage; boxes on this server use sftp:portenv-storage@host.portenv.internal:/storage"
}


[[ $(id -u) == 0 ]] || die "run as root (sudo)"
if [[ $only == storage-account ]]; then
	setup_storage_account
	exit 0
fi
[[ -z $only ]] || die "unknown --only $only (want storage-account)"

say "preflight"
. /etc/os-release
[[ $ID == ubuntu && ($VERSION_ID == 24.04 || $VERSION_ID == 22.04) ]] || die "needs Ubuntu 22.04 or 24.04 (found $PRETTY_NAME)"
arch=$(dpkg --print-architecture)
[[ $arch == arm64 || $arch == amd64 ]] || die "needs arm64 or amd64 (found $arch)"
mem_mb=$(awk '/MemTotal/{print int($2/1024)}' /proc/meminfo)
(( mem_mb >= 1800 )) || die "needs at least 2 GB of memory (found ${mem_mb} MB)"
free_gb=$(df -BG --output=avail / | tail -1 | tr -dc 0-9)
# A fresh install needs room for the homes volume and the toolbox image;
# once the homes volume exists, a few GB is enough to run setup again.
need_gb=15
[[ -e /var/lib/portenv/homes.luks || -n $homes_device ]] && need_gb=3
(( free_gb >= need_gb )) || die "needs at least ${need_gb} GB free on / (found ${free_gb} GB)"
timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -qx yes || echo "warning: the clock is not NTP-synchronised; leases compare save times"
[[ -e /dev/kvm ]] && echo "KVM available (recorded for microVMs later)" || echo "no KVM (fine for the docker driver)"
[[ -z $portenv_bin || -x $portenv_bin ]] || die "--portenv-bin $portenv_bin is not an executable"
echo "ok: $PRETTY_NAME, $arch, ${mem_mb} MB memory, ${free_gb} GB free"

say "packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q ca-certificates curl gnupg cryptsetup >/dev/null

if (( mem_mb < 3000 )) && ! swapon --show | grep -q /swapfile; then
	say "2 GB swap file (small machine)"
	fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap -q /swapfile && swapon /swapfile
	grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

say "Docker Engine"
if ! command -v docker >/dev/null; then
	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
	fpr=$(gpg --show-keys --with-colons /etc/apt/keyrings/docker.asc | awk -F: '$1 == "fpr" { print $10; exit }')
	if [[ $fpr != "$DOCKER_KEY_FINGERPRINT" ]]; then
		rm -f /etc/apt/keyrings/docker.asc
		die "Docker's signing key has fingerprint '$fpr', want $DOCKER_KEY_FINGERPRINT"
	fi
	chmod a+r /etc/apt/keyrings/docker.asc
	cat > /etc/apt/sources.list.d/docker.sources <<SRC
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${UBUNTU_CODENAME:-$VERSION_CODENAME}
Components: stable
Architectures: $arch
Signed-By: /etc/apt/keyrings/docker.asc
SRC
	apt-get update -q
	apt-get install -y -q docker-ce docker-ce-cli containerd.io >/dev/null
fi
docker version --format 'Docker {{.Server.Version}}'

say "encrypted volume for box homes"
# The key file lives on the root disk and unlocks the volume at boot. It
# never protects against root on the running server. On its own device
# (--homes-device) the volume protects a disk or snapshot of that device
# leaving the server; as a file on the root disk, a copy of that disk
# carries both volume and key (docs/PLAN.md, Phase 0 minimum).
install -d -m 0700 /etc/portenv /etc/portenv/luks
install -d -m 0755 /var/lib/portenv
keyfile=/etc/portenv/luks/homes.key
image=/var/lib/portenv/homes.luks
if [[ -n $homes_device ]]; then
	[[ -b $homes_device ]] || die "--homes-device $homes_device is not a block device"
	if ! cryptsetup isLuks "$homes_device"; then
		blkid "$homes_device" >/dev/null 2>&1 && die "$homes_device already holds data; refusing to format it"
		head -c 64 /dev/urandom > "$keyfile" && chmod 0400 "$keyfile"
		cryptsetup luksFormat -q --type luks2 --key-file "$keyfile" "$homes_device"
		cryptsetup open --key-file "$keyfile" "$homes_device" portenv-homes
		mkfs.ext4 -q -L portenv-homes /dev/mapper/portenv-homes
		cryptsetup close portenv-homes
	fi
	image=$homes_device
	echo "homes volume on its own device: $homes_device"
elif [[ ! -e $image ]]; then
	head -c 64 /dev/urandom > "$keyfile" && chmod 0400 "$keyfile"
	fallocate -l "$homes_size" "$image" && chmod 0600 "$image"
	cryptsetup luksFormat -q --type luks2 --key-file "$keyfile" "$image"
	cryptsetup open --key-file "$keyfile" "$image" portenv-homes
	mkfs.ext4 -q -L portenv-homes /dev/mapper/portenv-homes
	cryptsetup close portenv-homes
fi
[[ $(stat -c '%a %U' "$keyfile") == "400 root" ]] || die "$keyfile must be root-only (0400)"
unit=/etc/systemd/system/portenv-homes.service
cat > "$unit" <<'UNIT'
[Unit]
Description=Portenv: unlock and mount the encrypted volume for box homes
Before=docker.service
After=local-fs.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'cryptsetup status portenv-homes >/dev/null || cryptsetup open --key-file /etc/portenv/luks/homes.key @IMAGE@ portenv-homes'
ExecStart=/bin/mkdir -p /var/lib/portenv/homes
ExecStart=/bin/sh -c 'mountpoint -q /var/lib/portenv/homes || mount /dev/mapper/portenv-homes /var/lib/portenv/homes'
ExecStop=/bin/umount /var/lib/portenv/homes
ExecStop=/sbin/cryptsetup close portenv-homes

[Install]
WantedBy=multi-user.target
UNIT
sed -i "s|@IMAGE@|$image|" "$unit"
install -d /etc/systemd/system/docker.service.d
cat > /etc/systemd/system/docker.service.d/portenv-homes.conf <<'UNIT'
[Unit]
Requires=portenv-homes.service
After=portenv-homes.service
UNIT
systemctl daemon-reload
systemctl enable -q --now portenv-homes.service
systemctl restart docker
mountpoint -q /var/lib/portenv/homes || die "the homes volume is not mounted"
echo "mounted $(df -h --output=size /var/lib/portenv/homes | tail -1 | tr -d ' ') at /var/lib/portenv/homes"
if [[ -n $homes_device ]]; then
	echo "box homes: encrypted on their own device ($homes_device); a copy of that device alone cannot be read"
else
	echo "box homes: encrypted at rest by your provider (the volume and its key share the root disk)"
fi

setup_storage_account

say "portenv CLI"
if [[ -n $portenv_bin ]]; then
	install -m 0755 "$portenv_bin" /usr/local/bin/portenv
fi
install -d -m 0700 /root/.config/Portenv
printf '{"homes_dir": "/var/lib/portenv/homes"}\n' > /root/.config/Portenv/machine.json
chmod 0600 /root/.config/Portenv/machine.json
command -v portenv >/dev/null && portenv version

say "done"
echo "No port was opened. Box homes: /var/lib/portenv/homes. Keys: /etc/portenv (root-only)."
