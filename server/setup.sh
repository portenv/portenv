#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Phase 0 server setup (milestone 0.5). Run as root on a fresh Ubuntu 24.04
# server (arm64 or amd64):
#
#   sudo ./setup.sh --portenv-bin ./portenv [--homes-device /dev/X | --homes-size 8G]
#                   [--storage-key "ssh-ed25519 AAAA..."] [--rest-user "BOX-ID:BCRYPT-HASH"]
#                   [--image ghcr.io/portenv/toolbox-node@sha256:...]
#                   [--runner-bin ./portenv-runner] [--mac-key "ssh-ed25519 AAAA..."]
#
# Installs Docker Engine from Docker's repository (signing key pinned), an
# encrypted LUKS volume for box homes unlocked at boot from a root-only key
# file, a chrooted SFTP-only storage account for repositories, and the
# portenv CLI, and restic's REST server on the loopback address for fast
# listing and lease tags (reached through the storage account's SSH
# connection). Opens no port reachable from outside: everything goes through
# the server's existing SSH. Safe to run again.
set -euo pipefail

# Docker's release signing key, as published in Docker's install
# documentation. A different key is refused.
DOCKER_KEY_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88

portenv_bin=""
homes_size=8G
homes_device=""
storage_key=""
rest_user=""
runner_bin=""
mac_key=""
only=""
image_ref=""
while (($#)); do
	case $1 in
		--portenv-bin) portenv_bin=$2; shift 2 ;;
		--homes-size) homes_size=$2; shift 2 ;;
		--homes-device) homes_device=$2; shift 2 ;;
		--storage-key) storage_key=$2; shift 2 ;;
		--rest-user) rest_user=$2; shift 2 ;;
		--runner-bin) runner_bin=$2; shift 2 ;;
		--mac-key) mac_key=$2; shift 2 ;;
		--only) only=$2; shift 2 ;;
		--image) image_ref=$2; shift 2 ;;
		*) echo "unknown option $1" >&2; exit 2 ;;
	esac
done

# restic's REST server: loopback only, as the storage account. A key in the
# storage account may forward to this port and nothing else.
REST_ADDR=127.0.0.1:7422
REST_HTPASSWD=/etc/portenv-rest.htpasswd

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
	install -d -m 0700 -o portenv-storage -g portenv-storage /srv/portenv/storage-root/storage /srv/portenv/storage-root/storage/boxes
	# sshd reads authorized keys as the target user, so the file must be
	# readable by portenv-storage: root-owned, 0644, outside root-only /etc/portenv.
	keys=/etc/ssh/portenv-storage.authorized_keys
	touch "$keys" && chown root:root "$keys" && chmod 0644 "$keys"
	if [[ -n $storage_key ]]; then
		# restrict: no shell, PTY or agent; the one exception is a forward to
		# the REST server's loopback port.
		line="restrict,port-forwarding,permitopen=\"$REST_ADDR\" $storage_key"
		grep -vF " $storage_key" "$keys" > "$keys.new" || true
		echo "$line" >> "$keys.new" && chmod 0644 "$keys.new" && mv "$keys.new" "$keys"
	fi
	# REST users (one per box: BOX-ID:BCRYPT-HASH). Only the hash is here;
	# the password stays on the box's machines.
	touch "$REST_HTPASSWD" && chown root:portenv-storage "$REST_HTPASSWD" && chmod 0640 "$REST_HTPASSWD"
	if [[ -n $rest_user ]]; then
		[[ $rest_user =~ ^box-[0-9a-f]+:\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$ ]] || die "--rest-user must be BOX-ID:BCRYPT-HASH (from portenv init)"
		{ grep -v "^${rest_user%%:*}:" "$REST_HTPASSWD" || true; echo "$rest_user"; } > "$REST_HTPASSWD.new"
		chown root:portenv-storage "$REST_HTPASSWD.new" && chmod 0640 "$REST_HTPASSWD.new" && mv "$REST_HTPASSWD.new" "$REST_HTPASSWD"
		systemctl try-restart portenv-rest 2>/dev/null || true
	fi
	cat > /etc/ssh/sshd_config.d/50-portenv-storage.conf <<CONF
Match User portenv-storage
	ChrootDirectory /srv/portenv/storage-root
	ForceCommand internal-sftp
	AuthorizedKeysFile /etc/ssh/portenv-storage.authorized_keys
	AllowTcpForwarding local
	PermitOpen $REST_ADDR
	AllowStreamLocalForwarding no
	AllowAgentForwarding no
	X11Forwarding no
	PermitTTY no
	PasswordAuthentication no
CONF
	sshd -t || die "sshd rejected the storage account configuration"
	systemctl reload ssh 2>/dev/null || kill -HUP "$(cat /run/sshd.pid 2>/dev/null)" 2>/dev/null || true
	echo "repositories: sftp:portenv-storage@<this server>:/storage; boxes on this server use sftp:portenv-storage@host.portenv.internal:/storage"
}


# rest-server 0.14.0 from its GitHub release; checksums taken from the
# release's SHA256SUMS, signed by restic's release key (CF8F 18F2 8445 7597
# 3F79 D4E1 91A6 868B D3F7 A907), like restic below.
REST_SERVER_VERSION=0.14.0
declare -A REST_SERVER_SHA256=(
	[arm64]=cef139cbe8b27b16bda731d17f093b0aa466b8c60b136c12d78b6f2bff3daf22
	[amd64]=4c9c95bc079a0334e81fad379b19dc5c3353c71c2c88d652cafce2081c2b1c66
)
install_rest_server() {
	say "restic REST server on $REST_ADDR (loopback only)"
	local arch; arch=$(dpkg --print-architecture)
	if ! /usr/local/bin/rest-server --version 2>/dev/null | grep -q "rest-server $REST_SERVER_VERSION"; then
		local tmp; tmp=$(mktemp -d)
		curl -fsSL -o "$tmp/rs.tar.gz" "https://github.com/restic/rest-server/releases/download/v$REST_SERVER_VERSION/rest-server_${REST_SERVER_VERSION}_linux_${arch}.tar.gz"
		echo "${REST_SERVER_SHA256[$arch]}  $tmp/rs.tar.gz" | sha256sum -c --quiet - || { rm -rf "$tmp"; die "rest-server download failed its checksum"; }
		tar -xzf "$tmp/rs.tar.gz" -C "$tmp" && install -m 0755 "$tmp/rest-server_${REST_SERVER_VERSION}_linux_${arch}/rest-server" /usr/local/bin/rest-server
		rm -rf "$tmp"
	fi
	/usr/local/bin/rest-server --version | head -1
	if [[ ! -d /run/systemd/system ]]; then
		echo "no systemd: start it with: runuser -u portenv-storage -- /usr/local/bin/rest-server $(rest_server_args)"
		return
	fi
	cat > /etc/systemd/system/portenv-rest.service <<UNIT
[Unit]
Description=Portenv: restic REST server for box repositories (loopback only)
After=network.target

[Service]
User=portenv-storage
Group=portenv-storage
UMask=0077
ExecStart=/usr/local/bin/rest-server $(rest_server_args)
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ReadWritePaths=/srv/portenv/storage-root/storage
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_UNIX
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
	systemctl daemon-reload
	systemctl enable --now portenv-rest >/dev/null 2>&1
	systemctl restart portenv-rest
}
rest_server_args() { echo "--path /srv/portenv/storage-root/storage/boxes --listen $REST_ADDR --htpasswd-file $REST_HTPASSWD"; }

# The runner (portenv-runner): opens boxes with the agent channel and serves
# the Mac through `sudo portenv app` over SSH. Root, a root-only socket, and
# boxes keep running when it restarts.
setup_runner() {
	say "portenv-runner"
	if [[ -n $runner_bin ]]; then
		install -m 0755 "$runner_bin" /usr/local/bin/portenv-runner
	fi
	[[ -x /usr/local/bin/portenv-runner ]] || { echo "no portenv-runner (pass --runner-bin)"; return; }
	if [[ ! -d /run/systemd/system ]]; then
		echo "no systemd: start it with: /usr/local/bin/portenv-runner"
		return
	fi
	cat > /etc/systemd/system/portenv-runner.service <<'UNIT'
[Unit]
Description=Portenv runner: boxes on this server, reached through the agent channel
After=docker.service network.target
Requires=docker.service

[Service]
ExecStart=/usr/local/bin/portenv-runner
# The same Portenv directory as `sudo portenv` (root's).
Environment=HOME=/root
Restart=on-failure
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
	systemctl daemon-reload
	systemctl enable --now portenv-runner >/dev/null 2>&1
	systemctl restart portenv-runner
	/usr/local/bin/portenv-runner version
}

# The Mac's SSH user: a dedicated account whose sudo allows only portenv, by
# full path (never a shell), and whose SSH may forward only to this server's
# loopback (the box agents' ports).
setup_ssh_user() {
	say "SSH user portenv (sudo: /usr/local/bin/portenv only)"
	id portenv >/dev/null 2>&1 || useradd --create-home --shell /bin/bash portenv
	install -d -m 0700 -o portenv -g portenv /home/portenv/.ssh
	if [[ -n $mac_key ]]; then
		[[ $mac_key =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+ ]] || die "--mac-key must be an ssh-ed25519 public key"
		grep -qF "$mac_key" /home/portenv/.ssh/authorized_keys 2>/dev/null || echo "$mac_key" >> /home/portenv/.ssh/authorized_keys
		chown portenv:portenv /home/portenv/.ssh/authorized_keys && chmod 0600 /home/portenv/.ssh/authorized_keys
	fi
	printf 'portenv ALL=(root) NOPASSWD: /usr/local/bin/portenv\n' > /etc/sudoers.d/portenv
	chmod 0440 /etc/sudoers.d/portenv
	visudo -cf /etc/sudoers.d/portenv >/dev/null || die "sudoers rule for portenv rejected"
	cat > /etc/ssh/sshd_config.d/51-portenv-user.conf <<'CONF'
Match User portenv
	AllowTcpForwarding local
	PermitOpen 127.0.0.1:*
	AllowStreamLocalForwarding no
	AllowAgentForwarding no
	X11Forwarding no
	PermitTTY no
	PasswordAuthentication no
CONF
	sshd -t || die "sshd rejected the portenv user configuration"
	systemctl reload ssh 2>/dev/null || kill -HUP "$(cat /run/sshd.pid 2>/dev/null)" 2>/dev/null || true
}

[[ $(id -u) == 0 ]] || die "run as root (sudo)"
case $only in
	storage-account) setup_storage_account; exit 0 ;;
	rest-server) install_rest_server; exit 0 ;;
	runner) setup_runner; setup_ssh_user; exit 0 ;;
	"") ;;
	*) die "unknown --only $only (want storage-account, rest-server or runner)" ;;
esac

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
apt-get install -y -q ca-certificates curl gnupg cryptsetup bzip2 >/dev/null

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

if [[ -n $image_ref ]]; then
	say "toolbox image (by digest)"
	[[ $image_ref == *@sha256:* ]] || die "--image must be pinned by digest (…@sha256:…)"
	docker pull -q "$image_ref"
fi

say "restic on the host (lease checks, listing, keys; never /home)"
# restic 0.19.1 from its GitHub release; checksums taken from the release's
# SHA256SUMS, which is signed by restic's release key (CF8F 18F2 8445 7597
# 3F79 D4E1 91A6 868B D3F7 A907).
RESTIC_VERSION=0.19.1
declare -A RESTIC_SHA256=(
	[arm64]=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465
	[amd64]=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c
)
if ! restic version 2>/dev/null | grep -q "restic $RESTIC_VERSION "; then
	tmp=$(mktemp)
	curl -fsSL -o "$tmp" "https://github.com/restic/restic/releases/download/v$RESTIC_VERSION/restic_${RESTIC_VERSION}_linux_${arch}.bz2"
	echo "${RESTIC_SHA256[$arch]}  $tmp" | sha256sum -c --quiet - || { rm -f "$tmp"; die "restic download failed its checksum"; }
	bunzip2 -c "$tmp" > /usr/local/bin/restic && chmod 0755 /usr/local/bin/restic && rm -f "$tmp"
fi
restic version | head -1

install_rest_server

say "portenv CLI"
if [[ -n $portenv_bin ]]; then
	install -m 0755 "$portenv_bin" /usr/local/bin/portenv
fi
install -d -m 0700 /root/.config/Portenv
printf '{"homes_dir": "/var/lib/portenv/homes"}\n' > /root/.config/Portenv/machine.json
chmod 0600 /root/.config/Portenv/machine.json
command -v portenv >/dev/null && portenv version

setup_runner
setup_ssh_user

say "done"
echo "No port was opened (the REST server listens on $REST_ADDR only). Box homes: /var/lib/portenv/homes. Keys: /etc/portenv (root-only)."
