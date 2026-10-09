#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Builds the Swift packages the way the Makefile does, with a fake secret in
# the environment, and fails if it shows up anywhere in the build folders.
# SwiftPM's plugin caches record the environment a build ran in; the
# Makefile's $(SWIFT) runs swift with a clean one so tokens never land there.
#
# A control first: the same build without the clean environment must record
# the canary, so a search that can't find anything doesn't pass by accident.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
make=${MAKE:-make} # the GNU Make 4 that runs this (gmake on a Mac)
canary="portenv-canary-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/portenv-swiftenv.XXXXXX")
trap 'rm -rf "$scratch"' EXIT

# Lists up to five files holding the canary; finding none is not an error.
found() { { grep -rlF -- "$canary" "$@" 2>/dev/null || true; } | head -5; }

echo "== control: a plain swift build records the environment"
# Only the canary and the minimum: the control must not record real secrets.
(cd "$repo/apps/mac" && env -i PATH="$PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" PORTENV_FAKE_SECRET="$canary" swift build --scratch-path "$scratch/control" >/dev/null)
if [[ -z $(found "$scratch/control") ]]; then
	echo "FAIL  control: the canary is not in a plain build's folder; this check can't see a leak"
	exit 1
fi
echo "ok    control: a plain build records the canary"

echo "== make swift-test and make app with the canary in the environment"
(cd "$repo" && PORTENV_FAKE_SECRET=$canary "$make" swift-test >/dev/null)
(cd "$repo" && PORTENV_FAKE_SECRET=$canary "$make" app APP_BUILD="$scratch/app-build" >/dev/null)
leaks=$(found "$repo/apps/mac/.build" "$repo/shims/containerization/.build" "$scratch/app-build")
if [[ -n $leaks ]]; then
	echo "FAIL  the canary reached the build folders:"
	sed 's/^/  /' <<<"$leaks"
	exit 1
fi
echo "ok    the canary is nowhere under apps/mac/.build, shims/containerization/.build or the app build folder"
