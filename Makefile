GO       ?= go
BIN_DIR  ?= bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/digital-cube/shared-folder/internal/version.Version=$(VERSION)
GOFMT    ?= $(shell $(GO) env GOROOT 2>/dev/null)/bin/gofmt
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null)

.PHONY: all build test lint fmt check clean e2e

all: check

## build: compile shfd and shf into $(BIN_DIR)/
build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ ./cmd/...

## test: run all unit tests
test:
	$(GO) test ./...

## lint: go vet + gofmt (and golangci-lint when it is installed)
lint:
	$(GO) vet ./...
	$(GO) vet -tags e2e ./e2e/
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
ifneq ($(GOLANGCI),)
	$(GOLANGCI) run ./...
else
	@echo "golangci-lint not found, skipped (go vet + gofmt ran)"
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

clean:
	rm -rf $(BIN_DIR)

## gui: build shfd-gui (Node 22+): gui/dist with bin/shfd-gui, server.js and web/
##      (needs npm; see docs/gui.md)
.PHONY: gui gui-test
gui:
	cd gui && npm ci --no-audit --no-fund && VERSION=$(VERSION) npm run build

## gui-test: unit tests of shfd-gui
gui-test:
	cd gui && npm ci --no-audit --no-fund && npm test
