GO       ?= go
BIN_DIR  ?= bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/digital-cube/shared-folder/internal/version.Version=$(VERSION)
GOFMT    ?= $(shell $(GO) env GOROOT 2>/dev/null)/bin/gofmt
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null)
SHELLCHECK ?= $(shell command -v shellcheck 2>/dev/null)
SHELL_SCRIPTS := deploy/deploy.sh deploy/remote.sh deploy/test/deploy_test.sh deploy/test/fake-systemctl

.PHONY: all build test lint fmt check clean e2e

all: check

## build: compile shfd and shf into $(BIN_DIR)/
build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ ./cmd/...

## test: run all unit tests
test:
	$(GO) test ./...

## lint: go vet + gofmt (and golangci-lint and shellcheck when installed)
lint:
	$(GO) vet ./...
	$(GO) vet -tags e2e ./e2e/
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
ifneq ($(GOLANGCI),)
	$(GOLANGCI) run ./...
else
	@echo "golangci-lint not found, skipped (go vet + gofmt ran)"
endif
ifneq ($(SHELLCHECK),)
	$(SHELLCHECK) $(SHELL_SCRIPTS)
else
	@echo "shellcheck not found, deploy scripts not checked"
endif

## fmt: rewrite sources with gofmt
fmt:
	$(GOFMT) -w .

## check: lint, test and build — what CI and reviewers run
check: lint test build

## e2e: end-to-end tests: real shfd, shf daemons, rsync over a throwaway sshd
##      (not part of check; see README.md for what the machine needs)
E2E_FLAGS ?= -count=1 -timeout 30m
e2e:
	GO=$(GO) $(GO) test -tags e2e $(E2E_FLAGS) ./e2e/

## release: packages for download, one per platform, into $(DIST_DIR)/:
##          shared-folder-$(VERSION)-<os>-<arch>.tar.gz (shf, shfd, INSTALL.md,
##          README.md) and SHA256SUMS. Pure Go (CGO_ENABLED=0), so one machine
##          builds them all (docs/install.md)
DIST_DIR  ?= dist
PLATFORMS ?= linux/amd64 darwin/amd64 darwin/arm64
SHA256    := $(shell command -v sha256sum 2>/dev/null || echo shasum -a 256)
.PHONY: release
release:
	rm -rf $(DIST_DIR) && mkdir -p $(DIST_DIR)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=shared-folder-$(VERSION)-$$os-$$arch; d=$(DIST_DIR)/$$name; \
		echo "build $$name"; mkdir -p $$d; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $$d/ ./cmd/shf ./cmd/shfd; \
		cp docs/install.md $$d/INSTALL.md; cp README.md $$d/; \
		tar -C $(DIST_DIR) -czf $$d.tar.gz $$name; rm -rf $$d; \
	done
	cd $(DIST_DIR) && $(SHA256) *.tar.gz > SHA256SUMS && cat SHA256SUMS

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)

## gui: build shfd-gui (Node 22+): gui/dist with bin/shfd-gui, server.js and web/
##      (needs npm; see docs/gui.md)
.PHONY: gui gui-test
gui:
	cd gui && npm ci --no-audit --no-fund && VERSION=$(VERSION) npm run build

## gui-test: unit tests of shfd-gui
gui-test:
	cd gui && npm ci --no-audit --no-fund && npm test

## deploy: build TARGET's ref and put it on the server (deploy/deploy.sh,
##         docs/deploy.md): make deploy TARGET=<name> [REF=<git-ref>]
##         [DEPLOY_FLAGS="--dry-run --skip-tests --only shfd,gui,downloads"]
.PHONY: deploy deploy-test
deploy:
	@test -n "$(TARGET)" || { echo "usage: make deploy TARGET=<name> (deploy/targets/<name>.env)"; exit 2; }
	GO=$(GO) deploy/deploy.sh $(TARGET) $(if $(REF),--ref $(REF)) $(DEPLOY_FLAGS)

## deploy-test: deploy.sh against this machine as the server (SSH_TARGET=local,
##              real shfd and shfd-gui, fake systemctl): dry run, failed deploy
##              with rollback, deploy, cleanup. Builds REF twice (default HEAD)
deploy-test:
	GO=$(GO) deploy/test/deploy_test.sh $(REF)
