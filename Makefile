GO       ?= go
BIN_DIR  ?= bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/digital-cube/shared-folder/internal/version.Version=$(VERSION)
GOFMT    ?= $(shell $(GO) env GOROOT 2>/dev/null)/bin/gofmt
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null)

.PHONY: all build test lint fmt check clean

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

clean:
	rm -rf $(BIN_DIR)
