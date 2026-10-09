# SPDX-License-Identifier: Apache-2.0

# Developer and CI entry points. Every tool is pinned below and installed into
# bin/ on first use, so nothing needs installing besides Go, GNU Make 4 or
# later, and Swift on a Mac.

# GNU Make 4 or later only. macOS ships 3.81, which ignores .SHELLFLAGS and
# once let failing tests and lint pass locally; stop before running anything.
ifeq ($(filter 4.% 5.% 6.% 7.% 8.% 9.%,$(MAKE_VERSION)),)
$(error GNU Make $(MAKE_VERSION) is too old: this Makefile needs GNU Make 4 or later. On a Mac: brew install make, then run gmake instead of make (or put "$$(brew --prefix)/opt/make/libexec/gnubin" first in PATH so make is GNU Make 4))
endif

# Every recipe fails on any failing command (scripts/make-shell: bash -eu -o
# pipefail). Through SHELL, not .SHELLFLAGS, which GNU Make 3.81 (macOS's)
# ignores.
SHELL := $(CURDIR)/scripts/make-shell
.SHELLFLAGS := -c

BIN := $(CURDIR)/bin
GO_MODULES := core proto/gen/go
CMDS := portenvd portenv-runner portenv-agent portenv
AGENT_ARCHES := arm64 amd64

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/portenv/portenv/core/internal/version.Version=$(VERSION) \
	-X github.com/portenv/portenv/core/internal/version.Commit=$(COMMIT)

# Pinned developer tools. Each is installed with its own module graph
# (go install pkg@version), so their dependencies never conflict.
TOOLS := \
	github.com/bufbuild/buf/cmd/buf@v1.73.0 \
	google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 \
	google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2 \
	github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 \
	github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 \
	github.com/zricethezav/gitleaks/v8@v8.30.1 \
	github.com/restic/restic/cmd/restic@v0.19.1
TOOLS_STAMP := $(BIN)/.tools-stamp

.PHONY: all build app test lint fmt proto proto-check agent-linux swift-test swift-env-check \
	secrets spdx-check check tools clean image image-test driver-test e2e

all: build test lint proto-check

## tools: build the pinned developer tools into bin/
tools: $(TOOLS_STAMP)
$(TOOLS_STAMP): Makefile
	for t in $(TOOLS); do GOWORK=off GOBIN=$(BIN) go install $$t; done
	touch $@

## build: build all commands for this machine into bin/
build:
	for c in $(CMDS); do \
		go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/$$c ./core/cmd/$$c; \
	done

## agent-linux: build static portenv-agent binaries and check they are static
agent-linux:
	for a in $(AGENT_ARCHES); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(BIN)/linux-$$a/portenv-agent ./core/cmd/portenv-agent; \
		file $(BIN)/linux-$$a/portenv-agent | tee /dev/stderr | grep -q 'statically linked'; \
	done

## test: run Go tests with the race detector (sync tests use the pinned restic)
test: $(TOOLS_STAMP)
	for m in $(GO_MODULES); do RESTIC=$(BIN)/restic go test -race ./$$m/...; done

## lint: formatting, vet, golangci-lint, buf lint, workflow lint, SPDX headers
lint: $(TOOLS_STAMP) spdx-check
	test -z "$$(gofmt -l core proto/gen/go | tee /dev/stderr)"
	for m in $(GO_MODULES); do go vet ./$$m/...; done
	for m in $(GO_MODULES); do \
		(cd $$m && $(BIN)/golangci-lint run --config $(CURDIR)/.golangci.yml ./...); \
	done
	cd proto && $(BIN)/buf lint && $(BIN)/buf format --diff --exit-code
	$(BIN)/actionlint

## fmt: format Go and proto sources
fmt: $(TOOLS_STAMP)
	gofmt -w core
	cd proto && $(BIN)/buf format -w

## proto: regenerate Go code from proto/
proto: $(TOOLS_STAMP)
	rm -rf proto/gen/go/portenv
	cd proto && $(BIN)/buf generate

## proto-check: fail if generated code is out of date
proto-check: proto
	git diff --exit-code -- proto/gen
	test -z "$$(git status --porcelain -- proto/gen | tee /dev/stderr)"

## app: build Portenv.app into bin/, with portenv, portenvd and restic inside (1.0: ad-hoc signed to run here)
# Built outside the repository: SwiftPM's resource accessor falls back to the
# build folder's path, and a repository under ~/Documents would make macOS ask
# for access to Documents at launch. (A proper Xcode project, 1.8, embeds
# resources the usual way.)
APP_BUILD := $(HOME)/Library/Caches/Portenv/app-build
# Swift builds run with a clean environment: SwiftPM's plugin caches record
# the environment they ran in, so a session token or other secret in the
# shell would end up on disk under .build (scripts/check-swift-env.sh).
SWIFT := env -i PATH="$(PATH)" HOME="$(HOME)" TMPDIR="$(or $(TMPDIR),/tmp)" LANG=en_US.UTF-8 swift
app: build $(TOOLS_STAMP)
	cd apps/mac && $(SWIFT) build -c release --scratch-path "$(APP_BUILD)"
	rm -rf $(BIN)/Portenv.app && mkdir -p $(BIN)/Portenv.app/Contents/MacOS
	cp "$(APP_BUILD)/release/Portenv" $(BIN)/Portenv.app/Contents/MacOS/Portenv
	# The app must be able to prompt: it never links the process-wide no-prompt switch (core/keys).
	if nm -u $(BIN)/Portenv.app/Contents/MacOS/Portenv | grep -q SecKeychainSetUserInteractionAllowed; then echo "Portenv imports SecKeychainSetUserInteractionAllowed: prompts would stop working in the app" >&2; exit 1; fi
	# Helpers, not MacOS/: on a case-insensitive disk portenv would replace Portenv.
	mkdir -p $(BIN)/Portenv.app/Contents/Helpers && cp $(BIN)/portenv $(BIN)/portenvd $(BIN)/restic $(BIN)/Portenv.app/Contents/Helpers/
	for f in portenv portenvd restic; do codesign --force --sign - $(BIN)/Portenv.app/Contents/Helpers/$$f; done
	cp apps/mac/Info.plist $(BIN)/Portenv.app/Contents/Info.plist
	mkdir -p $(BIN)/Portenv.app/Contents/Resources && for b in "$(APP_BUILD)"/release/*.bundle; do [ -e "$$b" ] && cp -R "$$b" $(BIN)/Portenv.app/Contents/Resources/; done; true
	codesign --force --sign - $(BIN)/Portenv.app

## swift-test: build and test the Swift packages (macOS only)
swift-test:
	cd shims/containerization && $(SWIFT) build && $(SWIFT) test
	cd apps/mac && $(SWIFT) build && $(SWIFT) test

## swift-env-check: a secret in the environment never reaches the Swift build folders
swift-env-check:
	MAKE="$(MAKE)" scripts/check-swift-env.sh

## image: build the toolbox image for this machine's architecture only
IMAGE ?= portenv/toolbox-node:dev
image:
	docker build --platform linux/$$(docker version -f '{{.Server.Arch}}') \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-f images/toolbox-node/Dockerfile -t $(IMAGE) .

## image-test: test the toolbox image (builds it first)
image-test: image
	images/toolbox-node/test.sh $(IMAGE)

## driver-test: run the driver conformance suite against Docker with $(IMAGE)
driver-test:
	PORTENV_TEST_IMAGE=$(IMAGE) go test -count=1 -timeout 20m ./core/driver/docker/

## e2e: restic isolation probe, two machines, SFTP storage and portenvd against $(IMAGE)
e2e: build $(TOOLS_STAMP)
	tests/e2e/restic-isolation.sh $(IMAGE)
	tests/e2e/two-machines.sh $(IMAGE)
	tests/e2e/sftp-storage.sh $(IMAGE)
	tests/e2e/daemon.sh $(IMAGE)

## secrets: scan the full git history and the working tree for secrets
secrets: $(TOOLS_STAMP)
	$(BIN)/gitleaks git --redact --no-banner .
	$(BIN)/gitleaks dir --redact --no-banner .

## spdx-check: every source file carries an SPDX header
spdx-check:
	scripts/check-spdx.sh

## check: everything CI runs that works on this machine
check: all agent-linux secrets
	if [ "$$(uname)" = Darwin ]; then $(MAKE) swift-test swift-env-check; fi

clean:
	rm -rf $(BIN) shims/containerization/.build
