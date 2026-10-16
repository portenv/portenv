# SPDX-License-Identifier: Apache-2.0
#
# Add a Server's Check (ADR 0021 §3): one read-only probe, sent on stdin to
# `LC_ALL=C bash --noprofile --norc -s` over the server's pinned SSH
# connection. It writes nothing on the server: no package, file, user or
# setting (a test compares the file system before and after). Each answer
# is one key=value line between this run's markers; portenvd reads only
# those, ignores unknown keys and caps each line's length.
# check: reads only. Every probe below reads, or connects and closes.
set -u
say() { printf '%s=%s\n' "$1" "$2"; }
echo "PORTENV-CHECK-BEGIN __NONCE__"
if [ -r /etc/os-release ]; then
	# shellcheck disable=SC1091
	. /etc/os-release
	say os_id "${ID:-}"
	say os_version "${VERSION_ID:-}"
fi
say arch "$(uname -m 2>/dev/null)"
say mem_kb "$(awk '/^MemTotal:/{print $2}' /proc/meminfo 2>/dev/null)"
say disk_free_kb "$(df -Pk / 2>/dev/null | awk 'NR==2{print $4}')"
if [ "$(id -u)" = 0 ]; then say user_root yes; else say user_root no; fi
# Never a password: -n fails at once when sudo would ask. -l asks whether
# it may run, without running it (a run can leave a file, such as Ubuntu's
# ~/.sudo_as_admin_successful).
if sudo -n -l true >/dev/null 2>&1; then say sudo yes; else say sudo no; fi
say clock "$(date +%s)"
reach() { timeout 5 bash -c "</dev/tcp/$1/443" >/dev/null 2>&1 && echo yes || echo no; }
say https_ghcr "$(reach ghcr.io)"
say https_docker "$(reach download.docker.com)"
say docker "$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
if command -v apt-get >/dev/null 2>&1; then say apt yes; else say apt no; fi
say portenv "$(/usr/local/bin/portenv version 2>/dev/null | head -1 || true)"
if [ -e /dev/kvm ]; then say kvm yes; else say kvm no; fi
echo "PORTENV-CHECK-END __NONCE__"
