SHELL := /bin/bash

VERSIONS              := .versions.yaml
pin                    = $(shell awk '$$1=="$(1):"{print $$2}' $(VERSIONS))
GOLANGCI_LINT_VERSION := $(call pin,golangci-lint)
GOVULNCHECK_VERSION   := $(call pin,govulncheck)
ACTIONLINT_VERSION    := $(call pin,actionlint)
GITLEAKS_VERSION      := $(call pin,gitleaks)
GORELEASER_VERSION    := $(call pin,goreleaser)
# goreleaser refuses to run with more than one forge token set.
GORELEASER            := env -u GITLAB_TOKEN -u GITEA_TOKEN go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
VERSION               ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CLIENT_ID             ?=
LDFLAGS               := -s -w -X github.com/thingzio/devbox/internal/version.Version=$(VERSION)
ifneq ($(CLIENT_ID),)
LDFLAGS               += -X github.com/thingzio/devbox/internal/auth.DefaultClientID=$(CLIENT_ID)
endif
BIN                   := bin/devbox
PLATFORMS             := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build test fmt lint vuln cover run fixtures cross qualify actionlint leaks notices notices-check release-check snapshot release-tag

# The gate CI runs; if it passes here, CI passes.
qualify: lint actionlint vuln cover cross notices-check

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

actionlint:
	CGO_ENABLED=0 go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# Scans the full git history, not just the working tree.
leaks:
	CGO_ENABLED=0 go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --no-banner --redact .

notices:
	python3 hack/gen-third-party-notices

notices-check: notices
	@git diff --exit-code -- THIRD_PARTY_NOTICES.md || { echo "THIRD_PARTY_NOTICES.md is stale: run make notices and commit it"; exit 1; }

release-check:
	CGO_ENABLED=0 $(GORELEASER) check

# A local dry run of the release build; signing, SBOMs, and publishing happen only in CI.
snapshot:
	CGO_ENABLED=0 $(GORELEASER) release --snapshot --clean --skip=sign,sbom,publish

# Cuts a release: make release-tag VERSION=v0.1.0 (see RELEASING.md).
release-tag:
	./hack/release-tag.sh $(VERSION)
