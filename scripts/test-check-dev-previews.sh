#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# test-check-dev-previews.sh: check-dev-previews.sh catches the preview
# outside its #if, and in a binary (run by make test).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
t() {
	local name=$1 want=$2
	shift 2
	if "$@" >/dev/null 2>&1; then got=0; else got=1; fi
	if [[ $got == "$want" ]]; then echo "ok    $name"; else echo "FAIL  $name"; fail=1; fi
}
src() { mkdir -p "$tmp/$1/apps/mac/Sources/App"; cat >"$tmp/$1/apps/mac/Sources/App/A.swift"; }

src inside <<'SWIFT'
#if PORTENV_DEV_PREVIEWS
#if DEBUG
let x = 1
#endif
enum BlocksPreview { static let argument = "--blocks-preview" }
#endif
SWIFT
t "inside its #if, nested #if too: passes" 0 "$here/check-dev-previews.sh" "$tmp/inside"

src outside <<'SWIFT'
#if PORTENV_DEV_PREVIEWS
enum BlocksPreview {}
#endif
let arg = "--blocks-preview"
SWIFT
t "the argument outside the #if: fails" 1 "$here/check-dev-previews.sh" "$tmp/outside"

src otherif <<'SWIFT'
#if DEBUG
BlocksPreview.show()
#endif
SWIFT
t "inside another #if only: fails" 1 "$here/check-dev-previews.sh" "$tmp/otherif"

printf 'abc\0--blocks-preview\0def' >"$tmp/lane"
printf 'abc\0--render-screens\0def' >"$tmp/release"
t "a binary with the preview: fails" 1 "$here/check-dev-previews.sh" --binary "$tmp/lane"
t "a binary without it: passes" 0 "$here/check-dev-previews.sh" --binary "$tmp/release"
t "the repository's sources: pass" 0 "$here/check-dev-previews.sh"

if [[ $fail == 0 ]]; then echo "all checks passed"; else exit 1; fi
