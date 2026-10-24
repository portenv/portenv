#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The toolbox image's pinned base images (its Dockerfile is the one list of
# them), and the copy of them in ghcr.io/portenv/base that CI pulls from.
# Anonymous pulls from shared CI runners were refused with "429 Too Many
# Requests" by mirror.gcr.io and ECR Public alike; pulls of this
# organization's own packages with the workflow's token aren't.
#
#   base-images.sh list    one line per image: name tag digest source...
#   base-images.sh mirror  copies each image, by digest, into
#                          ghcr.io/portenv/base/<name> (skopeo, logged in);
#                          one already there is left as it is
#   base-images.sh choose  for CI: when every image is in ghcr.io/portenv/base,
#                          writes base= and syntax= (the BASE and
#                          BUILDKIT_SYNTAX build arguments) to $GITHUB_OUTPUT
#                          and PORTENV_BASE to $GITHUB_ENV; otherwise the
#                          Dockerfile's public defaults, with a warning
#
# Digests never change here: an image is copied with its digest preserved
# and checked against the pinned one, and a build pulls it by that digest.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
dockerfile=$repo/images/toolbox-node/Dockerfile
mirror=ghcr.io/portenv/base

list() {
	# The frontend: # syntax=REGISTRY/docker/dockerfile:TAG@DIGEST
	sed -nE 's|^# syntax=[^ ]*/docker/dockerfile:([^@ ]+)@(sha256:[0-9a-f]{64})$|dockerfile \1 \2 mirror.gcr.io/docker/dockerfile docker.io/docker/dockerfile|p' "$dockerfile"
	# The bases: FROM ... ${BASE}/NAME:TAG@DIGEST, Docker Hub's official images.
	sed -nE 's|^FROM .*\$\{BASE\}/([a-z0-9-]+):([^@ ]+)@(sha256:[0-9a-f]{64}).*$|\1 \2 \3 public.ecr.aws/docker/library/\1 mirror.gcr.io/library/\1 docker.io/library/\1|p' "$dockerfile" | sort -u
}

# digest_of REF: the sha256 of REF's manifest (an index, for a multi-arch image).
digest_of() { skopeo inspect --raw "docker://$1" | sha256sum | awk '{print "sha256:" $1}'; }

mirror_all() {
	local name tag digest src ok dst
	while read -r name tag digest sources; do
		dst=$mirror/$name
		if [[ $(digest_of "$dst@$digest" 2>/dev/null || true) == "$digest" ]]; then
			echo "ok    $dst@$digest is there already"
			continue
		fi
		ok=0
		for src in $sources; do
			echo "== $src@$digest -> $dst:$tag-${digest:7:12}"
			# Each source twice, a minute apart (their quotas are per minute).
			for wait in 65 0; do
				if skopeo copy --all --preserve-digests --retry-times 3 "docker://$src@$digest" "docker://$dst:$tag-${digest:7:12}" </dev/null; then
					ok=1
					break
				fi
				if ((wait)); then sleep "$wait"; fi
			done
			if ((ok)); then break; fi
		done
		if ((!ok)); then
			echo "FAIL  couldn't copy $name@$digest from any of: $sources" >&2
			exit 1
		fi
		got=$(digest_of "$dst@$digest")
		if [[ $got != "$digest" ]]; then
			echo "FAIL  $dst has $got, not the pinned $digest" >&2
			exit 1
		fi
		echo "ok    $dst@$digest"
	done < <(list)
}

choose() {
	local name tag digest missing="" syntax=""
	while read -r name tag digest _; do
		if docker buildx imagetools inspect "$mirror/$name@$digest" >/dev/null 2>&1; then
			if [[ $name == dockerfile ]]; then syntax=$mirror/$name@$digest; fi
		else
			missing+=" $name@$digest"
		fi
	done < <(list)
	if [[ -n $missing ]]; then
		echo "::warning::Not in $mirror yet:$missing. Pulling the base images from their public registries (they may refuse with 429); the Base images workflow copies them."
		echo "base=public.ecr.aws/docker/library" >>"$GITHUB_OUTPUT"
		echo "syntax=" >>"$GITHUB_OUTPUT"
		echo "PORTENV_BASE=public.ecr.aws/docker/library" >>"$GITHUB_ENV"
		return
	fi
	echo "base=$mirror" >>"$GITHUB_OUTPUT"
	echo "syntax=$syntax" >>"$GITHUB_OUTPUT"
	echo "PORTENV_BASE=$mirror" >>"$GITHUB_ENV"
	echo "Base images from $mirror"
}

case ${1:-} in
list) list ;;
mirror) mirror_all ;;
choose) choose ;;
*)
	echo "usage: $0 list|mirror|choose" >&2
	exit 2
	;;
esac
