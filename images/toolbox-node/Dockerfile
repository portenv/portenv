# syntax=mirror.gcr.io/docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# check=skip=SecretsUsedInArgOrEnv
# SPDX-License-Identifier: Apache-2.0
#
# portenv/toolbox-node: the default toolbox image. Build from the repository
# root so the agent can be compiled from source:
#
#   docker build -f images/toolbox-node/Dockerfile -t portenv/toolbox-node:dev .
#
# Base images and the Dockerfile frontend come from mirror.gcr.io, Google's
# public mirror of Docker Hub, pinned by the same digests as on Docker Hub
# (so the bytes are identical); CI's anonymous Docker Hub pulls were rate limited.
#
# Everything here is rebuilt or pulled on each machine and never saved; only
# /home travels. Nothing may depend on Docker-only features (the same image
# runs on Apple Containerization and microVMs).

# --- restic, built from source at the pinned version -----------------------
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS restic
ARG TARGETARCH
ARG RESTIC_VERSION=v0.19.1
# The module and build caches persist across builds (BuildKit cache mounts).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go install -trimpath -ldflags "-s -w" \
      github.com/restic/restic/cmd/restic@$RESTIC_VERSION \
 && install -D "$(find /go/bin -type f -name restic)" /out/restic

# --- portenv-agent and portenv, built natively for the target architecture -
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS agent
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.work ./
COPY core/go.mod core/go.sum core/
COPY proto/gen/go/go.mod proto/gen/go/go.sum proto/gen/go/
# Downloaded into this layer, not a cache mount, so CI's layer cache (type=gha)
# keeps the modules while go.mod and go.sum are unchanged; go.sum still checks
# every one. Only the built agent and CLI leave this stage.
RUN cd core && go mod download
COPY core core
COPY proto/gen/go proto/gen/go
# The agent refuses to run any restic but this exact build (ADR 0005).
COPY --from=restic /out/restic /tmp/restic
RUN --mount=type=cache,target=/root/.cache/go-build \
    restic_sha256="$(sha256sum /tmp/restic | cut -d' ' -f1)" \
 && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/portenv/portenv/core/internal/version.Version=$VERSION -X github.com/portenv/portenv/core/internal/version.Commit=$COMMIT -X github.com/portenv/portenv/core/agent.ResticSHA256=$restic_sha256" \
      -o /out/portenv-agent ./core/cmd/portenv-agent
# The portenv CLI, for an agent's own commands inside the workspace (split,
# msg, ask-owner, whoami, …; ADR 0022 §4) and the owner's team init. Built as
# make cli-linux builds it: static, CGO off, -trimpath. Without cgo it has
# no Keychain code at all.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/portenv/portenv/core/internal/version.Version=$VERSION -X github.com/portenv/portenv/core/internal/version.Commit=$COMMIT" \
      -o /out/portenv ./core/cmd/portenv

# --- the toolbox ------------------------------------------------------------
FROM mirror.gcr.io/library/ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55
ARG TARGETARCH

# Node 22, pinned by version and checksum per architecture.
ARG NODE_VERSION=22.23.3
ARG NODE_SHA256_arm64=a44aeb94849a299b22df10b9e622ec2f605c2183501bc40590705131de7c740f
ARG NODE_SHA256_amd64=df450af89261115ef9f9e3830c3eeb2cc9213b63c720b1af623cb5dcbe2e02de

# Chrome for Testing (Google's build of Chromium for automation), stable
# channel, pinned by version and checksum per architecture, from
# https://googlechromelabs.github.io/chrome-for-testing/. It is the one
# Chromium for both architectures that runs here: Ubuntu 24.04's chromium
# package only installs the snap, which a container cannot run, and Google
# Chrome has no Linux arm64 build. Playwright uses the same builds.
ARG CHROME_VERSION=156.0.8078.47
ARG CHROME_SHA256_arm64=9e37000e3ee2731e45e475a6a2eac587d3c0ee5f8aef7cab5211ecd52cf3fb7b
ARG CHROME_SHA256_amd64=e6c94490f0b6b337225aeb2d2a5034858516b7017356980ba94f5176d8774a50

# Claude Code, pinned by version; apt checks the package against the signed
# repository index, and the build fails if claude --version is anything
# else. test.sh reads this line.
ARG CLAUDE_CODE_VERSION=2.1.304
# Anthropic's Claude Code release signing key fingerprint (public, not a
# secret; the build check above is skipped for it), as published at
# https://code.claude.com/docs/en/setup (Install with Linux package managers).
ARG CLAUDE_KEY_FINGERPRINT=31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE

# Browser tools (Puppeteer, Lighthouse, axe and pa11y runners, the
# Playwright MCP server) find the browser through these. Playwright itself
# takes it as launchOptions.executablePath; it reads no variable for it.
# The browser's own sandbox needs user namespaces, which a box under Docker
# doesn't get (no CAP_SYS_ADMIN, ADR 0005), so there it runs only with
# --no-sandbox. The image never sets that globally (README.md).
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    CHROME_PATH=/opt/chrome/chrome \
    PUPPETEER_EXECUTABLE_PATH=/opt/chrome/chrome \
    PLAYWRIGHT_MCP_EXECUTABLE_PATH=/opt/chrome/chrome

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# tmux is pinned to 3.4: the helper's control mode was measured on it and
# refuses older (ADR 0020, decision 10); raising it reruns the spike's tests.
# Base toolchain. The stock "ubuntu" user (uid 1000) is removed so "work" can
# take uid 1000.
RUN userdel --remove ubuntu \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      acl build-essential ca-certificates curl git gnupg jq less libcap2-bin locales \
      openssh-client postgresql-client procps python3 python3-pip \
      python3-venv ripgrep sudo 'tmux=3.4-*' unzip xz-utils \
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

# The browser's system libraries and fonts: the same packages as
# "playwright install-deps chromium" on Ubuntu 24.04 (Playwright 1.64's
# list: its tools, then chromium's), so a project's own Playwright runs
# without root. Playwright's browser downloads go to the home (portenv.sh).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      xvfb fonts-noto-color-emoji fonts-unifont libfontconfig1 libfreetype6 \
      xfonts-cyrillic xfonts-scalable fonts-liberation fonts-ipafont-gothic \
      fonts-wqy-zenhei fonts-tlwg-loma-otf fonts-freefont-ttf \
      libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 \
      libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 \
      libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 \
      libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
 && rm -rf /var/lib/apt/lists/*

# Chrome for Testing in /opt/chrome, also on PATH as chromium. Its setuid
# sandbox helper is left out: the image adds no setuid-root binaries.
RUN case "$TARGETARCH" in \
      arm64) plat=linux-arm64; sum="$CHROME_SHA256_arm64" ;; \
      amd64) plat=linux64;     sum="$CHROME_SHA256_amd64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && curl -fsSLo /tmp/chrome.zip "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/${plat}/chrome-${plat}.zip" \
 && echo "$sum  /tmp/chrome.zip" | sha256sum -c - \
 && unzip -q /tmp/chrome.zip -d /tmp/chrome \
 && mv "/tmp/chrome/chrome-${plat}" /opt/chrome \
 && rm -rf /tmp/chrome /tmp/chrome.zip /opt/chrome/chrome_sandbox \
 && chown -R root:root /opt/chrome \
 && chmod -R go-w /opt/chrome \
 && ln -s /opt/chrome/chrome /usr/local/bin/chromium \
 && missing="$(ldd /opt/chrome/chrome | grep 'not found' || true)" \
 && if [ -n "$missing" ]; then echo "chrome is missing libraries: $missing" >&2; exit 1; fi \
 && /opt/chrome/chrome --version | grep -qF " ${CHROME_VERSION}"

# Claude Code from Anthropic's signed apt repository, stable channel, at the
# pinned version. The key is rejected unless its primary fingerprint matches
# the published one.
RUN install -d -m 0755 /etc/apt/keyrings \
 && curl -fsSLo /etc/apt/keyrings/claude-code.asc https://downloads.claude.ai/keys/claude-code.asc \
 && fpr="$(gpg --show-keys --with-colons /etc/apt/keyrings/claude-code.asc | awk -F: '$1 == "fpr" { print $10; exit }')" \
 && if [ "$fpr" != "$CLAUDE_KEY_FINGERPRINT" ]; then \
      echo "Claude Code signing key fingerprint is '$fpr', want $CLAUDE_KEY_FINGERPRINT" >&2; exit 1; \
    fi \
 && echo "deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/stable stable main" \
      > /etc/apt/sources.list.d/claude-code.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends "claude-code=${CLAUDE_CODE_VERSION}-*" \
 && rm -rf /var/lib/apt/lists/* \
 && claude --version \
 && test "$(claude --version)" = "${CLAUDE_CODE_VERSION} (Claude Code)"

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
COPY --from=agent --chown=root:root --chmod=0755 /out/portenv /usr/local/bin/portenv
# git runs git-remote-portenv for pushes to origin (R-0391): the portenv CLI
# under a second name.
RUN ln -s portenv /usr/local/bin/git-remote-portenv

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
# key and token or any password. Only what it must do, all in Docker's
# default set and none on ADR 0005's forbidden list:
#   cap_setuid, cap_setgid  start terminals as work and restic as portenv-sync
#                           (the agent allows no other uid, never root)
#   cap_chown               hand restic's ssh its per-run SSH agent socket
#   cap_kill                stop its own children, which run as other users
# Everything else it reads is its own (the channel files, written as
# portenv-agent), its group's (the readiness socket; restic's binary through
# the portenv-sync group), or checked as work by the plain agent binary
# (path checks in /home): no cap_dac_override.
RUN groupadd --system --gid 991 portenv-agent \
 && useradd --system --uid 991 --gid 991 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin portenv-agent \
 && install -o root -g portenv-agent -m 0750 /usr/local/bin/portenv-agent /usr/local/libexec/portenv/agent-serve \
 && setcap cap_setuid,cap_setgid,cap_chown,cap_kill+ep /usr/local/libexec/portenv/agent-serve \
 && install -d -o portenv-agent -g portenv-agent -m 0711 /run/portenv-sftp

LABEL org.opencontainers.image.title="toolbox-node" \
      org.opencontainers.image.description="Portenv toolbox: Ubuntu 24.04, Node 22, Claude Code, Chrome for Testing, portenv-agent" \
      org.opencontainers.image.source="https://github.com/portenv/portenv"

# Docker-specific, ignored by other engines; they ask the agent directly.
HEALTHCHECK --interval=5s --timeout=5s --start-period=15m CMD ["/usr/local/bin/portenv-agent", "ready"]
ENTRYPOINT ["/usr/local/bin/portenv-agent", "init"]
