#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# make-app-icon.sh APPICONSET OUT.icns [LANE SWIFT...]: compiles the app icon
# (R-0128) into an .icns with iconutil. With a dev lane's letter, every size
# gets that letter's badge first (scripts/badge-icon.swift, run with the
# Makefile's clean-environment swift), so a lane's app never looks like the
# release app.
set -euo pipefail
set_dir=$1 out=$2 lane=${3:-}
shift $(( $# < 3 ? $# : 3 ))
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
iconset=$tmp/AppIcon.iconset
mkdir "$iconset"
cp "$set_dir"/icon_*.png "$iconset"/
[[ $(find "$iconset" -name 'icon_*.png' | wc -l) -eq 10 ]] || { echo "make-app-icon: $set_dir needs all ten sizes" >&2; exit 1; }
if [[ -n $lane ]]; then
	[[ $lane =~ ^[A-Z]$ ]] || { echo "make-app-icon: lane $lane isn't one capital letter" >&2; exit 1; }
	(( $# > 0 )) || { echo "make-app-icon: a lane badge needs the swift command" >&2; exit 1; }
	"$@" "$(dirname "$0")/badge-icon.swift" "$iconset" "$lane"
fi
mkdir -p "$(dirname "$out")"
iconutil -c icns -o "$out" "$iconset"
