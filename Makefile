SHELL := /bin/bash

VERSIONS              := .versions.yaml
GOLANGCI_LINT_VERSION := $(shell awk '$$1=="golangci-lint:"{print $$2}' $(VERSIONS))
GOVULNCHECK_VERSION   := $(shell awk '$$1=="govulncheck:"{print $$2}' $(VERSIONS))
VERSION               ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CLIENT_ID             ?=
LDFLAGS               := -s -w \
  -X github.com/thingzio/devbox/internal/version.Version=$(VERSION) \
  -X github.com/thingzio/devbox/internal/auth.DefaultClientID=$(CLIENT_ID)
BIN                   := bin/devbox
PLATFORMS             := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build test fmt lint vuln cover run fixtures cross

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/devbox

test:
	CGO_ENABLED=1 go test -race -count=1 ./...

cover:
	CGO_ENABLED=1 go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	./hack/coverage-check.sh coverage.out .coverage-floor

fmt:
	CGO_ENABLED=0 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt ./...

lint:
	CGO_ENABLED=0 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vuln:
	CGO_ENABLED=0 go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

run: build
	./$(BIN) serve --debug

fixtures:
	./hack/record-fixtures.sh $(REPO) $(PR)

cross:
	@set -e; for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
	    -o bin/devbox-$$os-$$arch$$ext ./cmd/devbox; \
	done
