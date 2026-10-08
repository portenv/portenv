# syntax=docker/dockerfile:1
# check=skip=SecretsUsedInArgOrEnv
# SPDX-License-Identifier: Apache-2.0
#
# portenv/toolbox-node: the default toolbox image. Build from the repository
# root so the agent can be compiled from source:
#
#   docker build -f images/toolbox-node/Dockerfile -t portenv/toolbox-node:dev .
#
# Everything here is rebuilt or pulled on each machine and never saved; only
# /home travels. Nothing may depend on Docker-only features (the same image
# runs on Apple Containerization and microVMs).

# --- restic, built from source at the pinned version -----------------------
FROM --platform=$BUILDPLATFORM golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS restic
ARG TARGETARCH
ARG RESTIC_VERSION=v0.19.1
# The module and build caches persist across builds (BuildKit cache mounts).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go install -trimpath -ldflags "-s -w" \
      github.com/restic/restic/cmd/restic@$RESTIC_VERSION \
 && install -D "$(find /go/bin -type f -name restic)" /out/restic

# --- portenv-agent, built natively for the target architecture -------------
FROM --platform=$BUILDPLATFORM golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS agent
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.work ./
COPY core/go.mod core/go.sum core/
COPY proto/gen/go/go.mod proto/gen/go/go.sum proto/gen/go/
RUN --mount=type=cache,target=/go/pkg/mod cd core && go mod download
COPY core core
COPY proto/gen/go proto/gen/go
# The agent refuses to run any restic but this exact build (ADR 0005).
COPY --from=restic /out/restic /tmp/restic
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    restic_sha256="$(sha256sum /tmp/restic | cut -d' ' -f1)" \
 && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/portenv/portenv/core/internal/version.Version=$VERSION -X github.com/portenv/portenv/core/internal/version.Commit=$COMMIT -X github.com/portenv/portenv/core/agent.ResticSHA256=$restic_sha256" \
      -o /out/portenv-agent ./core/cmd/portenv-agent

# --- the toolbox ------------------------------------------------------------
FROM ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55
ARG TARGETARCH

# Node 22, pinned by version and checksum per architecture.
ARG NODE_VERSION=22.23.3
ARG NODE_SHA256_arm64=a44aeb94849a299b22df10b9e622ec2f605c2183501bc40590705131de7c740f
ARG NODE_SHA256_amd64=df450af89261115ef9f9e3830c3eeb2cc9213b63c720b1af623cb5dcbe2e02de

# Anthropic's Claude Code release signing key fingerprint (public, not a
# secret; the build check above is skipped for it), as published at
# https://code.claude.com/docs/en/setup (Install with Linux package managers).
ARG CLAUDE_KEY_FINGERPRINT=31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Base toolchain. The stock "ubuntu" user (uid 1000) is removed so "work" can
# take uid 1000.
RUN userdel --remove ubuntu \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git gnupg jq less libcap2-bin locales \
      openssh-client postgresql-client procps python3 python3-pip \
      python3-venv ripgrep sudo tmux unzip xz-utils \
 && rm -rf /var/lib/apt/lists/*

RUN case "$TARGETARCH" in \
      arm64) arch=arm64; sum="$NODE_SHA256_arm64" ;; \
      amd64) arch=x64;   sum="$NODE_SHA256_amd64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && tarball="node-v${NODE_VERSION}-linux-${arch}.tar.xz" \
 && curl -fsSLo "/tmp/$tarball" "https://nodejs.org/dist/v${NODE_VERSION}/$tarball" \
 && echo "$sum  /tmp/$tarball" | sha256sum -c - \
 && tar -xJf "/tmp/$tarball" -C /usr/local --strip-components=1 --no-same-owner \
      --exclude='*/CHANGELOG.md' --exclude='*/LICENSE' --exclude='*/README.md' \
 && rm "/tmp/$tarball" \
 && node --version | grep -q "^v${NODE_VERSION}$"

# Claude Code from Anthropic's signed apt repository, stable channel. The key
# is rejected unless its primary fingerprint matches the published one.
RUN install -d -m 0755 /etc/apt/keyrings \
 && curl -fsSLo /etc/apt/keyrings/claude-code.asc https://downloads.claude.ai/keys/claude-code.asc \
 && fpr="$(gpg --show-keys --with-colons /etc/apt/keyrings/claude-code.asc | awk -F: '$1 == "fpr" { print $10; exit }')" \
 && if [ "$fpr" != "$CLAUDE_KEY_FINGERPRINT" ]; then \
      echo "Claude Code signing key fingerprint is '$fpr', want $CLAUDE_KEY_FINGERPRINT" >&2; exit 1; \
    fi \
 && echo "deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/stable stable main" \
      > /etc/apt/sources.list.d/claude-code.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends claude-code \
 && rm -rf /var/lib/apt/lists/* \
 && claude --version

# The main user. Its home is not created here: it lives on the box's home
# storage, and portenv-agent creates it from the skeleton only for a new box.
RUN useradd --uid 1000 --user-group --no-create-home --home-dir /home/work --shell /bin/bash work \
 && test -z "$(ls -A /home)"

COPY images/toolbox-node/rootfs/ /
COPY images/toolbox-node/skel/ /usr/share/portenv/skel/
RUN chmod 0440 /etc/sudoers.d/work \
 && visudo -cf /etc/sudoers.d/work \
 && cp -an /etc/skel/. /usr/share/portenv/skel/

COPY --from=agent /out/portenv-agent /usr/local/bin/portenv-agent

# restic runs only as portenv-sync, a dedicated system user, with just the
# capabilities saving and restoring every home needs. Gaining them at exec
# makes the process non-dumpable, so other processes in the box, root
# included, cannot read its memory, environment or files (ADR 0005).
# Every capability must be in Docker's default set (no CAP_DAC_READ_SEARCH;
# CAP_DAC_OVERRIDE covers reading every home), or exec fails with EPERM.
COPY --from=restic /out/restic /usr/local/libexec/portenv/restic
RUN groupadd --system --gid 990 portenv-sync \
 && useradd --system --uid 990 --gid 990 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin portenv-sync \
 && chown root:portenv-sync /usr/local/libexec/portenv/restic \
 && chmod 0750 /usr/local/libexec/portenv/restic \
 && setcap cap_chown,cap_dac_override,cap_fowner,cap_fsetid+ep /usr/local/libexec/portenv/restic \
 && install -d -o portenv-sync -g portenv-sync -m 0700 /var/cache/portenv-sync

# The agent channel's API process (ADR 0010) runs as portenv-agent from a
# copy of the agent with file capabilities: gaining them at exec makes it
# non-dumpable from its first instruction, before it reads the channel's
# key and token or any password. setuid and setgid start restic as
# portenv-sync and terminals as work; dac_override reads and removes the
# channel files and answers path checks in /home; chown sets up restic's
# SFTP helper; kill stops its own children. All are in Docker's default set.
RUN groupadd --system --gid 991 portenv-agent \
 && useradd --system --uid 991 --gid 991 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin portenv-agent \
 && install -o root -g portenv-agent -m 0750 /usr/local/bin/portenv-agent /usr/local/libexec/portenv/agent-serve \
 && setcap cap_setuid,cap_setgid,cap_dac_override,cap_chown,cap_kill+ep /usr/local/libexec/portenv/agent-serve

LABEL org.opencontainers.image.title="toolbox-node" \
      org.opencontainers.image.description="Portenv toolbox: Ubuntu 24.04, Node 22, Claude Code, portenv-agent" \
      org.opencontainers.image.source="https://github.com/portenv/portenv"

# Docker-specific, ignored by other engines; they ask the agent directly.
HEALTHCHECK --interval=5s --timeout=5s --start-period=15m CMD ["/usr/local/bin/portenv-agent", "ready"]
ENTRYPOINT ["/usr/local/bin/portenv-agent", "init"]
