#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# install-shellcheck.sh BIN: puts shellcheck, pinned to the version on CI's
# runner image (ubuntu-24.04: 0.9.0), into BIN/shellcheck, so actionlint
# checks workflow scripts the same way on a Mac and in CI. Each download is
# checked against the SHA-256 recorded here. 0.9.0 has no Apple silicon
# build: on a Mac the Intel build runs under Rosetta.
#
# install-shellcheck.sh --check: only checks that the pinned shellcheck can
# run here (Rosetta on a Mac), so lint stops with a plain line instead of an
# exec error ("Bad CPU type") when Rosetta is missing.
set -euo pipefail
version=0.9.0
needs_rosetta() {
	[[ $(uname -s) == Darwin ]] && ! arch -x86_64 /usr/bin/true 2>/dev/null
}
if [[ ${1:-} == --check ]]; then
	if needs_rosetta; then
		echo "shellcheck $version needs Rosetta: softwareupdate --install-rosetta" >&2
		exit 1
	fi
	exit 0
fi
bin=$1
case "$(uname -s)-$(uname -m)" in
	Darwin-*) asset=darwin.x86_64 sum=7d3730694707605d6e60cec4efcb79a0632d61babc035aa16cda1b897536acf5 ;;
	Linux-x86_64) asset=linux.x86_64 sum=700324c6dd0ebea0117591c6cc9d7350d9c7c5c287acbad7630fa17b1d4d9e2f ;;
	Linux-aarch64 | Linux-arm64) asset=linux.aarch64 sum=179c579ef3481317d130adebede74a34dbbc2df961a70916dd4039ebf0735fae ;;
	*) echo "install-shellcheck: no pinned shellcheck for $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac
if needs_rosetta; then
	echo "shellcheck $version needs Rosetta: softwareupdate --install-rosetta" >&2
	exit 1
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
file=shellcheck-v$version.$asset.tar.xz
curl -fsSL --retry 3 -o "$tmp/$file" "https://github.com/koalaman/shellcheck/releases/download/v$version/$file"
got=$( (command -v sha256sum >/dev/null && sha256sum "$tmp/$file" || shasum -a 256 "$tmp/$file") | cut -d' ' -f1)
if [[ $got != "$sum" ]]; then
	echo "install-shellcheck: $file has SHA-256 $got, expected $sum" >&2
	exit 1
fi
tar -xJf "$tmp/$file" -C "$tmp"
install -m 0755 "$tmp/shellcheck-v$version/shellcheck" "$bin/shellcheck"
"$bin/shellcheck" --version | grep -qx "version: $version"
