# syntax=docker/dockerfile:1
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

# --- portenv-agent, built natively for the target architecture -------------
FROM --platform=$BUILDPLATFORM golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS agent
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.work ./
COPY core/go.mod core/go.sum core/
COPY proto/gen/go/go.mod proto/gen/go/go.sum proto/gen/go/
RUN cd core && go mod download
COPY core core
COPY proto/gen/go proto/gen/go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/portenv/portenv/core/internal/version.Version=$VERSION -X github.com/portenv/portenv/core/internal/version.Commit=$COMMIT" \
      -o /out/portenv-agent ./core/cmd/portenv-agent

# --- the toolbox ------------------------------------------------------------
FROM ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55
ARG TARGETARCH

# Node 22, pinned by version and checksum per architecture.
ARG NODE_VERSION=22.23.3
ARG NODE_SHA256_arm64=a44aeb94849a299b22df10b9e622ec2f605c2183501bc40590705131de7c740f
ARG NODE_SHA256_amd64=df450af89261115ef9f9e3830c3eeb2cc9213b63c720b1af623cb5dcbe2e02de

# Anthropic's Claude Code release signing key, as published at
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
      build-essential ca-certificates curl git gnupg jq less locales \
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

LABEL org.opencontainers.image.title="toolbox-node" \
      org.opencontainers.image.description="Portenv toolbox: Ubuntu 24.04, Node 22, Claude Code, portenv-agent" \
      org.opencontainers.image.source="https://github.com/portenv/portenv"

# Docker-specific, ignored by other engines; they ask the agent directly.
HEALTHCHECK --interval=5s --timeout=5s --start-period=15m CMD ["/usr/local/bin/portenv-agent", "ready"]
ENTRYPOINT ["/usr/local/bin/portenv-agent", "init"]
