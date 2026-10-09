# SPDX-License-Identifier: Apache-2.0
#
# Sourced by the server scripts (tests/e2e/server-session.sh,
# demo-server-box.sh): puts portenv and portenv-runner for a server into a
# directory. By default the binaries CI built for this commit
# (scripts/fetch-cli.sh), so a server runs exactly what CI built and tested;
# PORTENV_LOCAL_BUILD=1 builds them here instead (development).
#
#   server_bins ARCH DIR    (ARCH: arm64 or amd64; writes DIR/portenv, DIR/portenv-runner)
server_bins() {
	local arch=$1 dir=$2 repo src b
	repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
	mkdir -p "$dir"
	if [[ ${PORTENV_LOCAL_BUILD:-} == 1 ]]; then
		for b in portenv portenv-runner; do
			(cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$dir/$b" "./core/cmd/$b")
		done
		echo "  built portenv and portenv-runner here (PORTENV_LOCAL_BUILD=1)" >&2
		return
	fi
	src=$("$repo/scripts/fetch-cli.sh" "${PORTENV_CLI_COMMIT:-HEAD}")
	for b in portenv portenv-runner; do
		cp "$src/$b-linux-$arch" "$dir/$b"
	done
	echo "  portenv and portenv-runner from CI for $(basename "$src")" >&2
}
