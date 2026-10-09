#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Downloads the CLI that CI built for a commit (the portenv-cli-<commit>
# artifact: portenv and portenv-runner for linux/arm64 and amd64, portenv for
# macOS arm64) and checks it against its checksums.txt. Internal builds:
# signed public releases come with ADR 0009.
#
#   scripts/fetch-cli.sh [COMMIT] [DEST]   (default: HEAD, bin/ci/<commit>)
#
# Needs gh, signed in (artifacts can't be read anonymously). Prints DEST.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
commit=$(git -C "$repo" rev-parse "${1:-HEAD}")
dest=${2:-$repo/bin/ci/$commit}
if [[ -f $dest/checksums.txt ]] && (cd "$dest" && shasum -a 256 -c checksums.txt >/dev/null 2>&1); then
	echo "$dest"
	exit 0
fi
run=$(gh run list --repo portenv/portenv --workflow CI --commit "$commit" --status success --json databaseId --jq '.[0].databaseId // empty')
if [[ -z $run ]]; then
	echo "fetch-cli: no successful CI run for $commit (push it and wait for CI, or set PORTENV_LOCAL_BUILD=1)" >&2
	exit 1
fi
rm -rf "$dest" && mkdir -p "$dest"
gh run download "$run" --repo portenv/portenv --name "portenv-cli-$commit" --dir "$dest" >&2
(cd "$dest" && shasum -a 256 -c checksums.txt >&2)
chmod +x "$dest"/portenv*
echo "$dest"
