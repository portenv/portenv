# SPDX-License-Identifier: Apache-2.0

# Developer and CI entry points. Every tool is pinned below and installed into
# bin/ on first use, so nothing needs installing besides Go, and Swift on
# a Mac.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

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

.PHONY: all build test lint fmt proto proto-check agent-linux swift-test \
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

## swift-test: build and test the Swift packages (macOS only)
swift-test:
	cd shims/containerization && swift build && swift test

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

## e2e: restic isolation probe, two machines, and SFTP storage against $(IMAGE)
e2e: build $(TOOLS_STAMP)
	tests/e2e/restic-isolation.sh $(IMAGE)
	tests/e2e/two-machines.sh $(IMAGE)
	tests/e2e/sftp-storage.sh $(IMAGE)

## secrets: scan the full git history and the working tree for secrets
secrets: $(TOOLS_STAMP)
	$(BIN)/gitleaks git --redact --no-banner .
	$(BIN)/gitleaks dir --redact --no-banner .

## spdx-check: every source file carries an SPDX header
spdx-check:
	scripts/check-spdx.sh

## check: everything CI runs that works on this machine
check: all agent-linux secrets
	if [ "$$(uname)" = Darwin ]; then $(MAKE) swift-test; fi

clean:
	rm -rf $(BIN) shims/containerization/.build
