# devbox Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `devbox`, a local single-user Go server that continuously classifies open pull requests in monitored GitHub repositories by the action the maintainer owes, and serves a live web queue.

**Architecture:** One binary (`devbox serve`). Per-repo pollers fetch cheap GraphQL fingerprints; changed PRs are enriched (GraphQL detail plus REST evidence) into a versioned `classify.Record`, classified by a pure function, and committed to SQLite under optimistic revision guards with an append-only transitions log. An in-process hub pushes change events over SSE to a server-rendered `html/template` + htmx UI.

**Tech Stack:** Go 1.27.2; `modernc.org/sqlite` v1.60.1 (pure Go); `github.com/zalando/go-keyring` v0.2.8; `github.com/prometheus/client_golang` v1.25.0; htmx 2.0.11 (vendored, SHA-256 `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`); golangci-lint v2.14.0; govulncheck v1.9.0. All are the latest releases as of 2026-10-12 (project rule: use the latest versions unless there is a concrete reason not to; golangci-lint must be ≥ v2.14.0 to read Go 1.27 export data).

**Spec:** `docs/superpowers/specs/2026-10-12-devbox-phase1-design.md` (approved). Read it before starting any task; section numbers below (§N) refer to it.

## Global Constraints

- Module path `github.com/thingzio/devbox`; Go version comes from `.versions.yaml` (`go: 1.27.2`).
- `CGO_ENABLED=0` for `build`, `run`, `cross`, `lint`, `vuln`; `test` and `cover` run `-race` with `CGO_ENABLED=1`.
- No writes to GitHub: REST is `GET` only; GraphQL sends only operations starting with `query` (§4.1).
- Times are stored as INTEGER Unix milliseconds UTC (§6).
- Never log tokens, `Authorization` headers, `device_code`, PR or comment bodies, or diff contents (§10).
- Server listens on `127.0.0.1:8484` by default; a non-loopback address requires `--allow-remote` (§9.1).
- All PR-sourced text is untrusted and rendered only through `html/template`; links render only for `https://github.com/...` URLs (§9.1).
- Dependency versions are exactly those in **Tech Stack**; GitHub Actions are pinned by commit SHA:
  `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`,
  `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`,
  `golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0`.
- Commits go to `main`, are signed (`git commit -S`), have no `Co-Authored-By` lines, and are never `-s` signed-off.
- Before every commit: `make fmt`, then `make test` and `make lint` pass. Never skip or disable a test.

## Review Focus

Inputs and failure modes the spec implies but no spec-listed test exercises, most likely to bite first. Each has a pinned test in the owning task.

1. **Pasted repository references in any common form** (`https://github.com/NVIDIA/aicr`, `NVIDIA/aicr.git`, a PR URL, a trailing slash, `www.github.com/...`) normalize to `owner/name`; a non-GitHub host or malformed name is a clear validation error, never a silently wrong repo. → Task 15, `TestParseRepoReference`.
2. **Empty states**: first launch (no accounts), accounts but no repos, repos with nothing waiting each render an instructive message, not a blank page. → Task 14, `TestQueueEmptyStates`.
3. **Laptop sleep / network loss**: consecutive poll failures back off, the header shows staleness and the last error, and the first successful poll clears both. → Task 11, `TestPollFailureThenRecovery`; Task 14, `TestStatusBarShowsStaleRepo`.
4. **Same repository monitored under two accounts**: separate rows tagged by account, no key collisions, per-repo page shows both. → Task 8, `TestQueueSameRepoTwoAccounts`; Task 14, `TestRepoPageShowsBothAccounts`.
5. **Port already in use / second instance**: `devbox serve` exits non-zero with a clear `listen` error before any polling. → Task 16, `TestRunFailsWhenPortInUse`.

## Implementation Notes (decisions the spec leaves open)

- The spec's `sync` package is named `internal/syncer` to avoid shadowing the standard library `sync`.
- Small supporting packages: `internal/rules` (rules parsing, validation, collection hash; stdlib only, used by `classify`), `internal/metrics` (Prometheus definitions), `internal/logging`, `internal/version`, `internal/app` (wiring, used by `cmd/devbox` and the e2e test).
- Extra routes beyond §9: `GET /fragments/queue`, `GET /fragments/r/{owner}/{name}` (htmx refresh targets), `GET /static/*`, `POST /import`.
- `prs.title`, `prs.author` start empty for PRs first seen by a fingerprint poll and are filled by the first accepted enrichment; `prs.url` is derived immediately.
- Unclassified result written by a collection-rules edit uses reason `collection rules changed; refreshing evidence` and an empty action.
- When a cached signature policy is `unavailable`, the record gets a non-retryable evidence error so the note stays visible (§4.3 "permission failures remain visible").
- The spec's `TokenSource` (§8) is `auth.Secrets.Get`, called by the per-account source factory in `internal/app`; a missing or unreadable token surfaces as `syncer.ErrNoToken` and marks the account `needs_auth`.
- `repos.owner` and `repos.name` use `COLLATE NOCASE` (GitHub names are case-insensitive), so the same repository cannot be added twice under one account with different casing.
- Displayed times use `web.Options.Location` (default `time.Local`) so tests can pin UTC.

## Pre-Verification

On 2026-10-12 every file in this plan was extracted into a scratch module and checked with the pinned versions: `CGO_ENABLED=0 go build ./...` and `go vet ./...` clean; `CGO_ENABLED=1 go test -race ./...` passes in all packages (syncer, web, app, auth repeated `-count=3`); `golangci-lint` v2.14.0 with this plan's `.golangci.yaml` reports 0 issues; `govulncheck` v1.9.0 finds no vulnerabilities; all five `CGO_ENABLED=0` cross-builds succeed; total coverage 85.8%. `TestGolden` was excluded because its fixture is recorded live in Task 17. If a step fails during execution, suspect environment drift first (toolchain, network, keychain), not the plan code.

## File Structure

```
.versions.yaml                       tool versions (single source of truth)
.golangci.yaml                       lint configuration
.coverage-floor                      coverage ratchet value
.gitignore
Makefile
README.md
.github/workflows/ci.yaml            test/lint/vuln/cross jobs
hack/coverage-check.sh               enforces .coverage-floor
hack/record-fixtures.sh              records a golden collect fixture via gh
cmd/devbox/main.go                   CLI: serve, version
cmd/devbox/main_test.go
internal/version/version.go          ldflags-injected version
internal/rules/rules.go              Rules, Parse, Validate, CollectionHash
internal/classify/record.go          Record and evidence types, ReviewBaseline
internal/classify/bucket.go          Bucket ids, titles, order
internal/classify/blockers.go        merge blockers, signatures, summaries
internal/classify/classify.go        Classify and candidate rules, notes
internal/classify/rank.go            ranking, size badge, handoff lines
internal/github/client.go            Client, request loop, retries, rate headers
internal/github/errors.go            typed errors, Retryable
internal/github/api.go               GraphQL, Get, GetPages, GetDiff
internal/store/db.go                 Open, migrations, time helpers
internal/store/migrations/0001_init.sql
internal/store/accounts.go
internal/store/repos.go              repos, rules edit invalidation
internal/store/results.go            results, transitions, writeResult
internal/store/prs.go                PR state, ApplyPoll, commits, guards, due work
internal/store/caches.go             evidence caches and purge
internal/store/views.go              queue, details, statuses, bucket counts
internal/collect/fingerprint.go      Fingerprint
internal/collect/detail.go           Detail GraphQL shape and query
internal/collect/reader.go           Reader interface and GitHubReader
internal/collect/normalize.go        Detail -> Record
internal/collect/collect.go          Collector, follow-up evidence, Outcome, Source
internal/hub/hub.go                  in-process pub/sub
internal/metrics/metrics.go          Prometheus metrics and PR gauge
internal/syncer/engine.go            Engine, deps, rate pause, run loop
internal/syncer/plan.go              pure poll planning
internal/syncer/poll.go              PollRepo
internal/syncer/job.go               dispatch, RunJob, outcomes, backoff
internal/syncer/reclassify.go        Reclassify
internal/auth/secrets.go             Secrets, Keyring, MemSecrets
internal/auth/device.go              device flow client
internal/auth/manager.go             Manager: device flows, PATs, removal
internal/web/server.go               Server, routes, middleware, render
internal/web/views.go                view models, queue building
internal/web/handlers.go             queue, repo, explain, events
internal/web/settings.go             settings, accounts, repos, import
internal/web/templates/*.html
internal/web/static/{htmx.min.js,app.js,style.css}
internal/logging/logging.go
internal/app/app.go                  Config, Run (wiring and shutdown)
```

---

### Task 1: Project scaffold, tooling, and CI

**Files:**
- Create: `.gitignore`, `go.mod`, `.versions.yaml`, `Makefile`, `.golangci.yaml`, `.coverage-floor`, `hack/coverage-check.sh`, `.github/workflows/ci.yaml`, `internal/version/version.go`, `cmd/devbox/main.go`
- Test: `cmd/devbox/main_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `version.Version string`; `cmd/devbox` `run(args []string, stdout, stderr io.Writer) int` (extended with `serve` in Task 16); Makefile targets `build test fmt lint vuln cover run fixtures cross`.

- [ ] **Step 1: Initialize the module and tool version file**

```bash
cd /Users/mchmarny/dev/thingz/devbox
go mod init github.com/thingzio/devbox
go mod edit -go=1.27
```

Create `.versions.yaml`:

```yaml
# Single source of truth for toolchain versions (read by Makefile and CI).
go: 1.27.2
golangci-lint: v2.14.0
govulncheck: v1.9.0
```

Create `.gitignore`:

```
/bin/
/coverage.out
```

Create `.coverage-floor` (raised to the measured value in Task 17):

```
0.0
```

- [ ] **Step 2: Write the failing CLI test**

Create `cmd/devbox/main_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thingzio/devbox/internal/version"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version output = %q, want %q", got, version.Version)
	}
}

func TestRunUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":      nil,
		"unknown command": {"frobnicate"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !strings.Contains(errOut.String(), "usage: devbox") {
				t.Fatalf("stderr %q does not contain usage", errOut.String())
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "serve") {
		t.Fatalf("help %q does not list serve", out.String())
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./cmd/devbox/`
Expected: FAIL to compile (`undefined: run`, missing `internal/version`).

- [ ] **Step 4: Implement version and CLI skeleton**

Create `internal/version/version.go`:

```go
// Package version holds build metadata injected with -ldflags.
package version

// Version is the build version, overridden at link time.
var Version = "dev"
```

Create `cmd/devbox/main.go`:

```go
// Command devbox runs the maintainer queue server.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/thingzio/devbox/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "devbox: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: devbox <command> [flags]

commands:
  serve     run the maintainer queue server
  version   print the version
`)
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `CGO_ENABLED=1 go test -race ./cmd/devbox/`
Expected: PASS.

- [ ] **Step 6: Add Makefile, lint config, coverage script**

Create `Makefile`:

```make
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
```

Create `.golangci.yaml`:

```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard
  enable:
    - bodyclose
    - errorlint
    - gosec
    - misspell
    - noctx
    - rowserrcheck
    - sqlclosecheck
    - unconvert
  settings:
    gosec:
      excludes:
        - G115 # integer conversions are bounded by domain (PR numbers, counts)
  exclusions:
    presets:
      - std-error-handling # unchecked Close/Flush/print errors
    rules:
      - path: _test\.go
        linters: [bodyclose, gosec, noctx]
formatters:
  enable:
    - gofmt
    - goimports
```

Create `hack/coverage-check.sh`:

```bash
#!/usr/bin/env bash
# Fails when total statement coverage is below the floor in the given file.
set -euo pipefail
profile=${1:?usage: coverage-check.sh <coverage.out> <floor-file>}
floor_file=${2:?usage: coverage-check.sh <coverage.out> <floor-file>}
total=$(go tool cover -func="$profile" | awk '/^total:/ {gsub("%", "", $3); print $3}')
floor=$(tr -d '[:space:]' < "$floor_file")
echo "coverage: ${total}% (floor ${floor}%)"
if ! awk -v t="$total" -v f="$floor" 'BEGIN { exit !(t + 0 >= f + 0) }'; then
  echo "coverage ${total}% is below floor ${floor}%" >&2
  exit 1
fi
```

```bash
chmod +x hack/coverage-check.sh
```

- [ ] **Step 7: Add CI workflow**

Create `.github/workflows/ci.yaml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  versions:
    runs-on: ubuntu-latest
    outputs:
      go: ${{ steps.read.outputs.go }}
      golangci: ${{ steps.read.outputs.golangci }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - id: read
        run: |
          echo "go=$(awk '$1=="go:"{print $2}' .versions.yaml)" >> "$GITHUB_OUTPUT"
          echo "golangci=$(awk '$1=="golangci-lint:"{print $2}' .versions.yaml)" >> "$GITHUB_OUTPUT"

  test:
    needs: versions
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: ${{ needs.versions.outputs.go }}
      - name: Race tests with coverage floor
        run: make cover

  lint:
    needs: versions
    runs-on: ubuntu-latest
    env:
      CGO_ENABLED: "0"
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: ${{ needs.versions.outputs.go }}
      - uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0
        with:
          version: ${{ needs.versions.outputs.golangci }}

  vuln:
    needs: versions
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: ${{ needs.versions.outputs.go }}
      - run: make vuln

  cross:
    needs: versions
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: ${{ needs.versions.outputs.go }}
      - name: Cross-compile without CGO
        run: make cross
```

- [ ] **Step 8: Verify tooling**

Run: `make fmt && make test && make lint && make build && ./bin/devbox version && make cross`
Expected: tests PASS, lint reports `0 issues`, `./bin/devbox version` prints the git describe value, five binaries appear in `bin/`.

- [ ] **Step 9: Commit**

```bash
git add .gitignore go.mod .versions.yaml Makefile .golangci.yaml .coverage-floor hack/coverage-check.sh .github/workflows/ci.yaml internal/version cmd/devbox
git commit -S -m "build: scaffold devbox module, tooling, and CI"
```

---

### Task 2: Repository rules

**Files:**
- Create: `internal/rules/rules.go`
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `type TestApproval struct { MarkerChecks []string; Command string; CIPaths []string; CIPathsCommand string }`
  - `type Rules struct { TestApproval TestApproval; StaleDays int }`
  - `const DefaultStaleDays = 7`; `func Default() Rules`
  - `func Parse(data []byte) (Rules, error)` — strict, validates, empty input → `Default()`
  - `func (Rules) Validate() error`; `func (Rules) JSON() string` (indented); `func (Rules) CollectionHash() string` (hex SHA-256 of canonical `test_approval`)

- [ ] **Step 1: Write the failing tests**

Create `internal/rules/rules_test.go`:

```go
package rules

import (
	"strings"
	"testing"
)

const aicrOverlay = `{
  "test_approval": {
    "marker_checks": ["tests / Test", "tests-skip"],
    "command": "/ok-to-test",
    "ci_paths": [".github/"],
    "ci_paths_command": "/ok-to-test confirm-ci"
  },
  "stale_days": 7
}`

func TestParseOverlay(t *testing.T) {
	r, err := Parse([]byte(aicrOverlay))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := r.TestApproval.MarkerChecks; len(got) != 2 || got[0] != "tests / Test" {
		t.Fatalf("marker checks = %v", got)
	}
	if r.TestApproval.CIPathsCommand != "/ok-to-test confirm-ci" || r.StaleDays != 7 {
		t.Fatalf("unexpected rules %+v", r)
	}
}

func TestParseDefaults(t *testing.T) {
	for _, in := range []string{"", "  ", "{}", `{"test_approval": null}`} {
		r, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if r.StaleDays != DefaultStaleDays || len(r.TestApproval.MarkerChecks) != 0 {
			t.Fatalf("Parse(%q) = %+v, want defaults", in, r)
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown top-level key":     `{"stale": 3}`,
		"unknown nested key":        `{"test_approval": {"markers": ["x"]}}`,
		"stale days zero":           `{"stale_days": 0}`,
		"marker without command":    `{"test_approval": {"marker_checks": ["x"]}}`,
		"ci paths without command":  `{"test_approval": {"ci_paths": [".github/"]}}`,
		"empty marker entry":        `{"test_approval": {"marker_checks": [" "], "command": "/ok"}}`,
		"empty ci path entry":       `{"test_approval": {"ci_paths": [""], "ci_paths_command": "/ok"}}`,
		"trailing data":             `{} {}`,
		"not an object":             `[1]`,
		"wrong type for stale days": `{"stale_days": "7"}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Fatalf("Parse(%s) succeeded, want error", in)
			}
		})
	}
}

func TestCollectionHash(t *testing.T) {
	a, _ := Parse([]byte(`{"test_approval": {"marker_checks": ["a", "b"], "command": "/ok"}}`))
	b, _ := Parse([]byte(`{"test_approval": {"marker_checks": ["b", "a"], "command": "/ok"}, "stale_days": 30}`))
	if a.CollectionHash() != b.CollectionHash() {
		t.Fatal("marker order or stale_days changed the collection hash")
	}
	c, _ := Parse([]byte(`{"test_approval": {"marker_checks": ["a", "b"], "command": "/ok-to-test"}}`))
	if a.CollectionHash() == c.CollectionHash() {
		t.Fatal("command change did not change the collection hash")
	}
	if Default().CollectionHash() != (Rules{StaleDays: 1, TestApproval: TestApproval{MarkerChecks: []string{}}}).CollectionHash() {
		t.Fatal("nil and empty slices hash differently")
	}
	if len(a.CollectionHash()) != 64 {
		t.Fatalf("hash %q is not hex sha256", a.CollectionHash())
	}
}

func TestJSONRoundTrip(t *testing.T) {
	r, _ := Parse([]byte(aicrOverlay))
	back, err := Parse([]byte(r.JSON()))
	if err != nil {
		t.Fatalf("Parse(JSON()): %v", err)
	}
	if back.CollectionHash() != r.CollectionHash() || back.StaleDays != r.StaleDays {
		t.Fatalf("round trip changed rules: %+v vs %+v", back, r)
	}
	if !strings.Contains(Default().JSON(), `"stale_days": 7`) {
		t.Fatalf("default JSON %q lacks stale_days", Default().JSON())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/rules/`
Expected: FAIL to compile (`undefined: Parse`).

- [ ] **Step 3: Implement rules**

Create `internal/rules/rules.go`:

```go
// Package rules defines per-repository classification rules in the
// maintainer-sweep overlay shape, their validation, and the hash of the
// settings that govern which evidence is collected.
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// DefaultStaleDays applies when a repository does not set stale_days.
const DefaultStaleDays = 7

// TestApproval configures detection of fork PRs waiting for test approval.
type TestApproval struct {
	MarkerChecks   []string `json:"marker_checks,omitempty"`
	Command        string   `json:"command,omitempty"`
	CIPaths        []string `json:"ci_paths,omitempty"`
	CIPathsCommand string   `json:"ci_paths_command,omitempty"`
}

// Rules are validated per-repository settings.
type Rules struct {
	TestApproval TestApproval `json:"test_approval"`
	StaleDays    int          `json:"stale_days"`
}

// Default returns the rules used when a repository sets none.
func Default() Rules {
	return Rules{StaleDays: DefaultStaleDays}
}

// Parse strictly decodes and validates rules JSON. Unknown keys are an error;
// empty input yields Default().
func Parse(data []byte) (Rules, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Default(), nil
	}
	var raw struct {
		TestApproval *TestApproval `json:"test_approval"`
		StaleDays    *int          `json:"stale_days"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Rules{}, fmt.Errorf("rules: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Rules{}, errors.New("rules: unexpected data after the JSON object")
	}
	r := Default()
	if raw.TestApproval != nil {
		r.TestApproval = *raw.TestApproval
	}
	if raw.StaleDays != nil {
		r.StaleDays = *raw.StaleDays
	}
	if err := r.Validate(); err != nil {
		return Rules{}, err
	}
	return r, nil
}

// Validate reports every rule violation at once.
func (r Rules) Validate() error {
	var errs []error
	if r.StaleDays < 1 {
		errs = append(errs, errors.New("stale_days must be at least 1"))
	}
	ta := r.TestApproval
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	if slices.ContainsFunc(ta.MarkerChecks, blank) {
		errs = append(errs, errors.New("test_approval.marker_checks entries must be non-empty"))
	}
	if slices.ContainsFunc(ta.CIPaths, blank) {
		errs = append(errs, errors.New("test_approval.ci_paths entries must be non-empty"))
	}
	if len(ta.MarkerChecks) > 0 && blank(ta.Command) {
		errs = append(errs, errors.New("test_approval.command is required with marker_checks"))
	}
	if len(ta.CIPaths) > 0 && blank(ta.CIPathsCommand) {
		errs = append(errs, errors.New("test_approval.ci_paths_command is required with ci_paths"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("rules: %w", errors.Join(errs...))
	}
	return nil
}

// JSON renders the rules as indented JSON for storage and editing.
func (r Rules) JSON() string {
	b, _ := json.MarshalIndent(r, "", "  ") // strings, slices, and ints always marshal
	return string(b)
}

// CollectionHash identifies the settings that govern which evidence is read
// (§4.3). Order of list entries and stale_days do not affect it.
func (r Rules) CollectionHash() string {
	ta := r.TestApproval
	canonical := struct {
		MarkerChecks   []string `json:"marker_checks"`
		Command        string   `json:"command"`
		CIPaths        []string `json:"ci_paths"`
		CIPathsCommand string   `json:"ci_paths_command"`
	}{sortedCopy(ta.MarkerChecks), ta.Command, sortedCopy(ta.CIPaths), ta.CIPathsCommand}
	b, _ := json.Marshal(canonical) // strings and string slices always marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	slices.Sort(out)
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/rules/ -v`
Expected: PASS for all tests.

- [ ] **Step 5: Lint and commit**

```bash
make fmt lint
git add internal/rules
git commit -S -m "feat(rules): parse, validate, and hash repository rules"
```

---

### Task 3: Classification types and merge blockers

**Files:**
- Create: `internal/classify/record.go`, `internal/classify/bucket.go`, `internal/classify/blockers.go`
- Test: `internal/classify/blockers_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `const RecordVersion = 1`
  - Types `Review{State string; SubmittedAt time.Time; Commit string}`, `OtherReview{Author, State string}`, `Thread{URL string; LastAt time.Time}`, `Check{Name, State string; Required bool; RunID int64}`, `Unverified{SHA, Reason string}`, `EvidenceError{Field, Message string; Retryable bool}`, `CoreRetry{Message string; Since time.Time}`, `Milestone{Title string; DueOn *time.Time}`, `Record` (fields below, JSON snake_case).
  - `func (Record) HeadActivityAt() time.Time`; `func ReviewBaseline(reviews []Review) *Review`
  - `type Bucket string` with constants `WaitingTestApproval, WaitingOnMe, NeedsRereview, ApprovedBlocked, NeedsInitialReview, StaleChangesRequested, Bot, ReadyToMerge, WaitingOnAuthor, Own, Draft, Unclassified, Closed`; `func ActionBuckets() []Bucket`; methods `Title() string`, `IsAction() bool`, `Settled() bool`, `Order() int`.
  - Unexported: `type cause struct{ cause, action string }`, `mergeBlockers(Record) []cause`, `summarize([]cause) (reason, action string)`.

- [ ] **Step 1: Write the record and bucket types**

Create `internal/classify/record.go`:

```go
// Package classify turns normalized pull request evidence into the action the
// maintainer owes (§5). It performs no I/O and never reads the clock.
package classify

import "time"

// RecordVersion changes whenever Record's shape or normalization changes;
// stored records with another version are re-collected (§4.3).
const RecordVersion = 1

// Review is one of the viewer's submitted reviews; slices are chronological.
type Review struct {
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	Commit      string    `json:"commit,omitempty"`
}

// OtherReview is another reviewer's standing decision.
type OtherReview struct {
	Author string `json:"author"`
	State  string `json:"state"`
}

// Thread is an unresolved thread the viewer started whose last comment is the author's.
type Thread struct {
	URL    string    `json:"url"`
	LastAt time.Time `json:"last_at"`
}

// Check is one status check context on the head commit.
type Check struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Required bool   `json:"required"`
	RunID    int64  `json:"run_id,omitempty"`
}

// Unverified is a commit whose signature is not valid.
type Unverified struct {
	SHA    string `json:"sha"`
	Reason string `json:"reason"`
}

// EvidenceError records an optional read that failed and fell back (§4.3).
type EvidenceError struct {
	Field     string `json:"field"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// CoreRetry marks retained evidence while a retryable core read keeps failing (§7).
type CoreRetry struct {
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

// Milestone is the PR's milestone.
type Milestone struct {
	Title string     `json:"title"`
	DueOn *time.Time `json:"due_on,omitempty"`
}

// Record is the normalized evidence for one pull request (§4.3).
type Record struct {
	Version             int             `json:"version"`
	CollectionRulesHash string          `json:"collection_rules_hash"`
	Error               string          `json:"error,omitempty"`
	CoreRetry           *CoreRetry      `json:"core_retry,omitempty"`
	EvidenceErrors      []EvidenceError `json:"evidence_errors,omitempty"`

	Number            int        `json:"number"`
	Title             string     `json:"title"`
	URL               string     `json:"url"`
	Author            string     `json:"author"`
	IsBot             bool       `json:"is_bot"`
	Draft             bool       `json:"draft"`
	Fork              bool       `json:"fork"`
	AuthorAssociation string     `json:"author_association,omitempty"`
	Additions         int        `json:"additions"`
	Deletions         int        `json:"deletions"`
	Milestone         *Milestone `json:"milestone,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	BaseRef         string     `json:"base_ref"`
	BaseOID         string     `json:"base_oid"`
	HeadOID         string     `json:"head_oid"`
	HeadCommittedAt time.Time  `json:"head_committed_at"`
	HeadChangedAt   *time.Time `json:"head_changed_at,omitempty"`

	MergeState     string     `json:"merge_state"`
	ReviewDecision string     `json:"review_decision,omitempty"`
	AutoMerge      bool       `json:"auto_merge"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`

	ReviewRequestedFromMe bool          `json:"review_requested_from_me"`
	MyReviews             []Review      `json:"my_reviews,omitempty"`
	OthersDecisive        []OtherReview `json:"others_decisive,omitempty"`
	MyThreadsWaiting      []Thread      `json:"my_threads_waiting,omitempty"`
	UnresolvedThreads     int           `json:"unresolved_threads"`
	AuthorUpdatedAt       *time.Time    `json:"author_updated_at,omitempty"`
	MyLastCommentAt       *time.Time    `json:"my_last_comment_at,omitempty"`

	UnverifiedCommits  []Unverified `json:"unverified_commits,omitempty"`
	SignaturesComplete bool         `json:"signatures_complete"`
	SignaturePolicy    string       `json:"signature_policy,omitempty"`

	AwaitingRuns   int     `json:"awaiting_runs"`
	MarkerPresent  *bool   `json:"marker_present,omitempty"`
	CIPathsTouched *bool   `json:"ci_paths_touched,omitempty"`
	DiffChanged    *bool   `json:"diff_changed,omitempty"`
	Checks         []Check `json:"checks,omitempty"`
}

// HeadActivityAt is the observed head change, else the head commit date
// clamped to PR creation; commit dates are author-controlled (§4.3).
func (r Record) HeadActivityAt() time.Time {
	if r.HeadChangedAt != nil {
		return *r.HeadChangedAt
	}
	if r.HeadCommittedAt.Before(r.CreatedAt) {
		return r.CreatedAt
	}
	return r.HeadCommittedAt
}

var decisiveStates = map[string]bool{"APPROVED": true, "CHANGES_REQUESTED": true, "DISMISSED": true}

func latestReview(reviews []Review, states map[string]bool) *Review {
	for i := len(reviews) - 1; i >= 0; i-- {
		if states == nil || states[reviews[i].State] {
			r := reviews[i]
			return &r
		}
	}
	return nil
}

// ReviewBaseline is the review new commits are judged against: the viewer's
// standing decision, else the viewer's latest review. A later COMMENTED review
// does not renew an approval or a changes request.
func ReviewBaseline(reviews []Review) *Review {
	if d := latestReview(reviews, decisiveStates); d != nil && d.State != "DISMISSED" {
		return d
	}
	return latestReview(reviews, nil)
}
```

Create `internal/classify/bucket.go`:

```go
package classify

import "slices"

// Bucket identifies the action group a PR belongs to (§5.1).
type Bucket string

// Action buckets in precedence order, then non-action buckets.
const (
	WaitingTestApproval   Bucket = "waiting_test_approval"
	WaitingOnMe           Bucket = "waiting_on_me"
	NeedsRereview         Bucket = "needs_rereview"
	ApprovedBlocked       Bucket = "approved_blocked"
	NeedsInitialReview    Bucket = "needs_initial_review"
	StaleChangesRequested Bucket = "stale_changes_requested"
	Bot                   Bucket = "bot"
	ReadyToMerge          Bucket = "ready_to_merge"
	WaitingOnAuthor       Bucket = "waiting_on_author"
	Own                   Bucket = "own"
	Draft                 Bucket = "draft"
	Unclassified          Bucket = "unclassified"
	Closed                Bucket = "closed"
)

var actionOrder = []Bucket{
	WaitingTestApproval, WaitingOnMe, NeedsRereview, ApprovedBlocked,
	NeedsInitialReview, StaleChangesRequested, Bot,
}

var order = append(slices.Clone(actionOrder), ReadyToMerge, WaitingOnAuthor, Own, Draft, Unclassified, Closed)

var titles = map[Bucket]string{
	WaitingTestApproval:   "Waiting test approval",
	WaitingOnMe:           "Waiting on me",
	NeedsRereview:         "Needs re-review",
	ApprovedBlocked:       "Approved, blocked",
	NeedsInitialReview:    "Needs initial review",
	StaleChangesRequested: "Stale changes requested",
	Bot:                   "Bot PRs",
	ReadyToMerge:          "Ready to merge",
	WaitingOnAuthor:       "Waiting on author",
	Own:                   "Own",
	Draft:                 "Drafts",
	Unclassified:          "Unclassified",
	Closed:                "Closed",
}

// ActionBuckets returns the action buckets in precedence order.
func ActionBuckets() []Bucket { return slices.Clone(actionOrder) }

// Title is the human-readable bucket name.
func (b Bucket) Title() string {
	if t, ok := titles[b]; ok {
		return t
	}
	return string(b)
}

// IsAction reports whether the bucket means the maintainer owes an action.
func (b Bucket) IsAction() bool { return slices.Contains(actionOrder, b) }

// Settled reports whether transitions consumers treat the bucket as settled (§6).
func (b Bucket) Settled() bool { return b != Unclassified }

// Order is the bucket's display and ranking position.
func (b Bucket) Order() int {
	if i := slices.Index(order, b); i >= 0 {
		return i
	}
	return len(order)
}
```

- [ ] **Step 2: Write the failing blocker tests**

Create `internal/classify/blockers_test.go`:

```go
package classify

import (
	"reflect"
	"testing"
	"time"
)

func TestMergeBlockers(t *testing.T) {
	failing := Check{Name: "tests", State: "FAILURE", Required: true, RunID: 42}
	cases := []struct {
		name string
		rec  Record
		want []cause
	}{
		{"clean has none", Record{MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "absent"}, nil},
		{"unstable is eligible", Record{MergeState: "UNSTABLE", SignaturesComplete: true, SignaturePolicy: "absent"}, nil},
		{"dirty", Record{MergeState: "DIRTY", SignaturePolicy: "absent"},
			[]cause{{"conflicts with base", "ask the author to rebase"}}},
		{"behind", Record{MergeState: "BEHIND", SignaturePolicy: "absent"},
			[]cause{{"behind base", "update the branch"}}},
		{"behind with auto-merge", Record{MergeState: "BEHIND", AutoMerge: true, SignaturePolicy: "absent"},
			[]cause{{"behind base", "run serial-auto-merge"}}},
		{"unknown", Record{MergeState: "UNKNOWN", SignaturePolicy: "absent"},
			[]cause{{"mergeability unknown", "check the merge box on GitHub"}}},
		{"other state", Record{MergeState: "DRAFT", SignaturePolicy: "absent"},
			[]cause{{"merge state draft", "check the merge box on GitHub"}}},
		{"blocked by failing required check", Record{MergeState: "BLOCKED", SignaturesComplete: true, SignaturePolicy: "absent",
			Checks: []Check{failing, {Name: "lint", State: "SUCCESS", Required: true}, {Name: "docs", State: "FAILURE"}}},
			[]cause{{"required check `tests` is failure", "`gh run rerun 42 --failed` after a flake check"}}},
		{"blocked by pending required check", Record{MergeState: "BLOCKED", SignaturesComplete: true, SignaturePolicy: "absent",
			Checks: []Check{{Name: "e2e", State: "IN_PROGRESS", Required: true, RunID: 7}}},
			[]cause{{"required check `e2e` is in_progress", "wait for the check, or investigate why it has not passed"}}},
		{"blocked without cause", Record{MergeState: "BLOCKED", SignaturesComplete: true, SignaturePolicy: "absent", UnresolvedThreads: 2},
			[]cause{{"blocked by branch policy; cause not identified (2 unresolved threads)", "check the merge box on GitHub"}}},
		{"signatures incomplete", Record{MergeState: "CLEAN", SignaturePolicy: "required"},
			[]cause{{"signature state unknown", "check commit signatures on GitHub"}}},
		{"unverified with required policy", Record{MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "required",
			UnverifiedCommits: []Unverified{{SHA: "abcdef0123", Reason: "unsigned"}, {SHA: "1234567890", Reason: "bad_email"}}},
			[]cause{{"unverified commits: abcdef0 (unsigned), 1234567 (bad_email)", "ask the author to sign the listed commits"}}},
		{"unverified with unreadable policy", Record{MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "unavailable",
			UnverifiedCommits: []Unverified{{SHA: "abcdef0123", Reason: "unsigned"}}},
			[]cause{{"1 unverified commits; signature policy unreadable: abcdef0 (unsigned)", "confirm the signature policy, then ask the author to sign if required"}}},
		{"unverified with absent policy", Record{MergeState: "CLEAN", SignaturePolicy: "absent",
			UnverifiedCommits: []Unverified{{SHA: "abcdef0123", Reason: "unsigned"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeBlockers(tc.rec); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeBlockers = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSummarizeDeduplicatesActions(t *testing.T) {
	reason, action := summarize([]cause{
		{"behind base", "update the branch"},
		{"signature state unknown", "check commit signatures on GitHub"},
		{"mergeability unknown", "update the branch"},
	})
	if reason != "approved; behind base; signature state unknown; mergeability unknown" {
		t.Fatalf("reason = %q", reason)
	}
	if action != "update the branch; check commit signatures on GitHub" {
		t.Fatalf("action = %q", action)
	}
}

func TestReviewBaseline(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	approved := Review{State: "APPROVED", SubmittedAt: at, Commit: "a"}
	commented := Review{State: "COMMENTED", SubmittedAt: at.Add(time.Hour), Commit: "b"}
	dismissed := Review{State: "DISMISSED", SubmittedAt: at.Add(2 * time.Hour), Commit: "c"}
	if got := ReviewBaseline([]Review{approved, commented}); got.Commit != "a" {
		t.Fatalf("later comment renewed the approval: %+v", got)
	}
	if got := ReviewBaseline([]Review{approved, commented, dismissed}); got.Commit != "c" {
		t.Fatalf("dismissed decision should fall back to latest review: %+v", got)
	}
	if ReviewBaseline(nil) != nil {
		t.Fatal("no reviews should have no baseline")
	}
}

func TestHeadActivityAt(t *testing.T) {
	created := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	backdated := Record{CreatedAt: created, HeadCommittedAt: created.Add(-48 * time.Hour)}
	if got := backdated.HeadActivityAt(); !got.Equal(created) {
		t.Fatalf("backdated commit not clamped: %v", got)
	}
	observed := created.Add(5 * time.Hour)
	withChange := Record{CreatedAt: created, HeadCommittedAt: created.Add(time.Hour), HeadChangedAt: &observed}
	if got := withChange.HeadActivityAt(); !got.Equal(observed) {
		t.Fatalf("observed head change ignored: %v", got)
	}
}

func TestBucketOrderAndTitles(t *testing.T) {
	if WaitingTestApproval.Order() >= Bot.Order() || Bot.Order() >= ReadyToMerge.Order() {
		t.Fatal("action buckets must precede non-action buckets in precedence order")
	}
	if !Bot.IsAction() || ReadyToMerge.IsAction() || Unclassified.Settled() || !Closed.Settled() {
		t.Fatal("bucket predicates wrong")
	}
	if NeedsRereview.Title() != "Needs re-review" || Bucket("x").Title() != "x" {
		t.Fatal("titles wrong")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/classify/`
Expected: FAIL to compile (`undefined: mergeBlockers`, `cause`, `summarize`).

- [ ] **Step 4: Implement blockers**

Create `internal/classify/blockers.go`:

```go
package classify

import (
	"fmt"
	"strings"
	"time"
)

type cause struct {
	cause  string
	action string
}

var (
	passingChecks    = map[string]bool{"SUCCESS": true, "NEUTRAL": true, "SKIPPED": true}
	rerunnableChecks = map[string]bool{"FAILURE": true, "TIMED_OUT": true, "CANCELLED": true, "STARTUP_FAILURE": true}
	eligibleStates   = map[string]bool{"CLEAN": true, "HAS_HOOKS": true, "UNSTABLE": true}
)

// mergeBlockers names why GitHub would refuse the merge (§5.4); empty means mergeable.
func mergeBlockers(rec Record) []cause {
	var causes []cause
	switch state := rec.MergeState; {
	case state == "DIRTY":
		causes = append(causes, cause{"conflicts with base", "ask the author to rebase"})
	case state == "BEHIND":
		action := "update the branch"
		if rec.AutoMerge {
			action = "run serial-auto-merge"
		}
		causes = append(causes, cause{"behind base", action})
	case state == "UNKNOWN":
		causes = append(causes, cause{"mergeability unknown", "check the merge box on GitHub"})
	case state == "BLOCKED":
		for _, c := range rec.Checks {
			if !c.Required || passingChecks[c.State] {
				continue
			}
			action := "wait for the check, or investigate why it has not passed"
			if c.RunID != 0 && rerunnableChecks[c.State] {
				action = fmt.Sprintf("`gh run rerun %d --failed` after a flake check", c.RunID)
			}
			causes = append(causes, cause{fmt.Sprintf("required check `%s` is %s", c.Name, strings.ToLower(c.State)), action})
		}
	case !eligibleStates[state]:
		causes = append(causes, cause{"merge state " + strings.ToLower(state), "check the merge box on GitHub"})
	}
	if c := signatureBlocker(rec); c != nil {
		causes = append(causes, *c)
	}
	if rec.MergeState == "BLOCKED" && len(causes) == 0 {
		causes = append(causes, cause{
			fmt.Sprintf("blocked by branch policy; cause not identified (%d unresolved threads)", rec.UnresolvedThreads),
			"check the merge box on GitHub",
		})
	}
	return causes
}

func signatureBlocker(rec Record) *cause {
	if rec.SignaturePolicy == "absent" {
		return nil
	}
	if !rec.SignaturesComplete {
		return &cause{"signature state unknown", "check commit signatures on GitHub"}
	}
	if len(rec.UnverifiedCommits) == 0 {
		return nil
	}
	listed := make([]string, len(rec.UnverifiedCommits))
	for i, u := range rec.UnverifiedCommits {
		listed[i] = fmt.Sprintf("%s (%s)", short(u.SHA), u.Reason)
	}
	joined := strings.Join(listed, ", ")
	if rec.SignaturePolicy == "required" {
		return &cause{"unverified commits: " + joined, "ask the author to sign the listed commits"}
	}
	return &cause{
		fmt.Sprintf("%d unverified commits; signature policy unreadable: %s", len(rec.UnverifiedCommits), joined),
		"confirm the signature policy, then ask the author to sign if required",
	}
}

func summarize(causes []cause) (string, string) {
	reasons := make([]string, 0, len(causes))
	var actions []string
	seen := map[string]bool{}
	for _, c := range causes {
		reasons = append(reasons, c.cause)
		if !seen[c.action] {
			seen[c.action] = true
			actions = append(actions, c.action)
		}
	}
	return "approved; " + strings.Join(reasons, "; "), strings.Join(actions, "; ")
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func timePtr(t time.Time) *time.Time { return &t }
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/classify/ -v`
Expected: PASS.

- [ ] **Step 6: Lint and commit**

```bash
make fmt lint
git add internal/classify
git commit -S -m "feat(classify): add evidence record, buckets, and merge blockers"
```

---

### Task 4: Classification rules

**Files:**
- Create: `internal/classify/classify.go`
- Test: `internal/classify/classify_test.go`

**Interfaces:**
- Consumes: Task 2 `rules.Rules`, `rules.TestApproval`; Task 3 types and `mergeBlockers`, `summarize`, `short`, `timePtr`, `latestReview`, `decisiveStates`.
- Produces:
  - `type Result struct { Bucket Bucket; Reason, Action string; WaitingSince *time.Time; Also, Notes []string }`
  - `func Classify(rec Record, rl rules.Rules, viewer string, now time.Time) Result`

- [ ] **Step 1: Write the failing tests**

Create `internal/classify/classify_test.go`:

```go
package classify

import (
	"slices"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func hours(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }
func days(d float64) time.Time  { return hours(d * 24) }
func ptr[T any](v T) *T         { return &v }

const me = "me"

var sweepRules = rules.Rules{StaleDays: 7, TestApproval: rules.TestApproval{
	MarkerChecks:   []string{"tests / Test", "tests-skip"},
	Command:        "/ok-to-test",
	CIPaths:        []string{".github/"},
	CIPathsCommand: "/ok-to-test confirm-ci",
}}

// baseRecord is a non-draft PR by alice, never reviewed by me, mergeable.
func baseRecord() Record {
	return Record{
		Version: RecordVersion, Number: 7, Title: "Fix flaky test", URL: "https://github.com/o/r/pull/7",
		Author: "alice", CreatedAt: hours(0), UpdatedAt: hours(2),
		BaseRef: "main", BaseOID: "base0000000", HeadOID: "head111aaaa", HeadCommittedAt: hours(1),
		MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "absent",
	}
}

func review(state string, at time.Time, commit string) Review {
	return Review{State: state, SubmittedAt: at, Commit: commit}
}

type classifyCase struct {
	name      string
	mod       func(r *Record)
	rules     *rules.Rules
	now       time.Time
	bucket    Bucket
	reason    string
	action    string
	since     *time.Time
	noSince   bool
	also      []string
	notes     []string
	checkAlso bool
}

func TestClassify(t *testing.T) {
	cases := []classifyCase{
		{name: "record error is unclassified", mod: func(r *Record) { r.Error = "boom" },
			bucket: Unclassified, reason: "boom", action: "check the PR on GitHub", noSince: true},
		{name: "draft", mod: func(r *Record) { r.Draft = true }, bucket: Draft, reason: "draft", since: ptr(hours(0))},
		{name: "own", mod: func(r *Record) { r.Author = me }, bucket: Own, reason: "your pull request"},
		{name: "bot approved and clean", mod: func(r *Record) { r.IsBot = true; r.ReviewDecision = "APPROVED" },
			bucket: Bot, reason: "approved; ready to merge", action: "merge", since: ptr(hours(0))},
		{name: "bot approved and behind", mod: func(r *Record) { r.IsBot = true; r.ReviewDecision = "APPROVED"; r.MergeState = "BEHIND" },
			bucket: Bot, reason: "approved; behind base", action: "update the branch"},
		{name: "bot without decision", mod: func(r *Record) { r.IsBot = true },
			bucket: Bot, reason: "no review decision", action: "review in batch"},
		{name: "bot review required", mod: func(r *Record) { r.IsBot = true; r.ReviewDecision = "REVIEW_REQUIRED" },
			bucket: Bot, reason: "review required", action: "review in batch"},
		{name: "awaiting workflow runs", mod: func(r *Record) { r.AwaitingRuns = 2 },
			bucket: WaitingTestApproval, reason: "2 workflow runs awaiting approval",
			action: "approve and run workflows after reading the diff", since: ptr(hours(1)),
			also: []string{"Needs initial review"}, checkAlso: true},
		{name: "one awaiting run", mod: func(r *Record) { r.AwaitingRuns = 1 },
			bucket: WaitingTestApproval, reason: "1 workflow run awaiting approval"},
		{name: "conflicted PR never waits for test approval", mod: func(r *Record) { r.AwaitingRuns = 1; r.MergeState = "DIRTY" },
			bucket: WaitingOnAuthor, reason: "waiting on author"},
		{name: "test approval since clamps backdated commit", mod: func(r *Record) { r.AwaitingRuns = 1; r.HeadCommittedAt = hours(-5) },
			bucket: WaitingTestApproval, since: ptr(hours(0))},
		{name: "observed head change dates test approval", mod: func(r *Record) { r.AwaitingRuns = 1; r.HeadChangedAt = ptr(hours(6)) },
			bucket: WaitingTestApproval, since: ptr(hours(6))},
		{name: "fork missing marker", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(false); r.CIPathsTouched = ptr(false) },
			bucket: WaitingTestApproval, reason: "fork PR; no `tests / Test` or `tests-skip` check on head111",
			action: "comment `/ok-to-test` after reading the diff"},
		{name: "fork missing marker changes CI", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(false); r.CIPathsTouched = ptr(true) },
			bucket: WaitingTestApproval, reason: "fork PR; no `tests / Test` or `tests-skip` check on head111; changes CI files",
			action: "comment `/ok-to-test confirm-ci` after reading the diff"},
		{name: "fork missing marker with unreadable files", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(false) },
			bucket: WaitingTestApproval, reason: "fork PR; no `tests / Test` or `tests-skip` check on head111; file list unreadable",
			action: "comment `/ok-to-test confirm-ci` after reading the diff"},
		{name: "fork with marker present", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(true) },
			bucket: NeedsInitialReview},
		{name: "review requested and replies", mod: func(r *Record) {
			r.ReviewRequestedFromMe = true
			r.MyThreadsWaiting = []Thread{{URL: "https://github.com/o/r/pull/7#c1", LastAt: hours(5)}, {URL: "https://github.com/o/r/pull/7#c2", LastAt: hours(3)}}
		}, bucket: WaitingOnMe, reason: "your review is requested; author replied in your threads (2)", action: "review",
			since: ptr(hours(5)), also: []string{"Needs initial review"}, checkAlso: true,
			notes: []string{"https://github.com/o/r/pull/7#c1", "https://github.com/o/r/pull/7#c2"}},
		{name: "replies only", mod: func(r *Record) {
			r.MyReviews = []Review{review("COMMENTED", hours(1.5), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.AuthorUpdatedAt = ptr(hours(5))
			r.MyThreadsWaiting = []Thread{{URL: "https://github.com/o/r/pull/7#c1", LastAt: hours(5)}}
		}, bucket: WaitingOnMe, action: "reply in the linked threads"},
		{name: "author silent since my comment", mod: func(r *Record) {
			r.ReviewRequestedFromMe = true
			r.MyReviews = []Review{review("COMMENTED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
			r.AuthorUpdatedAt = ptr(hours(2))
		}, bucket: WaitingOnAuthor, reason: "waiting on author", since: ptr(hours(3))},
		{name: "diff changed since approval", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.AuthorUpdatedAt = ptr(hours(4))
			r.HeadChangedAt = ptr(hours(4))
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview, reason: "diff changed since your review (approved at old2222)", action: "re-review", since: ptr(hours(4))},
		{name: "diff unknown since approval", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.HeadChangedAt = ptr(hours(4))
		}, bucket: NeedsRereview, reason: "could not confirm the diff is unchanged since your review (approved at old2222)"},
		{name: "diff unchanged after branch update", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.DiffChanged = ptr(false)
			r.ReviewDecision = "APPROVED"
			r.ApprovedAt = ptr(hours(1.5))
		}, bucket: ReadyToMerge, reason: "approved; ready to merge", action: "merge", since: ptr(hours(1.5)),
			notes: []string{"branch updated, diff unchanged"}},
		{name: "re-review does not need author activity", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(6))
			r.AuthorUpdatedAt = ptr(hours(4))
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview},
		{name: "re-review never when conflicted", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.DiffChanged = ptr(true)
			r.MergeState = "DIRTY"
		}, bucket: WaitingOnAuthor},
		{name: "re-review since clamps to review time", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.HeadCommittedAt = hours(-10)
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview, since: ptr(hours(1.5))},
		{name: "approved and behind", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.ApprovedAt = ptr(hours(3))
			r.MergeState = "BEHIND"
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ApprovedBlocked, reason: "approved; behind base", action: "update the branch", since: ptr(hours(3))},
		{name: "approved and behind with auto-merge", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.MergeState = "BEHIND"
			r.AutoMerge = true
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ApprovedBlocked, action: "run serial-auto-merge", since: ptr(hours(2))},
		{name: "approved with failing required check", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.MergeState = "BLOCKED"
			r.Checks = []Check{{Name: "tests", State: "FAILURE", Required: true, RunID: 42}}
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ApprovedBlocked, reason: "approved; required check `tests` is failure",
			action: "`gh run rerun 42 --failed` after a flake check"},
		{name: "initial review", bucket: NeedsInitialReview, reason: "not reviewed by you", action: "review", since: ptr(hours(0))},
		{name: "stale changes requested", now: days(9), mod: func(r *Record) {
			r.MyReviews = []Review{review("CHANGES_REQUESTED", days(1), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(1))
			r.AuthorUpdatedAt = ptr(hours(2))
			r.HeadChangedAt = ptr(hours(1))
		}, bucket: StaleChangesRequested, reason: "changes requested; no author activity in 8 days since your feedback",
			action: "nudge the author, or close", since: ptr(days(1))},
		{name: "changes requested not yet stale", now: days(5), mod: func(r *Record) {
			r.MyReviews = []Review{review("CHANGES_REQUESTED", days(1), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(1))
			r.AuthorUpdatedAt = ptr(hours(2))
			r.HeadChangedAt = ptr(hours(1))
		}, bucket: WaitingOnAuthor},
		{name: "unknown push history cannot backdate staleness", now: days(9), mod: func(r *Record) {
			r.MyReviews = []Review{review("COMMENTED", days(0.1), "head111aaaa"), review("CHANGES_REQUESTED", days(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(3))
		}, bucket: WaitingOnAuthor},
		{name: "unknown push history dates staleness from standing request", now: days(10.5), mod: func(r *Record) {
			r.MyReviews = []Review{review("COMMENTED", days(0.1), "head111aaaa"), review("CHANGES_REQUESTED", days(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(3))
		}, bucket: StaleChangesRequested, reason: "changes requested; no author activity in 7 days since your feedback", since: ptr(days(3))},
		{name: "re-review suppresses stale nudge", now: days(9), mod: func(r *Record) {
			r.MyReviews = []Review{review("CHANGES_REQUESTED", days(1), "old2222bbbb")}
			r.MyLastCommentAt = ptr(days(1))
			r.AuthorUpdatedAt = ptr(hours(2))
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview, also: nil, checkAlso: true},
		{name: "ready to merge", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.ApprovedAt = ptr(hours(3))
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ReadyToMerge, since: ptr(hours(3))},
		{name: "notes from reviews, checks, and evidence", mod: func(r *Record) {
			r.OthersDecisive = []OtherReview{{Author: "bob", State: "APPROVED"}, {Author: "carol", State: "CHANGES_REQUESTED"}}
			r.MergeState = "UNSTABLE"
			r.EvidenceErrors = []EvidenceError{{Field: "signature_policy", Message: "HTTP 403"}, {Field: "diff", Message: "timeout", Retryable: true}}
			r.CoreRetry = &CoreRetry{Message: "HTTP 502", Since: hours(5)}
		}, bucket: NeedsInitialReview, notes: []string{
			"approved by @bob", "changes requested by @carol", "non-required checks failing",
			"signature_policy unavailable: HTTP 403", "diff unavailable: timeout (retry pending)",
			"evidence stale since 2026-10-01T17:00:00Z, retrying: HTTP 502",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := baseRecord()
			if tc.mod != nil {
				tc.mod(&rec)
			}
			rl := rules.Default()
			if tc.rules != nil {
				rl = *tc.rules
			}
			now := tc.now
			if now.IsZero() {
				now = days(1)
			}
			got := Classify(rec, rl, me, now)
			if got.Bucket != tc.bucket {
				t.Fatalf("bucket = %s (%q), want %s", got.Bucket, got.Reason, tc.bucket)
			}
			if tc.reason != "" && got.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.reason)
			}
			if tc.action != "" && got.Action != tc.action {
				t.Errorf("action = %q, want %q", got.Action, tc.action)
			}
			if tc.noSince && got.WaitingSince != nil {
				t.Errorf("waiting since = %v, want none", got.WaitingSince)
			}
			if tc.since != nil && (got.WaitingSince == nil || !got.WaitingSince.Equal(*tc.since)) {
				t.Errorf("waiting since = %v, want %v", got.WaitingSince, *tc.since)
			}
			if tc.checkAlso && !slices.Equal(got.Also, tc.also) {
				t.Errorf("also = %v, want %v", got.Also, tc.also)
			}
			for _, note := range tc.notes {
				if !slices.Contains(got.Notes, note) {
					t.Errorf("notes %v missing %q", got.Notes, note)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/classify/ -run TestClassify`
Expected: FAIL to compile (`undefined: Classify`).

- [ ] **Step 3: Implement classification**

Create `internal/classify/classify.go`:

```go
package classify

import (
	"fmt"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

// Result is the action owed for one pull request.
type Result struct {
	Bucket       Bucket
	Reason       string
	Action       string
	WaitingSince *time.Time
	Also         []string
	Notes        []string
}

// Classify derives the result for rec as seen by viewer at now (§5.2).
func Classify(rec Record, rl rules.Rules, viewer string, now time.Time) Result {
	if rec.Error != "" {
		return Result{Bucket: Unclassified, Reason: rec.Error, Action: "check the PR on GitHub"}
	}
	var res Result
	switch {
	case rec.Draft:
		res = Result{Bucket: Draft, Reason: "draft", WaitingSince: timePtr(rec.CreatedAt)}
	case rec.Author == viewer:
		res = Result{Bucket: Own, Reason: "your pull request", WaitingSince: timePtr(rec.CreatedAt)}
	default:
		causes := mergeBlockers(rec)
		if rec.IsBot {
			res = botResult(rec, causes)
		} else {
			res = candidateResult(rec, rl, now, causes)
		}
		res.Notes = append(res.Notes, commonNotes(rec)...)
	}
	res.Notes = append(res.Notes, evidenceNotes(rec)...)
	return res
}

type candidate struct {
	bucket Bucket
	result *Result
}

func candidateResult(rec Record, rl rules.Rules, now time.Time, causes []cause) Result {
	review := reviewable(rec)
	rereview := needsRereview(rec)
	candidates := []candidate{
		{WaitingTestApproval, awaitingTestApproval(rec, rl.TestApproval)},
		{WaitingOnMe, onlyIf(review, waitingOnMe(rec))},
		{NeedsRereview, rereview},
		{ApprovedBlocked, approvedBlocked(rec, causes)},
		{NeedsInitialReview, onlyIf(review, needsInitialReview(rec))},
		{StaleChangesRequested, onlyIf(rereview == nil, staleChangesRequested(rec, now, rl.StaleDays))},
	}
	var primary *Result
	var also []string
	for _, c := range candidates {
		if c.result == nil {
			continue
		}
		if primary == nil {
			primary = c.result
			primary.Bucket = c.bucket
			continue
		}
		also = append(also, c.bucket.Title())
	}
	if primary != nil {
		primary.Also = also
		return *primary
	}
	if rec.ReviewDecision == "APPROVED" && len(causes) == 0 {
		return Result{Bucket: ReadyToMerge, Reason: "approved; ready to merge", Action: "merge",
			WaitingSince: firstTime(rec.ApprovedAt, rec.UpdatedAt)}
	}
	return Result{Bucket: WaitingOnAuthor, Reason: "waiting on author", WaitingSince: firstTime(rec.MyLastCommentAt, rec.UpdatedAt)}
}

func onlyIf(ok bool, r *Result) *Result {
	if !ok {
		return nil
	}
	return r
}

// authorMoved reports whether the PR is new to the viewer or the author acted after the viewer's last comment.
func authorMoved(rec Record) bool {
	return rec.MyLastCommentAt == nil || (rec.AuthorUpdatedAt != nil && rec.AuthorUpdatedAt.After(*rec.MyLastCommentAt))
}

func reviewable(rec Record) bool { return rec.MergeState != "DIRTY" && authorMoved(rec) }

func awaitingTestApproval(rec Record, ta rules.TestApproval) *Result {
	// GitHub runs no pull_request workflows while a PR conflicts.
	if rec.MergeState == "DIRTY" {
		return nil
	}
	since := timePtr(rec.HeadActivityAt())
	if n := rec.AwaitingRuns; n > 0 {
		return &Result{Reason: fmt.Sprintf("%d workflow run%s awaiting approval", n, plural(n)),
			Action: "approve and run workflows after reading the diff", WaitingSince: since}
	}
	if !rec.Fork || len(ta.MarkerChecks) == 0 || rec.MarkerPresent == nil || *rec.MarkerPresent {
		return nil
	}
	names := make([]string, len(ta.MarkerChecks))
	for i, name := range ta.MarkerChecks {
		names[i] = "`" + name + "`"
	}
	reason := fmt.Sprintf("fork PR; no %s check on %s", strings.Join(names, " or "), short(rec.HeadOID))
	command := ta.Command
	if len(ta.CIPaths) > 0 && (rec.CIPathsTouched == nil || *rec.CIPathsTouched) {
		command = ta.CIPathsCommand
		if rec.CIPathsTouched == nil {
			reason += "; file list unreadable"
		} else {
			reason += "; changes CI files"
		}
	}
	return &Result{Reason: reason, Action: fmt.Sprintf("comment `%s` after reading the diff", command), WaitingSince: since}
}

func waitingOnMe(rec Record) *Result {
	threads := rec.MyThreadsWaiting
	if !rec.ReviewRequestedFromMe && len(threads) == 0 {
		return nil
	}
	var parts []string
	action := "reply in the linked threads"
	if rec.ReviewRequestedFromMe {
		parts = append(parts, "your review is requested")
		action = "review"
	}
	if len(threads) > 0 {
		parts = append(parts, fmt.Sprintf("author replied in your threads (%d)", len(threads)))
	}
	since := rec.UpdatedAt
	notes := make([]string, 0, len(threads))
	for i, th := range threads {
		if i == 0 || th.LastAt.After(since) {
			since = th.LastAt
		}
		notes = append(notes, th.URL)
	}
	return &Result{Reason: strings.Join(parts, "; "), Action: action, WaitingSince: timePtr(since), Notes: notes}
}

func needsRereview(rec Record) *Result {
	if rec.MergeState == "DIRTY" {
		return nil
	}
	baseline := ReviewBaseline(rec.MyReviews)
	if baseline == nil || baseline.Commit == rec.HeadOID || (rec.DiffChanged != nil && !*rec.DiffChanged) {
		return nil
	}
	reason := "could not confirm the diff is unchanged since your review"
	if rec.DiffChanged != nil {
		reason = "diff changed since your review"
	}
	commit := baseline.Commit
	if commit == "" {
		commit = "unknown"
	}
	state := strings.ReplaceAll(strings.ToLower(baseline.State), "_", " ")
	since := rec.HeadActivityAt()
	if baseline.SubmittedAt.After(since) {
		since = baseline.SubmittedAt
	}
	return &Result{Reason: fmt.Sprintf("%s (%s at %s)", reason, state, short(commit)), Action: "re-review", WaitingSince: timePtr(since)}
}

func approvedBlocked(rec Record, causes []cause) *Result {
	if rec.ReviewDecision != "APPROVED" || len(causes) == 0 {
		return nil
	}
	reason, action := summarize(causes)
	return &Result{Reason: reason, Action: action, WaitingSince: firstTime(rec.ApprovedAt, rec.UpdatedAt)}
}

func needsInitialReview(rec Record) *Result {
	if len(rec.MyReviews) > 0 {
		return nil
	}
	return &Result{Reason: "not reviewed by you", Action: "review", WaitingSince: timePtr(rec.CreatedAt)}
}

func staleChangesRequested(rec Record, now time.Time, staleDays int) *Result {
	decisive := latestReview(rec.MyReviews, decisiveStates)
	if decisive == nil || decisive.State != "CHANGES_REQUESTED" || authorMoved(rec) {
		return nil
	}
	var handed *time.Time
	for _, r := range rec.MyReviews {
		if rec.AuthorUpdatedAt != nil && !r.SubmittedAt.After(*rec.AuthorUpdatedAt) {
			continue
		}
		if handed == nil || r.SubmittedAt.Before(*handed) {
			handed = timePtr(r.SubmittedAt)
		}
	}
	if handed == nil && rec.MyLastCommentAt != nil {
		handed = timePtr(*rec.MyLastCommentAt)
	}
	if handed == nil {
		return nil
	}
	// Without an observed push the history is unknown: a push may have followed earlier feedback.
	if rec.HeadChangedAt == nil && handed.Before(decisive.SubmittedAt) {
		handed = timePtr(decisive.SubmittedAt)
	}
	age := now.Sub(*handed)
	if age < time.Duration(staleDays)*24*time.Hour {
		return nil
	}
	return &Result{
		Reason:       fmt.Sprintf("changes requested; no author activity in %d days since your feedback", int(age/(24*time.Hour))),
		Action:       "nudge the author, or close",
		WaitingSince: handed,
	}
}

func botResult(rec Record, causes []cause) Result {
	since := timePtr(rec.CreatedAt)
	switch {
	case rec.ReviewDecision == "APPROVED" && len(causes) == 0:
		return Result{Bucket: Bot, Reason: "approved; ready to merge", Action: "merge", WaitingSince: since}
	case rec.ReviewDecision == "APPROVED":
		reason, action := summarize(causes)
		return Result{Bucket: Bot, Reason: reason, Action: action, WaitingSince: since}
	}
	decision := "no review decision"
	if rec.ReviewDecision != "" {
		decision = strings.ReplaceAll(strings.ToLower(rec.ReviewDecision), "_", " ")
	}
	return Result{Bucket: Bot, Reason: decision, Action: "review in batch", WaitingSince: since}
}

func commonNotes(rec Record) []string {
	var notes []string
	for _, o := range rec.OthersDecisive {
		switch o.State {
		case "APPROVED":
			notes = append(notes, "approved by @"+o.Author)
		case "CHANGES_REQUESTED":
			notes = append(notes, "changes requested by @"+o.Author)
		}
	}
	if b := ReviewBaseline(rec.MyReviews); b != nil && b.Commit != rec.HeadOID && rec.DiffChanged != nil && !*rec.DiffChanged {
		notes = append(notes, "branch updated, diff unchanged")
	}
	if rec.MergeState == "UNSTABLE" {
		notes = append(notes, "non-required checks failing")
	}
	return notes
}

func evidenceNotes(rec Record) []string {
	var notes []string
	for _, e := range rec.EvidenceErrors {
		note := fmt.Sprintf("%s unavailable: %s", e.Field, e.Message)
		if e.Retryable {
			note += " (retry pending)"
		}
		notes = append(notes, note)
	}
	if rec.CoreRetry != nil {
		notes = append(notes, fmt.Sprintf("evidence stale since %s, retrying: %s",
			rec.CoreRetry.Since.UTC().Format(time.RFC3339), rec.CoreRetry.Message))
	}
	return notes
}

func firstTime(p *time.Time, fallback time.Time) *time.Time {
	if p != nil {
		return timePtr(*p)
	}
	return timePtr(fallback)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/classify/ -v`
Expected: PASS for `TestClassify` (all subtests) and Task 3 tests.

- [ ] **Step 5: Lint and commit**

```bash
make fmt lint
git add internal/classify
git commit -S -m "feat(classify): classify pull requests into action buckets"
```

---

### Task 5: Ranking, size badges, and handoff lines

**Files:**
- Create: `internal/classify/rank.go`
- Test: `internal/classify/rank_test.go`

**Interfaces:**
- Consumes: Task 3 `Bucket`, `Record`.
- Produces:
  - `type Rankable struct { Bucket Bucket; MilestoneDue *time.Time; FirstTime bool; WaitingSince *time.Time; Number int; Repo string }`
  - `func RankKey(rec Record, bucket Bucket, waitingSince *time.Time, repo string) Rankable`
  - `func Compare(a, b Rankable) int` (§5.8 order)
  - `func IsFirstTime(association string) bool`; `func SizeBadge(additions, deletions int) string`
  - `func Handoff(b Bucket, urls []string) string` (§5.7)

- [ ] **Step 1: Write the failing tests**

Create `internal/classify/rank_test.go`:

```go
package classify

import (
	"slices"
	"testing"
	"time"
)

func TestCompareOrdersBySpec(t *testing.T) {
	soon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	later := soon.Add(72 * time.Hour)
	old := soon.Add(-96 * time.Hour)
	recent := soon.Add(-24 * time.Hour)
	items := []Rankable{
		{Bucket: NeedsInitialReview, WaitingSince: &recent, Number: 9, Repo: "o/r"},
		{Bucket: NeedsInitialReview, WaitingSince: &old, Number: 8, Repo: "o/r"},
		{Bucket: NeedsInitialReview, FirstTime: true, WaitingSince: &recent, Number: 7, Repo: "o/r"},
		{Bucket: NeedsInitialReview, MilestoneDue: &later, WaitingSince: &recent, Number: 6, Repo: "o/r"},
		{Bucket: NeedsInitialReview, MilestoneDue: &soon, WaitingSince: &recent, Number: 5, Repo: "o/r"},
		{Bucket: WaitingOnMe, WaitingSince: &recent, Number: 4, Repo: "o/r"},
		{Bucket: NeedsInitialReview, Number: 3, Repo: "o/r"},
		{Bucket: NeedsInitialReview, WaitingSince: &old, Number: 8, Repo: "a/b"},
	}
	slices.SortFunc(items, Compare)
	var got []int
	for _, it := range items {
		got = append(got, it.Number)
	}
	want := []int{4, 5, 6, 7, 8, 8, 9, 3}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if items[4].Repo != "a/b" {
		t.Fatalf("ties should break by repo name, got %s first", items[4].Repo)
	}
}

func TestRankKey(t *testing.T) {
	due := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	since := due.Add(-time.Hour)
	rec := Record{Number: 3, AuthorAssociation: "FIRST_TIMER", Milestone: &Milestone{Title: "v1", DueOn: &due}}
	k := RankKey(rec, WaitingOnMe, &since, "o/r")
	if !k.FirstTime || k.MilestoneDue == nil || !k.MilestoneDue.Equal(due) || k.Number != 3 || k.Repo != "o/r" || k.Bucket != WaitingOnMe {
		t.Fatalf("RankKey = %+v", k)
	}
}

func TestIsFirstTime(t *testing.T) {
	for assoc, want := range map[string]bool{
		"FIRST_TIME_CONTRIBUTOR": true, "FIRST_TIMER": true, "CONTRIBUTOR": false, "MEMBER": false, "": false,
	} {
		if got := IsFirstTime(assoc); got != want {
			t.Errorf("IsFirstTime(%q) = %v", assoc, got)
		}
	}
}

func TestSizeBadge(t *testing.T) {
	cases := []struct {
		add, del int
		want     string
	}{{0, 0, "S"}, {30, 19, "S"}, {40, 10, "M"}, {200, 49, "M"}, {200, 50, "L"}, {999, 0, "L"}, {500, 500, "XL"}}
	for _, tc := range cases {
		if got := SizeBadge(tc.add, tc.del); got != tc.want {
			t.Errorf("SizeBadge(%d, %d) = %s, want %s", tc.add, tc.del, got, tc.want)
		}
	}
}

func TestHandoff(t *testing.T) {
	urls := []string{"https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"}
	want := "/reviewing-pull-requests https://github.com/o/r/pull/1 https://github.com/o/r/pull/2"
	for _, b := range []Bucket{NeedsRereview, NeedsInitialReview, Bot} {
		if got := Handoff(b, urls); got != want {
			t.Errorf("Handoff(%s) = %q", b, got)
		}
	}
	if got := Handoff(WaitingTestApproval, urls); got != "post each command only after reading that PR's diff" {
		t.Errorf("test approval handoff = %q", got)
	}
	if got := Handoff(WaitingOnMe, urls); got != "" {
		t.Errorf("waiting on me handoff = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/classify/ -run 'TestCompare|TestRankKey|TestIsFirstTime|TestSizeBadge|TestHandoff'`
Expected: FAIL to compile (`undefined: Rankable`).

- [ ] **Step 3: Implement ranking**

Create `internal/classify/rank.go`:

```go
package classify

import (
	"cmp"
	"strings"
	"time"
)

// Rankable carries the fields the queue sorts by (§5.8).
type Rankable struct {
	Bucket       Bucket
	MilestoneDue *time.Time
	FirstTime    bool
	WaitingSince *time.Time
	Number       int
	Repo         string
}

// RankKey builds the sort key for one classified PR.
func RankKey(rec Record, bucket Bucket, waitingSince *time.Time, repo string) Rankable {
	k := Rankable{Bucket: bucket, FirstTime: IsFirstTime(rec.AuthorAssociation), WaitingSince: waitingSince, Number: rec.Number, Repo: repo}
	if rec.Milestone != nil {
		k.MilestoneDue = rec.Milestone.DueOn
	}
	return k
}

// Compare orders by bucket precedence, soonest milestone due date, first-time
// contributors, oldest waiting, PR number, then repository for stability.
func Compare(a, b Rankable) int {
	if c := cmp.Compare(a.Bucket.Order(), b.Bucket.Order()); c != 0 {
		return c
	}
	if c := compareNilLast(a.MilestoneDue, b.MilestoneDue); c != 0 {
		return c
	}
	if a.FirstTime != b.FirstTime {
		if a.FirstTime {
			return -1
		}
		return 1
	}
	if c := compareNilLast(a.WaitingSince, b.WaitingSince); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Number, b.Number); c != 0 {
		return c
	}
	return cmp.Compare(a.Repo, b.Repo)
}

func compareNilLast(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

// IsFirstTime reports whether the author association marks a first-time contributor.
func IsFirstTime(association string) bool {
	return association == "FIRST_TIME_CONTRIBUTOR" || association == "FIRST_TIMER"
}

// SizeBadge buckets changed lines: S < 50, M < 250, L < 1000, XL otherwise.
func SizeBadge(additions, deletions int) string {
	switch n := additions + deletions; {
	case n < 50:
		return "S"
	case n < 250:
		return "M"
	case n < 1000:
		return "L"
	default:
		return "XL"
	}
}

// Handoff is the per-repo line shown under a bucket (§5.7), or "".
func Handoff(b Bucket, urls []string) string {
	switch b {
	case NeedsRereview, NeedsInitialReview, Bot:
		return "/reviewing-pull-requests " + strings.Join(urls, " ")
	case WaitingTestApproval:
		return "post each command only after reading that PR's diff"
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/classify/ -v`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

```bash
make fmt lint
git add internal/classify
git commit -S -m "feat(classify): rank queue rows and add handoff lines"
```

---

### Task 6: Read-only GitHub client

**Files:**
- Create: `internal/github/client.go`, `internal/github/errors.go`, `internal/github/api.go`
- Test: `internal/github/client_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `const DefaultAPIURL = "https://api.github.com/"`
  - `type Rate struct { Resource string; Limit, Remaining int; Reset time.Time }`
  - `type Options struct { APIURL, Token, UserAgent string; HTTPClient *http.Client; Logger *slog.Logger; Sleep func(context.Context, time.Duration) error; OnRate func(Rate); OnRequest func(kind string, status int) }`
  - `func New(Options) (*Client, error)`
  - `func (*Client) GraphQL(ctx, op, query string, vars map[string]any, out any) error`
  - `func (*Client) Get(ctx, path string, out any) (http.Header, error)` — `path` is relative, e.g. `repos/o/r/pulls?x=y`
  - `func (*Client) GetPages(ctx, path string, each func(page []byte) error) error`
  - `func (*Client) GetDiff(ctx, path string) ([]byte, error)`
  - Errors: `ErrUnauthorized`, `ErrReadOnly`, `*StatusError{Status int; Message string; RetryAfter time.Duration; RateLimited bool; Reset time.Time}`, `*SSORequiredError{URL string}`, `*OAuthRestrictedError{Org string}`, `*GraphQLError{Messages, Types []string}`, `*TransportError{Err error}`
  - `func Retryable(err error) bool`; `func RateLimitReset(err error) (time.Time, bool)`

- [ ] **Step 1: Write the failing tests**

Create `internal/github/client_test.go`:

```go
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type harness struct {
	client *Client
	server *httptest.Server
	calls  atomic.Int32
	sleeps []time.Duration
	rates  []Rate
	kinds  []string
}

func newHarness(t *testing.T, h http.HandlerFunc) *harness {
	t.Helper()
	hs := &harness{}
	hs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hs.calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(hs.server.Close)
	c, err := New(Options{
		APIURL: hs.server.URL,
		Token:  "secret-token",
		Sleep: func(ctx context.Context, d time.Duration) error {
			hs.sleeps = append(hs.sleeps, d)
			return ctx.Err()
		},
		OnRate:    func(r Rate) { hs.rates = append(hs.rates, r) },
		OnRequest: func(kind string, status int) { hs.kinds = append(hs.kinds, fmt.Sprintf("%s:%d", kind, status)) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs.client = c
	return hs
}

func TestGetSendsHeadersAndReportsRate(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r" || r.URL.Query().Get("x") != "1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("missing API headers: %v", r.Header)
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1790000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		fmt.Fprint(w, `{"full_name":"o/r"}`)
	})
	var out struct {
		FullName string `json:"full_name"`
	}
	if _, err := hs.client.Get(context.Background(), "repos/o/r?x=1", &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.FullName != "o/r" {
		t.Fatalf("decoded %+v", out)
	}
	want := Rate{Resource: "core", Limit: 5000, Remaining: 4999, Reset: time.Unix(1790000000, 0).UTC()}
	if len(hs.rates) != 1 || hs.rates[0] != want {
		t.Fatalf("rates = %+v, want %+v", hs.rates, want)
	}
	if len(hs.kinds) != 1 || hs.kinds[0] != "rest:200" {
		t.Fatalf("request kinds = %v", hs.kinds)
	}
}

func TestGetRejectsInvalidPaths(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, p := range []string{"", "-X", "/repos/o/r", "https://evil.example/x"} {
		if _, err := hs.client.Get(context.Background(), p, nil); err == nil {
			t.Errorf("Get(%q) succeeded", p)
		}
	}
	if hs.calls.Load() != 0 {
		t.Fatal("invalid paths reached the network")
	}
}

func TestGraphQLRefusesWrites(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"viewer":{"login":"me"}}}`)
	})
	for _, q := range []string{
		`mutation { addStar(input:{}) { clientMutationId } }`,
		`subscription { x }`,
		`{ viewer { login } }`,
		`query A { viewer { login } } mutation B { x }`,
	} {
		if err := hs.client.GraphQL(context.Background(), "op", q, nil, &struct{}{}); !errors.Is(err, ErrReadOnly) {
			t.Errorf("GraphQL(%q) err = %v, want ErrReadOnly", q, err)
		}
	}
	if hs.calls.Load() != 0 {
		t.Fatal("write operations reached the network")
	}
	allowed := "# mutation in a comment\nquery Q { search(query:\"mutation\") { issueCount } viewer { login } }"
	var out struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := hs.client.GraphQL(context.Background(), "Q", allowed, map[string]any{"n": 1}, &out); err != nil {
		t.Fatalf("read query refused: %v", err)
	}
	if out.Viewer.Login != "me" {
		t.Fatalf("decoded %+v", out)
	}
}

func TestGraphQLErrors(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		body   string
		check  func(t *testing.T, err error)
	}{
		{"generic errors fail", nil, `{"data":{"x":1},"errors":[{"type":"NOT_FOUND","message":"nope"}]}`,
			func(t *testing.T, err error) {
				var ge *GraphQLError
				if !errors.As(err, &ge) || ge.Messages[0] != "nope" || Retryable(err) {
					t.Fatalf("err = %v", err)
				}
			}},
		{"rate limited is retryable", nil, `{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`,
			func(t *testing.T, err error) {
				if !Retryable(err) {
					t.Fatalf("RATE_LIMITED not retryable: %v", err)
				}
			}},
		{"oauth restriction", nil,
			"{\"errors\":[{\"type\":\"FORBIDDEN\",\"message\":\"Although you appear to have the correct authorization credentials, the `NVIDIA` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited.\"}]}",
			func(t *testing.T, err error) {
				var oe *OAuthRestrictedError
				if !errors.As(err, &oe) || oe.Org != "NVIDIA" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"sso required", map[string]string{"X-GitHub-SSO": "required; url=https://github.com/orgs/x/sso?authorization_request=abc"},
			`{"errors":[{"type":"FORBIDDEN","message":"Resource protected by organization SAML enforcement."}]}`,
			func(t *testing.T, err error) {
				var se *SSORequiredError
				if !errors.As(err, &se) || se.URL != "https://github.com/orgs/x/sso?authorization_request=abc" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"null data fails", nil, `{"data":null}`,
			func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("null data accepted")
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				fmt.Fprint(w, tc.body)
			})
			tc.check(t, hs.client.GraphQL(context.Background(), "Q", "query Q { x }", nil, &struct{}{}))
		})
	}
}

func TestStatusErrorsAreTyped(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		calls  int32
		check  func(t *testing.T, err error)
	}{
		{"401", 401, nil, `{"message":"Bad credentials"}`, 1, func(t *testing.T, err error) {
			if !errors.Is(err, ErrUnauthorized) || Retryable(err) {
				t.Fatalf("err = %v", err)
			}
		}},
		{"403 sso", 403, map[string]string{"X-GitHub-SSO": "required; url=https://github.com/orgs/o/sso"}, `{"message":"SAML"}`, 1,
			func(t *testing.T, err error) {
				var se *SSORequiredError
				if !errors.As(err, &se) || se.URL != "https://github.com/orgs/o/sso" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"403 oauth restriction", 403, nil,
			"{\"message\":\"Although you appear to have the correct authorization credentials, the `acme` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited.\"}", 1,
			func(t *testing.T, err error) {
				var oe *OAuthRestrictedError
				if !errors.As(err, &oe) || oe.Org != "acme" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"404 is final", 404, nil, `{"message":"Not Found"}`, 1, func(t *testing.T, err error) {
			var se *StatusError
			if !errors.As(err, &se) || se.Status != 404 || se.Message != "Not Found" || Retryable(err) {
				t.Fatalf("err = %v", err)
			}
		}},
		{"primary rate limit is not retried in place", 403,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Reset": "1790000000"},
			`{"message":"API rate limit exceeded"}`, 1, func(t *testing.T, err error) {
				reset, ok := RateLimitReset(err)
				if !ok || !reset.Equal(time.Unix(1790000000, 0)) || !Retryable(err) {
					t.Fatalf("err = %v reset = %v", err, reset)
				}
			}},
		{"long retry-after is deferred", 403, map[string]string{"Retry-After": "120"}, `{"message":"secondary"}`, 1,
			func(t *testing.T, err error) {
				if !Retryable(err) {
					t.Fatalf("err = %v", err)
				}
			}},
		{"503 retried three times", 503, nil, `oops`, 3, func(t *testing.T, err error) {
			var se *StatusError
			if !errors.As(err, &se) || se.Status != 503 || !Retryable(err) {
				t.Fatalf("err = %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := hs.client.Get(context.Background(), "repos/o/r", nil)
			tc.check(t, err)
			if got := hs.calls.Load(); got != tc.calls {
				t.Fatalf("calls = %d, want %d", got, tc.calls)
			}
		})
	}
}

func TestRetriesTransientStatusWithBackoff(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := hs.client.Get(context.Background(), "x", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(hs.sleeps) != 2 || hs.sleeps[0] != time.Second || hs.sleeps[1] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [1s 2s]", hs.sleeps)
	}
}

func TestRetryAfterHonored(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := hs.client.Get(context.Background(), "x", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(hs.sleeps) != 1 || hs.sleeps[0] != 7*time.Second {
		t.Fatalf("sleeps = %v, want [7s]", hs.sleeps)
	}
}

func TestTransportErrorRetried(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := hs.client.Get(context.Background(), "x", nil); err != nil {
		t.Fatalf("Get after dropped connection: %v", err)
	}
	if hs.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", hs.calls.Load())
	}
}

func TestCancelledContextStopsRetries(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hs.client.Get(ctx, "x", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGetPages(t *testing.T) {
	var base string
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=2>; rel="next", <%s/items?page=2>; rel="last"`, base, base))
			fmt.Fprint(w, `[1,2]`)
		case "2":
			fmt.Fprint(w, `[3]`)
		}
	})
	base = hs.server.URL
	var pages []string
	err := hs.client.GetPages(context.Background(), "items?per_page=2", func(p []byte) error {
		pages = append(pages, string(p))
		return nil
	})
	if err != nil {
		t.Fatalf("GetPages: %v", err)
	}
	if strings.Join(pages, "|") != "[1,2]|[3]" {
		t.Fatalf("pages = %v", pages)
	}
}

func TestGetPagesRejectsLoopsAndForeignHosts(t *testing.T) {
	var base string
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("foreign") == "1" {
			w.Header().Set("Link", `<https://evil.example/x>; rel="next"`)
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=1>; rel="next"`, base))
		}
		fmt.Fprint(w, `[]`)
	})
	base = hs.server.URL
	noop := func([]byte) error { return nil }
	if err := hs.client.GetPages(context.Background(), "items?page=1", noop); err == nil {
		t.Fatal("repeated next link accepted")
	}
	if err := hs.client.GetPages(context.Background(), "items?foreign=1", noop); err == nil {
		t.Fatal("cross-host next link accepted")
	}
}

func TestGetDiffUsesDiffMediaType(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.diff" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		fmt.Fprint(w, "diff --git a/x b/x\n")
	})
	got, err := hs.client.GetDiff(context.Background(), "repos/o/r/compare/a...b")
	if err != nil || string(got) != "diff --git a/x b/x\n" {
		t.Fatalf("GetDiff = %q, %v", got, err)
	}
	if hs.kinds[0] != "diff:200" {
		t.Fatalf("kind = %v", hs.kinds)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, u := range []string{"ftp://x", "://", "http://"} {
		if _, err := New(Options{APIURL: u}); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/github/`
Expected: FAIL to compile (`undefined: New`).

- [ ] **Step 3: Implement errors**

Create `internal/github/errors.go`:

```go
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized means GitHub rejected the token (HTTP 401).
var ErrUnauthorized = errors.New("github: token rejected")

// ErrReadOnly means a caller attempted a non-read operation.
var ErrReadOnly = errors.New("github: only read operations are allowed")

// StatusError is a non-success HTTP response.
type StatusError struct {
	Status      int
	Message     string
	RetryAfter  time.Duration
	RateLimited bool
	Reset       time.Time
}

func (e *StatusError) Error() string { return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message) }

// SSORequiredError means the token must be authorized for an organization's SAML SSO.
type SSORequiredError struct{ URL string }

func (e *SSORequiredError) Error() string {
	if e.URL == "" {
		return "github: organization SAML SSO requires authorizing this token"
	}
	return "github: organization SAML SSO requires authorizing this token at " + e.URL
}

// OAuthRestrictedError means an organization blocks this OAuth App.
type OAuthRestrictedError struct{ Org string }

func (e *OAuthRestrictedError) Error() string {
	return fmt.Sprintf("github: organization %q restricts OAuth App access; request approval or use a classic token", e.Org)
}

// GraphQLError carries the errors array of a GraphQL response.
type GraphQLError struct {
	Messages []string
	Types    []string
}

func (e *GraphQLError) Error() string { return "github: graphql: " + strings.Join(e.Messages, "; ") }

// TransportError is a network-level failure.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "github: transport: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

var transientStatus = map[int]bool{
	http.StatusRequestTimeout: true, http.StatusTooManyRequests: true, http.StatusInternalServerError: true,
	http.StatusBadGateway: true, http.StatusServiceUnavailable: true, http.StatusGatewayTimeout: true,
}

// retryNow reports whether the client should retry inside the same call.
func retryNow(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.RateLimited:
			return false
		case se.RetryAfter > 0:
			return se.RetryAfter <= maxRetryAfter
		}
		return transientStatus[se.Status]
	}
	return false
}

// Retryable reports whether a failed read may succeed if attempted again later.
func Retryable(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.RateLimited || se.RetryAfter > 0 || transientStatus[se.Status]
	}
	var ge *GraphQLError
	if errors.As(err, &ge) {
		return slices.Contains(ge.Types, "RATE_LIMITED")
	}
	return false
}

// RateLimitReset returns when an exhausted primary rate limit resets.
func RateLimitReset(err error) (time.Time, bool) {
	var se *StatusError
	if errors.As(err, &se) && se.RateLimited {
		return se.Reset, true
	}
	return time.Time{}, false
}

var oauthRestricted = regexp.MustCompile("the `([^`]+)` organization has enabled OAuth App access restrictions")

func statusError(resp *http.Response, body []byte) error {
	msg := apiMessage(body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, msg)
	case http.StatusForbidden:
		if u, ok := ssoRequired(resp.Header); ok {
			return &SSORequiredError{URL: u}
		}
		if m := oauthRestricted.FindStringSubmatch(msg); m != nil {
			return &OAuthRestrictedError{Org: m[1]}
		}
	}
	se := &StatusError{Status: resp.StatusCode, Message: msg}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if d := parseRetryAfter(resp.Header.Get("Retry-After")); d > 0 {
			se.RetryAfter = d
		} else if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			se.RateLimited = true
			se.Reset = resetTime(resp.Header)
		}
	}
	return se
}

func graphQLError(h http.Header, errs []gqlError) error {
	if u, ok := ssoRequired(h); ok {
		return &SSORequiredError{URL: u}
	}
	ge := &GraphQLError{}
	for _, e := range errs {
		if m := oauthRestricted.FindStringSubmatch(e.Message); m != nil {
			return &OAuthRestrictedError{Org: m[1]}
		}
		ge.Messages = append(ge.Messages, e.Message)
		ge.Types = append(ge.Types, e.Type)
	}
	return ge
}

type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func ssoRequired(h http.Header) (string, bool) {
	v := h.Get("X-GitHub-SSO")
	if !strings.HasPrefix(v, "required") {
		return "", false
	}
	if i := strings.Index(v, "url="); i >= 0 {
		return strings.TrimSpace(v[i+len("url="):]), true
	}
	return "", true
}

func apiMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		s = "no response body"
	}
	return s
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func resetTime(h http.Header) time.Time {
	v, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func rateFromHeader(h http.Header) (Rate, bool) {
	limit, errL := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, errR := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if errL != nil || errR != nil {
		return Rate{}, false
	}
	return Rate{Resource: h.Get("X-RateLimit-Resource"), Limit: limit, Remaining: remaining, Reset: resetTime(h)}, true
}
```

- [ ] **Step 4: Implement the request loop**

Create `internal/github/client.go`:

```go
// Package github is a bounded, read-only GitHub API client (§4.1).
package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPIURL is the public GitHub REST and GraphQL base.
const DefaultAPIURL = "https://api.github.com/"

const (
	apiVersion     = "2022-11-28"
	maxAttempts    = 3
	requestTimeout = 30 * time.Second
	maxBackoff     = 5 * time.Second
	maxRetryAfter  = 60 * time.Second
	maxBodyBytes   = 64 << 20
	maxPages       = 100
)

// Rate is the latest rate-limit state GitHub reported for one resource.
type Rate struct {
	Resource  string
	Limit     int
	Remaining int
	Reset     time.Time
}

// Options configures a Client.
type Options struct {
	APIURL     string
	Token      string
	UserAgent  string
	HTTPClient *http.Client
	Logger     *slog.Logger
	Sleep      func(context.Context, time.Duration) error
	OnRate     func(Rate)
	OnRequest  func(kind string, status int)
}

// Client performs read-only GitHub API calls with bounded retries.
type Client struct {
	api       *url.URL
	token     string
	userAgent string
	hc        *http.Client
	log       *slog.Logger
	sleep     func(context.Context, time.Duration) error
	onRate    func(Rate)
	onRequest func(string, int)
}

// New validates options and returns a Client.
func New(o Options) (*Client, error) {
	raw := o.APIURL
	if raw == "" {
		raw = DefaultAPIURL
	}
	if !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("github: invalid API URL %q", o.APIURL)
	}
	c := &Client{
		api: u, token: o.Token, userAgent: o.UserAgent, hc: o.HTTPClient, log: o.Logger,
		sleep: o.Sleep, onRate: o.OnRate, onRequest: o.OnRequest,
	}
	if c.userAgent == "" {
		c.userAgent = "devbox"
	}
	if c.hc == nil {
		c.hc = &http.Client{}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.sleep == nil {
		c.sleep = sleepContext
	}
	return c, nil
}

type request struct {
	kind   string // metrics label: rest, graphql, diff
	label  string // log label: REST path without query, or GraphQL operation
	method string
	path   string
	accept string
	body   []byte
}

type response struct {
	header http.Header
	body   []byte
}

func (c *Client) do(ctx context.Context, rq request) (*response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.once(ctx, rq, attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxAttempts || !retryNow(err) {
			break
		}
		wait := min(time.Duration(1<<(attempt-1))*time.Second, maxBackoff)
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > 0 {
			wait = se.RetryAfter
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, rq request, attempt int) (*response, error) {
	ref, err := url.Parse(rq.path)
	if err != nil {
		return nil, fmt.Errorf("github: bad path %q: %w", rq.label, err)
	}
	u := c.api.ResolveReference(ref)
	if u.Scheme != c.api.Scheme || u.Host != c.api.Host {
		return nil, fmt.Errorf("github: refusing cross-host URL %s", u.Redacted())
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if rq.body != nil {
		body = bytes.NewReader(rq.body)
	}
	req, err := http.NewRequestWithContext(rctx, rq.method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", rq.accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	if rq.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.observe(ctx, rq, attempt, 0, time.Since(start), nil)
		return nil, &TransportError{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Err: err}
	}
	c.observe(ctx, rq, attempt, resp.StatusCode, time.Since(start), resp.Header)
	if len(data) > maxBodyBytes {
		return nil, fmt.Errorf("github: response for %s exceeds %d bytes", rq.label, maxBodyBytes)
	}
	if resp.StatusCode >= 300 {
		return nil, statusError(resp, data)
	}
	return &response{header: resp.Header, body: data}, nil
}

func (c *Client) observe(ctx context.Context, rq request, attempt, status int, d time.Duration, h http.Header) {
	if c.onRequest != nil {
		c.onRequest(rq.kind, status)
	}
	if h != nil && c.onRate != nil {
		if r, ok := rateFromHeader(h); ok {
			c.onRate(r)
		}
	}
	if !c.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{
		slog.String("kind", rq.kind), slog.String("target", rq.label), slog.String("method", rq.method),
		slog.Int("status", status), slog.Duration("duration", d), slog.Int("attempt", attempt),
	}
	if h != nil {
		attrs = append(attrs,
			slog.String("rate_remaining", h.Get("X-RateLimit-Remaining")),
			slog.String("request_id", h.Get("X-GitHub-Request-Id")))
	}
	c.log.LogAttrs(ctx, slog.LevelDebug, "github request", attrs...)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
```

- [ ] **Step 5: Implement the API surface**

Create `internal/github/api.go`:

```go
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var (
	gqlLiterals   = regexp.MustCompile(`"(?:\\.|[^"\\])*"|#[^\n]*`)
	gqlQueryStart = regexp.MustCompile(`^\s*query\b`)
	gqlWrite      = regexp.MustCompile(`\b(mutation|subscription)\b`)
	linkNext      = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

func checkReadOnly(query string) error {
	ops := gqlLiterals.ReplaceAllString(query, "")
	if !gqlQueryStart.MatchString(ops) || gqlWrite.MatchString(ops) {
		return ErrReadOnly
	}
	return nil
}

// GraphQL runs a read-only query and decodes its data into out. A non-empty
// errors array is a failure: partial data is unusable (§4.1).
func (c *Client) GraphQL(ctx context.Context, op, query string, vars map[string]any, out any) error {
	if err := checkReadOnly(query); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{query, vars})
	if err != nil {
		return fmt.Errorf("github: encode %s variables: %w", op, err)
	}
	resp, err := c.do(ctx, request{kind: "graphql", label: op, method: http.MethodPost, path: "graphql", accept: "application/json", body: payload})
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(resp.body, &env); err != nil {
		return fmt.Errorf("github: graphql %s: invalid JSON: %w", op, err)
	}
	if len(env.Errors) > 0 {
		return graphQLError(resp.header, env.Errors)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("github: graphql %s: empty data", op)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("github: graphql %s: decode: %w", op, err)
	}
	return nil
}

// Get performs a REST GET and decodes JSON into out when out is non-nil.
func (c *Client) Get(ctx context.Context, path string, out any) (http.Header, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{kind: "rest", label: stripQuery(path), method: http.MethodGet, path: path, accept: "application/vnd.github+json"})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.body, out); err != nil {
			return nil, fmt.Errorf("github: GET %s: invalid JSON: %w", stripQuery(path), err)
		}
	}
	return resp.header, nil
}

// GetPages follows rel="next" links, passing each raw page body to each.
func (c *Client) GetPages(ctx context.Context, path string, each func(page []byte) error) error {
	if err := checkPath(path); err != nil {
		return err
	}
	seen := map[string]bool{}
	next := path
	for page := 0; next != ""; page++ {
		if page >= maxPages {
			return fmt.Errorf("github: GET %s: more than %d pages", stripQuery(path), maxPages)
		}
		if seen[next] {
			return fmt.Errorf("github: GET %s: pagination repeated a link", stripQuery(path))
		}
		seen[next] = true
		resp, err := c.do(ctx, request{kind: "rest", label: stripQuery(path), method: http.MethodGet, path: next, accept: "application/vnd.github+json"})
		if err != nil {
			return err
		}
		if err := each(resp.body); err != nil {
			return err
		}
		next = ""
		if m := linkNext.FindStringSubmatch(resp.header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return nil
}

// GetDiff fetches a REST resource in diff media type.
func (c *Client) GetDiff(ctx context.Context, path string) ([]byte, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{kind: "diff", label: stripQuery(path), method: http.MethodGet, path: path, accept: "application/vnd.github.diff"})
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.Contains(p, "://") {
		return fmt.Errorf("github: invalid REST path %q", p)
	}
	return nil
}

func stripQuery(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/github/ -v`
Expected: PASS for all tests.

- [ ] **Step 7: Lint and commit**

```bash
make fmt lint
git add internal/github
git commit -S -m "feat(github): add bounded read-only GitHub client"
```

---

### Task 7: Store foundation — schema, accounts, repositories

**Files:**
- Create: `internal/store/db.go`, `internal/store/migrations/0001_init.sql`, `internal/store/accounts.go`, `internal/store/repos.go`, `internal/store/results.go`
- Test: `internal/store/helpers_test.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: Task 2 `rules.Rules`, `rules.Parse`, `(Rules).JSON`, `(Rules).CollectionHash`; Task 3 `classify.Unclassified`, `classify.Closed`.
- Produces:
  - `func Open(ctx, file string, log *slog.Logger) (*DB, error)`; `(*DB).Close() error`
  - `ErrNotFound`, `ErrExists`, `ErrStale`; `const StatusOK = "ok"`, `StatusNeedsAuth = "needs_auth"`
  - `type Account struct { ID int64; Login, Kind string; Scopes []string; Status, StatusDetail string; CreatedAt time.Time; VerifiedAt *time.Time }`
  - `(*DB) UpsertAccount(ctx, login, kind string, scopes []string, now time.Time) (Account, error)`, `SetAccountStatus(ctx, login, status, detail string) error`, `DeleteAccount(ctx, login string) error`, `Account(ctx, login string) (Account, error)`, `Accounts(ctx) ([]Account, error)`
  - `type Repo struct { ID, AccountID, Revision int64; Account, Owner, Name string; Enabled bool; Rules rules.Rules; AddedAt time.Time }`; `(Repo) FullName() string`; `func PullURL(owner, name string, number int) string`
  - `(*DB) AddRepo(ctx, accountID int64, owner, name string, rl rules.Rules, now time.Time) (Repo, error)`, `Repo(ctx, id int64) (Repo, error)`, `Repos(ctx) ([]Repo, error)`, `ReposByName(ctx, owner, name string) ([]Repo, error)`, `SetRepoEnabled(ctx, id int64, enabled bool) (Repo, error)`, `DeleteRepo(ctx, id int64) error`
  - `type RulesUpdate struct { Repo Repo; CollectionChanged bool; Transitions []Transition }`; `(*DB) UpdateRepoRules(ctx, id int64, rl rules.Rules, now time.Time) (RulesUpdate, error)`
  - `type ResultRow struct { Bucket, Reason, Action string; WaitingSince *time.Time; Also, Notes []string }`
  - `type Transition struct { ID, RepoID int64; Number int; At time.Time; From *string; To, Reason string }`; `(*DB) Transitions(ctx, repoID int64, number int) ([]Transition, error)`
  - Unexported for Task 8: `inTx`, `writeResult`, `readResult`, `insertTransition`, `ms`, `fromMS`, `nullTime`, `timePtr`, `scanner`, `affected`.

- [ ] **Step 1: Write the schema**

Create `internal/store/migrations/0001_init.sql`:

```sql
CREATE TABLE accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  login TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('oauth', 'pat')),
  scopes TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('ok', 'needs_auth')),
  status_detail TEXT,
  created_at INTEGER NOT NULL,
  verified_at INTEGER
);

CREATE TABLE repos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  revision INTEGER NOT NULL DEFAULT 0,
  account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
  owner TEXT NOT NULL COLLATE NOCASE,
  name TEXT NOT NULL COLLATE NOCASE,
  enabled INTEGER NOT NULL DEFAULT 1,
  rules TEXT NOT NULL DEFAULT '{}',
  added_at INTEGER NOT NULL,
  UNIQUE (account_id, owner, name)
);

CREATE TABLE repo_sync (
  repo_id INTEGER PRIMARY KEY REFERENCES repos (id) ON DELETE CASCADE,
  last_poll_at INTEGER,
  last_success_at INTEGER,
  last_error TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE prs (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  url TEXT NOT NULL,
  author TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('open', 'closed')),
  revision INTEGER NOT NULL DEFAULT 0,
  fingerprint TEXT,
  head_oid TEXT,
  head_changed_at INTEGER,
  record TEXT,
  record_at INTEGER,
  last_enrich_attempt_at INTEGER,
  enrich_failures INTEGER NOT NULL DEFAULT 0,
  retry_at INTEGER,
  error TEXT,
  first_seen_at INTEGER NOT NULL,
  closed_at INTEGER,
  PRIMARY KEY (repo_id, number)
);
CREATE INDEX prs_retry_at ON prs (retry_at) WHERE retry_at IS NOT NULL;

CREATE TABLE results (
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  bucket TEXT NOT NULL,
  reason TEXT NOT NULL,
  action TEXT NOT NULL,
  waiting_since INTEGER,
  also TEXT NOT NULL DEFAULT '[]',
  notes TEXT NOT NULL DEFAULT '[]',
  classified_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, number),
  FOREIGN KEY (repo_id, number) REFERENCES prs (repo_id, number) ON DELETE CASCADE
);

CREATE TABLE transitions (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  at INTEGER NOT NULL,
  from_bucket TEXT,
  to_bucket TEXT NOT NULL,
  reason TEXT NOT NULL,
  FOREIGN KEY (repo_id, number) REFERENCES prs (repo_id, number) ON DELETE CASCADE
);
CREATE INDEX transitions_pr ON transitions (repo_id, number, at);
CREATE INDEX transitions_at ON transitions (at);

CREATE TABLE cache_diff (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  base_oid TEXT NOT NULL,
  reviewed_oid TEXT NOT NULL,
  head_oid TEXT NOT NULL,
  changed INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, base_oid, reviewed_oid, head_oid)
);
CREATE INDEX cache_diff_created_at ON cache_diff (created_at);

CREATE TABLE cache_comment (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  comment_id INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  is_command INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, comment_id, updated_at)
);
CREATE INDEX cache_comment_created_at ON cache_comment (created_at);

CREATE TABLE cache_policy (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  base_ref TEXT NOT NULL,
  policy TEXT NOT NULL CHECK (policy IN ('required', 'absent', 'unavailable')),
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, base_ref)
);
```

Note: `owner` and `name` use `COLLATE NOCASE` because GitHub names are case-insensitive; this keeps `NVIDIA/aicr` and `nvidia/AICR` from becoming two repos under one account.

- [ ] **Step 2: Add the sqlite dependency**

```bash
go get modernc.org/sqlite@v1.60.1
```

- [ ] **Step 3: Write the failing tests**

Create `internal/store/helpers_test.go`:

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "devbox.db"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustAccount(t *testing.T, db *DB, login string) Account {
	t.Helper()
	a, err := db.UpsertAccount(context.Background(), login, "oauth", []string{"repo", "read:org"}, t0)
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	return a
}

func mustRepo(t *testing.T, db *DB, a Account, owner, name string) Repo {
	t.Helper()
	r, err := db.AddRepo(context.Background(), a.ID, owner, name, rules.Default(), t0)
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	return r
}
```

Create `internal/store/store_test.go`:

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thingzio/devbox/internal/rules"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "nested", "devbox.db")
	db, err := Open(ctx, file, nil)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = db.Close()
	db, err = Open(ctx, file, nil)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations rows = %d, err = %v", n, err)
	}
	var fk int
	if err := db.w.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", fk, err)
	}
}

func TestOpenRejectsQueryCharacters(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "bad?.db"), nil); err == nil {
		t.Fatal("path with '?' accepted")
	}
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "zed")
	if a.Status != StatusOK || !slices.Equal(a.Scopes, []string{"repo", "read:org"}) || a.VerifiedAt == nil {
		t.Fatalf("account = %+v", a)
	}
	if err := db.SetAccountStatus(ctx, "zed", StatusNeedsAuth, "token rejected"); err != nil {
		t.Fatalf("SetAccountStatus: %v", err)
	}
	got, _ := db.Account(ctx, "zed")
	if got.Status != StatusNeedsAuth || got.StatusDetail != "token rejected" {
		t.Fatalf("after status change: %+v", got)
	}
	again, err := db.UpsertAccount(ctx, "zed", "pat", []string{"repo"}, at(5))
	if err != nil || again.ID != a.ID || again.Status != StatusOK || again.StatusDetail != "" || again.Kind != "pat" {
		t.Fatalf("re-upsert = %+v, %v", again, err)
	}
	mustAccount(t, db, "amy")
	all, _ := db.Accounts(ctx)
	if len(all) != 2 || all[0].Login != "amy" {
		t.Fatalf("Accounts = %+v", all)
	}
	if _, err := db.Account(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account err = %v", err)
	}
	if err := db.SetAccountStatus(ctx, "nobody", StatusOK, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing status err = %v", err)
	}
}

func TestAddRepo(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "NVIDIA", "aicr")
	if r.Account != "me" || r.Revision != 0 || !r.Enabled || r.Rules.StaleDays != rules.DefaultStaleDays || r.FullName() != "NVIDIA/aicr" {
		t.Fatalf("repo = %+v", r)
	}
	if _, err := db.AddRepo(ctx, a.ID, "nvidia", "AICR", rules.Default(), t0); !errors.Is(err, ErrExists) {
		t.Fatalf("case-insensitive duplicate err = %v", err)
	}
	b := mustAccount(t, db, "work")
	if _, err := db.AddRepo(ctx, b.ID, "NVIDIA", "aicr", rules.Default(), t0); err != nil {
		t.Fatalf("same repo under another account: %v", err)
	}
	byName, _ := db.ReposByName(ctx, "nvidia", "aicr")
	if len(byName) != 2 {
		t.Fatalf("ReposByName = %+v", byName)
	}
	if PullURL("NVIDIA", "aicr", 12) != "https://github.com/NVIDIA/aicr/pull/12" {
		t.Fatalf("PullURL = %s", PullURL("NVIDIA", "aicr", 12))
	}
}

func TestRepoIDsNeverReused(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	first := mustRepo(t, db, a, "o", "r")
	if err := db.DeleteRepo(ctx, first.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	second := mustRepo(t, db, a, "o", "r")
	if second.ID <= first.ID {
		t.Fatalf("repo id reused: %d then %d", first.ID, second.ID)
	}
	if err := db.DeleteRepo(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
}

func TestSetRepoEnabledBumpsRevision(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	off, err := db.SetRepoEnabled(ctx, r.ID, false)
	if err != nil || off.Enabled || off.Revision != r.Revision+1 {
		t.Fatalf("disable = %+v, %v", off, err)
	}
	on, _ := db.SetRepoEnabled(ctx, r.ID, true)
	if !on.Enabled || on.Revision != r.Revision+2 {
		t.Fatalf("enable = %+v", on)
	}
}

func TestDeleteAccountCascades(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	mustRepo(t, db, a, "o", "r")
	if err := db.DeleteAccount(ctx, "me"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if repos, _ := db.Repos(ctx); len(repos) != 0 {
		t.Fatalf("repos survived account deletion: %+v", repos)
	}
}

func TestUpdateRepoRulesWithoutPRs(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	staleOnly := rules.Default()
	staleOnly.StaleDays = 14
	upd, err := db.UpdateRepoRules(ctx, r.ID, staleOnly, at(1))
	if err != nil || upd.CollectionChanged || upd.Repo.Revision != 1 || upd.Repo.Rules.StaleDays != 14 {
		t.Fatalf("stale-only update = %+v, %v", upd, err)
	}
	collect := staleOnly
	collect.TestApproval = rules.TestApproval{MarkerChecks: []string{"tests"}, Command: "/ok-to-test"}
	upd, err = db.UpdateRepoRules(ctx, r.ID, collect, at(2))
	if err != nil || !upd.CollectionChanged || upd.Repo.Revision != 2 {
		t.Fatalf("collection update = %+v, %v", upd, err)
	}
	if _, err := db.UpdateRepoRules(ctx, 999, collect, at(3)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing repo err = %v", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/store/`
Expected: FAIL to compile (`undefined: Open`).

- [ ] **Step 5: Implement the database core**

Create `internal/store/db.go`:

```go
// Package store persists devbox state in SQLite (§6).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// Errors returned by store operations.
var (
	ErrNotFound = errors.New("store: not found")
	ErrExists   = errors.New("store: already exists")
	ErrStale    = errors.New("store: stale revision")
)

// DB is the devbox database: one writer connection and a small reader pool.
type DB struct {
	w   *sql.DB
	r   *sql.DB
	log *slog.Logger
}

// Open creates the database directory, opens SQLite in WAL mode, and applies migrations.
func Open(ctx context.Context, file string, log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("store: resolve %q: %w", file, err)
	}
	if strings.ContainsAny(abs, "?#") {
		return nil, fmt.Errorf("store: database path %q must not contain '?' or '#'", abs)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: create directory: %w", err)
	}
	w, err := sql.Open("sqlite", dsn(abs, "immediate"))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", dsn(abs, ""))
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	r.SetMaxOpenConns(4)
	db := &DB{w: w, r: r, log: log}
	if err := db.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func dsn(file, txlock string) string {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	if txlock != "" {
		q.Set("_txlock", txlock)
	}
	return "file:" + file + "?" + q.Encode()
}

// Close closes both connection pools.
func (db *DB) Close() error { return errors.Join(db.r.Close(), db.w.Close()) }

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: list migrations: %w", err)
	}
	sort.Strings(names)
	var done []int
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		for _, name := range names {
			v, err := strconv.Atoi(strings.SplitN(path.Base(name), "_", 2)[0])
			if err != nil {
				return fmt.Errorf("store: migration %s has no numeric prefix", name)
			}
			if applied[v] {
				continue
			}
			body, err := migrations.ReadFile(name)
			if err != nil {
				return fmt.Errorf("store: read migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("store: apply migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, ms(time.Now())); err != nil {
				return fmt.Errorf("store: record migration %s: %w", name, err)
			}
			done = append(done, v)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, v := range done {
		db.log.Info("migration applied", "version", v)
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := db.w.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func (db *DB) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullTime(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

func affected(res sql.Result) int64 {
	n, _ := res.RowsAffected() // SQLite always reports affected rows
	return n
}
```

- [ ] **Step 6: Implement accounts**

Create `internal/store/accounts.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Account statuses.
const (
	StatusOK        = "ok"
	StatusNeedsAuth = "needs_auth"
)

// Account is a GitHub identity whose token lives in the OS keychain.
type Account struct {
	ID           int64
	Login        string
	Kind         string
	Scopes       []string
	Status       string
	StatusDetail string
	CreatedAt    time.Time
	VerifiedAt   *time.Time
}

const accountCols = `id, login, kind, scopes, status, COALESCE(status_detail, ''), created_at, verified_at`

func scanAccount(sc scanner) (Account, error) {
	var a Account
	var scopes string
	var created int64
	var verified sql.NullInt64
	if err := sc.Scan(&a.ID, &a.Login, &a.Kind, &scopes, &a.Status, &a.StatusDetail, &created, &verified); err != nil {
		return Account{}, err
	}
	for _, s := range strings.Split(scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			a.Scopes = append(a.Scopes, s)
		}
	}
	a.CreatedAt = fromMS(created)
	a.VerifiedAt = timePtr(verified)
	return a, nil
}

// UpsertAccount records a verified account; re-verification restores status ok.
func (db *DB) UpsertAccount(ctx context.Context, login, kind string, scopes []string, now time.Time) (Account, error) {
	row := db.w.QueryRowContext(ctx, `INSERT INTO accounts (login, kind, scopes, status, created_at, verified_at)
VALUES (?, ?, ?, 'ok', ?, ?)
ON CONFLICT (login) DO UPDATE SET kind = excluded.kind, scopes = excluded.scopes, status = 'ok',
  status_detail = NULL, verified_at = excluded.verified_at
RETURNING `+accountCols, login, kind, strings.Join(scopes, ","), ms(now), ms(now))
	a, err := scanAccount(row)
	if err != nil {
		return Account{}, fmt.Errorf("store: upsert account %s: %w", login, err)
	}
	return a, nil
}

// SetAccountStatus marks an account ok or needs_auth with a detail message.
func (db *DB) SetAccountStatus(ctx context.Context, login, status, detail string) error {
	res, err := db.w.ExecContext(ctx, `UPDATE accounts SET status = ?, status_detail = NULLIF(?, '') WHERE login = ?`, status, detail, login)
	if err != nil {
		return fmt.Errorf("store: set account status: %w", err)
	}
	if affected(res) == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAccount removes an account and, by cascade, its repositories and their data.
func (db *DB) DeleteAccount(ctx context.Context, login string) error {
	res, err := db.w.ExecContext(ctx, `DELETE FROM accounts WHERE login = ?`, login)
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	if affected(res) == 0 {
		return ErrNotFound
	}
	return nil
}

// Account returns one account by login.
func (db *DB) Account(ctx context.Context, login string) (Account, error) {
	a, err := scanAccount(db.r.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE login = ?`, login))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account %s: %w", login, err)
	}
	return a, nil
}

// Accounts lists accounts ordered by login.
func (db *DB) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY login`)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
```

- [ ] **Step 7: Implement results and transitions helpers**

Create `internal/store/results.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ResultRow is a stored classification result.
type ResultRow struct {
	Bucket       string
	Reason       string
	Action       string
	WaitingSince *time.Time
	Also         []string
	Notes        []string
}

// Transition is one bucket change in the append-only log (§6).
type Transition struct {
	ID     int64
	RepoID int64
	Number int
	At     time.Time
	From   *string
	To     string
	Reason string
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func encodeList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(items) // string slices always marshal
	return string(b)
}

func decodeList(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out) // written by encodeList; malformed rows read as empty
	return out
}

func readResult(ctx context.Context, q rowQuerier, repoID int64, number int) (*ResultRow, error) {
	var r ResultRow
	var since sql.NullInt64
	var also, notes string
	err := q.QueryRowContext(ctx, `SELECT bucket, reason, action, waiting_since, also, notes FROM results WHERE repo_id = ? AND number = ?`,
		repoID, number).Scan(&r.Bucket, &r.Reason, &r.Action, &since, &also, &notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read result: %w", err)
	}
	r.WaitingSince = timePtr(since)
	r.Also, r.Notes = decodeList(also), decodeList(notes)
	return &r, nil
}

func sameResult(a, b ResultRow) bool {
	sameSince := (a.WaitingSince == nil) == (b.WaitingSince == nil) &&
		(a.WaitingSince == nil || ms(*a.WaitingSince) == ms(*b.WaitingSince))
	return a.Bucket == b.Bucket && a.Reason == b.Reason && a.Action == b.Action && sameSince &&
		slices.Equal(decodeList(encodeList(a.Also)), decodeList(encodeList(b.Also))) &&
		slices.Equal(decodeList(encodeList(a.Notes)), decodeList(encodeList(b.Notes)))
}

// writeResult upserts the result and appends a transition when the bucket
// differs from the stored result, or from the last logged bucket when no
// result row exists (first classification or reopen).
func writeResult(ctx context.Context, tx *sql.Tx, repoID int64, number int, res ResultRow, now time.Time) (*Transition, error) {
	var prev sql.NullString
	cur, err := readResult(ctx, tx, repoID, number)
	if err != nil {
		return nil, err
	}
	if cur != nil {
		prev = sql.NullString{String: cur.Bucket, Valid: true}
	} else {
		err := tx.QueryRowContext(ctx, `SELECT to_bucket FROM transitions WHERE repo_id = ? AND number = ? ORDER BY id DESC LIMIT 1`,
			repoID, number).Scan(&prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: read last transition: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO results (repo_id, number, bucket, reason, action, waiting_since, also, notes, classified_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, number) DO UPDATE SET bucket = excluded.bucket, reason = excluded.reason, action = excluded.action,
  waiting_since = excluded.waiting_since, also = excluded.also, notes = excluded.notes, classified_at = excluded.classified_at`,
		repoID, number, res.Bucket, res.Reason, res.Action, nullTime(res.WaitingSince), encodeList(res.Also), encodeList(res.Notes), ms(now))
	if err != nil {
		return nil, fmt.Errorf("store: write result: %w", err)
	}
	if prev.Valid && prev.String == res.Bucket {
		return nil, nil
	}
	tr := &Transition{RepoID: repoID, Number: number, At: now, To: res.Bucket, Reason: res.Reason}
	if prev.Valid {
		from := prev.String
		tr.From = &from
	}
	if err := insertTransition(ctx, tx, tr); err != nil {
		return nil, err
	}
	return tr, nil
}

func insertTransition(ctx context.Context, tx *sql.Tx, tr *Transition) error {
	var from sql.NullString
	if tr.From != nil {
		from = sql.NullString{String: *tr.From, Valid: true}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO transitions (repo_id, number, at, from_bucket, to_bucket, reason) VALUES (?, ?, ?, ?, ?, ?)`,
		tr.RepoID, tr.Number, ms(tr.At), from, tr.To, tr.Reason)
	if err != nil {
		return fmt.Errorf("store: insert transition: %w", err)
	}
	tr.ID, err = res.LastInsertId()
	return err
}

// Transitions returns one PR's transition history, oldest first.
func (db *DB) Transitions(ctx context.Context, repoID int64, number int) ([]Transition, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id, at, from_bucket, to_bucket, reason FROM transitions WHERE repo_id = ? AND number = ? ORDER BY id`,
		repoID, number)
	if err != nil {
		return nil, fmt.Errorf("store: transitions: %w", err)
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		tr := Transition{RepoID: repoID, Number: number}
		var atMS int64
		var from sql.NullString
		if err := rows.Scan(&tr.ID, &atMS, &from, &tr.To, &tr.Reason); err != nil {
			return nil, err
		}
		tr.At = fromMS(atMS)
		if from.Valid {
			f := from.String
			tr.From = &f
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}
```

- [ ] **Step 8: Implement repositories and rules edits**

Create `internal/store/repos.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/rules"
)

// Repo is a monitored repository owned by one account (§2 D4).
type Repo struct {
	ID        int64
	AccountID int64
	Revision  int64
	Account   string
	Owner     string
	Name      string
	Enabled   bool
	Rules     rules.Rules
	AddedAt   time.Time
}

// FullName is owner/name.
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// PullURL is the canonical GitHub URL of a pull request.
func PullURL(owner, name string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, name, number)
}

// RulesUpdate describes the effect of a rules edit.
type RulesUpdate struct {
	Repo              Repo
	CollectionChanged bool
	Transitions       []Transition
}

const repoSelect = `SELECT r.id, r.account_id, r.revision, a.login, r.owner, r.name, r.enabled, r.rules, r.added_at
FROM repos r JOIN accounts a ON a.id = r.account_id`

func scanRepo(sc scanner) (Repo, error) {
	var r Repo
	var raw string
	var added int64
	if err := sc.Scan(&r.ID, &r.AccountID, &r.Revision, &r.Account, &r.Owner, &r.Name, &r.Enabled, &raw, &added); err != nil {
		return Repo{}, err
	}
	rl, err := rules.Parse([]byte(raw))
	if err != nil {
		return Repo{}, fmt.Errorf("store: repo %d rules: %w", r.ID, err)
	}
	r.Rules = rl
	r.AddedAt = fromMS(added)
	return r, nil
}

// AddRepo starts monitoring owner/name for an account.
func (db *DB) AddRepo(ctx context.Context, accountID int64, owner, name string, rl rules.Rules, now time.Time) (Repo, error) {
	var id int64
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO repos (account_id, owner, name, rules, added_at) VALUES (?, ?, ?, ?, ?)`,
			accountID, owner, name, rl.JSON(), ms(now))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrExists
			}
			return fmt.Errorf("store: add repo: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO repo_sync (repo_id) VALUES (?)`, id)
		return err
	})
	if err != nil {
		return Repo{}, err
	}
	return db.Repo(ctx, id)
}

// Repo returns one repository.
func (db *DB) Repo(ctx context.Context, id int64) (Repo, error) {
	r, err := scanRepo(db.r.QueryRowContext(ctx, repoSelect+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	return r, err
}

// Repos lists all repositories ordered by name and account.
func (db *DB) Repos(ctx context.Context) ([]Repo, error) {
	return db.queryRepos(ctx, repoSelect+` ORDER BY r.owner, r.name, a.login`)
}

// ReposByName lists every account's entry for owner/name (case-insensitive).
func (db *DB) ReposByName(ctx context.Context, owner, name string) ([]Repo, error) {
	return db.queryRepos(ctx, repoSelect+` WHERE r.owner = ? AND r.name = ? ORDER BY a.login`, owner, name)
}

func (db *DB) queryRepos(ctx context.Context, query string, args ...any) ([]Repo, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list repos: %w", err)
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRepoEnabled toggles monitoring and advances the repo revision (§7).
func (db *DB) SetRepoEnabled(ctx context.Context, id int64, enabled bool) (Repo, error) {
	res, err := db.w.ExecContext(ctx, `UPDATE repos SET enabled = ?, revision = revision + 1 WHERE id = ?`, enabled, id)
	if err != nil {
		return Repo{}, fmt.Errorf("store: set repo enabled: %w", err)
	}
	if affected(res) == 0 {
		return Repo{}, ErrNotFound
	}
	return db.Repo(ctx, id)
}

// DeleteRepo removes a repository and, by cascade, its PRs, results, transitions, and caches.
func (db *DB) DeleteRepo(ctx context.Context, id int64) error {
	res, err := db.w.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete repo: %w", err)
	}
	if affected(res) == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateRepoRules stores new rules and advances the repo revision. When the
// collection settings change, every open PR is invalidated and its result is
// replaced by an unclassified refresh placeholder in the same transaction (§6).
func (db *DB) UpdateRepoRules(ctx context.Context, id int64, rl rules.Rules, now time.Time) (RulesUpdate, error) {
	var upd RulesUpdate
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT rules FROM repos WHERE id = ?`, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: read rules: %w", err)
		}
		old, err := rules.Parse([]byte(raw))
		if err != nil {
			return fmt.Errorf("store: stored rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE repos SET rules = ?, revision = revision + 1 WHERE id = ?`, rl.JSON(), id); err != nil {
			return fmt.Errorf("store: update rules: %w", err)
		}
		upd.CollectionChanged = old.CollectionHash() != rl.CollectionHash()
		if !upd.CollectionChanged {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE prs SET revision = revision + 1, retry_at = ? WHERE repo_id = ? AND state = 'open'`, ms(now), id); err != nil {
			return fmt.Errorf("store: invalidate prs: %w", err)
		}
		numbers, err := openResultNumbers(ctx, tx, id)
		if err != nil {
			return err
		}
		refresh := ResultRow{Bucket: string(classify.Unclassified), Reason: "collection rules changed; refreshing evidence"}
		for _, n := range numbers {
			tr, err := writeResult(ctx, tx, id, n, refresh, now)
			if err != nil {
				return err
			}
			if tr != nil {
				upd.Transitions = append(upd.Transitions, *tr)
			}
		}
		return nil
	})
	if err != nil {
		return RulesUpdate{}, err
	}
	upd.Repo, err = db.Repo(ctx, id)
	return upd, err
}

func openResultNumbers(ctx context.Context, tx *sql.Tx, repoID int64) ([]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.number FROM results r JOIN prs p ON p.repo_id = r.repo_id AND p.number = r.number
WHERE r.repo_id = ? AND p.state = 'open' ORDER BY r.number`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: list results: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
```

- [ ] **Step 9: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 10: Lint and commit**

```bash
go mod tidy
make fmt lint
git add go.mod go.sum internal/store
git commit -S -m "feat(store): add SQLite schema, accounts, and repositories"
```

---

### Task 8: Store PR lifecycle — polls, commits, caches, views

**Files:**
- Create: `internal/store/prs.go`, `internal/store/caches.go`, `internal/store/views.go`
- Test: `internal/store/prs_test.go`, `internal/store/views_test.go`

**Interfaces:**
- Consumes: Task 7 store internals; Task 3 `classify.Closed`.
- Produces:
  - `type PRState struct { Number int; State string; Revision int64; Fingerprint, HeadOID string; HasRecord bool; RecordVersion int; RulesHash string; RecordAt, LastEnrichAttemptAt, RetryAt *time.Time; Error string }`; `(*DB) PRStates(ctx, repoID int64) ([]PRState, error)`
  - `type Observation struct { Number int; Fingerprint, HeadOID string; Exists bool; ExpectedRevision int64; Invalidate bool }`
  - `type PollApply struct { RepoID, RepoRevision int64; Owner, Name string; Now time.Time; Upserts []Observation; Closes []int }`; `type PollApplied struct { Inserted, Updated, Skipped []int; Closed []Transition }`; `(*DB) ApplyPoll(ctx, PollApply) (PollApplied, error)`
  - `type RepoSync struct { RepoID int64; LastPollAt, LastSuccessAt *time.Time; LastError string; ConsecutiveFailures int }`; `(*DB) RecordPoll(ctx, repoID int64, now time.Time, pollErr error) (RepoSync, error)`
  - `type Work struct { RepoID, RepoRevision int64; Account, Owner, Name string; Rules rules.Rules; Number int; PRRevision int64; Fingerprint string; HeadChangedAt, RecordAt *time.Time; Record []byte; EnrichFailures int }`; `(*DB) DueWork(ctx, now time.Time, limit int) ([]Work, error)`
  - `type EnrichCommit struct { RepoID, RepoRevision int64; Number int; PRRevision int64; Now time.Time; Record []byte; AdvanceRecordAt bool; Title, URL, Author, Error string; EnrichFailures int; RetryAt *time.Time; Result ResultRow; Caches Caches }`; `(*DB) CommitEnrichment(ctx, EnrichCommit) (*Transition, error)`
  - `type DetailObservation struct { RepoID, RepoRevision int64; Number int; PRRevision int64; Now time.Time; Fingerprint, HeadOID string }`; `(*DB) CommitObservation(ctx, DetailObservation) error`
  - `type Classifiable struct { RepoID, RepoRevision int64; Account string; Rules rules.Rules; Number int; PRRevision int64; Record []byte }`; `(*DB) Reclassifiable(ctx, repoID int64) ([]Classifiable, error)` (0 = all); `(*DB) CommitResult(ctx, repoID, repoRev int64, number int, prRev int64, res ResultRow, now time.Time) (*Transition, bool, error)`
  - `type DiffCache struct { BaseOID, ReviewedOID, HeadOID string; Changed bool }`, `type CommentCache struct { CommentID int64; UpdatedAt time.Time; IsCommand bool }`, `type PolicyCache struct { BaseRef, Policy string; FetchedAt time.Time }`, `type Caches struct { Diffs []DiffCache; Comments []CommentCache; Policies []PolicyCache }`
  - `(*DB) CachedDiff(ctx, repoID int64, base, reviewed, head string) (changed, ok bool, err error)`, `CachedComment(ctx, repoID, commentID int64, updatedAt time.Time) (isCommand, ok bool, err error)`, `CachedPolicy(ctx, repoID int64, baseRef string, now time.Time, ttl time.Duration) (string, bool, error)`, `Purge(ctx, now time.Time) (int64, error)`
  - `type QueueFilter struct { Owner, Name, Account, Bucket string }`; `type QueueRow struct { RepoID int64; Owner, Name, Account string; Number int; Title, URL, Author string; Result ResultRow; Record []byte }`; `(*DB) Queue(ctx, QueueFilter) ([]QueueRow, error)`
  - `type PRDetail struct { Repo Repo; Number int; Title, URL, Author, State string; Revision int64; Fingerprint, HeadOID string; HeadChangedAt, RecordAt, LastEnrichAttemptAt, RetryAt, ClosedAt *time.Time; FirstSeenAt time.Time; Record []byte; EnrichFailures int; Error string; Result *ResultRow }`; `(*DB) PRDetails(ctx, owner, name string, number int) ([]PRDetail, error)`
  - `type RepoStatus struct { Repo Repo; Sync RepoSync; AccountStatus, AccountDetail string; Open, Pending, Errored, Retrying int; NextRetryAt, LastAttemptAt *time.Time }`; `(*DB) RepoStatuses(ctx) ([]RepoStatus, error)`
  - `type BucketCount struct { Repo, Bucket string; Count int }`; `(*DB) BucketCounts(ctx) ([]BucketCount, error)` (enabled repos only)

- [ ] **Step 1: Write the failing PR lifecycle tests**

Create `internal/store/prs_test.go`:

```go
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

func observeNew(t *testing.T, db *DB, r Repo, now time.Time, numbers ...int) {
	t.Helper()
	var ups []Observation
	for _, n := range numbers {
		ups = append(ups, Observation{Number: n, Fingerprint: "fp1", HeadOID: "head1"})
	}
	if _, err := db.ApplyPoll(context.Background(), PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: r.Owner, Name: r.Name, Now: now, Upserts: ups}); err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
}

func prState(t *testing.T, db *DB, repoID int64, number int) PRState {
	t.Helper()
	states, err := db.PRStates(context.Background(), repoID)
	if err != nil {
		t.Fatalf("PRStates: %v", err)
	}
	for _, s := range states {
		if s.Number == number {
			return s
		}
	}
	t.Fatalf("pr %d not found", number)
	return PRState{}
}

func commitBucket(t *testing.T, db *DB, r Repo, number int, bucket string, now time.Time) *Transition {
	t.Helper()
	s := prState(t, db, r.ID, number)
	tr, err := db.CommitEnrichment(context.Background(), EnrichCommit{
		RepoID: r.ID, RepoRevision: r.Revision, Number: number, PRRevision: s.Revision, Now: now,
		Record: []byte(`{"version":1,"collection_rules_hash":"h"}`), AdvanceRecordAt: true,
		Title: "Fix it", Author: "alice", Result: ResultRow{Bucket: bucket, Reason: "because", Action: "act"},
	})
	if err != nil {
		t.Fatalf("CommitEnrichment: %v", err)
	}
	return tr
}

func TestApplyPollInsertsAndTracksHeadChanges(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	s := prState(t, db, r.ID, 7)
	if s.State != "open" || s.Revision != 1 || s.RetryAt == nil || !s.RetryAt.Equal(at(0)) || s.HasRecord {
		t.Fatalf("new pr state = %+v", s)
	}
	details, _ := db.PRDetails(ctx, "o", "r", 7)
	if details[0].URL != "https://github.com/o/r/pull/7" || details[0].HeadChangedAt != nil {
		t.Fatalf("new pr detail = %+v", details[0])
	}
	_, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: "o", Name: "r", Now: at(2),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp2", HeadOID: "head2", Exists: true, ExpectedRevision: 1, Invalidate: true}}})
	if err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
	s = prState(t, db, r.ID, 7)
	details, _ = db.PRDetails(ctx, "o", "r", 7)
	if s.Revision != 2 || s.Fingerprint != "fp2" || s.HeadOID != "head2" || details[0].HeadChangedAt == nil || !details[0].HeadChangedAt.Equal(at(2)) {
		t.Fatalf("after head change: state %+v detail %+v", s, details[0])
	}
	_, err = db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: "o", Name: "r", Now: at(4),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp3", HeadOID: "head2", Exists: true, ExpectedRevision: 2}}})
	if err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
	s = prState(t, db, r.ID, 7)
	if s.Revision != 2 || !s.RetryAt.Equal(at(2)) || s.Fingerprint != "fp3" {
		t.Fatalf("observation without invalidation changed revision or retry: %+v", s)
	}
}

func TestApplyPollGuards(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision + 1, Now: at(1)}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale repo revision err = %v", err)
	}
	applied, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(1),
		Upserts: []Observation{{Number: 7, Fingerprint: "x", HeadOID: "y", Exists: true, ExpectedRevision: 99, Invalidate: true}}})
	if err != nil || len(applied.Skipped) != 1 {
		t.Fatalf("pr revision mismatch applied = %+v, %v", applied, err)
	}
	off, _ := db.SetRepoEnabled(ctx, r.ID, false)
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: off.Revision, Now: at(1)}); !errors.Is(err, ErrStale) {
		t.Fatalf("disabled repo err = %v", err)
	}
}

func TestCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	if tr := commitBucket(t, db, r, 7, "needs_initial_review", at(1)); tr == nil || tr.From != nil || tr.To != "needs_initial_review" {
		t.Fatalf("first classification transition = %+v", tr)
	}
	applied, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(2), Closes: []int{7}})
	if err != nil || len(applied.Closed) != 1 || *applied.Closed[0].From != "needs_initial_review" || applied.Closed[0].To != "closed" {
		t.Fatalf("close applied = %+v, %v", applied, err)
	}
	if rows, _ := db.Queue(ctx, QueueFilter{}); len(rows) != 0 {
		t.Fatalf("closed pr still queued: %+v", rows)
	}
	s := prState(t, db, r.ID, 7)
	if s.State != "closed" || s.RetryAt != nil {
		t.Fatalf("closed state = %+v", s)
	}
	_, err = db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(3),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp9", HeadOID: "head1", Exists: true, ExpectedRevision: s.Revision, Invalidate: true}}})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if tr := commitBucket(t, db, r, 7, "waiting_on_me", at(4)); tr == nil || tr.From == nil || *tr.From != "closed" {
		t.Fatalf("reopen transition = %+v", tr)
	}
}

func TestCommitEnrichmentGuards(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	if tr := commitBucket(t, db, r, 7, "needs_initial_review", at(2)); tr != nil {
		t.Fatalf("same bucket wrote transition %+v", tr)
	}
	s := prState(t, db, r.ID, 7)
	if s.Revision != 3 || s.RetryAt != nil || s.RecordAt == nil || !s.RecordAt.Equal(at(2)) || s.RecordVersion != 1 || s.RulesHash != "h" {
		t.Fatalf("after commits = %+v", s)
	}
	_, err := db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: s.Revision - 1,
		Now: at(3), Record: []byte(`{}`), Result: ResultRow{Bucket: "own"},
		Caches: Caches{Policies: []PolicyCache{{BaseRef: "main", Policy: "required", FetchedAt: at(3)}}}})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale pr revision err = %v", err)
	}
	if _, ok, _ := db.CachedPolicy(ctx, r.ID, "main", at(3), time.Hour); ok {
		t.Fatal("discarded outcome wrote a cache entry")
	}
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(4), Closes: []int{7}}); err != nil {
		t.Fatal(err)
	}
	closed := prState(t, db, r.ID, 7)
	_, err = db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: closed.Revision,
		Now: at(5), Record: []byte(`{}`), Result: ResultRow{Bucket: "own"}})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("commit to closed pr err = %v", err)
	}
}

func TestFatalCommitKeepsRecordAt(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	s := prState(t, db, r.ID, 7)
	retry := at(3)
	_, err := db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: s.Revision,
		Now: at(2), Record: []byte(`{"version":1,"collection_rules_hash":"h","error":"boom"}`), Error: "boom",
		EnrichFailures: 1, RetryAt: &retry, Result: ResultRow{Bucket: "unclassified", Reason: "boom"}})
	if err != nil {
		t.Fatal(err)
	}
	s = prState(t, db, r.ID, 7)
	if !s.RecordAt.Equal(at(1)) || s.LastEnrichAttemptAt == nil || !s.LastEnrichAttemptAt.Equal(at(2)) || s.Error != "boom" || !s.RetryAt.Equal(retry) {
		t.Fatalf("fatal commit state = %+v", s)
	}
}

func TestCommitObservation(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	err := db.CommitObservation(ctx, DetailObservation{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: 1, Now: at(1), Fingerprint: "fpX", HeadOID: "headX"})
	if err != nil {
		t.Fatalf("CommitObservation: %v", err)
	}
	s := prState(t, db, r.ID, 7)
	d, _ := db.PRDetails(ctx, "o", "r", 7)
	if s.Revision != 2 || s.Fingerprint != "fpX" || !s.RetryAt.Equal(at(1)) || d[0].HeadChangedAt == nil || !d[0].HeadChangedAt.Equal(at(1)) {
		t.Fatalf("after detail observation: %+v %+v", s, d[0])
	}
	if err := db.CommitObservation(ctx, DetailObservation{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: 1, Now: at(2)}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale observation err = %v", err)
	}
}

func TestCommitResult(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "waiting_on_author", at(1))
	s := prState(t, db, r.ID, 7)
	same := ResultRow{Bucket: "waiting_on_author", Reason: "because", Action: "act"}
	if tr, changed, err := db.CommitResult(ctx, r.ID, r.Revision, 7, s.Revision, same, at(2)); err != nil || changed || tr != nil {
		t.Fatalf("no-op reclassify = %+v %v %v", tr, changed, err)
	}
	stale := ResultRow{Bucket: "stale_changes_requested", Reason: "8 days", Action: "nudge"}
	tr, changed, err := db.CommitResult(ctx, r.ID, r.Revision, 7, s.Revision, stale, at(3))
	if err != nil || !changed || tr == nil || *tr.From != "waiting_on_author" {
		t.Fatalf("reclassify = %+v %v %v", tr, changed, err)
	}
	if prState(t, db, r.ID, 7).Revision != s.Revision {
		t.Fatal("result-only write advanced the evidence revision")
	}
	if _, _, err := db.CommitResult(ctx, r.ID, r.Revision, 7, s.Revision-1, stale, at(4)); !errors.Is(err, ErrStale) {
		t.Fatalf("stale reclassify err = %v", err)
	}
	items, _ := db.Reclassifiable(ctx, 0)
	if len(items) != 1 || items[0].Number != 7 || items[0].Account != "me" || items[0].PRRevision != s.Revision {
		t.Fatalf("Reclassifiable = %+v", items)
	}
}

func TestDueWork(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "o", "r")
	observeNew(t, db, r, at(0), 7, 8)
	off := mustRepo(t, db, a, "o", "off")
	observeNew(t, db, off, at(0), 1)
	if _, err := db.SetRepoEnabled(ctx, off.ID, false); err != nil {
		t.Fatal(err)
	}
	work, err := db.DueWork(ctx, at(0), 10)
	if err != nil || len(work) != 2 || work[0].Number != 7 || work[0].Account != "me" || work[0].Fingerprint != "fp1" || work[0].PRRevision != 1 {
		t.Fatalf("DueWork = %+v, %v", work, err)
	}
	if work, _ := db.DueWork(ctx, at(0).Add(-time.Minute), 10); len(work) != 0 {
		t.Fatalf("future work returned: %+v", work)
	}
	if err := db.SetAccountStatus(ctx, "me", StatusNeedsAuth, "x"); err != nil {
		t.Fatal(err)
	}
	if work, _ := db.DueWork(ctx, at(0), 10); len(work) != 0 {
		t.Fatalf("needs_auth account work returned: %+v", work)
	}
}

func TestRecordPoll(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	s, err := db.RecordPoll(ctx, r.ID, at(0), errors.New("network down"))
	if err != nil || s.ConsecutiveFailures != 1 || s.LastError != "network down" || s.LastSuccessAt != nil {
		t.Fatalf("failure = %+v, %v", s, err)
	}
	s, _ = db.RecordPoll(ctx, r.ID, at(1), errors.New("still down"))
	if s.ConsecutiveFailures != 2 {
		t.Fatalf("failures = %d", s.ConsecutiveFailures)
	}
	s, _ = db.RecordPoll(ctx, r.ID, at(2), nil)
	if s.ConsecutiveFailures != 0 || s.LastError != "" || !s.LastSuccessAt.Equal(at(2)) {
		t.Fatalf("success = %+v", s)
	}
}

func TestRulesEditInvalidatesOpenPRs(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7, 8)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(2), Closes: []int{8}}); err != nil {
		t.Fatal(err)
	}
	before7, before8 := prState(t, db, r.ID, 7), prState(t, db, r.ID, 8)
	rl := rules.Default()
	rl.TestApproval = rules.TestApproval{MarkerChecks: []string{"tests"}, Command: "/ok-to-test"}
	upd, err := db.UpdateRepoRules(ctx, r.ID, rl, at(3))
	if err != nil || !upd.CollectionChanged || len(upd.Transitions) != 1 || upd.Transitions[0].To != "unclassified" {
		t.Fatalf("UpdateRepoRules = %+v, %v", upd, err)
	}
	after7, after8 := prState(t, db, r.ID, 7), prState(t, db, r.ID, 8)
	if after7.Revision != before7.Revision+1 || !after7.RetryAt.Equal(at(3)) {
		t.Fatalf("open pr not invalidated: %+v", after7)
	}
	if after8.Revision != before8.Revision {
		t.Fatalf("closed pr invalidated: %+v", after8)
	}
	rows, _ := db.Queue(ctx, QueueFilter{})
	if len(rows) != 1 || rows[0].Result.Reason != "collection rules changed; refreshing evidence" {
		t.Fatalf("queue after rules edit = %+v", rows)
	}
}

func TestCachesPurgeAndCascade(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "o", "r")
	observeNew(t, db, r, at(0), 7)
	s := prState(t, db, r.ID, 7)
	_, err := db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: s.Revision, Now: at(1),
		Record: []byte(`{}`), Result: ResultRow{Bucket: "own"}, Caches: Caches{
			Diffs:    []DiffCache{{BaseOID: "b", ReviewedOID: "r", HeadOID: "h", Changed: true}},
			Comments: []CommentCache{{CommentID: 99, UpdatedAt: at(0), IsCommand: true}},
			Policies: []PolicyCache{{BaseRef: "main", Policy: "required", FetchedAt: at(1)}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if changed, ok, _ := db.CachedDiff(ctx, r.ID, "b", "r", "h"); !ok || !changed {
		t.Fatal("diff cache miss")
	}
	if isCmd, ok, _ := db.CachedComment(ctx, r.ID, 99, at(0)); !ok || !isCmd {
		t.Fatal("comment cache miss")
	}
	if _, ok, _ := db.CachedComment(ctx, r.ID, 99, at(5)); ok {
		t.Fatal("edited comment hit stale cache entry")
	}
	if p, ok, _ := db.CachedPolicy(ctx, r.ID, "main", at(30), time.Hour); !ok || p != "required" {
		t.Fatal("policy cache miss inside TTL")
	}
	if _, ok, _ := db.CachedPolicy(ctx, r.ID, "main", at(62), time.Hour); ok {
		t.Fatal("policy cache hit after TTL")
	}
	if n, err := db.Purge(ctx, at(1).Add(31*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
	if err := db.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	readded := mustRepo(t, db, a, "o", "r")
	if _, ok, _ := db.CachedPolicy(ctx, readded.ID, "main", at(30), time.Hour); ok {
		t.Fatal("re-added repo inherited a policy cache entry")
	}
	var n int
	if err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_policy`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cache_policy rows after delete = %d, %v", n, err)
	}
}
```

- [ ] **Step 2: Write the failing view tests**

Create `internal/store/views_test.go`:

```go
package store

import (
	"context"
	"testing"
)

func TestQueueSameRepoTwoAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	me, work := mustAccount(t, db, "me"), mustAccount(t, db, "work")
	r1, r2 := mustRepo(t, db, me, "NVIDIA", "aicr"), mustRepo(t, db, work, "NVIDIA", "aicr")
	observeNew(t, db, r1, at(0), 7)
	observeNew(t, db, r2, at(0), 7)
	commitBucket(t, db, r1, 7, "waiting_on_me", at(1))
	commitBucket(t, db, r2, 7, "needs_initial_review", at(1))
	rows, err := db.Queue(ctx, QueueFilter{Owner: "nvidia", Name: "AICR"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("Queue = %+v, %v", rows, err)
	}
	accounts := map[string]string{}
	for _, row := range rows {
		accounts[row.Account] = row.Result.Bucket
	}
	if accounts["me"] != "waiting_on_me" || accounts["work"] != "needs_initial_review" {
		t.Fatalf("rows by account = %v", accounts)
	}
	mine, _ := db.Queue(ctx, QueueFilter{Account: "work"})
	if len(mine) != 1 || mine[0].Account != "work" || mine[0].Title != "Fix it" {
		t.Fatalf("account filter = %+v", mine)
	}
	details, _ := db.PRDetails(ctx, "NVIDIA", "aicr", 7)
	if len(details) != 2 || details[0].Result == nil {
		t.Fatalf("PRDetails = %+v", details)
	}
}

func TestQueueFiltersAndExcludesDisabled(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "o", "r")
	off := mustRepo(t, db, a, "o", "off")
	observeNew(t, db, r, at(0), 1, 2)
	observeNew(t, db, off, at(0), 3)
	commitBucket(t, db, r, 1, "waiting_on_me", at(1))
	commitBucket(t, db, r, 2, "draft", at(1))
	commitBucket(t, db, off, 3, "waiting_on_me", at(1))
	if _, err := db.SetRepoEnabled(ctx, off.ID, false); err != nil {
		t.Fatal(err)
	}
	all, _ := db.Queue(ctx, QueueFilter{})
	if len(all) != 2 {
		t.Fatalf("disabled repo rows leaked: %+v", all)
	}
	drafts, _ := db.Queue(ctx, QueueFilter{Bucket: "draft"})
	if len(drafts) != 1 || drafts[0].Number != 2 {
		t.Fatalf("bucket filter = %+v", drafts)
	}
	counts, _ := db.BucketCounts(ctx)
	if len(counts) != 2 {
		t.Fatalf("BucketCounts included disabled repo: %+v", counts)
	}
}

func TestRepoStatuses(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 1, 2)
	commitBucket(t, db, r, 1, "waiting_on_me", at(1))
	if _, err := db.RecordPoll(ctx, r.ID, at(2), nil); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.RepoStatuses(ctx)
	if err != nil || len(statuses) != 1 {
		t.Fatalf("RepoStatuses = %+v, %v", statuses, err)
	}
	st := statuses[0]
	if st.Open != 2 || st.Pending != 1 || st.Errored != 0 || st.AccountStatus != StatusOK || st.Sync.LastSuccessAt == nil || st.NextRetryAt == nil {
		t.Fatalf("status = %+v", st)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/store/`
Expected: FAIL to compile (`undefined: ApplyPoll`, `Queue`, ...).

- [ ] **Step 4: Implement PR lifecycle**

Create `internal/store/prs.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/rules"
)

// PRState is what the poll planner needs about one stored PR.
type PRState struct {
	Number              int
	State               string
	Revision            int64
	Fingerprint         string
	HeadOID             string
	HasRecord           bool
	RecordVersion       int
	RulesHash           string
	RecordAt            *time.Time
	LastEnrichAttemptAt *time.Time
	RetryAt             *time.Time
	Error               string
}

// Observation is the planned write for one PR seen by a poll.
type Observation struct {
	Number           int
	Fingerprint      string
	HeadOID          string
	Exists           bool
	ExpectedRevision int64
	Invalidate       bool
}

// PollApply is one complete poll observation (§7 steps 3-4).
type PollApply struct {
	RepoID       int64
	RepoRevision int64
	Owner, Name  string
	Now          time.Time
	Upserts      []Observation
	Closes       []int
}

// PollApplied reports what ApplyPoll changed.
type PollApplied struct {
	Inserted []int
	Updated  []int
	Skipped  []int
	Closed   []Transition
}

// RepoSync is a repository's poll bookkeeping.
type RepoSync struct {
	RepoID              int64
	LastPollAt          *time.Time
	LastSuccessAt       *time.Time
	LastError           string
	ConsecutiveFailures int
}

// Work is one due enrichment job with everything its commit guards need.
type Work struct {
	RepoID         int64
	RepoRevision   int64
	Account        string
	Owner, Name    string
	Rules          rules.Rules
	Number         int
	PRRevision     int64
	Fingerprint    string
	HeadChangedAt  *time.Time
	RecordAt       *time.Time
	Record         []byte
	EnrichFailures int
}

// EnrichCommit is one accepted enrichment outcome (§7).
type EnrichCommit struct {
	RepoID          int64
	RepoRevision    int64
	Number          int
	PRRevision      int64
	Now             time.Time
	Record          []byte
	AdvanceRecordAt bool
	Title           string
	URL             string
	Author          string
	Error           string
	EnrichFailures  int
	RetryAt         *time.Time
	Result          ResultRow
	Caches          Caches
}

// DetailObservation commits a detail read whose fingerprint differs from the queued one (§4.3).
type DetailObservation struct {
	RepoID       int64
	RepoRevision int64
	Number       int
	PRRevision   int64
	Now          time.Time
	Fingerprint  string
	HeadOID      string
}

// Classifiable is a stored open record the reclassify pass can re-run.
type Classifiable struct {
	RepoID       int64
	RepoRevision int64
	Account      string
	Rules        rules.Rules
	Number       int
	PRRevision   int64
	Record       []byte
}

// PRStates lists every stored PR of a repository.
func (db *DB) PRStates(ctx context.Context, repoID int64) ([]PRState, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT number, state, revision, COALESCE(fingerprint, ''), COALESCE(head_oid, ''),
  record IS NOT NULL, COALESCE(json_extract(record, '$.version'), 0), COALESCE(json_extract(record, '$.collection_rules_hash'), ''),
  record_at, last_enrich_attempt_at, retry_at, COALESCE(error, '')
FROM prs WHERE repo_id = ? ORDER BY number`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: pr states: %w", err)
	}
	defer rows.Close()
	var out []PRState
	for rows.Next() {
		var s PRState
		var recordAt, attemptAt, retryAt sql.NullInt64
		if err := rows.Scan(&s.Number, &s.State, &s.Revision, &s.Fingerprint, &s.HeadOID, &s.HasRecord, &s.RecordVersion,
			&s.RulesHash, &recordAt, &attemptAt, &retryAt, &s.Error); err != nil {
			return nil, err
		}
		s.RecordAt, s.LastEnrichAttemptAt, s.RetryAt = timePtr(recordAt), timePtr(attemptAt), timePtr(retryAt)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ApplyPoll persists a complete poll observation if the repo is still enabled at
// the captured revision. Per-PR updates are guarded by the expected revision.
func (db *DB) ApplyPoll(ctx context.Context, p PollApply) (PollApplied, error) {
	var out PollApplied
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := repoCurrent(ctx, tx, p.RepoID, p.RepoRevision); err != nil {
			return err
		}
		now := ms(p.Now)
		for _, o := range p.Upserts {
			if !o.Exists {
				res, err := tx.ExecContext(ctx, `INSERT INTO prs (repo_id, number, title, url, author, state, revision, fingerprint, head_oid, retry_at, first_seen_at)
VALUES (?, ?, '', ?, '', 'open', 1, ?, ?, ?, ?) ON CONFLICT (repo_id, number) DO NOTHING`,
					p.RepoID, o.Number, PullURL(p.Owner, p.Name, o.Number), o.Fingerprint, o.HeadOID, now, now)
				if err != nil {
					return fmt.Errorf("store: insert pr %d: %w", o.Number, err)
				}
				if affected(res) == 0 {
					out.Skipped = append(out.Skipped, o.Number)
				} else {
					out.Inserted = append(out.Inserted, o.Number)
				}
				continue
			}
			inc := 0
			if o.Invalidate {
				inc = 1
			}
			res, err := tx.ExecContext(ctx, `UPDATE prs SET
  head_changed_at = CASE WHEN head_oid IS NOT NULL AND head_oid <> ? THEN ? ELSE head_changed_at END,
  head_oid = ?, fingerprint = ?, state = 'open', closed_at = NULL,
  revision = revision + ?,
  retry_at = CASE WHEN ? = 1 THEN ? ELSE retry_at END
WHERE repo_id = ? AND number = ? AND revision = ?`,
				o.HeadOID, now, o.HeadOID, o.Fingerprint, inc, inc, now, p.RepoID, o.Number, o.ExpectedRevision)
			if err != nil {
				return fmt.Errorf("store: update pr %d: %w", o.Number, err)
			}
			if affected(res) == 0 {
				out.Skipped = append(out.Skipped, o.Number)
			} else {
				out.Updated = append(out.Updated, o.Number)
			}
		}
		for _, n := range p.Closes {
			tr, err := closePR(ctx, tx, p.RepoID, n, p.Now)
			if err != nil {
				return err
			}
			if tr != nil {
				out.Closed = append(out.Closed, *tr)
			}
		}
		return nil
	})
	return out, err
}

func closePR(ctx context.Context, tx *sql.Tx, repoID int64, number int, now time.Time) (*Transition, error) {
	res, err := tx.ExecContext(ctx, `UPDATE prs SET state = 'closed', closed_at = ?, revision = revision + 1, retry_at = NULL, enrich_failures = 0
WHERE repo_id = ? AND number = ? AND state = 'open'`, ms(now), repoID, number)
	if err != nil {
		return nil, fmt.Errorf("store: close pr %d: %w", number, err)
	}
	if affected(res) == 0 {
		return nil, nil
	}
	prev, err := readResult(ctx, tx, repoID, number)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM results WHERE repo_id = ? AND number = ?`, repoID, number); err != nil {
		return nil, fmt.Errorf("store: delete result: %w", err)
	}
	tr := &Transition{RepoID: repoID, Number: number, At: now, To: string(classify.Closed), Reason: "closed or merged"}
	if prev != nil {
		from := prev.Bucket
		tr.From = &from
	}
	if err := insertTransition(ctx, tx, tr); err != nil {
		return nil, err
	}
	return tr, nil
}

func repoCurrent(ctx context.Context, tx *sql.Tx, repoID, revision int64) error {
	var cur int64
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT revision, enabled FROM repos WHERE id = ?`, repoID).Scan(&cur, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("store: read repo revision: %w", err)
	}
	if cur != revision || !enabled {
		return ErrStale
	}
	return nil
}

// guardPR enforces the §7 commit conditions inside the write transaction.
func guardPR(ctx context.Context, tx *sql.Tx, repoID, repoRevision int64, number int, prRevision int64) error {
	var rRev, pRev int64
	var enabled bool
	var state string
	err := tx.QueryRowContext(ctx, `SELECT r.revision, r.enabled, p.revision, p.state FROM prs p JOIN repos r ON r.id = p.repo_id
WHERE p.repo_id = ? AND p.number = ?`, repoID, number).Scan(&rRev, &enabled, &pRev, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("store: read guard: %w", err)
	}
	if rRev != repoRevision || !enabled || pRev != prRevision || state != "open" {
		return ErrStale
	}
	return nil
}

// RecordPoll stores a poll's outcome and returns the updated bookkeeping.
func (db *DB) RecordPoll(ctx context.Context, repoID int64, now time.Time, pollErr error) (RepoSync, error) {
	var row *sql.Row
	if pollErr == nil {
		row = db.w.QueryRowContext(ctx, `UPDATE repo_sync SET last_poll_at = ?, last_success_at = ?, last_error = NULL, consecutive_failures = 0
WHERE repo_id = ? RETURNING last_poll_at, last_success_at, COALESCE(last_error, ''), consecutive_failures`, ms(now), ms(now), repoID)
	} else {
		row = db.w.QueryRowContext(ctx, `UPDATE repo_sync SET last_poll_at = ?, last_error = ?, consecutive_failures = consecutive_failures + 1
WHERE repo_id = ? RETURNING last_poll_at, last_success_at, COALESCE(last_error, ''), consecutive_failures`, ms(now), pollErr.Error(), repoID)
	}
	s := RepoSync{RepoID: repoID}
	var pollAt, successAt sql.NullInt64
	err := row.Scan(&pollAt, &successAt, &s.LastError, &s.ConsecutiveFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return RepoSync{}, ErrNotFound
	}
	if err != nil {
		return RepoSync{}, fmt.Errorf("store: record poll: %w", err)
	}
	s.LastPollAt, s.LastSuccessAt = timePtr(pollAt), timePtr(successAt)
	return s, nil
}

// DueWork lists open PRs in enabled repos of ok accounts whose retry deadline has passed.
func (db *DB) DueWork(ctx context.Context, now time.Time, limit int) ([]Work, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT p.repo_id, r.revision, a.login, r.owner, r.name, r.rules, p.number, p.revision,
  COALESCE(p.fingerprint, ''), p.head_changed_at, p.record_at, p.record, p.enrich_failures
FROM prs p JOIN repos r ON r.id = p.repo_id JOIN accounts a ON a.id = r.account_id
WHERE p.state = 'open' AND r.enabled = 1 AND a.status = 'ok' AND p.retry_at IS NOT NULL AND p.retry_at <= ?
ORDER BY p.retry_at, p.repo_id, p.number LIMIT ?`, ms(now), limit)
	if err != nil {
		return nil, fmt.Errorf("store: due work: %w", err)
	}
	defer rows.Close()
	var out []Work
	for rows.Next() {
		var w Work
		var rawRules string
		var headChanged, recordAt sql.NullInt64
		var record sql.NullString
		if err := rows.Scan(&w.RepoID, &w.RepoRevision, &w.Account, &w.Owner, &w.Name, &rawRules, &w.Number, &w.PRRevision,
			&w.Fingerprint, &headChanged, &recordAt, &record, &w.EnrichFailures); err != nil {
			return nil, err
		}
		if w.Rules, err = rules.Parse([]byte(rawRules)); err != nil {
			return nil, fmt.Errorf("store: repo %d rules: %w", w.RepoID, err)
		}
		w.HeadChangedAt, w.RecordAt = timePtr(headChanged), timePtr(recordAt)
		if record.Valid {
			w.Record = []byte(record.String)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CommitEnrichment writes an accepted enrichment outcome, its result, any
// transition, and staged caches atomically, or returns ErrStale (§7).
func (db *DB) CommitEnrichment(ctx context.Context, c EnrichCommit) (*Transition, error) {
	var tr *Transition
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := guardPR(ctx, tx, c.RepoID, c.RepoRevision, c.Number, c.PRRevision); err != nil {
			return err
		}
		advance := 0
		if c.AdvanceRecordAt {
			advance = 1
		}
		now := ms(c.Now)
		_, err := tx.ExecContext(ctx, `UPDATE prs SET
  record = ?, record_at = CASE WHEN ? = 1 THEN ? ELSE record_at END,
  last_enrich_attempt_at = ?, error = NULLIF(?, ''), enrich_failures = ?, retry_at = ?,
  revision = revision + 1,
  title = CASE WHEN ? <> '' THEN ? ELSE title END,
  url = CASE WHEN ? <> '' THEN ? ELSE url END,
  author = CASE WHEN ? <> '' THEN ? ELSE author END
WHERE repo_id = ? AND number = ?`,
			string(c.Record), advance, now, now, c.Error, c.EnrichFailures, nullTime(c.RetryAt),
			c.Title, c.Title, c.URL, c.URL, c.Author, c.Author, c.RepoID, c.Number)
		if err != nil {
			return fmt.Errorf("store: write enrichment: %w", err)
		}
		if tr, err = writeResult(ctx, tx, c.RepoID, c.Number, c.Result, c.Now); err != nil {
			return err
		}
		return writeCaches(ctx, tx, c.RepoID, c.Caches, c.Now)
	})
	if err != nil {
		return nil, err
	}
	return tr, nil
}

// CommitObservation records a newer detail observation and invalidates the PR.
func (db *DB) CommitObservation(ctx context.Context, o DetailObservation) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if err := guardPR(ctx, tx, o.RepoID, o.RepoRevision, o.Number, o.PRRevision); err != nil {
			return err
		}
		now := ms(o.Now)
		_, err := tx.ExecContext(ctx, `UPDATE prs SET
  head_changed_at = CASE WHEN head_oid IS NOT NULL AND head_oid <> ? THEN ? ELSE head_changed_at END,
  head_oid = ?, fingerprint = ?, revision = revision + 1, retry_at = ?
WHERE repo_id = ? AND number = ?`, o.HeadOID, now, o.HeadOID, o.Fingerprint, now, o.RepoID, o.Number)
		if err != nil {
			return fmt.Errorf("store: commit observation: %w", err)
		}
		return nil
	})
}

// Reclassifiable lists open PRs with records in enabled repos (repoID 0 = all).
func (db *DB) Reclassifiable(ctx context.Context, repoID int64) ([]Classifiable, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT p.repo_id, r.revision, a.login, r.rules, p.number, p.revision, p.record
FROM prs p JOIN repos r ON r.id = p.repo_id JOIN accounts a ON a.id = r.account_id
WHERE p.state = 'open' AND r.enabled = 1 AND p.record IS NOT NULL AND (? = 0 OR p.repo_id = ?)
ORDER BY p.repo_id, p.number`, repoID, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: reclassifiable: %w", err)
	}
	defer rows.Close()
	var out []Classifiable
	for rows.Next() {
		var c Classifiable
		var rawRules, record string
		if err := rows.Scan(&c.RepoID, &c.RepoRevision, &c.Account, &rawRules, &c.Number, &c.PRRevision, &record); err != nil {
			return nil, err
		}
		if c.Rules, err = rules.Parse([]byte(rawRules)); err != nil {
			return nil, fmt.Errorf("store: repo %d rules: %w", c.RepoID, err)
		}
		c.Record = []byte(record)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CommitResult stores a reclassified result without advancing the evidence revision.
// It reports whether anything changed.
func (db *DB) CommitResult(ctx context.Context, repoID, repoRev int64, number int, prRev int64, res ResultRow, now time.Time) (*Transition, bool, error) {
	var tr *Transition
	changed := false
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := guardPR(ctx, tx, repoID, repoRev, number, prRev); err != nil {
			return err
		}
		cur, err := readResult(ctx, tx, repoID, number)
		if err != nil {
			return err
		}
		if cur != nil && sameResult(*cur, res) {
			return nil
		}
		changed = true
		tr, err = writeResult(ctx, tx, repoID, number, res, now)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return tr, changed, nil
}
```

- [ ] **Step 5: Implement caches**

Create `internal/store/caches.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Cache entries staged by collection and written with an accepted outcome (§4.3).
type (
	DiffCache struct {
		BaseOID, ReviewedOID, HeadOID string
		Changed                       bool
	}
	CommentCache struct {
		CommentID int64
		UpdatedAt time.Time
		IsCommand bool
	}
	PolicyCache struct {
		BaseRef, Policy string
		FetchedAt       time.Time
	}
	Caches struct {
		Diffs    []DiffCache
		Comments []CommentCache
		Policies []PolicyCache
	}
)

const cacheRetention = 30 * 24 * time.Hour

func writeCaches(ctx context.Context, tx *sql.Tx, repoID int64, c Caches, now time.Time) error {
	for _, d := range c.Diffs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_diff (repo_id, base_oid, reviewed_oid, head_oid, changed, created_at)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (repo_id, base_oid, reviewed_oid, head_oid) DO UPDATE SET changed = excluded.changed`,
			repoID, d.BaseOID, d.ReviewedOID, d.HeadOID, d.Changed, ms(now)); err != nil {
			return fmt.Errorf("store: cache diff: %w", err)
		}
	}
	for _, cm := range c.Comments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_comment (repo_id, comment_id, updated_at, is_command, created_at)
VALUES (?, ?, ?, ?, ?) ON CONFLICT (repo_id, comment_id, updated_at) DO NOTHING`,
			repoID, cm.CommentID, ms(cm.UpdatedAt), cm.IsCommand, ms(now)); err != nil {
			return fmt.Errorf("store: cache comment: %w", err)
		}
	}
	for _, p := range c.Policies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_policy (repo_id, base_ref, policy, fetched_at) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, base_ref) DO UPDATE SET policy = excluded.policy, fetched_at = excluded.fetched_at`,
			repoID, p.BaseRef, p.Policy, ms(p.FetchedAt)); err != nil {
			return fmt.Errorf("store: cache policy: %w", err)
		}
	}
	return nil
}

// CachedDiff returns a definite "effective diff changed" answer if cached.
func (db *DB) CachedDiff(ctx context.Context, repoID int64, base, reviewed, head string) (changed, ok bool, err error) {
	err = db.r.QueryRowContext(ctx, `SELECT changed FROM cache_diff WHERE repo_id = ? AND base_oid = ? AND reviewed_oid = ? AND head_oid = ?`,
		repoID, base, reviewed, head).Scan(&changed)
	return cacheResult(changed, err)
}

// CachedComment returns whether a comment version is a slash command, if cached.
func (db *DB) CachedComment(ctx context.Context, repoID, commentID int64, updatedAt time.Time) (isCommand, ok bool, err error) {
	err = db.r.QueryRowContext(ctx, `SELECT is_command FROM cache_comment WHERE repo_id = ? AND comment_id = ? AND updated_at = ?`,
		repoID, commentID, ms(updatedAt)).Scan(&isCommand)
	return cacheResult(isCommand, err)
}

// CachedPolicy returns a base branch's signature policy fetched within ttl.
func (db *DB) CachedPolicy(ctx context.Context, repoID int64, baseRef string, now time.Time, ttl time.Duration) (string, bool, error) {
	var policy string
	err := db.r.QueryRowContext(ctx, `SELECT policy FROM cache_policy WHERE repo_id = ? AND base_ref = ? AND fetched_at >= ?`,
		repoID, baseRef, ms(now.Add(-ttl))).Scan(&policy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: cached policy: %w", err)
	}
	return policy, true, nil
}

func cacheResult(v bool, err error) (bool, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("store: cache read: %w", err)
	}
	return v, true, nil
}

// Purge deletes diff and comment cache rows older than 30 days (§6).
func (db *DB) Purge(ctx context.Context, now time.Time) (int64, error) {
	cutoff := ms(now.Add(-cacheRetention))
	var total int64
	for _, q := range []string{`DELETE FROM cache_diff WHERE created_at < ?`, `DELETE FROM cache_comment WHERE created_at < ?`} {
		res, err := db.w.ExecContext(ctx, q, cutoff)
		if err != nil {
			return total, fmt.Errorf("store: purge: %w", err)
		}
		total += affected(res)
	}
	return total, nil
}
```

- [ ] **Step 6: Implement views**

Create `internal/store/views.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// QueueFilter narrows the queue; empty fields match everything.
type QueueFilter struct {
	Owner   string
	Name    string
	Account string
	Bucket  string
}

// QueueRow is one classified open PR in an enabled repository.
type QueueRow struct {
	RepoID  int64
	Owner   string
	Name    string
	Account string
	Number  int
	Title   string
	URL     string
	Author  string
	Result  ResultRow
	Record  []byte
}

// Queue lists classified open PRs of enabled repositories.
func (db *DB) Queue(ctx context.Context, f QueueFilter) ([]QueueRow, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT r.id, r.owner, r.name, a.login, p.number, p.title, p.url, p.author,
  res.bucket, res.reason, res.action, res.waiting_since, res.also, res.notes, COALESCE(p.record, '')
FROM results res
JOIN prs p ON p.repo_id = res.repo_id AND p.number = res.number
JOIN repos r ON r.id = p.repo_id
JOIN accounts a ON a.id = r.account_id
WHERE r.enabled = 1 AND p.state = 'open'
  AND (? = '' OR r.owner = ?) AND (? = '' OR r.name = ?) AND (? = '' OR a.login = ?) AND (? = '' OR res.bucket = ?)
ORDER BY r.owner, r.name, p.number, a.login`,
		f.Owner, f.Owner, f.Name, f.Name, f.Account, f.Account, f.Bucket, f.Bucket)
	if err != nil {
		return nil, fmt.Errorf("store: queue: %w", err)
	}
	defer rows.Close()
	var out []QueueRow
	for rows.Next() {
		var q QueueRow
		var since sql.NullInt64
		var also, notes, record string
		if err := rows.Scan(&q.RepoID, &q.Owner, &q.Name, &q.Account, &q.Number, &q.Title, &q.URL, &q.Author,
			&q.Result.Bucket, &q.Result.Reason, &q.Result.Action, &since, &also, &notes, &record); err != nil {
			return nil, err
		}
		q.Result.WaitingSince = timePtr(since)
		q.Result.Also, q.Result.Notes = decodeList(also), decodeList(notes)
		q.Record = []byte(record)
		out = append(out, q)
	}
	return out, rows.Err()
}

// PRDetail is everything the explain view shows about one PR entry.
type PRDetail struct {
	Repo                Repo
	Number              int
	Title, URL, Author  string
	State               string
	Revision            int64
	Fingerprint         string
	HeadOID             string
	HeadChangedAt       *time.Time
	RecordAt            *time.Time
	LastEnrichAttemptAt *time.Time
	RetryAt             *time.Time
	ClosedAt            *time.Time
	FirstSeenAt         time.Time
	Record              []byte
	EnrichFailures      int
	Error               string
	Result              *ResultRow
}

// PRDetails returns the PR entry under every account monitoring owner/name.
func (db *DB) PRDetails(ctx context.Context, owner, name string, number int) ([]PRDetail, error) {
	repos, err := db.ReposByName(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	var out []PRDetail
	for _, repo := range repos {
		d := PRDetail{Repo: repo, Number: number}
		var headChanged, recordAt, attemptAt, retryAt, closedAt sql.NullInt64
		var firstSeen int64
		var record sql.NullString
		err := db.r.QueryRowContext(ctx, `SELECT title, url, author, state, revision, COALESCE(fingerprint, ''), COALESCE(head_oid, ''),
  head_changed_at, record_at, last_enrich_attempt_at, retry_at, closed_at, first_seen_at, record, enrich_failures, COALESCE(error, '')
FROM prs WHERE repo_id = ? AND number = ?`, repo.ID, number).Scan(&d.Title, &d.URL, &d.Author, &d.State, &d.Revision,
			&d.Fingerprint, &d.HeadOID, &headChanged, &recordAt, &attemptAt, &retryAt, &closedAt, &firstSeen, &record, &d.EnrichFailures, &d.Error)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store: pr detail: %w", err)
		}
		d.HeadChangedAt, d.RecordAt, d.LastEnrichAttemptAt = timePtr(headChanged), timePtr(recordAt), timePtr(attemptAt)
		d.RetryAt, d.ClosedAt, d.FirstSeenAt = timePtr(retryAt), timePtr(closedAt), fromMS(firstSeen)
		if record.Valid {
			d.Record = []byte(record.String)
		}
		if d.Result, err = readResult(ctx, db.r, repo.ID, number); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// RepoStatus is the header-bar health of one repository (§9).
type RepoStatus struct {
	Repo          Repo
	Sync          RepoSync
	AccountStatus string
	AccountDetail string
	Open          int
	Pending       int
	Errored       int
	Retrying      int
	NextRetryAt   *time.Time
	LastAttemptAt *time.Time
}

// RepoStatuses reports poll and enrichment health for every repository.
func (db *DB) RepoStatuses(ctx context.Context) ([]RepoStatus, error) {
	repos, err := db.Repos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RepoStatus, 0, len(repos))
	for _, repo := range repos {
		st := RepoStatus{Repo: repo, Sync: RepoSync{RepoID: repo.ID}}
		var pollAt, successAt, nextRetry, lastAttempt sql.NullInt64
		err := db.r.QueryRowContext(ctx, `SELECT s.last_poll_at, s.last_success_at, COALESCE(s.last_error, ''), COALESCE(s.consecutive_failures, 0),
  a.status, COALESCE(a.status_detail, ''),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open'),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open' AND p.retry_at IS NOT NULL AND p.enrich_failures = 0),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open' AND p.error IS NOT NULL),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open' AND p.retry_at IS NOT NULL AND p.enrich_failures > 0),
  (SELECT MIN(p.retry_at) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open'),
  (SELECT MAX(p.last_enrich_attempt_at) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open')
FROM repos r JOIN accounts a ON a.id = r.account_id LEFT JOIN repo_sync s ON s.repo_id = r.id
WHERE r.id = ?`, repo.ID).Scan(&pollAt, &successAt, &st.Sync.LastError, &st.Sync.ConsecutiveFailures,
			&st.AccountStatus, &st.AccountDetail, &st.Open, &st.Pending, &st.Errored, &st.Retrying, &nextRetry, &lastAttempt)
		if err != nil {
			return nil, fmt.Errorf("store: repo status %d: %w", repo.ID, err)
		}
		st.Sync.LastPollAt, st.Sync.LastSuccessAt = timePtr(pollAt), timePtr(successAt)
		st.NextRetryAt, st.LastAttemptAt = timePtr(nextRetry), timePtr(lastAttempt)
		out = append(out, st)
	}
	return out, nil
}

// BucketCount is the number of open PRs of one enabled repository in one bucket.
type BucketCount struct {
	Repo   string
	Bucket string
	Count  int
}

// BucketCounts feeds the devbox_prs gauge; disabled repos are excluded (§7).
func (db *DB) BucketCounts(ctx context.Context) ([]BucketCount, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT r.owner || '/' || r.name, res.bucket, COUNT(*)
FROM results res JOIN prs p ON p.repo_id = res.repo_id AND p.number = res.number JOIN repos r ON r.id = p.repo_id
WHERE r.enabled = 1 AND p.state = 'open' GROUP BY 1, 2 ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("store: bucket counts: %w", err)
	}
	defer rows.Close()
	var out []BucketCount
	for rows.Next() {
		var c BucketCount
		if err := rows.Scan(&c.Repo, &c.Bucket, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/store/ -v`
Expected: PASS for all store tests.

- [ ] **Step 8: Lint and commit**

```bash
make fmt lint
git add internal/store
git commit -S -m "feat(store): add PR lifecycle, guarded commits, caches, and views"
```

---

### Task 9: Evidence reader — queries, fingerprints, normalization

**Files:**
- Create: `internal/collect/fingerprint.go`, `internal/collect/detail.go`, `internal/collect/reader.go`, `internal/collect/normalize.go`, `internal/collect/testdata/detail.json`
- Test: `internal/collect/reader_test.go`, `internal/collect/normalize_test.go`

**Interfaces:**
- Consumes: Task 6 `*github.Client` (`GraphQL`, `Get`, `GetPages`, `GetDiff`, `*github.StatusError`); Task 3 `classify.Record`, `classify.Check`, and evidence types.
- Produces:
  - `type FingerprintFields struct { UpdatedAt time.Time; BaseOID, HeadOID, MergeState, ReviewDecision string; Draft bool; Rollup string }`; `func Fingerprint(FingerprintFields) string`
  - GraphQL shape types: `Actor`, `Milestone`, `AutoMerge`, `ReviewRequest`, `ReviewNode`, `OID`, `ThreadComment`, `ThreadNode`, `CommentNode`, `Signature`, `Rollup`, `Commit`, `CommitNode`, `Nodes[T]`, `Detail`; `(*Detail) FingerprintFields() FingerprintFields`
  - `type Observed struct { Number int; Fingerprint, HeadOID string }`
  - `type Reader interface` (methods below); `type GitHubReader struct { Client *github.Client }` implements it; `var ErrTooManyFiles`
  - `func Normalize(d *Detail, viewer string, headChangedAt *time.Time) (classify.Record, error)`

```go
type Reader interface {
	Fingerprints(ctx context.Context, owner, name string) ([]Observed, error)
	Detail(ctx context.Context, owner, name string, number int, viewer string) (*Detail, error)
	Checks(ctx context.Context, owner, name, head string, number int) ([]classify.Check, error)
	AwaitingRuns(ctx context.Context, owner, name, head string) (int, error)
	MarkerConclusions(ctx context.Context, owner, name, head, check string) ([]string, error)
	ChangedFiles(ctx context.Context, owner, name, base, head string) ([]string, error)
	Diff(ctx context.Context, owner, name, base, commit string) ([]byte, error)
	CommentBody(ctx context.Context, owner, name string, id int64) (string, error)
	RulesetTypes(ctx context.Context, owner, name, branch string) ([]string, error)
	ClassicSignatures(ctx context.Context, owner, name, branch string) (bool, error)
}
```

- [ ] **Step 1: Add the detail fixture**

Create `internal/collect/testdata/detail.json` (a `PullRequestDetail` response; viewer is `me`):

```json
{
  "data": {
    "rateLimit": {"limit": 5000, "cost": 1, "remaining": 4990, "resetAt": "2026-10-01T13:00:00Z"},
    "repository": {
      "pullRequest": {
        "number": 42,
        "title": "Add GPU recipe",
        "url": "https://github.com/NVIDIA/aicr/pull/42",
        "isDraft": false,
        "isCrossRepository": true,
        "createdAt": "2026-09-28T10:00:00Z",
        "updatedAt": "2026-10-01T09:30:00Z",
        "authorAssociation": "FIRST_TIME_CONTRIBUTOR",
        "additions": 120,
        "deletions": 4,
        "milestone": {"title": "v1.0", "dueOn": "2026-10-15T00:00:00Z"},
        "author": {"login": "alice", "__typename": "User"},
        "baseRefName": "main",
        "baseRefOid": "b000000000000000000000000000000000000000",
        "headRefOid": "h111111111111111111111111111111111111111",
        "mergeStateStatus": "BLOCKED",
        "reviewDecision": "APPROVED",
        "autoMergeRequest": null,
        "reviewRequests": {"nodes": [
          {"requestedReviewer": {"__typename": "User", "login": "me"}},
          {"requestedReviewer": {"__typename": "Team"}}
        ]},
        "myReviews": {"nodes": [
          {"state": "APPROVED", "submittedAt": "2026-09-30T08:00:00Z", "commit": {"oid": "o000000000000000000000000000000000000000"}},
          {"state": "PENDING", "submittedAt": null, "commit": null},
          {"state": "COMMENTED", "submittedAt": "2026-09-29T08:00:00Z", "commit": {"oid": "o000000000000000000000000000000000000000"}}
        ]},
        "latestOpinionatedReviews": {"nodes": [
          {"state": "APPROVED", "submittedAt": "2026-09-30T08:00:00Z", "author": {"login": "me"}},
          {"state": "CHANGES_REQUESTED", "submittedAt": "2026-09-30T09:00:00Z", "author": {"login": "bob"}}
        ]},
        "reviewThreads": {"totalCount": 2, "nodes": [
          {"isResolved": false,
           "first": {"nodes": [{"author": {"login": "me"}, "url": "https://github.com/NVIDIA/aicr/pull/42#discussion_r1"}]},
           "last": {"nodes": [{"author": {"login": "alice"}, "createdAt": "2026-10-01T09:00:00Z"}]}},
          {"isResolved": true,
           "first": {"nodes": [{"author": {"login": "bob"}, "url": "https://github.com/NVIDIA/aicr/pull/42#discussion_r2"}]},
           "last": {"nodes": [{"author": {"login": "bob"}, "createdAt": "2026-09-30T10:00:00Z"}]}}
        ]},
        "comments": {"nodes": [
          {"author": {"login": "alice"}, "createdAt": "2026-09-30T12:00:00Z", "updatedAt": "2026-09-30T12:00:00Z", "databaseId": 1001},
          {"author": {"login": "me"}, "createdAt": "2026-09-30T13:00:00Z", "updatedAt": "2026-09-30T13:05:00Z", "databaseId": 1002}
        ]},
        "commits": {"totalCount": 2, "nodes": [
          {"commit": {"oid": "c100000000000000000000000000000000000000", "signature": {"state": "VALID"}}},
          {"commit": {"oid": "c200000000000000000000000000000000000000", "signature": null}}
        ]},
        "head": {"nodes": [
          {"commit": {"oid": "h111111111111111111111111111111111111111", "committedDate": "2026-10-01T08:00:00Z", "statusCheckRollup": {"state": "FAILURE"}}}
        ]}
      }
    }
  }
}
```

- [ ] **Step 2: Write the failing reader tests**

Create `internal/collect/reader_test.go`:

```go
package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
)

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func newReader(t *testing.T, h http.HandlerFunc) GitHubReader {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := github.New(github.Options{APIURL: srv.URL, Token: "t", Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return GitHubReader{Client: c}
}

func decodeGQL(t *testing.T, r *http.Request) gqlRequest {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	var req gqlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("bad graphql body: %v", err)
	}
	return req
}

func fpNodeJSON(number int, head string) string {
	return fmt.Sprintf(`{"number":%d,"updatedAt":"2026-10-01T09:30:00Z","baseRefOid":"b0","headRefOid":%q,
"mergeStateStatus":"CLEAN","reviewDecision":null,"isDraft":false,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}`, number, head)
}

func TestFingerprintsPaginates(t *testing.T) {
	var cursors []any
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeGQL(t, r)
		if !strings.HasPrefix(strings.TrimSpace(req.Query), "query Fingerprints") {
			t.Errorf("unexpected query %q", req.Query)
		}
		cursors = append(cursors, req.Variables["endCursor"])
		if req.Variables["endCursor"] == nil {
			fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[%s]}}}}`, fpNodeJSON(1, "h1"))
			return
		}
		fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[%s]}}}}`, fpNodeJSON(2, "h2"))
	})
	obs, err := reader.Fingerprints(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Fingerprints: %v", err)
	}
	if len(obs) != 2 || obs[0].Number != 1 || obs[1].HeadOID != "h2" {
		t.Fatalf("observed = %+v", obs)
	}
	want := Fingerprint(FingerprintFields{UpdatedAt: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), BaseOID: "b0", HeadOID: "h1", MergeState: "CLEAN", Rollup: "SUCCESS"})
	if obs[0].Fingerprint != want {
		t.Fatalf("fingerprint mismatch")
	}
	if len(cursors) != 2 || cursors[1] != "c1" {
		t.Fatalf("cursors = %v", cursors)
	}
}

func TestFingerprintsRejectsRepeatedCursor(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":true,"endCursor":"same"},"nodes":[%s]}}}}`, fpNodeJSON(1, "h1"))
	})
	if _, err := reader.Fingerprints(context.Background(), "o", "r"); err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("err = %v", err)
	}
}

func TestFingerprintsMissingRepository(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":{"repository":null}}`) })
	if _, err := reader.Fingerprints(context.Background(), "o", "gone"); err == nil {
		t.Fatal("missing repository accepted")
	}
}

func TestDetailFixtureMatchesFingerprint(t *testing.T) {
	fixture, err := os.ReadFile("testdata/detail.json")
	if err != nil {
		t.Fatal(err)
	}
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeGQL(t, r)
		if req.Variables["viewer"] != "me" || req.Variables["number"] != float64(42) {
			t.Errorf("variables = %v", req.Variables)
		}
		_, _ = w.Write(fixture)
	})
	d, err := reader.Detail(context.Background(), "NVIDIA", "aicr", 42, "me")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	want := Fingerprint(FingerprintFields{
		UpdatedAt: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), BaseOID: "b000000000000000000000000000000000000000",
		HeadOID: "h111111111111111111111111111111111111111", MergeState: "BLOCKED", ReviewDecision: "APPROVED", Rollup: "FAILURE",
	})
	if Fingerprint(d.FingerprintFields()) != want {
		t.Fatalf("detail fingerprint differs from fingerprint-query fingerprint for identical fields")
	}
}

func TestDetailNotFound(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":null}}}`)
	})
	if _, err := reader.Detail(context.Background(), "o", "r", 1, "me"); err == nil {
		t.Fatal("missing pull request accepted")
	}
}

func TestChecksDecodesRunsAndStatuses(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"repository":{"object":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
{"__typename":"CheckRun","name":"tests","status":"COMPLETED","conclusion":"FAILURE","isRequired":true,"checkSuite":{"workflowRun":{"databaseId":42}}},
{"__typename":"CheckRun","name":"e2e","status":"IN_PROGRESS","conclusion":null,"isRequired":true,"checkSuite":{"workflowRun":null}},
{"__typename":"StatusContext","context":"ci/legacy","state":"PENDING","isRequired":false}]}}}}}}`)
	})
	checks, err := reader.Checks(context.Background(), "o", "r", "head", 7)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(checks) != 3 || checks[0].RunID != 42 || checks[0].State != "FAILURE" || checks[1].State != "IN_PROGRESS" ||
		checks[2].Name != "ci/legacy" || checks[2].Required {
		t.Fatalf("checks = %+v", checks)
	}
}

func TestRESTReads(t *testing.T) {
	var base string
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/runs":
			if r.URL.Query().Get("head_sha") != "head" || r.URL.Query().Get("status") != "action_required" {
				t.Errorf("runs query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"total_count":2}`)
		case "/repos/o/r/commits/head/check-runs":
			if r.URL.Query().Get("check_name") != "tests / Test" {
				t.Errorf("check_name = %q", r.URL.Query().Get("check_name"))
			}
			fmt.Fprint(w, `{"check_runs":[{"conclusion":"skipped"},{"conclusion":null}]}`)
		case "/repos/o/r/compare/base...head":
			fmt.Fprint(w, `{"files":[{"filename":"a.go"},{"filename":".github/x.yml","previous_filename":"ci/x.yml"}]}`)
		case "/repos/o/r/compare/base...huge":
			files := make([]string, 300)
			for i := range files {
				files[i] = fmt.Sprintf(`{"filename":"f%d"}`, i)
			}
			fmt.Fprintf(w, `{"files":[%s]}`, strings.Join(files, ","))
		case "/repos/o/r/issues/comments/9":
			fmt.Fprint(w, `{"body":"/ok-to-test"}`)
		case "/repos/o/r/rules/branches/release/v1":
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/rules/branches/release/v1?per_page=100&page=2>; rel="next"`, base))
				fmt.Fprint(w, `[{"type":"pull_request"}]`)
				return
			}
			fmt.Fprint(w, `[{"type":"required_signatures"}]`)
		case "/repos/o/r/branches/main/protection/required_signatures":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Branch not protected"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	base = strings.TrimSuffix(reader.Client.BaseURL(), "/")
	ctx := context.Background()
	if n, err := reader.AwaitingRuns(ctx, "o", "r", "head"); err != nil || n != 2 {
		t.Fatalf("AwaitingRuns = %d, %v", n, err)
	}
	if c, err := reader.MarkerConclusions(ctx, "o", "r", "head", "tests / Test"); err != nil || !slices.Equal(c, []string{"skipped", ""}) {
		t.Fatalf("MarkerConclusions = %v, %v", c, err)
	}
	if f, err := reader.ChangedFiles(ctx, "o", "r", "base", "head"); err != nil || !slices.Equal(f, []string{"a.go", ".github/x.yml", "ci/x.yml"}) {
		t.Fatalf("ChangedFiles = %v, %v", f, err)
	}
	if _, err := reader.ChangedFiles(ctx, "o", "r", "base", "huge"); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("huge compare err = %v", err)
	}
	if b, err := reader.CommentBody(ctx, "o", "r", 9); err != nil || b != "/ok-to-test" {
		t.Fatalf("CommentBody = %q, %v", b, err)
	}
	if types, err := reader.RulesetTypes(ctx, "o", "r", "release/v1"); err != nil || !slices.Equal(types, []string{"pull_request", "required_signatures"}) {
		t.Fatalf("RulesetTypes = %v, %v", types, err)
	}
	_, err := reader.ClassicSignatures(ctx, "o", "r", "main")
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 404 || se.Message != "Branch not protected" {
		t.Fatalf("ClassicSignatures err = %v", err)
	}
}
```

This test calls `reader.Client.BaseURL()`. Add that accessor to `internal/github/client.go` in Step 4 below.

- [ ] **Step 3: Write the failing normalization tests**

Create `internal/collect/normalize_test.go`:

```go
package collect

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureDetail(t *testing.T) *Detail {
	t.Helper()
	raw, err := os.ReadFile("testdata/detail.json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data struct {
			Repository struct {
				PullRequest *Detail `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data.Repository.PullRequest
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNormalizeFixture(t *testing.T) {
	rec, err := Normalize(fixtureDetail(t), "me", nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"author", rec.Author == "alice" && !rec.IsBot && rec.Fork && rec.AuthorAssociation == "FIRST_TIME_CONTRIBUTOR"},
		{"size", rec.Additions == 120 && rec.Deletions == 4},
		{"milestone", rec.Milestone != nil && rec.Milestone.Title == "v1.0" && rec.Milestone.DueOn.Equal(ts("2026-10-15T00:00:00Z"))},
		{"review requested", rec.ReviewRequestedFromMe},
		{"my reviews sorted, pending dropped", len(rec.MyReviews) == 2 && rec.MyReviews[0].State == "COMMENTED" && rec.MyReviews[1].State == "APPROVED"},
		{"approved at", rec.ApprovedAt != nil && rec.ApprovedAt.Equal(ts("2026-09-30T08:00:00Z"))},
		{"others decisive", len(rec.OthersDecisive) == 1 && rec.OthersDecisive[0].Author == "bob" && rec.OthersDecisive[0].State == "CHANGES_REQUESTED"},
		{"threads waiting", len(rec.MyThreadsWaiting) == 1 && strings.HasSuffix(rec.MyThreadsWaiting[0].URL, "discussion_r1") &&
			rec.MyThreadsWaiting[0].LastAt.Equal(ts("2026-10-01T09:00:00Z"))},
		{"unresolved", rec.UnresolvedThreads == 1},
		{"author updated", rec.AuthorUpdatedAt != nil && rec.AuthorUpdatedAt.Equal(ts("2026-10-01T09:00:00Z"))},
		{"unverified", len(rec.UnverifiedCommits) == 1 && rec.UnverifiedCommits[0].Reason == "unsigned" && strings.HasPrefix(rec.UnverifiedCommits[0].SHA, "c2")},
		{"signatures complete", rec.SignaturesComplete},
		{"head", rec.HeadCommittedAt.Equal(ts("2026-10-01T08:00:00Z")) && rec.HeadChangedAt == nil && rec.HeadOID == "h111111111111111111111111111111111111111"},
		{"merge", rec.MergeState == "BLOCKED" && rec.ReviewDecision == "APPROVED" && !rec.AutoMerge && rec.BaseRef == "main"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: record = %+v", c.name, rec)
		}
	}
}

func TestNormalizeHeadChangeCountsAsAuthorActivity(t *testing.T) {
	changed := ts("2026-10-02T00:00:00Z")
	rec, err := Normalize(fixtureDetail(t), "me", &changed)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.AuthorUpdatedAt.Equal(changed) || !rec.HeadChangedAt.Equal(changed) {
		t.Fatalf("head change not applied: %+v %+v", rec.AuthorUpdatedAt, rec.HeadChangedAt)
	}
}

func TestNormalizeEdgeCases(t *testing.T) {
	d := fixtureDetail(t)
	d.Author = nil
	d.Commits.TotalCount = 150
	rec, err := Normalize(d, "me", nil)
	if err != nil || rec.Author != "ghost" || rec.SignaturesComplete {
		t.Fatalf("ghost/incomplete = %+v, %v", rec, err)
	}
	d = fixtureDetail(t)
	d.Author = &Actor{Login: "dependabot[bot]", Typename: "Bot"}
	if rec, _ := Normalize(d, "me", nil); !rec.IsBot {
		t.Fatal("bot not detected")
	}
	d = fixtureDetail(t)
	d.Author = &Actor{Login: "renovate[bot]", Typename: "User"}
	if rec, _ := Normalize(d, "me", nil); !rec.IsBot {
		t.Fatal("[bot] suffix not detected")
	}
	d = fixtureDetail(t)
	d.ReviewThreads.TotalCount = 101
	rec, err = Normalize(d, "me", nil)
	if err == nil || !strings.Contains(err.Error(), "more than 100 review threads") || rec.Title != "Add GPU recipe" {
		t.Fatalf("thread overflow = %v, title %q", err, rec.Title)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/collect/`
Expected: FAIL to compile (`undefined: GitHubReader`, `Normalize`).

- [ ] **Step 5: Add the client base URL accessor**

Append to `internal/github/client.go`:

```go
// BaseURL is the API base URL with a trailing slash.
func (c *Client) BaseURL() string { return c.api.String() }
```

- [ ] **Step 6: Implement fingerprints and the detail shape**

Create `internal/collect/fingerprint.go`:

```go
// Package collect reads GitHub evidence for pull requests and normalizes it
// into classify.Record (§4.2, §4.3).
package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// FingerprintFields are the PR fields whose change triggers re-enrichment (§4.2).
type FingerprintFields struct {
	UpdatedAt      time.Time
	BaseOID        string
	HeadOID        string
	MergeState     string
	ReviewDecision string
	Draft          bool
	Rollup         string
}

// Fingerprint is SHA-256 over updatedAt|baseRefOid|headRefOid|mergeStateStatus|reviewDecision|isDraft|rollupState.
func Fingerprint(f FingerprintFields) string {
	s := strings.Join([]string{
		f.UpdatedAt.UTC().Format(time.RFC3339Nano), f.BaseOID, f.HeadOID, f.MergeState, f.ReviewDecision,
		strconv.FormatBool(f.Draft), f.Rollup,
	}, "|")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
```

Create `internal/collect/detail.go`:

```go
package collect

import "time"

// GraphQL response shapes for PullRequestDetail (§4.2).
type (
	Actor struct {
		Login    string `json:"login"`
		Typename string `json:"__typename,omitempty"`
	}
	Milestone struct {
		Title string     `json:"title"`
		DueOn *time.Time `json:"dueOn"`
	}
	AutoMerge struct {
		EnabledAt *time.Time `json:"enabledAt"`
	}
	ReviewRequest struct {
		RequestedReviewer *Actor `json:"requestedReviewer"`
	}
	OID struct {
		OID string `json:"oid"`
	}
	ReviewNode struct {
		State       string     `json:"state"`
		SubmittedAt *time.Time `json:"submittedAt"`
		Commit      *OID       `json:"commit,omitempty"`
		Author      *Actor     `json:"author,omitempty"`
	}
	ThreadComment struct {
		Author    *Actor    `json:"author"`
		URL       string    `json:"url,omitempty"`
		CreatedAt time.Time `json:"createdAt"`
	}
	ThreadNode struct {
		IsResolved bool                 `json:"isResolved"`
		First      Nodes[ThreadComment] `json:"first"`
		Last       Nodes[ThreadComment] `json:"last"`
	}
	CommentNode struct {
		Author     *Actor    `json:"author"`
		CreatedAt  time.Time `json:"createdAt"`
		UpdatedAt  time.Time `json:"updatedAt"`
		DatabaseID int64     `json:"databaseId"`
	}
	Signature struct {
		State string `json:"state"`
	}
	Rollup struct {
		State string `json:"state"`
	}
	Commit struct {
		OID               string     `json:"oid"`
		CommittedDate     time.Time  `json:"committedDate"`
		Signature         *Signature `json:"signature"`
		StatusCheckRollup *Rollup    `json:"statusCheckRollup"`
	}
	CommitNode struct {
		Commit Commit `json:"commit"`
	}
)

// Nodes is a GraphQL connection.
type Nodes[T any] struct {
	TotalCount int `json:"totalCount"`
	Nodes      []T `json:"nodes"`
}

// Detail is one pullRequest node of the PullRequestDetail query.
type Detail struct {
	Number                   int                  `json:"number"`
	Title                    string               `json:"title"`
	URL                      string               `json:"url"`
	IsDraft                  bool                 `json:"isDraft"`
	IsCrossRepository        bool                 `json:"isCrossRepository"`
	CreatedAt                time.Time            `json:"createdAt"`
	UpdatedAt                time.Time            `json:"updatedAt"`
	AuthorAssociation        string               `json:"authorAssociation"`
	Additions                int                  `json:"additions"`
	Deletions                int                  `json:"deletions"`
	Milestone                *Milestone           `json:"milestone"`
	Author                   *Actor               `json:"author"`
	BaseRefName              string               `json:"baseRefName"`
	BaseRefOid               string               `json:"baseRefOid"`
	HeadRefOid               string               `json:"headRefOid"`
	MergeStateStatus         string               `json:"mergeStateStatus"`
	ReviewDecision           string               `json:"reviewDecision"`
	AutoMergeRequest         *AutoMerge           `json:"autoMergeRequest"`
	ReviewRequests           Nodes[ReviewRequest] `json:"reviewRequests"`
	MyReviews                Nodes[ReviewNode]    `json:"myReviews"`
	LatestOpinionatedReviews Nodes[ReviewNode]    `json:"latestOpinionatedReviews"`
	ReviewThreads            Nodes[ThreadNode]    `json:"reviewThreads"`
	Comments                 Nodes[CommentNode]   `json:"comments"`
	Commits                  Nodes[CommitNode]    `json:"commits"`
	Head                     Nodes[CommitNode]    `json:"head"`
}

func (d *Detail) headCommit() *Commit {
	if n := len(d.Head.Nodes); n > 0 {
		return &d.Head.Nodes[n-1].Commit
	}
	return nil
}

// FingerprintFields makes a detail read a complete observation (§4.2).
func (d *Detail) FingerprintFields() FingerprintFields {
	rollup := ""
	if hc := d.headCommit(); hc != nil && hc.StatusCheckRollup != nil {
		rollup = hc.StatusCheckRollup.State
	}
	return FingerprintFields{
		UpdatedAt: d.UpdatedAt, BaseOID: d.BaseRefOid, HeadOID: d.HeadRefOid, MergeState: d.MergeStateStatus,
		ReviewDecision: d.ReviewDecision, Draft: d.IsDraft, Rollup: rollup,
	}
}

const detailQuery = `query PullRequestDetail($owner: String!, $name: String!, $number: Int!, $viewer: String!) {
  rateLimit { limit cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url isDraft isCrossRepository createdAt updatedAt
      authorAssociation additions deletions
      milestone { title dueOn }
      author { login __typename }
      baseRefName baseRefOid headRefOid mergeStateStatus reviewDecision
      autoMergeRequest { enabledAt }
      reviewRequests(first: 50) { nodes { requestedReviewer { __typename ... on User { login } } } }
      myReviews: reviews(author: $viewer, last: 50) { nodes { state submittedAt commit { oid } } }
      latestOpinionatedReviews(first: 50) { nodes { state submittedAt author { login } } }
      reviewThreads(first: 100) { totalCount nodes { isResolved
        first: comments(first: 1) { nodes { author { login } url } }
        last: comments(last: 1) { nodes { author { login } createdAt } } } }
      comments(last: 100) { nodes { author { login } createdAt updatedAt databaseId } }
      commits(first: 100) { totalCount nodes { commit { oid signature { state } } } }
      head: commits(last: 1) { nodes { commit { oid committedDate statusCheckRollup { state } } } }
    }
  }
}`

const fingerprintsQuery = `query Fingerprints($owner: String!, $name: String!, $endCursor: String) {
  rateLimit { limit cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 100, after: $endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number updatedAt baseRefOid headRefOid mergeStateStatus reviewDecision isDraft
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}`

const checksQuery = `query Checks($owner: String!, $name: String!, $oid: GitObjectID!, $pr: Int!, $endCursor: String) {
  rateLimit { limit cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    object(oid: $oid) { ... on Commit { statusCheckRollup { contexts(first: 100, after: $endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes { __typename
        ... on CheckRun { name status conclusion isRequired(pullRequestNumber: $pr) checkSuite { workflowRun { databaseId } } }
        ... on StatusContext { context state isRequired(pullRequestNumber: $pr) } } } } } }
  }
}`
```

- [ ] **Step 7: Implement the reader**

Create `internal/collect/reader.go`:

```go
package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
)

// ErrTooManyFiles means a comparison lists 300 or more files, so the list may be truncated.
var ErrTooManyFiles = errors.New("comparison lists 300 or more files")

const (
	compareFileLimit = 300
	maxGraphQLPages  = 100
)

// Observed is one open PR seen by a fingerprint poll.
type Observed struct {
	Number      int
	Fingerprint string
	HeadOID     string
}

// Reader is every GitHub read collection needs.
type Reader interface {
	Fingerprints(ctx context.Context, owner, name string) ([]Observed, error)
	Detail(ctx context.Context, owner, name string, number int, viewer string) (*Detail, error)
	Checks(ctx context.Context, owner, name, head string, number int) ([]classify.Check, error)
	AwaitingRuns(ctx context.Context, owner, name, head string) (int, error)
	MarkerConclusions(ctx context.Context, owner, name, head, check string) ([]string, error)
	ChangedFiles(ctx context.Context, owner, name, base, head string) ([]string, error)
	Diff(ctx context.Context, owner, name, base, commit string) ([]byte, error)
	CommentBody(ctx context.Context, owner, name string, id int64) (string, error)
	RulesetTypes(ctx context.Context, owner, name, branch string) ([]string, error)
	ClassicSignatures(ctx context.Context, owner, name, branch string) (bool, error)
}

// GitHubReader implements Reader with the read-only GitHub client.
type GitHubReader struct {
	Client *github.Client
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type fingerprintNode struct {
	Number           int       `json:"number"`
	UpdatedAt        time.Time `json:"updatedAt"`
	BaseRefOid       string    `json:"baseRefOid"`
	HeadRefOid       string    `json:"headRefOid"`
	MergeStateStatus string    `json:"mergeStateStatus"`
	ReviewDecision   string    `json:"reviewDecision"`
	IsDraft          bool      `json:"isDraft"`
	Commits          Nodes[struct {
		Commit struct {
			StatusCheckRollup *Rollup `json:"statusCheckRollup"`
		} `json:"commit"`
	}] `json:"commits"`
}

func (n fingerprintNode) fields() FingerprintFields {
	rollup := ""
	if c := n.Commits.Nodes; len(c) > 0 && c[len(c)-1].Commit.StatusCheckRollup != nil {
		rollup = c[len(c)-1].Commit.StatusCheckRollup.State
	}
	return FingerprintFields{
		UpdatedAt: n.UpdatedAt, BaseOID: n.BaseRefOid, HeadOID: n.HeadRefOid, MergeState: n.MergeStateStatus,
		ReviewDecision: n.ReviewDecision, Draft: n.IsDraft, Rollup: rollup,
	}
}

// Fingerprints lists every open PR with its fingerprint; a repeated cursor is an error (§4.2).
func (g GitHubReader) Fingerprints(ctx context.Context, owner, name string) ([]Observed, error) {
	var out []Observed
	seen := map[string]bool{}
	var cursor *string
	for page := 0; page < maxGraphQLPages; page++ {
		var data struct {
			Repository *struct {
				PullRequests struct {
					PageInfo pageInfo          `json:"pageInfo"`
					Nodes    []fingerprintNode `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "endCursor": cursor}
		if err := g.Client.GraphQL(ctx, "Fingerprints", fingerprintsQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Repository == nil {
			return nil, fmt.Errorf("repository %s/%s not found", owner, name)
		}
		conn := data.Repository.PullRequests
		for _, n := range conn.Nodes {
			out = append(out, Observed{Number: n.Number, Fingerprint: Fingerprint(n.fields()), HeadOID: n.HeadRefOid})
		}
		if !conn.PageInfo.HasNextPage {
			return out, nil
		}
		if seen[conn.PageInfo.EndCursor] {
			return nil, fmt.Errorf("pull request pagination repeated cursor %s", conn.PageInfo.EndCursor)
		}
		seen[conn.PageInfo.EndCursor] = true
		next := conn.PageInfo.EndCursor
		cursor = &next
	}
	return nil, fmt.Errorf("pull requests of %s/%s exceed %d pages", owner, name, maxGraphQLPages)
}

// Detail reads one PR's PullRequestDetail node.
func (g GitHubReader) Detail(ctx context.Context, owner, name string, number int, viewer string) (*Detail, error) {
	var data struct {
		Repository *struct {
			PullRequest *Detail `json:"pullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "number": number, "viewer": viewer}
	if err := g.Client.GraphQL(ctx, "PullRequestDetail", detailQuery, vars, &data); err != nil {
		return nil, err
	}
	if data.Repository == nil || data.Repository.PullRequest == nil {
		return nil, fmt.Errorf("pull request %s/%s#%d not found", owner, name, number)
	}
	return data.Repository.PullRequest, nil
}

type checkNode struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	IsRequired bool   `json:"isRequired"`
	CheckSuite *struct {
		WorkflowRun *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	Context string `json:"context"`
	State   string `json:"state"`
}

func (n checkNode) check() classify.Check {
	if n.Typename != "CheckRun" {
		return classify.Check{Name: n.Context, State: n.State, Required: n.IsRequired}
	}
	state := n.Conclusion
	if state == "" {
		state = n.Status
	}
	var run int64
	if n.CheckSuite != nil && n.CheckSuite.WorkflowRun != nil {
		run = n.CheckSuite.WorkflowRun.DatabaseID
	}
	return classify.Check{Name: n.Name, State: state, Required: n.IsRequired, RunID: run}
}

// Checks reads the head commit's status contexts with required flags for the PR.
func (g GitHubReader) Checks(ctx context.Context, owner, name, head string, number int) ([]classify.Check, error) {
	out := []classify.Check{}
	seen := map[string]bool{}
	var cursor *string
	for page := 0; page < maxGraphQLPages; page++ {
		var data struct {
			Repository struct {
				Object *struct {
					StatusCheckRollup *struct {
						Contexts struct {
							PageInfo pageInfo    `json:"pageInfo"`
							Nodes    []checkNode `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "oid": head, "pr": number, "endCursor": cursor}
		if err := g.Client.GraphQL(ctx, "Checks", checksQuery, vars, &data); err != nil {
			return nil, err
		}
		obj := data.Repository.Object
		if obj == nil || obj.StatusCheckRollup == nil {
			return out, nil
		}
		conn := obj.StatusCheckRollup.Contexts
		for _, n := range conn.Nodes {
			out = append(out, n.check())
		}
		if !conn.PageInfo.HasNextPage {
			return out, nil
		}
		if seen[conn.PageInfo.EndCursor] {
			return nil, fmt.Errorf("check pagination repeated cursor %s", conn.PageInfo.EndCursor)
		}
		seen[conn.PageInfo.EndCursor] = true
		next := conn.PageInfo.EndCursor
		cursor = &next
	}
	return nil, fmt.Errorf("checks exceed %d pages", maxGraphQLPages)
}

func repoPath(owner, name string) string {
	return "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func escapeBranch(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// AwaitingRuns counts workflow runs on head waiting for approval.
func (g GitHubReader) AwaitingRuns(ctx context.Context, owner, name, head string) (int, error) {
	var out struct {
		TotalCount int `json:"total_count"`
	}
	path := fmt.Sprintf("%s/actions/runs?head_sha=%s&status=action_required&per_page=1", repoPath(owner, name), url.QueryEscape(head))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return 0, err
	}
	return out.TotalCount, nil
}

// MarkerConclusions lists the conclusions of runs of one check on head ("" while running).
func (g GitHubReader) MarkerConclusions(ctx context.Context, owner, name, head, check string) ([]string, error) {
	var out struct {
		CheckRuns []struct {
			Conclusion *string `json:"conclusion"`
		} `json:"check_runs"`
	}
	path := fmt.Sprintf("%s/commits/%s/check-runs?check_name=%s&per_page=100", repoPath(owner, name), url.PathEscape(head), url.QueryEscape(check))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	conclusions := make([]string, 0, len(out.CheckRuns))
	for _, r := range out.CheckRuns {
		c := ""
		if r.Conclusion != nil {
			c = *r.Conclusion
		}
		conclusions = append(conclusions, c)
	}
	return conclusions, nil
}

// ChangedFiles lists changed and previous file names of base...head.
func (g GitHubReader) ChangedFiles(ctx context.Context, owner, name, base, head string) ([]string, error) {
	var out struct {
		Files *[]struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		} `json:"files"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s", repoPath(owner, name), url.PathEscape(base), url.PathEscape(head))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	if out.Files == nil {
		return nil, errors.New("comparison has no file list")
	}
	if len(*out.Files) >= compareFileLimit {
		return nil, ErrTooManyFiles
	}
	var names []string
	for _, f := range *out.Files {
		names = append(names, f.Filename)
		if f.PreviousFilename != "" {
			names = append(names, f.PreviousFilename)
		}
	}
	return names, nil
}

// Diff fetches the three-dot diff base...commit.
func (g GitHubReader) Diff(ctx context.Context, owner, name, base, commit string) ([]byte, error) {
	return g.Client.GetDiff(ctx, fmt.Sprintf("%s/compare/%s...%s", repoPath(owner, name), url.PathEscape(base), url.PathEscape(commit)))
}

// CommentBody reads one issue comment body.
func (g GitHubReader) CommentBody(ctx context.Context, owner, name string, id int64) (string, error) {
	var out struct {
		Body string `json:"body"`
	}
	if _, err := g.Client.Get(ctx, fmt.Sprintf("%s/issues/comments/%d", repoPath(owner, name), id), &out); err != nil {
		return "", err
	}
	return out.Body, nil
}

// RulesetTypes lists rule types that apply to a branch across all ruleset pages.
func (g GitHubReader) RulesetTypes(ctx context.Context, owner, name, branch string) ([]string, error) {
	var types []string
	path := fmt.Sprintf("%s/rules/branches/%s?per_page=100", repoPath(owner, name), escapeBranch(branch))
	err := g.Client.GetPages(ctx, path, func(page []byte) error {
		var rulesOnPage []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(page, &rulesOnPage); err != nil {
			return fmt.Errorf("decode rulesets: %w", err)
		}
		for _, r := range rulesOnPage {
			types = append(types, r.Type)
		}
		return nil
	})
	return types, err
}

// ClassicSignatures reads classic branch protection's required-signatures flag.
func (g GitHubReader) ClassicSignatures(ctx context.Context, owner, name, branch string) (bool, error) {
	var out struct {
		Enabled bool `json:"enabled"`
	}
	path := fmt.Sprintf("%s/branches/%s/protection/required_signatures", repoPath(owner, name), escapeBranch(branch))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return false, err
	}
	return out.Enabled, nil
}
```

- [ ] **Step 8: Implement normalization**

Create `internal/collect/normalize.go`:

```go
package collect

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
)

func login(a *Actor) string {
	if a == nil {
		return ""
	}
	return a.Login
}

// Normalize converts a detail node into a Record (§4.3). When mandatory
// evidence is incomplete it returns an error together with the partially
// filled record so an error result keeps its title and link.
func Normalize(d *Detail, viewer string, headChangedAt *time.Time) (classify.Record, error) {
	author, isBot := "ghost", false
	if d.Author != nil && d.Author.Login != "" {
		author = d.Author.Login
		isBot = d.Author.Typename == "Bot" || strings.HasSuffix(author, "[bot]")
	}
	rec := classify.Record{
		Number: d.Number, Title: d.Title, URL: d.URL, Author: author, IsBot: isBot,
		Draft: d.IsDraft, Fork: d.IsCrossRepository, AuthorAssociation: d.AuthorAssociation,
		Additions: d.Additions, Deletions: d.Deletions, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		BaseRef: d.BaseRefName, BaseOID: d.BaseRefOid, HeadOID: d.HeadRefOid,
		MergeState: d.MergeStateStatus, ReviewDecision: d.ReviewDecision, AutoMerge: d.AutoMergeRequest != nil,
		SignaturesComplete: d.Commits.TotalCount <= len(d.Commits.Nodes),
	}
	if headChangedAt != nil {
		t := *headChangedAt
		rec.HeadChangedAt = &t
	}
	if d.Milestone != nil {
		rec.Milestone = &classify.Milestone{Title: d.Milestone.Title, DueOn: d.Milestone.DueOn}
	}
	if hc := d.headCommit(); hc != nil {
		rec.HeadCommittedAt = hc.CommittedDate
	}

	var authorTimes []time.Time
	for _, c := range d.Comments.Nodes {
		if login(c.Author) == author {
			authorTimes = append(authorTimes, c.CreatedAt)
		}
	}
	for _, th := range d.ReviewThreads.Nodes {
		if !th.IsResolved {
			rec.UnresolvedThreads++
		}
		if len(th.Last.Nodes) == 0 || login(th.Last.Nodes[0].Author) != author {
			continue
		}
		last := th.Last.Nodes[0]
		authorTimes = append(authorTimes, last.CreatedAt)
		if !th.IsResolved && len(th.First.Nodes) > 0 && login(th.First.Nodes[0].Author) == viewer {
			rec.MyThreadsWaiting = append(rec.MyThreadsWaiting, classify.Thread{URL: th.First.Nodes[0].URL, LastAt: last.CreatedAt})
		}
	}
	if headChangedAt != nil {
		authorTimes = append(authorTimes, *headChangedAt)
	}
	if len(authorTimes) > 0 {
		latest := slices.MaxFunc(authorTimes, func(a, b time.Time) int { return a.Compare(b) })
		rec.AuthorUpdatedAt = &latest
	}

	for _, rr := range d.ReviewRequests.Nodes {
		if rr.RequestedReviewer != nil && rr.RequestedReviewer.Login == viewer {
			rec.ReviewRequestedFromMe = true
		}
	}
	for _, r := range d.MyReviews.Nodes {
		if r.State == "PENDING" || r.SubmittedAt == nil {
			continue
		}
		commit := ""
		if r.Commit != nil {
			commit = r.Commit.OID
		}
		rec.MyReviews = append(rec.MyReviews, classify.Review{State: r.State, SubmittedAt: *r.SubmittedAt, Commit: commit})
	}
	slices.SortStableFunc(rec.MyReviews, func(a, b classify.Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	for _, r := range d.LatestOpinionatedReviews.Nodes {
		if r.State == "APPROVED" && r.SubmittedAt != nil && (rec.ApprovedAt == nil || r.SubmittedAt.After(*rec.ApprovedAt)) {
			t := *r.SubmittedAt
			rec.ApprovedAt = &t
		}
		if who := login(r.Author); who != "" && who != viewer && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED") {
			rec.OthersDecisive = append(rec.OthersDecisive, classify.OtherReview{Author: who, State: r.State})
		}
	}
	for _, c := range d.Commits.Nodes {
		state := "UNSIGNED"
		if c.Commit.Signature != nil && c.Commit.Signature.State != "" {
			state = c.Commit.Signature.State
		}
		if state != "VALID" {
			rec.UnverifiedCommits = append(rec.UnverifiedCommits, classify.Unverified{SHA: c.Commit.OID, Reason: strings.ToLower(state)})
		}
	}
	if d.ReviewThreads.TotalCount > len(d.ReviewThreads.Nodes) {
		return rec, errors.New("more than 100 review threads; cannot evaluate replies")
	}
	return rec, nil
}
```

- [ ] **Step 9: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/collect/ ./internal/github/ -v`
Expected: PASS.

- [ ] **Step 10: Lint and commit**

```bash
make fmt lint
git add internal/collect internal/github/client.go
git commit -S -m "feat(collect): read fingerprints and PR detail, normalize evidence"
```

---

### Task 10: Evidence collection — follow-up reads, caches, outcomes

**Files:**
- Create: `internal/collect/collect.go`
- Test: `internal/collect/collect_test.go`

**Interfaces:**
- Consumes: Task 9 `Reader`, `Detail`, `Normalize`, `Fingerprint`, `ErrTooManyFiles`; Task 8 `store.Caches`, `store.DiffCache`, `store.CommentCache`, `store.PolicyCache`; Task 6 `github.Retryable`, `*github.StatusError`; Task 3 `classify.ReviewBaseline`, `classify.RecordVersion`; Task 2 `rules.Rules`.
- Produces:
  - `type Cache interface { CachedDiff(ctx, repoID int64, base, reviewed, head string) (bool, bool, error); CachedComment(ctx, repoID, commentID int64, updatedAt time.Time) (bool, bool, error); CachedPolicy(ctx, repoID int64, baseRef string, now time.Time, ttl time.Duration) (string, bool, error) }` (satisfied by `*store.DB`)
  - `type Input struct { RepoID int64; Owner, Name, Viewer string; Number int; Rules rules.Rules; QueuedFingerprint string; HeadChangedAt *time.Time; Now time.Time }`
  - `type Kind int` with `Collected`, `Mismatch`, `Failed`
  - `type Outcome struct { Kind Kind; Record classify.Record; Fingerprint, HeadOID string; Caches store.Caches; Err error; Retryable, RetryableEvidence bool }`
  - `type Collector struct { Reader Reader; Cache Cache; Log *slog.Logger }`; `(*Collector) Collect(ctx, Input) Outcome`
  - `func EffectiveDiff(diff []byte) []byte`
  - `type Source struct`; `func NewSource(r Reader, cache Cache, log *slog.Logger) *Source`; methods `Fingerprints(ctx, owner, name string) ([]Observed, error)` and `Collect(ctx, Input) Outcome`

- [ ] **Step 1: Write the failing tests**

Create `internal/collect/collect_test.go`:

```go
package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
)

type fakeReader struct {
	detail      *Detail
	detailErr   error
	awaiting    int
	awaitingErr error
	markers     map[string][]string
	files       []string
	filesErr    error
	diffs       map[string][]byte
	diffErr     error
	bodies      map[int64]string
	bodyErr     error
	rulesets    []string
	rulesetErr  error
	classic     bool
	classicErr  error
	checks      []classify.Check
	checksErr   error
	calls       map[string]int
}

func (f *fakeReader) count(name string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeReader) Fingerprints(context.Context, string, string) ([]Observed, error) {
	f.count("Fingerprints")
	return nil, nil
}
func (f *fakeReader) Detail(context.Context, string, string, int, string) (*Detail, error) {
	f.count("Detail")
	return f.detail, f.detailErr
}
func (f *fakeReader) Checks(context.Context, string, string, string, int) ([]classify.Check, error) {
	f.count("Checks")
	return f.checks, f.checksErr
}
func (f *fakeReader) AwaitingRuns(context.Context, string, string, string) (int, error) {
	f.count("AwaitingRuns")
	return f.awaiting, f.awaitingErr
}
func (f *fakeReader) MarkerConclusions(_ context.Context, _, _, _, check string) ([]string, error) {
	f.count("MarkerConclusions")
	return f.markers[check], nil
}
func (f *fakeReader) ChangedFiles(context.Context, string, string, string, string) ([]string, error) {
	f.count("ChangedFiles")
	return f.files, f.filesErr
}
func (f *fakeReader) Diff(_ context.Context, _, _, base, commit string) ([]byte, error) {
	f.count("Diff")
	if f.diffErr != nil {
		return nil, f.diffErr
	}
	return f.diffs[base+"..."+commit], nil
}
func (f *fakeReader) CommentBody(_ context.Context, _, _ string, id int64) (string, error) {
	f.count("CommentBody")
	return f.bodies[id], f.bodyErr
}
func (f *fakeReader) RulesetTypes(context.Context, string, string, string) ([]string, error) {
	f.count("RulesetTypes")
	return f.rulesets, f.rulesetErr
}
func (f *fakeReader) ClassicSignatures(context.Context, string, string, string) (bool, error) {
	f.count("ClassicSignatures")
	return f.classic, f.classicErr
}

type fakeCache struct {
	diffs    map[string]bool
	comments map[int64]bool
	policy   string
}

func (c *fakeCache) CachedDiff(_ context.Context, _ int64, base, reviewed, head string) (bool, bool, error) {
	v, ok := c.diffs[base+reviewed+head]
	return v, ok, nil
}
func (c *fakeCache) CachedComment(_ context.Context, _, id int64, _ time.Time) (bool, bool, error) {
	v, ok := c.comments[id]
	return v, ok, nil
}
func (c *fakeCache) CachedPolicy(context.Context, int64, string, time.Time, time.Duration) (string, bool, error) {
	return c.policy, c.policy != "", nil
}

var created = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

func baseDetail() *Detail {
	return &Detail{
		Number: 7, Title: "Fix", URL: "https://github.com/o/r/pull/7", CreatedAt: created, UpdatedAt: created.Add(time.Hour),
		Author: &Actor{Login: "alice", Typename: "User"}, BaseRefName: "main", BaseRefOid: "base", HeadRefOid: "head",
		MergeStateStatus: "CLEAN",
		Commits:          Nodes[CommitNode]{TotalCount: 1, Nodes: []CommitNode{{Commit: Commit{OID: "head", Signature: &Signature{State: "VALID"}}}}},
		Head:             Nodes[CommitNode]{Nodes: []CommitNode{{Commit: Commit{OID: "head", CommittedDate: created}}}},
	}
}

func inputFor(d *Detail, rl rules.Rules) Input {
	return Input{RepoID: 1, Owner: "o", Name: "r", Viewer: "me", Number: d.Number, Rules: rl,
		QueuedFingerprint: Fingerprint(d.FingerprintFields()), Now: created.Add(2 * time.Hour)}
}

func collect(reader *fakeReader, cache *fakeCache, in Input) Outcome {
	if cache == nil {
		cache = &fakeCache{}
	}
	return (&Collector{Reader: reader, Cache: cache}).Collect(context.Background(), in)
}

var forkRules = rules.Rules{StaleDays: 7, TestApproval: rules.TestApproval{
	MarkerChecks: []string{"tests / Test", "tests-skip"}, Command: "/ok-to-test",
	CIPaths: []string{".github/"}, CIPathsCommand: "/ok-to-test confirm-ci",
}}

func TestCollectMismatchStopsBeforeFollowUps(t *testing.T) {
	d := baseDetail()
	in := inputFor(d, rules.Default())
	in.QueuedFingerprint = "stale"
	r := &fakeReader{detail: d}
	out := collect(r, nil, in)
	if out.Kind != Mismatch || out.Fingerprint != Fingerprint(d.FingerprintFields()) || out.HeadOID != "head" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.calls) != 1 {
		t.Fatalf("follow-up reads after mismatch: %v", r.calls)
	}
}

func TestCollectDraftAndOwnSkipFollowUps(t *testing.T) {
	for name, mod := range map[string]func(*Detail){
		"draft": func(d *Detail) { d.IsDraft = true },
		"own":   func(d *Detail) { d.Author = &Actor{Login: "me"} },
	} {
		t.Run(name, func(t *testing.T) {
			d := baseDetail()
			mod(d)
			r := &fakeReader{detail: d}
			out := collect(r, nil, inputFor(d, rules.Default()))
			if out.Kind != Collected || len(r.calls) != 1 || out.Record.Version != classify.RecordVersion {
				t.Fatalf("outcome = %+v calls = %v", out, r.calls)
			}
		})
	}
}

func TestCollectForkTestApproval(t *testing.T) {
	d := baseDetail()
	d.IsCrossRepository = true
	r := &fakeReader{detail: d, markers: map[string][]string{"tests / Test": {"skipped"}}, files: []string{"a.go", ".github/workflows/ci.yml"}}
	out := collect(r, nil, inputFor(d, forkRules))
	rec := out.Record
	if out.Kind != Collected || rec.MarkerPresent == nil || *rec.MarkerPresent || rec.CIPathsTouched == nil || !*rec.CIPathsTouched {
		t.Fatalf("record = %+v", rec)
	}
	if rec.CollectionRulesHash != forkRules.CollectionHash() {
		t.Fatal("collection rules hash not recorded")
	}
	if r.calls["MarkerConclusions"] != 2 || r.calls["AwaitingRuns"] != 1 {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestCollectCIPathsFallbacks(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"transient", &github.StatusError{Status: 502, Message: "bad gateway"}, true},
		{"too many files", ErrTooManyFiles, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDetail()
			d.IsCrossRepository = true
			r := &fakeReader{detail: d, filesErr: tc.err}
			out := collect(r, nil, inputFor(d, forkRules))
			rec := out.Record
			if out.Kind != Collected || rec.CIPathsTouched != nil || len(rec.EvidenceErrors) != 1 ||
				rec.EvidenceErrors[0].Field != "ci_paths" || rec.EvidenceErrors[0].Retryable != tc.retryable || out.RetryableEvidence != tc.retryable {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestCollectFatalFollowUp(t *testing.T) {
	d := baseDetail()
	d.IsCrossRepository = true
	r := &fakeReader{detail: d, awaitingErr: &github.TransportError{Err: errors.New("reset")}}
	out := collect(r, nil, inputFor(d, forkRules))
	if out.Kind != Failed || !out.Retryable || out.Record.Error == "" || out.Record.Title != "Fix" || out.Record.Version != classify.RecordVersion {
		t.Fatalf("outcome = %+v", out)
	}
	r = &fakeReader{detailErr: &github.StatusError{Status: 404, Message: "Not Found"}}
	out = collect(r, nil, inputFor(baseDetail(), rules.Default()))
	if out.Kind != Failed || out.Retryable || out.Record.Number != 7 || out.Record.CollectionRulesHash == "" {
		t.Fatalf("detail failure outcome = %+v", out)
	}
}

func TestCollectLastCommentAt(t *testing.T) {
	d := baseDetail()
	reviewAt := created.Add(2 * time.Hour)
	d.MyReviews = Nodes[ReviewNode]{Nodes: []ReviewNode{{State: "COMMENTED", SubmittedAt: &reviewAt, Commit: &OID{OID: "head"}}}}
	me := &Actor{Login: "me"}
	d.Comments = Nodes[CommentNode]{Nodes: []CommentNode{
		{Author: me, CreatedAt: created.Add(time.Hour), UpdatedAt: created.Add(time.Hour), DatabaseID: 1},
		{Author: me, CreatedAt: created.Add(3 * time.Hour), UpdatedAt: created.Add(3 * time.Hour), DatabaseID: 2},
	}}
	r := &fakeReader{detail: d, bodies: map[int64]string{2: "  /ok-to-test"}}
	out := collect(r, nil, inputFor(d, rules.Default()))
	if out.Record.MyLastCommentAt == nil || !out.Record.MyLastCommentAt.Equal(reviewAt) {
		t.Fatalf("slash command counted as hand-off: %v", out.Record.MyLastCommentAt)
	}
	if len(out.Caches.Comments) != 1 || !out.Caches.Comments[0].IsCommand || r.calls["CommentBody"] != 1 {
		t.Fatalf("comment cache staging = %+v calls %v", out.Caches.Comments, r.calls)
	}
	d.Comments.Nodes = append(d.Comments.Nodes, CommentNode{Author: me, CreatedAt: created.Add(4 * time.Hour), UpdatedAt: created.Add(4 * time.Hour), DatabaseID: 3})
	r = &fakeReader{detail: d, bodies: map[int64]string{3: "looks good"}}
	out = collect(r, &fakeCache{comments: map[int64]bool{2: true}}, inputFor(d, rules.Default()))
	if !out.Record.MyLastCommentAt.Equal(created.Add(4*time.Hour)) || r.calls["CommentBody"] != 1 {
		t.Fatalf("last comment = %v calls %v", out.Record.MyLastCommentAt, r.calls)
	}
	r = &fakeReader{detail: d, bodyErr: &github.StatusError{Status: 500, Message: "x"}}
	if out := collect(r, nil, inputFor(d, rules.Default())); out.Kind != Failed || !out.Retryable {
		t.Fatalf("comment read failure outcome = %+v", out)
	}
}

func approvedOn(d *Detail, commit string) {
	at := created.Add(30 * time.Minute)
	d.MyReviews = Nodes[ReviewNode]{Nodes: []ReviewNode{{State: "APPROVED", SubmittedAt: &at, Commit: &OID{OID: commit}}}}
}

func TestCollectDiffChanged(t *testing.T) {
	cases := []struct {
		name      string
		old, new  string
		changed   bool
		cacheHit  bool
		diffErr   error
		wantNil   bool
		diffCalls int
	}{
		{name: "index-only difference is unchanged", old: "diff\nindex 1..2\n+a\n", new: "diff\nindex 3..4\n+a\n", changed: false, diffCalls: 2},
		{name: "content difference is changed", old: "diff\n+a\n", new: "diff\n+b\n", changed: true, diffCalls: 2},
		{name: "binary keeps index lines", old: "Binary files a and b differ\nindex 1..2\n", new: "Binary files a and b differ\nindex 3..4\n", changed: true, diffCalls: 2},
		{name: "cache hit skips reads", cacheHit: true, changed: true, diffCalls: 0},
		{name: "read failure is unknown", diffErr: &github.StatusError{Status: 502}, wantNil: true, diffCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDetail()
			approvedOn(d, "old")
			r := &fakeReader{detail: d, diffErr: tc.diffErr, diffs: map[string][]byte{"base...old": []byte(tc.old), "base...head": []byte(tc.new)}}
			cache := &fakeCache{}
			if tc.cacheHit {
				cache.diffs = map[string]bool{"baseoldhead": true}
			}
			out := collect(r, cache, inputFor(d, rules.Default()))
			rec := out.Record
			if tc.wantNil {
				if rec.DiffChanged != nil || len(rec.EvidenceErrors) != 1 || !out.RetryableEvidence {
					t.Fatalf("record = %+v", rec)
				}
				return
			}
			if rec.DiffChanged == nil || *rec.DiffChanged != tc.changed || r.calls["Diff"] != tc.diffCalls {
				t.Fatalf("diff changed = %v calls = %v", rec.DiffChanged, r.calls)
			}
			if !tc.cacheHit && (len(out.Caches.Diffs) != 1 || out.Caches.Diffs[0].Changed != tc.changed) {
				t.Fatalf("diff cache staging = %+v", out.Caches.Diffs)
			}
		})
	}
}

func TestCollectChecksOnlyWhenApprovedAndBlocked(t *testing.T) {
	d := baseDetail()
	d.ReviewDecision = "APPROVED"
	d.MergeStateStatus = "BLOCKED"
	r := &fakeReader{detail: d, checks: []classify.Check{{Name: "tests", State: "FAILURE", Required: true}}}
	out := collect(r, nil, inputFor(d, rules.Default()))
	if len(out.Record.Checks) != 1 || r.calls["Checks"] != 1 {
		t.Fatalf("checks = %+v calls %v", out.Record.Checks, r.calls)
	}
	d = baseDetail()
	d.MergeStateStatus = "BLOCKED"
	r = &fakeReader{detail: d}
	collect(r, nil, inputFor(d, rules.Default()))
	if r.calls["Checks"] != 0 {
		t.Fatal("checks read for an unapproved PR")
	}
}

func TestCollectSignaturePolicy(t *testing.T) {
	cases := []struct {
		name      string
		reader    fakeReader
		cache     string
		want      string
		cached    bool
		evidence  bool
		retryable bool
	}{
		{name: "ruleset requires signatures", reader: fakeReader{rulesets: []string{"pull_request", "required_signatures"}}, want: "required", cached: true},
		{name: "classic enabled", reader: fakeReader{classic: true}, want: "required", cached: true},
		{name: "branch not protected", reader: fakeReader{classicErr: &github.StatusError{Status: 404, Message: "Branch not protected"}}, want: "absent", cached: true},
		{name: "no admin permission", reader: fakeReader{classicErr: &github.StatusError{Status: 403, Message: "Must have admin rights"}},
			want: "unavailable", cached: true, evidence: true},
		{name: "transient failure not cached", reader: fakeReader{rulesetErr: &github.StatusError{Status: 502}},
			want: "unavailable", evidence: true, retryable: true},
		{name: "cached answer", cache: "absent", want: "absent"},
		{name: "cached unavailable stays visible", cache: "unavailable", want: "unavailable", evidence: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDetail()
			r := tc.reader
			r.detail = d
			out := collect(&r, &fakeCache{policy: tc.cache}, inputFor(d, rules.Default()))
			rec := out.Record
			if rec.SignaturePolicy != tc.want {
				t.Fatalf("policy = %q, want %q", rec.SignaturePolicy, tc.want)
			}
			if got := len(out.Caches.Policies) == 1; got != tc.cached {
				t.Fatalf("policy cached = %v, want %v", got, tc.cached)
			}
			if got := len(rec.EvidenceErrors) == 1; got != tc.evidence {
				t.Fatalf("evidence errors = %+v", rec.EvidenceErrors)
			}
			if tc.evidence && rec.EvidenceErrors[0].Retryable != tc.retryable {
				t.Fatalf("retryable = %v", rec.EvidenceErrors[0].Retryable)
			}
		})
	}
}

func TestEffectiveDiff(t *testing.T) {
	in := []byte("diff --git a/x b/x\nindex 111..222 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b")
	want := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b"
	if got := string(EffectiveDiff(in)); got != want {
		t.Fatalf("EffectiveDiff = %q", got)
	}
	bin := []byte("diff --git a/i b/i\nindex 1..2\nGIT binary patch\n")
	if string(EffectiveDiff(bin)) != string(bin) {
		t.Fatal("binary diff lost index lines")
	}
}

func TestSourceDelegates(t *testing.T) {
	d := baseDetail()
	r := &fakeReader{detail: d}
	s := NewSource(r, &fakeCache{}, nil)
	if _, err := s.Fingerprints(context.Background(), "o", "r"); err != nil || r.calls["Fingerprints"] != 1 {
		t.Fatalf("Fingerprints delegation: %v %v", err, r.calls)
	}
	if out := s.Collect(context.Background(), inputFor(d, rules.Default())); out.Kind != Collected {
		t.Fatalf("Collect delegation = %+v", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/collect/ -run 'TestCollect|TestEffectiveDiff|TestSource'`
Expected: FAIL to compile (`undefined: Collector`).

- [ ] **Step 3: Implement collection**

Create `internal/collect/collect.go`:

```go
package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

const policyTTL = time.Hour

// Cache is the read side of the evidence caches; *store.DB implements it.
type Cache interface {
	CachedDiff(ctx context.Context, repoID int64, base, reviewed, head string) (bool, bool, error)
	CachedComment(ctx context.Context, repoID, commentID int64, updatedAt time.Time) (bool, bool, error)
	CachedPolicy(ctx context.Context, repoID int64, baseRef string, now time.Time, ttl time.Duration) (string, bool, error)
}

// Input identifies one enrichment attempt.
type Input struct {
	RepoID            int64
	Owner, Name       string
	Viewer            string
	Number            int
	Rules             rules.Rules
	QueuedFingerprint string
	HeadChangedAt     *time.Time
	Now               time.Time
}

// Kind classifies an enrichment outcome.
type Kind int

// Outcome kinds (§4.3, §7).
const (
	Collected Kind = iota + 1
	Mismatch
	Failed
)

// Outcome is the result of one enrichment attempt.
type Outcome struct {
	Kind              Kind
	Record            classify.Record
	Fingerprint       string // Mismatch: the detail's fingerprint
	HeadOID           string // Mismatch: the detail's head
	Caches            store.Caches
	Err               error
	Retryable         bool // Failed: a later attempt may succeed
	RetryableEvidence bool // Collected: an optional read failed transiently
}

// Collector gathers evidence for one PR.
type Collector struct {
	Reader Reader
	Cache  Cache
	Log    *slog.Logger
}

// Collect reads detail and follow-up evidence for in (§4.3).
func (c *Collector) Collect(ctx context.Context, in Input) Outcome {
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log = log.With("repo", in.Owner+"/"+in.Name, "pr", in.Number)
	partial := classify.Record{Version: classify.RecordVersion, CollectionRulesHash: in.Rules.CollectionHash(), Number: in.Number}
	d, err := c.Reader.Detail(ctx, in.Owner, in.Name, in.Number, in.Viewer)
	if err != nil {
		return failed(partial, fmt.Errorf("pull request detail: %w", err))
	}
	log = log.With("head", d.HeadRefOid)
	if fp := Fingerprint(d.FingerprintFields()); fp != in.QueuedFingerprint {
		log.Debug("detail differs from queued fingerprint")
		return Outcome{Kind: Mismatch, Fingerprint: fp, HeadOID: d.HeadRefOid}
	}
	rec, err := Normalize(d, in.Viewer, in.HeadChangedAt)
	rec.Version, rec.CollectionRulesHash = partial.Version, partial.CollectionRulesHash
	if err != nil {
		return failed(rec, err)
	}
	if rec.Draft || rec.Author == in.Viewer {
		return Outcome{Kind: Collected, Record: rec}
	}
	f := &followUp{c: c, in: in, d: d, rec: &rec, log: log}
	if err := f.run(ctx); err != nil {
		return failed(rec, err)
	}
	log.Debug("evidence collected", "evidence_errors", len(rec.EvidenceErrors),
		"staged_caches", len(f.caches.Diffs)+len(f.caches.Comments)+len(f.caches.Policies))
	return Outcome{Kind: Collected, Record: rec, Caches: f.caches, RetryableEvidence: f.retryable}
}

func failed(rec classify.Record, err error) Outcome {
	rec.Error = err.Error()
	return Outcome{Kind: Failed, Record: rec, Err: err, Retryable: github.Retryable(err)}
}

type followUp struct {
	c         *Collector
	in        Input
	d         *Detail
	rec       *classify.Record
	caches    store.Caches
	retryable bool
	log       *slog.Logger
}

func (f *followUp) run(ctx context.Context) error {
	rec := f.rec
	if rec.Fork && !rec.IsBot {
		if err := f.testApproval(ctx); err != nil {
			return fmt.Errorf("test approval evidence: %w", err)
		}
	}
	last, err := f.lastCommentAt(ctx)
	if err != nil {
		return fmt.Errorf("comment evidence: %w", err)
	}
	rec.MyLastCommentAt = last
	f.diffChanged(ctx)
	if (rec.ReviewDecision == "APPROVED" || rec.IsBot) && (rec.MergeState == "BLOCKED" || rec.MergeState == "UNSTABLE") {
		checks, err := f.c.Reader.Checks(ctx, f.in.Owner, f.in.Name, rec.HeadOID, rec.Number)
		if err != nil {
			return fmt.Errorf("required checks: %w", err)
		}
		rec.Checks = checks
	}
	rec.SignaturePolicy = f.signaturePolicy(ctx)
	return nil
}

func (f *followUp) evidence(field string, err error) {
	retry := github.Retryable(err)
	f.rec.EvidenceErrors = append(f.rec.EvidenceErrors, classify.EvidenceError{Field: field, Message: err.Error(), Retryable: retry})
	if retry {
		f.retryable = true
	}
	f.log.Debug("optional evidence unavailable", "field", field, "retryable", retry, "err", err)
}

func (f *followUp) testApproval(ctx context.Context) error {
	rec, in, ta := f.rec, f.in, f.in.Rules.TestApproval
	n, err := f.c.Reader.AwaitingRuns(ctx, in.Owner, in.Name, rec.HeadOID)
	if err != nil {
		return err
	}
	rec.AwaitingRuns = n
	if len(ta.MarkerChecks) > 0 {
		present := false
		for _, check := range ta.MarkerChecks {
			conclusions, err := f.c.Reader.MarkerConclusions(ctx, in.Owner, in.Name, rec.HeadOID, check)
			if err != nil {
				return err
			}
			for _, c := range conclusions {
				// A run that concluded "skipped" was filtered out by its workflow, not approved to run.
				if c != "skipped" {
					present = true
				}
			}
		}
		rec.MarkerPresent = &present
	}
	if len(ta.CIPaths) > 0 && (rec.AwaitingRuns > 0 || (rec.MarkerPresent != nil && !*rec.MarkerPresent)) {
		files, err := f.c.Reader.ChangedFiles(ctx, in.Owner, in.Name, rec.BaseOID, rec.HeadOID)
		if err != nil {
			f.evidence("ci_paths", err)
			return nil
		}
		touched := false
		for _, name := range files {
			for _, prefix := range ta.CIPaths {
				if strings.HasPrefix(name, prefix) {
					touched = true
				}
			}
		}
		rec.CIPathsTouched = &touched
	}
	return nil
}

// lastCommentAt is the viewer's latest hand-off to the author: the newest issue
// comment after the viewer's latest review that is not a slash command, else
// that review's time (§4.3 step 2).
func (f *followUp) lastCommentAt(ctx context.Context) (*time.Time, error) {
	var reviewed *time.Time
	for _, r := range f.rec.MyReviews {
		if reviewed == nil || r.SubmittedAt.After(*reviewed) {
			t := r.SubmittedAt
			reviewed = &t
		}
	}
	nodes := f.d.Comments.Nodes
	for i := len(nodes) - 1; i >= 0; i-- {
		cm := nodes[i]
		if login(cm.Author) != f.in.Viewer {
			continue
		}
		if reviewed != nil && !cm.CreatedAt.After(*reviewed) {
			break
		}
		isCommand, ok, err := f.c.Cache.CachedComment(ctx, f.in.RepoID, cm.DatabaseID, cm.UpdatedAt)
		if err != nil {
			f.log.Warn("comment cache read failed", "err", err)
		}
		f.log.Debug("comment cache", "comment", cm.DatabaseID, "hit", err == nil && ok)
		if err != nil || !ok {
			body, err := f.c.Reader.CommentBody(ctx, f.in.Owner, f.in.Name, cm.DatabaseID)
			if err != nil {
				return nil, err
			}
			isCommand = strings.HasPrefix(strings.TrimLeftFunc(body, unicode.IsSpace), "/")
			f.caches.Comments = append(f.caches.Comments, store.CommentCache{CommentID: cm.DatabaseID, UpdatedAt: cm.UpdatedAt, IsCommand: isCommand})
		}
		if !isCommand {
			t := cm.CreatedAt
			return &t, nil
		}
	}
	return reviewed, nil
}

func (f *followUp) diffChanged(ctx context.Context) {
	rec, in := f.rec, f.in
	baseline := classify.ReviewBaseline(rec.MyReviews)
	if baseline == nil || baseline.Commit == "" || baseline.Commit == rec.HeadOID {
		return
	}
	changed, ok, err := f.c.Cache.CachedDiff(ctx, in.RepoID, rec.BaseOID, baseline.Commit, rec.HeadOID)
	if err != nil {
		f.log.Warn("diff cache read failed", "err", err)
	}
	f.log.Debug("diff cache", "reviewed", baseline.Commit, "hit", err == nil && ok)
	if err == nil && ok {
		rec.DiffChanged = &changed
		return
	}
	reviewed, err := f.c.Reader.Diff(ctx, in.Owner, in.Name, rec.BaseOID, baseline.Commit)
	if err != nil {
		f.evidence("diff", err)
		return
	}
	current, err := f.c.Reader.Diff(ctx, in.Owner, in.Name, rec.BaseOID, rec.HeadOID)
	if err != nil {
		f.evidence("diff", err)
		return
	}
	changed = !bytes.Equal(EffectiveDiff(reviewed), EffectiveDiff(current))
	rec.DiffChanged = &changed
	f.caches.Diffs = append(f.caches.Diffs, store.DiffCache{BaseOID: rec.BaseOID, ReviewedOID: baseline.Commit, HeadOID: rec.HeadOID, Changed: changed})
}

func (f *followUp) signaturePolicy(ctx context.Context) string {
	rec, in := f.rec, f.in
	policy, ok, err := f.c.Cache.CachedPolicy(ctx, in.RepoID, rec.BaseRef, in.Now, policyTTL)
	if err != nil {
		f.log.Warn("policy cache read failed", "err", err)
	}
	f.log.Debug("policy cache", "base_ref", rec.BaseRef, "hit", err == nil && ok)
	if err == nil && ok {
		if policy == "unavailable" {
			f.evidence("signature_policy", errors.New("signature policy unreadable (cached)"))
		}
		return policy
	}
	types, err := f.c.Reader.RulesetTypes(ctx, in.Owner, in.Name, rec.BaseRef)
	if err != nil {
		return f.unavailablePolicy(err)
	}
	for _, t := range types {
		if t == "required_signatures" {
			return f.definitePolicy("required")
		}
	}
	enabled, err := f.c.Reader.ClassicSignatures(ctx, in.Owner, in.Name, rec.BaseRef)
	if err != nil {
		var se *github.StatusError
		if errors.As(err, &se) && se.Status == 404 && strings.Contains(se.Message, "Branch not protected") {
			return f.definitePolicy("absent")
		}
		return f.unavailablePolicy(err)
	}
	if enabled {
		return f.definitePolicy("required")
	}
	return f.definitePolicy("absent")
}

func (f *followUp) definitePolicy(policy string) string {
	f.caches.Policies = append(f.caches.Policies, store.PolicyCache{BaseRef: f.rec.BaseRef, Policy: policy, FetchedAt: f.in.Now})
	return policy
}

func (f *followUp) unavailablePolicy(err error) string {
	f.evidence("signature_policy", err)
	if !github.Retryable(err) {
		f.caches.Policies = append(f.caches.Policies, store.PolicyCache{BaseRef: f.rec.BaseRef, Policy: "unavailable", FetchedAt: f.in.Now})
	}
	return "unavailable"
}

// EffectiveDiff drops "index" lines, whose blob hashes change on rebase, unless
// the diff has a binary section whose identity lives only in those lines.
func EffectiveDiff(diff []byte) []byte {
	lines := bytes.Split(diff, []byte("\n"))
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte("Binary files ")) || bytes.HasPrefix(l, []byte("GIT binary patch")) {
			return diff
		}
	}
	kept := make([][]byte, 0, len(lines))
	for _, l := range lines {
		if !bytes.HasPrefix(l, []byte("index ")) {
			kept = append(kept, l)
		}
	}
	return bytes.Join(kept, []byte("\n"))
}

// Source adapts a Reader and Cache to the poller and enrichment interface.
type Source struct {
	reader    Reader
	collector *Collector
}

// NewSource returns a Source backed by r and cache.
func NewSource(r Reader, cache Cache, log *slog.Logger) *Source {
	return &Source{reader: r, collector: &Collector{Reader: r, Cache: cache, Log: log}}
}

// Fingerprints lists the open PRs of a repository.
func (s *Source) Fingerprints(ctx context.Context, owner, name string) ([]Observed, error) {
	return s.reader.Fingerprints(ctx, owner, name)
}

// Collect enriches one PR.
func (s *Source) Collect(ctx context.Context, in Input) Outcome {
	return s.collector.Collect(ctx, in)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/collect/ -v`
Expected: PASS for all collect tests.

- [ ] **Step 5: Lint and commit**

```bash
make fmt lint
git add internal/collect
git commit -S -m "feat(collect): collect follow-up evidence with caches and outcomes"
```

---

### Task 11: Hub, metrics, poll planning, and polling

**Files:**
- Create: `internal/hub/hub.go`, `internal/metrics/metrics.go`, `internal/syncer/engine.go`, `internal/syncer/plan.go`, `internal/syncer/poll.go`
- Test: `internal/hub/hub_test.go`, `internal/metrics/metrics_test.go`, `internal/syncer/helpers_test.go`, `internal/syncer/plan_test.go`, `internal/syncer/poll_test.go`

**Interfaces:**
- Consumes: Task 8 store (`Repo`, `PRState`, `PollApply`, `Observation`, `ApplyPoll`, `RecordPoll`, `PRStates`, `Repo`, `Account`, `SetAccountStatus`, `ErrStale`, `StatusOK`, `StatusNeedsAuth`, `Transition`, `BucketCount`); Task 10 `collect.Observed`, `collect.Input`, `collect.Outcome`; Task 6 `github.Rate`, `github.ErrUnauthorized`, `github.RateLimitReset`; Task 3 `classify.RecordVersion`.
- Produces:
  - `hub.Event{RepoID int64; Number int}`; `hub.New() *Hub`; `(*Hub) Subscribe() (<-chan Event, func())`, `Publish(Event)`, `Close()`, `Subscribers() int`
  - `metrics.Metrics{PollDuration *prometheus.HistogramVec; PollFailures, GitHubRequests, Transitions *prometheus.CounterVec; RateRemaining, EnrichQueue *prometheus.GaugeVec}`; `metrics.New(prometheus.Registerer) *Metrics`; `(*Metrics) ObserveRequest(kind string, status int)`; `metrics.RegisterPRGauge(prometheus.Registerer, BucketCounter)`
  - `syncer.Source` interface `{ Fingerprints(ctx, owner, name string) ([]collect.Observed, error); Collect(ctx, collect.Input) collect.Outcome }`
  - `syncer.Config{PollInterval time.Duration; Workers int; DispatchEvery, ReclassifyEvery, ResyncEvery time.Duration}`; `syncer.Deps{Store *store.DB; Hub *hub.Hub; Metrics *metrics.Metrics; Log *slog.Logger; Sources func(account string) (Source, error); Now func() time.Time; Rand func() float64}`
  - `syncer.ErrNoToken`; `syncer.New(Config, Deps) *Engine`
  - `(*Engine) PollRepo(ctx, repoID int64) (failures int, err error)`, `ObserveRate(account string, r github.Rate)`, `RateStatus() map[string]time.Time`, `ReposChanged()`, `Wait()`
  - `syncer.Plan(repo store.Repo, states []store.PRState, observed []collect.Observed, now time.Time, recordVersion int) store.PollApply`

- [ ] **Step 1: Add the Prometheus dependency**

```bash
go get github.com/prometheus/client_golang@v1.25.0
```

- [ ] **Step 2: Write the failing hub and metrics tests**

Create `internal/hub/hub_test.go`:

```go
package hub

import "testing"

func TestPublishSubscribe(t *testing.T) {
	h := New()
	ch, unsubscribe := h.Subscribe()
	h.Publish(Event{RepoID: 1, Number: 7})
	if ev := <-ch; ev.RepoID != 1 || ev.Number != 7 {
		t.Fatalf("event = %+v", ev)
	}
	if h.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", h.Subscribers())
	}
	unsubscribe()
	if _, ok := <-ch; ok {
		t.Fatal("channel open after unsubscribe")
	}
	unsubscribe()
}

func TestPublishNeverBlocks(t *testing.T) {
	h := New()
	_, unsubscribe := h.Subscribe()
	defer unsubscribe()
	for i := 0; i < 100; i++ {
		h.Publish(Event{Number: i})
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	h := New()
	ch, _ := h.Subscribe()
	h.Close()
	if _, ok := <-ch; ok {
		t.Fatal("channel open after Close")
	}
	late, _ := h.Subscribe()
	if _, ok := <-late; ok {
		t.Fatal("subscription after Close is open")
	}
	h.Close()
	h.Publish(Event{})
}
```

Create `internal/metrics/metrics_test.go`:

```go
package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/thingzio/devbox/internal/store"
)

type fakeCounts struct {
	counts []store.BucketCount
	err    error
}

func (f fakeCounts) BucketCounts(context.Context) ([]store.BucketCount, error) {
	return f.counts, f.err
}

func TestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.ObserveRequest("graphql", 200)
	m.ObserveRequest("graphql", 200)
	if got := testutil.ToFloat64(m.GitHubRequests.WithLabelValues("graphql", "200")); got != 2 {
		t.Fatalf("requests = %v", got)
	}
	RegisterPRGauge(reg, fakeCounts{counts: []store.BucketCount{{Repo: "o/r", Bucket: "waiting_on_me", Count: 3}}})
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, f := range families {
		if f.GetName() == "devbox_prs" && f.GetMetric()[0].GetGauge().GetValue() == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("devbox_prs gauge missing")
	}
}

func TestPRGaugeReportsErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterPRGauge(reg, fakeCounts{err: errors.New("db closed")})
	if _, err := reg.Gather(); err == nil {
		t.Fatal("store error not surfaced")
	}
}
```

- [ ] **Step 3: Implement hub and metrics**

Create `internal/hub/hub.go`:

```go
// Package hub is an in-process publish/subscribe channel for change events.
package hub

import "sync"

// Event says a PR's stored state or result changed.
type Event struct {
	RepoID int64
	Number int
}

// Hub fans events out to subscribers without blocking publishers.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	closed bool
}

// New returns an open hub.
func New() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

// Subscribe returns an event channel and an idempotent unsubscribe function.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Publish delivers ev to every subscriber with buffer room. A full subscriber
// misses it but re-fetches its whole view on the events it does receive.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Close ends every subscription; later subscriptions are closed immediately.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// Subscribers is the number of active subscriptions.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
```

Create `internal/metrics/metrics.go`:

```go
// Package metrics defines devbox's Prometheus metrics (§10).
package metrics

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/thingzio/devbox/internal/store"
)

// Metrics are the instruments updated by the engine and GitHub client.
type Metrics struct {
	PollDuration   *prometheus.HistogramVec
	PollFailures   *prometheus.CounterVec
	GitHubRequests *prometheus.CounterVec
	RateRemaining  *prometheus.GaugeVec
	EnrichQueue    *prometheus.GaugeVec
	Transitions    *prometheus.CounterVec
}

// New creates and registers the metrics.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		PollDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "devbox_poll_duration_seconds", Help: "Fingerprint poll duration.", Buckets: prometheus.DefBuckets,
		}, []string{"repo"}),
		PollFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_poll_failures_total", Help: "Failed fingerprint polls.",
		}, []string{"repo"}),
		GitHubRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_github_requests_total", Help: "GitHub API requests by kind and HTTP status (0 = transport error).",
		}, []string{"kind", "status"}),
		RateRemaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devbox_github_rate_remaining", Help: "Latest remaining GitHub rate budget.",
		}, []string{"account", "resource"}),
		EnrichQueue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devbox_enrich_queue_depth", Help: "Due enrichment jobs waiting for a worker or a rate-limit pause.",
		}, []string{"account"}),
		Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_transitions_total", Help: "Bucket transitions by destination bucket.",
		}, []string{"to_bucket"}),
	}
	reg.MustRegister(m.PollDuration, m.PollFailures, m.GitHubRequests, m.RateRemaining, m.EnrichQueue, m.Transitions)
	return m
}

// ObserveRequest counts one GitHub request; it matches github.Options.OnRequest.
func (m *Metrics) ObserveRequest(kind string, status int) {
	m.GitHubRequests.WithLabelValues(kind, strconv.Itoa(status)).Inc()
}

// BucketCounter reads open PR counts per repository and bucket; *store.DB implements it.
type BucketCounter interface {
	BucketCounts(ctx context.Context) ([]store.BucketCount, error)
}

// RegisterPRGauge exposes devbox_prs{repo,bucket}, computed on each scrape.
func RegisterPRGauge(reg prometheus.Registerer, src BucketCounter) {
	reg.MustRegister(&prCollector{
		src:  src,
		desc: prometheus.NewDesc("devbox_prs", "Open PRs per enabled repository and bucket.", []string{"repo", "bucket"}, nil),
	})
}

type prCollector struct {
	src  BucketCounter
	desc *prometheus.Desc
}

func (c *prCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *prCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts, err := c.src.BucketCounts(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}
	for _, bc := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(bc.Count), bc.Repo, bc.Bucket)
	}
}
```

Run: `CGO_ENABLED=1 go test -race ./internal/hub/ ./internal/metrics/ -v`
Expected: PASS.

- [ ] **Step 4: Write the failing syncer test helpers and tests**

Create `internal/syncer/helpers_test.go`:

```go
package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	gosync "sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type testClock struct {
	mu  gosync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeSource struct {
	mu        gosync.Mutex
	observed  []collect.Observed
	fpErr     error
	fpHook    func()
	outcome   func(ctx context.Context, in collect.Input) collect.Outcome
	fpCalls   int
	collected map[int]int
	inputs    []collect.Input
}

func (f *fakeSource) Fingerprints(context.Context, string, string) ([]collect.Observed, error) {
	f.mu.Lock()
	f.fpCalls++
	hook, obs, err := f.fpHook, slices.Clone(f.observed), f.fpErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return obs, err
}

func (f *fakeSource) Collect(ctx context.Context, in collect.Input) collect.Outcome {
	f.mu.Lock()
	f.collected[in.Number]++
	f.inputs = append(f.inputs, in)
	fn := f.outcome
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return collectedOutcome(in)
}

func (f *fakeSource) set(update func(f *fakeSource)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

func (f *fakeSource) calls(number int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.collected[number]
}

func goodRecord(in collect.Input) classify.Record {
	return classify.Record{
		Version: classify.RecordVersion, CollectionRulesHash: in.Rules.CollectionHash(), Number: in.Number,
		Title: fmt.Sprintf("PR %d", in.Number), URL: store.PullURL("o", "r", in.Number), Author: "alice",
		CreatedAt: t0, UpdatedAt: t0, HeadOID: "h", HeadCommittedAt: t0, HeadChangedAt: in.HeadChangedAt,
		MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "absent",
	}
}

func collectedOutcome(in collect.Input) collect.Outcome {
	return collect.Outcome{Kind: collect.Collected, Record: goodRecord(in)}
}

func obs(number int, fp string) collect.Observed {
	return collect.Observed{Number: number, Fingerprint: fp, HeadOID: "head-" + fp}
}

type env struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	hub    *hub.Hub
	src    *fakeSource
	clock  *testClock
	eng    *Engine
	repo   store.Repo
	srcErr error
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "devbox.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	acct, err := db.UpsertAccount(ctx, "me", "oauth", []string{"repo"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.AddRepo(ctx, acct.ID, "o", "r", rules.Default(), t0)
	if err != nil {
		t.Fatal(err)
	}
	en := &env{t: t, ctx: ctx, db: db, hub: hub.New(), src: &fakeSource{collected: map[int]int{}}, clock: &testClock{now: t0}, repo: repo}
	en.eng = New(cfg, Deps{
		Store: db, Hub: en.hub, Metrics: metrics.New(prometheus.NewRegistry()),
		Sources: func(string) (Source, error) {
			if en.srcErr != nil {
				return nil, en.srcErr
			}
			return en.src, nil
		},
		Now:  en.clock.Now,
		Rand: func() float64 { return 0.5 },
	})
	return en
}

func (en *env) poll() (int, error) { return en.eng.PollRepo(en.ctx, en.repo.ID) }

func (en *env) mustPoll() {
	en.t.Helper()
	if _, err := en.poll(); err != nil {
		en.t.Fatalf("PollRepo: %v", err)
	}
}

func (en *env) state(number int) store.PRState {
	en.t.Helper()
	states, err := en.db.PRStates(en.ctx, en.repo.ID)
	if err != nil {
		en.t.Fatal(err)
	}
	for _, s := range states {
		if s.Number == number {
			return s
		}
	}
	en.t.Fatalf("pr %d not stored", number)
	return store.PRState{}
}

func (en *env) detail(number int) store.PRDetail {
	en.t.Helper()
	d, err := en.db.PRDetails(en.ctx, "o", "r", number)
	if err != nil || len(d) == 0 {
		en.t.Fatalf("PRDetails(%d) = %v, %v", number, d, err)
	}
	return d[0]
}

func (en *env) transitions(number int) []store.Transition {
	en.t.Helper()
	trs, err := en.db.Transitions(en.ctx, en.repo.ID, number)
	if err != nil {
		en.t.Fatal(err)
	}
	return trs
}
```

Create `internal/syncer/plan_test.go`:

```go
package syncer

import (
	"slices"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func TestPlan(t *testing.T) {
	repo := store.Repo{ID: 1, Revision: 3, Owner: "o", Name: "r", Rules: rules.Default()}
	hash := repo.Rules.CollectionHash()
	now := t0.Add(time.Hour)
	fresh := func(n int, fp string) store.PRState {
		at := now.Add(-time.Minute)
		return store.PRState{Number: n, State: "open", Revision: 5, Fingerprint: fp, HasRecord: true,
			RecordVersion: classify.RecordVersion, RulesHash: hash, RecordAt: &at}
	}
	upserted := func(p store.PollApply) map[int]store.Observation {
		m := map[int]store.Observation{}
		for _, o := range p.Upserts {
			m[o.Number] = o
		}
		return m
	}

	t.Run("new and changed invalidate; unchanged fresh records are skipped", func(t *testing.T) {
		p := Plan(repo, []store.PRState{fresh(1, "a"), fresh(2, "b")}, []collect.Observed{obs(1, "a"), obs(2, "b2"), obs(3, "c")}, now, classify.RecordVersion)
		u := upserted(p)
		if _, ok := u[1]; ok {
			t.Fatal("unchanged fresh PR was written")
		}
		if o := u[2]; !o.Invalidate || !o.Exists || o.ExpectedRevision != 5 {
			t.Fatalf("changed = %+v", o)
		}
		if o := u[3]; !o.Invalidate || o.Exists {
			t.Fatalf("new = %+v", o)
		}
		if p.RepoRevision != 3 || len(p.Closes) != 0 {
			t.Fatalf("plan = %+v", p)
		}
	})

	t.Run("pending unchanged PRs keep revision and deadline", func(t *testing.T) {
		s := fresh(1, "a")
		s.RetryAt = &now
		s.HasRecord = false
		if p := Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion); len(p.Upserts) != 0 {
			t.Fatalf("pending PR rewritten: %+v", p.Upserts)
		}
		if p := Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a2")}, now, classify.RecordVersion); !upserted(p)[1].Invalidate {
			t.Fatal("pending PR with changed fingerprint not invalidated")
		}
	})

	t.Run("stale record version or collection hash invalidates", func(t *testing.T) {
		oldVersion := fresh(1, "a")
		oldVersion.RecordVersion = 0
		otherHash := fresh(2, "b")
		otherHash.RulesHash = "different"
		p := Plan(repo, []store.PRState{oldVersion, otherHash}, []collect.Observed{obs(1, "a"), obs(2, "b")}, now, classify.RecordVersion)
		if u := upserted(p); !u[1].Invalidate || !u[2].Invalidate {
			t.Fatalf("upserts = %+v", p.Upserts)
		}
	})

	t.Run("closed PR that reappears is reopened", func(t *testing.T) {
		s := fresh(1, "a")
		s.State = "closed"
		s.Revision = 9
		if o := upserted(Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion))[1]; !o.Invalidate || o.ExpectedRevision != 9 {
			t.Fatalf("reopen = %+v", o)
		}
	})

	t.Run("open PRs missing from the observation close", func(t *testing.T) {
		closed := fresh(4, "d")
		closed.State = "closed"
		p := Plan(repo, []store.PRState{fresh(1, "a"), fresh(2, "b"), closed}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion)
		if !slices.Equal(p.Closes, []int{2}) {
			t.Fatalf("closes = %v", p.Closes)
		}
	})

	t.Run("safety reconcile is capped at 10 percent, oldest first", func(t *testing.T) {
		var states []store.PRState
		var observed []collect.Observed
		for i := 1; i <= 25; i++ {
			s := fresh(i, "x")
			old := now.Add(-31*time.Minute - time.Duration(i)*time.Minute)
			s.RecordAt = &old
			states = append(states, s)
			observed = append(observed, obs(i, "x"))
		}
		p := Plan(repo, states, observed, now, classify.RecordVersion)
		var got []int
		for _, o := range p.Upserts {
			if o.Invalidate {
				got = append(got, o.Number)
			}
		}
		if !slices.Equal(got, []int{25, 24}) {
			t.Fatalf("safety reconcile = %v, want the two oldest", got)
		}
	})

	t.Run("fatal records reconcile at most every 30 minutes", func(t *testing.T) {
		s := fresh(1, "a")
		old := now.Add(-2 * time.Hour)
		recent := now.Add(-5 * time.Minute)
		s.RecordAt, s.LastEnrichAttemptAt, s.Error = &old, &recent, "boom"
		if p := Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion); len(p.Upserts) != 0 {
			t.Fatalf("fatal record reconciled early: %+v", p.Upserts)
		}
	})
}
```

Create `internal/syncer/poll_test.go`:

```go
package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func TestPollInsertsAndCloses(t *testing.T) {
	en := newEnv(t, Config{})
	events, unsubscribe := en.hub.Subscribe()
	defer unsubscribe()
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{obs(1, "a"), obs(2, "b")} })
	en.mustPoll()
	if s := en.state(1); s.State != "open" || s.RetryAt == nil || !s.RetryAt.Equal(t0) {
		t.Fatalf("new pr state = %+v", s)
	}
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{obs(2, "b")} })
	en.clock.Advance(2 * time.Minute)
	en.mustPoll()
	if s := en.state(1); s.State != "closed" {
		t.Fatalf("missing pr not closed: %+v", s)
	}
	select {
	case ev := <-events:
		if ev.Number != 1 {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("close not published")
	}
}

func TestPollFailureThenRecovery(t *testing.T) {
	en := newEnv(t, Config{PollInterval: 2 * time.Minute})
	en.src.set(func(f *fakeSource) { f.fpErr = &github.TransportError{Err: errors.New("network unreachable")} })
	if failures, err := en.poll(); failures != 1 || err == nil {
		t.Fatalf("first failure = %d, %v", failures, err)
	}
	if failures, _ := en.poll(); failures != 2 {
		t.Fatalf("second failure = %d", failures)
	}
	if d := en.eng.pollDelay(2); d != 8*time.Minute {
		t.Fatalf("backoff after 2 failures = %v, want 8m", d)
	}
	if d := en.eng.pollDelay(5); d != 15*time.Minute {
		t.Fatalf("backoff cap = %v, want 15m", d)
	}
	if d := en.eng.pollDelay(0); d != 2*time.Minute {
		t.Fatalf("steady interval with mid jitter = %v", d)
	}
	statuses, _ := en.db.RepoStatuses(en.ctx)
	if !strings.Contains(statuses[0].Sync.LastError, "network unreachable") || statuses[0].Sync.LastSuccessAt != nil {
		t.Fatalf("failure not recorded: %+v", statuses[0].Sync)
	}
	en.src.set(func(f *fakeSource) { f.fpErr = nil; f.observed = []collect.Observed{obs(1, "a")} })
	en.clock.Advance(8 * time.Minute)
	if failures, err := en.poll(); failures != 0 || err != nil {
		t.Fatalf("recovery = %d, %v", failures, err)
	}
	statuses, _ = en.db.RepoStatuses(en.ctx)
	if statuses[0].Sync.LastError != "" || statuses[0].Sync.ConsecutiveFailures != 0 || !statuses[0].Sync.LastSuccessAt.Equal(en.clock.Now()) {
		t.Fatalf("recovery not recorded: %+v", statuses[0].Sync)
	}
}

func TestPollSkipsUnavailableAccountsAndRepos(t *testing.T) {
	en := newEnv(t, Config{})
	if _, err := en.db.SetRepoEnabled(en.ctx, en.repo.ID, false); err != nil {
		t.Fatal(err)
	}
	en.mustPoll()
	if _, err := en.db.SetRepoEnabled(en.ctx, en.repo.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := en.db.SetAccountStatus(en.ctx, "me", store.StatusNeedsAuth, "x"); err != nil {
		t.Fatal(err)
	}
	en.mustPoll()
	if _, err := en.db.UpsertAccount(en.ctx, "me", "oauth", []string{"repo"}, t0); err != nil {
		t.Fatal(err)
	}
	en.eng.ObserveRate("me", github.Rate{Resource: "graphql", Limit: 5000, Remaining: 100, Reset: t0.Add(time.Hour)})
	en.mustPoll()
	if en.src.fpCalls != 0 {
		t.Fatalf("polled %d times while unavailable", en.src.fpCalls)
	}
	if until, ok := en.eng.RateStatus()["me"]; !ok || !until.Equal(t0.Add(time.Hour)) {
		t.Fatalf("rate status = %v", en.eng.RateStatus())
	}
	en.clock.Advance(61 * time.Minute)
	en.mustPoll()
	if en.src.fpCalls != 1 || len(en.eng.RateStatus()) != 0 {
		t.Fatalf("pause did not lift: calls %d status %v", en.src.fpCalls, en.eng.RateStatus())
	}
}

func TestPollAuthFailures(t *testing.T) {
	cases := []struct {
		name       string
		fpErr      error
		srcErr     error
		wantStatus string
	}{
		{name: "token rejected", fpErr: fmt.Errorf("%w: Bad credentials", github.ErrUnauthorized), wantStatus: store.StatusNeedsAuth},
		{name: "keychain unavailable", srcErr: errors.New("keychain locked"), wantStatus: store.StatusNeedsAuth},
		{name: "sso is per org", fpErr: &github.SSORequiredError{URL: "https://github.com/orgs/o/sso"}, wantStatus: store.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			en := newEnv(t, Config{})
			en.srcErr = tc.srcErr
			en.src.set(func(f *fakeSource) { f.fpErr = tc.fpErr })
			if _, err := en.poll(); err == nil {
				t.Fatal("poll succeeded")
			}
			acct, _ := en.db.Account(en.ctx, "me")
			if acct.Status != tc.wantStatus {
				t.Fatalf("account status = %s (%s)", acct.Status, acct.StatusDetail)
			}
			statuses, _ := en.db.RepoStatuses(en.ctx)
			if statuses[0].Sync.LastError == "" {
				t.Fatal("poll error not recorded")
			}
		})
	}
}

func TestPollDiscardsWhenRepoChangesMidPoll(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{obs(1, "a")}
		f.fpHook = func() {
			rl := rules.Default()
			rl.StaleDays = 3
			if _, err := en.db.UpdateRepoRules(context.Background(), en.repo.ID, rl, t0); err != nil {
				t.Errorf("UpdateRepoRules: %v", err)
			}
		}
	})
	if failures, err := en.poll(); failures != 0 || err != nil {
		t.Fatalf("poll = %d, %v", failures, err)
	}
	if states, _ := en.db.PRStates(en.ctx, en.repo.ID); len(states) != 0 {
		t.Fatalf("stale observation persisted: %+v", states)
	}
}

func TestObservedHeadChangeIsTimestampedByObservation(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{{Number: 1, Fingerprint: "a", HeadOID: "h1"}} })
	en.mustPoll()
	if en.detail(1).HeadChangedAt != nil {
		t.Fatal("first observation counted as a push")
	}
	en.clock.Advance(5 * time.Minute)
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{{Number: 1, Fingerprint: "b", HeadOID: "h2"}} })
	en.mustPoll()
	if got := en.detail(1).HeadChangedAt; got == nil || !got.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("head changed at = %v, want observation time", got)
	}
}
```

- [ ] **Step 5: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/syncer/`
Expected: FAIL to compile (`undefined: New`, `Plan`).

- [ ] **Step 6: Implement the engine core**

Create `internal/syncer/engine.go`:

```go
// Package syncer polls GitHub, schedules enrichment, and keeps stored results
// current (§7). It is the spec's "sync" package, renamed to avoid shadowing
// the standard library.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	gosync "sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/store"
)

// ErrNoToken wraps failures to obtain an account's token (missing or keychain error).
var ErrNoToken = errors.New("no usable token for account")

// Source is one account's view of GitHub; *collect.Source implements it.
type Source interface {
	Fingerprints(ctx context.Context, owner, name string) ([]collect.Observed, error)
	Collect(ctx context.Context, in collect.Input) collect.Outcome
}

// Config tunes the engine; zero values take defaults.
type Config struct {
	PollInterval    time.Duration // default 2m
	Workers         int           // concurrent enrichments per account, default 4
	DispatchEvery   time.Duration // default 1s
	ReclassifyEvery time.Duration // default 5m
	ResyncEvery     time.Duration // poller set reconciliation, default 30s
}

// Deps are the engine's collaborators.
type Deps struct {
	Store   *store.DB
	Hub     *hub.Hub
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Sources func(account string) (Source, error)
	Now     func() time.Time
	Rand    func() float64 // [0, 1), for jitter
}

const (
	maxPollBackoff     = 15 * time.Minute
	safetyAge          = 30 * time.Minute
	staleEvidenceLimit = 30 * time.Minute
	retryBase          = 30 * time.Second
	retryCap           = 2 * time.Minute
	dispatchBatch      = 256
)

type prKey struct {
	repoID int64
	number int
}

// Engine runs pollers, enrichment workers, and reclassification.
type Engine struct {
	cfg       Config
	d         Deps
	mu        gosync.Mutex
	jobs      map[prKey]context.CancelFunc
	sems      map[string]chan struct{}
	paused    map[string]time.Time
	reclassMu gosync.Mutex
	wg        gosync.WaitGroup
	wake      chan struct{}
	resync    chan struct{}
}

// New returns an engine with defaults applied.
func New(cfg Config, d Deps) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Minute
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.DispatchEvery <= 0 {
		cfg.DispatchEvery = time.Second
	}
	if cfg.ReclassifyEvery <= 0 {
		cfg.ReclassifyEvery = 5 * time.Minute
	}
	if cfg.ResyncEvery <= 0 {
		cfg.ResyncEvery = 30 * time.Second
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Rand == nil {
		d.Rand = rand.Float64 //nolint:gosec // jitter needs no cryptographic randomness
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Metrics == nil {
		d.Metrics = metrics.New(prometheus.NewRegistry())
	}
	if d.Hub == nil {
		d.Hub = hub.New()
	}
	return &Engine{
		cfg: cfg, d: d,
		jobs: map[prKey]context.CancelFunc{}, sems: map[string]chan struct{}{}, paused: map[string]time.Time{},
		wake: make(chan struct{}, 1), resync: make(chan struct{}, 1),
	}
}

func (e *Engine) now() time.Time { return e.d.Now().UTC() }

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ReposChanged asks the run loop to start or stop pollers now.
func (e *Engine) ReposChanged() { signal(e.resync) }

// Wait blocks until every started enrichment job has finished.
func (e *Engine) Wait() { e.wg.Wait() }

// ObserveRate records a rate reading and pauses the account below 10% remaining (§7).
func (e *Engine) ObserveRate(account string, r github.Rate) {
	if r.Resource != "" {
		e.d.Metrics.RateRemaining.WithLabelValues(account, r.Resource).Set(float64(r.Remaining))
	}
	if r.Limit > 0 && r.Remaining*10 < r.Limit {
		e.pauseUntil(account, r.Reset, fmt.Sprintf("%s rate limit low (%d of %d left)", r.Resource, r.Remaining, r.Limit))
	}
}

func (e *Engine) pauseUntil(account string, until time.Time, why string) {
	now := e.now()
	if !until.After(now) {
		until = now.Add(time.Minute)
	}
	e.mu.Lock()
	extended := until.After(e.paused[account])
	if extended {
		e.paused[account] = until
	}
	e.mu.Unlock()
	if extended {
		e.d.Log.Info("pausing account", "account", account, "until", until, "reason", why)
	}
}

func (e *Engine) isPaused(account string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	until, ok := e.paused[account]
	if !ok {
		return false
	}
	if !e.now().Before(until) {
		delete(e.paused, account)
		return false
	}
	return true
}

// RateStatus returns accounts paused for rate limits and when they resume.
func (e *Engine) RateStatus() map[string]time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := map[string]time.Time{}
	for account, until := range e.paused {
		if now.Before(until) {
			out[account] = until
		}
	}
	return out
}

// accountBlocked pauses the account on rate exhaustion, or marks it
// needs_auth on a rejected or unavailable token, and reports whether it did.
func (e *Engine) accountBlocked(ctx context.Context, account string, err error) bool {
	if reset, ok := github.RateLimitReset(err); ok {
		e.pauseUntil(account, reset, "rate limit exhausted")
		return true
	}
	if !errors.Is(err, github.ErrUnauthorized) && !errors.Is(err, ErrNoToken) {
		return false
	}
	detail := "token rejected by GitHub; re-authenticate"
	if errors.Is(err, ErrNoToken) {
		detail = err.Error()
	}
	if serr := e.d.Store.SetAccountStatus(ctx, account, store.StatusNeedsAuth, detail); serr != nil {
		e.d.Log.Error("mark account needs_auth", "account", account, "err", serr)
	}
	e.d.Log.Warn("account needs re-authentication", "account", account, "err", err)
	return true
}

func (e *Engine) semaphore(account string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	sem, ok := e.sems[account]
	if !ok {
		sem = make(chan struct{}, e.cfg.Workers)
		e.sems[account] = sem
	}
	return sem
}

func (e *Engine) reserve(ctx context.Context, k prKey) (context.Context, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, busy := e.jobs[k]; busy {
		return nil, false
	}
	jctx, cancel := context.WithCancel(ctx)
	e.jobs[k] = cancel
	return jctx, true
}

func (e *Engine) release(k prKey) {
	e.mu.Lock()
	cancel, ok := e.jobs[k]
	delete(e.jobs, k)
	e.mu.Unlock()
	if ok {
		cancel()
	}
}

func (e *Engine) cancelJob(k prKey) {
	e.mu.Lock()
	cancel := e.jobs[k]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) cancelRepo(repoID int64) {
	e.mu.Lock()
	var cancels []context.CancelFunc
	for k, cancel := range e.jobs {
		if k.repoID == repoID {
			cancels = append(cancels, cancel)
		}
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (e *Engine) published(log *slog.Logger, repoID int64, number int, tr *store.Transition) {
	if tr != nil {
		from := ""
		if tr.From != nil {
			from = *tr.From
		}
		log.Info("bucket transition", "pr", number, "from", from, "to", tr.To, "reason", tr.Reason)
		e.d.Metrics.Transitions.WithLabelValues(tr.To).Inc()
	}
	e.d.Hub.Publish(hub.Event{RepoID: repoID, Number: number})
}
```

- [ ] **Step 7: Implement planning and polling**

Create `internal/syncer/plan.go`:

```go
package syncer

import (
	"slices"
	"time"

	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/store"
)

type safetyCandidate struct {
	obs  store.Observation
	last *time.Time
}

// Plan turns a complete fingerprint observation into store writes (§7 steps 3-4).
// It is pure: the store applies the plan under revision guards.
func Plan(repo store.Repo, states []store.PRState, observed []collect.Observed, now time.Time, recordVersion int) store.PollApply {
	p := store.PollApply{RepoID: repo.ID, RepoRevision: repo.Revision, Owner: repo.Owner, Name: repo.Name, Now: now}
	rulesHash := repo.Rules.CollectionHash()
	byNumber := make(map[int]store.PRState, len(states))
	for _, s := range states {
		byNumber[s.Number] = s
	}
	seen := make(map[int]bool, len(observed))
	var safety []safetyCandidate
	for _, o := range observed {
		seen[o.Number] = true
		s, exists := byNumber[o.Number]
		ob := store.Observation{Number: o.Number, Fingerprint: o.Fingerprint, HeadOID: o.HeadOID, Exists: exists, ExpectedRevision: s.Revision}
		switch {
		case !exists, s.State != "open", s.Fingerprint != o.Fingerprint:
			ob.Invalidate = true
		case s.RetryAt != nil:
			continue // already pending: keep its revision and retry deadline
		case !s.HasRecord, s.RecordVersion != recordVersion, s.RulesHash != rulesHash:
			ob.Invalidate = true
		default:
			if last := lastCollection(s); last == nil || now.Sub(*last) >= safetyAge {
				safety = append(safety, safetyCandidate{obs: ob, last: last})
			}
			continue
		}
		p.Upserts = append(p.Upserts, ob)
	}
	slices.SortFunc(safety, func(a, b safetyCandidate) int {
		switch {
		case a.last == nil && b.last == nil:
			return a.obs.Number - b.obs.Number
		case a.last == nil:
			return -1
		case b.last == nil:
			return 1
		}
		return a.last.Compare(*b.last)
	})
	limit := max(1, len(observed)/10)
	for i := 0; i < len(safety) && i < limit; i++ {
		ob := safety[i].obs
		ob.Invalidate = true
		p.Upserts = append(p.Upserts, ob)
	}
	for _, s := range states {
		if s.State == "open" && !seen[s.Number] {
			p.Closes = append(p.Closes, s.Number)
		}
	}
	slices.Sort(p.Closes)
	return p
}

// lastCollection is when the PR was last collected for safety-reconcile
// purposes; a fatal attempt bounds it without counting as success (§7).
func lastCollection(s store.PRState) *time.Time {
	last := s.RecordAt
	if s.Error != "" && s.LastEnrichAttemptAt != nil && (last == nil || s.LastEnrichAttemptAt.After(*last)) {
		last = s.LastEnrichAttemptAt
	}
	return last
}
```

Create `internal/syncer/poll.go`:

```go
package syncer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

// PollRepo runs one fingerprint poll (§7) and returns the repository's
// consecutive failure count for backoff.
func (e *Engine) PollRepo(ctx context.Context, repoID int64) (int, error) {
	repo, err := e.d.Store.Repo(ctx, repoID)
	if err != nil {
		return 0, err
	}
	if !repo.Enabled {
		return 0, nil
	}
	acct, err := e.d.Store.Account(ctx, repo.Account)
	if err != nil {
		return 0, err
	}
	if acct.Status != store.StatusOK || e.isPaused(repo.Account) {
		return 0, nil
	}
	log := e.d.Log.With("repo", repo.FullName(), "poll_id", rand.Text()[:8])
	start := e.now()
	src, err := e.d.Sources(repo.Account)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrNoToken, err)
		e.accountBlocked(ctx, repo.Account, err)
		return e.recordPoll(ctx, log, repo, start, err)
	}
	observed, err := src.Fingerprints(ctx, repo.Owner, repo.Name)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		e.accountBlocked(ctx, repo.Account, err)
		return e.recordPoll(ctx, log, repo, start, err)
	}
	states, err := e.d.Store.PRStates(ctx, repo.ID)
	if err != nil {
		return 0, err
	}
	plan := Plan(repo, states, observed, start, classify.RecordVersion)
	for _, o := range plan.Upserts {
		log.Debug("pr observed", "pr", o.Number, "new", !o.Exists, "invalidate", o.Invalidate, "head", o.HeadOID)
	}
	applied, err := e.d.Store.ApplyPoll(ctx, plan)
	if errors.Is(err, store.ErrStale) {
		log.Debug("poll discarded: repository changed during poll")
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, tr := range applied.Closed {
		e.cancelJob(prKey{repo.ID, tr.Number})
		e.published(log, repo.ID, tr.Number, &tr)
	}
	elapsed := e.now().Sub(start)
	e.d.Metrics.PollDuration.WithLabelValues(repo.FullName()).Observe(elapsed.Seconds())
	log.Debug("poll complete", "prs", len(observed), "inserted", len(applied.Inserted), "updated", len(applied.Updated),
		"skipped", len(applied.Skipped), "closed", len(applied.Closed), "duration", elapsed)
	if len(plan.Upserts) > 0 {
		signal(e.wake)
	}
	return e.recordPoll(ctx, log, repo, start, nil)
}

func (e *Engine) recordPoll(ctx context.Context, log *slog.Logger, repo store.Repo, at time.Time, pollErr error) (int, error) {
	s, err := e.d.Store.RecordPoll(ctx, repo.ID, at, pollErr)
	if err != nil {
		return 0, errors.Join(pollErr, err)
	}
	if pollErr == nil {
		return 0, nil
	}
	e.d.Metrics.PollFailures.WithLabelValues(repo.FullName()).Inc()
	log.Warn("poll failed", "err", pollErr, "consecutive_failures", s.ConsecutiveFailures)
	return s.ConsecutiveFailures, pollErr
}

// pollDelay is the jittered interval, or min(interval × 2^failures, 15m) after failures (§7).
func (e *Engine) pollDelay(failures int) time.Duration {
	if failures <= 0 {
		return time.Duration(float64(e.cfg.PollInterval) * (0.9 + 0.2*e.d.Rand()))
	}
	d := e.cfg.PollInterval
	for i := 0; i < failures && d < maxPollBackoff; i++ {
		d *= 2
	}
	return min(d, maxPollBackoff)
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/syncer/ ./internal/hub/ ./internal/metrics/ -v`
Expected: PASS.

- [ ] **Step 9: Lint and commit**

```bash
go mod tidy
make fmt lint
git add go.mod go.sum internal/hub internal/metrics internal/syncer
git commit -S -m "feat(syncer): add hub, metrics, poll planning, and fingerprint polling"
```

---

### Task 12: Enrichment jobs, reclassification, and the run loop

**Files:**
- Create: `internal/syncer/job.go`, `internal/syncer/reclassify.go`, `internal/syncer/run.go`
- Test: `internal/syncer/job_test.go`, `internal/syncer/run_test.go`

**Interfaces:**
- Consumes: Task 11 engine internals; Task 8 `store.Work`, `DueWork`, `EnrichCommit`, `CommitEnrichment`, `DetailObservation`, `CommitObservation`, `Classifiable`, `Reclassifiable`, `CommitResult`, `ResultRow`; Task 10 `collect.Outcome` kinds; Task 4 `classify.Classify`, `classify.Result`, `classify.CoreRetry`.
- Produces:
  - `(*Engine) DispatchDue(ctx) (int, error)`, `RunJob(ctx, store.Work)`
  - `(*Engine) Reclassify(ctx, repoID int64) error` (0 = all)
  - `(*Engine) RulesChanged(ctx, repoID int64, collectionChanged bool) error`
  - `(*Engine) Run(ctx) error` — blocks until ctx is cancelled and all pollers and jobs have stopped

- [ ] **Step 1: Write the failing job tests**

Create `internal/syncer/job_test.go`:

```go
package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func (en *env) dispatch() int {
	en.t.Helper()
	n, err := en.eng.DispatchDue(en.ctx)
	if err != nil {
		en.t.Fatalf("DispatchDue: %v", err)
	}
	en.eng.Wait()
	return n
}

func (en *env) observeAndEnrich(observed ...collect.Observed) {
	en.t.Helper()
	en.src.set(func(f *fakeSource) { f.observed = observed })
	en.mustPoll()
	en.dispatch()
}

func TestDispatchCommitsCollectedRecords(t *testing.T) {
	en := newEnv(t, Config{})
	events, unsubscribe := en.hub.Subscribe()
	defer unsubscribe()
	en.observeAndEnrich(obs(1, "a"))
	d := en.detail(1)
	if d.Result == nil || d.Result.Bucket != string(classify.NeedsInitialReview) || d.RetryAt != nil || d.Title != "PR 1" || d.RecordAt == nil {
		t.Fatalf("after enrichment = %+v result %+v", d, d.Result)
	}
	if in := en.src.inputs[0]; in.QueuedFingerprint != "a" || in.Viewer != "me" || in.Owner != "o" || in.RepoID != en.repo.ID {
		t.Fatalf("collect input = %+v", in)
	}
	if len(events) == 0 {
		t.Fatal("no change event published")
	}
	trs := en.transitions(1)
	if len(trs) != 1 || trs[0].From != nil || trs[0].To != string(classify.NeedsInitialReview) {
		t.Fatalf("transitions = %+v", trs)
	}
}

func TestOnlyChangedPRsAreEnriched(t *testing.T) {
	en := newEnv(t, Config{})
	en.observeAndEnrich(obs(1, "a"), obs(2, "b"))
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a"), obs(2, "b"))
	if en.src.calls(1) != 1 || en.src.calls(2) != 1 {
		t.Fatalf("unchanged PRs re-enriched: %v", en.src.collected)
	}
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a"), obs(2, "b2"))
	if en.src.calls(1) != 1 || en.src.calls(2) != 2 {
		t.Fatalf("calls after one change = %v", en.src.collected)
	}
}

func TestMismatchCommitsObservationAndRedispatchesOnce(t *testing.T) {
	en := newEnv(t, Config{})
	first := true
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{obs(1, "a")}
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			if first {
				first = false
				return collect.Outcome{Kind: collect.Mismatch, Fingerprint: "a2", HeadOID: "head-new"}
			}
			return collectedOutcome(in)
		}
	})
	en.mustPoll()
	en.dispatch()
	s := en.state(1)
	if s.Fingerprint != "a2" || s.RetryAt == nil || s.HasRecord || en.detail(1).HeadChangedAt == nil {
		t.Fatalf("detail observation not committed: %+v", s)
	}
	en.dispatch()
	if en.src.calls(1) != 2 || en.src.inputs[1].QueuedFingerprint != "a2" || en.src.inputs[1].HeadChangedAt == nil {
		t.Fatalf("re-dispatch inputs = %+v", en.src.inputs)
	}
	if started := en.dispatch(); started != 0 || en.src.calls(1) != 2 {
		t.Fatalf("detail reads repeated: started %d calls %d", started, en.src.calls(1))
	}
}

func TestRetryableFatalKeepsLastGoodResultUntilBound(t *testing.T) {
	en := newEnv(t, Config{})
	en.observeAndEnrich(obs(1, "a"))
	good := en.detail(1).Result.Bucket
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			rec := classify.Record{Number: in.Number}
			return collect.Outcome{Kind: collect.Failed, Record: rec, Err: errors.New("HTTP 502"), Retryable: true}
		}
	})
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a2"))
	d := en.detail(1)
	if d.Result.Bucket != good || !slices.ContainsFunc(d.Result.Notes, func(n string) bool { return strings.HasPrefix(n, "evidence stale since") }) {
		t.Fatalf("stale evidence not retained: %+v", d.Result)
	}
	if d.Error != "HTTP 502" || d.EnrichFailures != 1 || d.RetryAt == nil || !d.RetryAt.Equal(en.clock.Now().Add(30*time.Second)) {
		t.Fatalf("retry bookkeeping = %+v", d)
	}
	if len(en.transitions(1)) != 1 {
		t.Fatalf("keep-stale wrote a transition: %+v", en.transitions(1))
	}
	en.clock.Advance(31 * time.Minute)
	en.dispatch()
	d = en.detail(1)
	if d.Result.Bucket != string(classify.Unclassified) || d.EnrichFailures != 2 {
		t.Fatalf("after bound = %+v %+v", d, d.Result)
	}
	if trs := en.transitions(1); len(trs) != 2 || trs[1].To != string(classify.Unclassified) {
		t.Fatalf("transitions = %+v", trs)
	}
}

func TestNonRetryableFatalIsUnclassifiedWithoutFastRetry(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			return collect.Outcome{Kind: collect.Failed, Record: classify.Record{Number: in.Number, Title: "T"}, Err: errors.New("more than 100 review threads")}
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	d := en.detail(1)
	if d.Result.Bucket != string(classify.Unclassified) || d.RetryAt != nil || d.EnrichFailures != 0 || d.RecordAt != nil || d.Title != "T" {
		t.Fatalf("non-retryable fatal = %+v %+v", d, d.Result)
	}
	var rec classify.Record
	if err := json.Unmarshal(d.Record, &rec); err != nil || rec.Version != classify.RecordVersion || rec.CollectionRulesHash == "" {
		t.Fatalf("error record = %+v, %v", rec, err)
	}
}

func TestRetryableEvidenceSchedulesRetryThenClears(t *testing.T) {
	en := newEnv(t, Config{})
	retry := true
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			out := collectedOutcome(in)
			out.RetryableEvidence = retry
			return out
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	if d := en.detail(1); d.RetryAt == nil || d.EnrichFailures != 1 || d.RecordAt == nil {
		t.Fatalf("evidence retry = %+v", d)
	}
	retry = false
	en.clock.Advance(time.Minute)
	en.dispatch()
	if d := en.detail(1); d.RetryAt != nil || d.EnrichFailures != 0 {
		t.Fatalf("retry not cleared = %+v", d)
	}
}

func TestFailedWorkResumesAfterRestart(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			return collect.Outcome{Kind: collect.Failed, Err: &github.TransportError{Err: errors.New("reset")}, Retryable: true, Record: classify.Record{Number: in.Number}}
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	en.src.set(func(f *fakeSource) { f.outcome = nil })
	restarted := New(Config{}, Deps{Store: en.db, Hub: en.hub, Sources: func(string) (Source, error) { return en.src, nil }, Now: en.clock.Now, Rand: func() float64 { return 0.5 }})
	en.clock.Advance(time.Minute)
	if _, err := restarted.DispatchDue(en.ctx); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	if d := en.detail(1); d.Result.Bucket != string(classify.NeedsInitialReview) || d.RetryAt != nil {
		t.Fatalf("after restart = %+v", d.Result)
	}
}

func TestLateCompletionIsDiscarded(t *testing.T) {
	for _, lifecycle := range []string{"close", "disable", "delete"} {
		t.Run(lifecycle, func(t *testing.T) {
			en := newEnv(t, Config{})
			release := make(chan struct{})
			started := make(chan struct{})
			en.src.set(func(f *fakeSource) {
				f.observed = []collect.Observed{obs(1, "a")}
				f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
					close(started)
					<-release
					return collectedOutcome(in)
				}
			})
			en.mustPoll()
			if _, err := en.eng.DispatchDue(en.ctx); err != nil {
				t.Fatal(err)
			}
			<-started
			switch lifecycle {
			case "close":
				en.src.set(func(f *fakeSource) { f.observed = nil })
				en.mustPoll()
			case "disable":
				if _, err := en.db.SetRepoEnabled(en.ctx, en.repo.ID, false); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := en.db.DeleteRepo(en.ctx, en.repo.ID); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			en.eng.Wait()
			details, err := en.db.PRDetails(en.ctx, "o", "r", 1)
			if err != nil {
				t.Fatal(err)
			}
			if lifecycle == "delete" {
				if len(details) != 0 {
					t.Fatalf("late completion recreated a deleted repo's PR: %+v", details)
				}
				return
			}
			if len(details) != 1 || details[0].Result != nil || details[0].RecordAt != nil {
				t.Fatalf("late completion committed: %+v", details)
			}
		})
	}
}

func TestBlockedAccountLeavesWorkPending(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			return collect.Outcome{Kind: collect.Failed, Err: fmt.Errorf("%w: revoked", github.ErrUnauthorized), Record: classify.Record{Number: in.Number}}
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	acct, _ := en.db.Account(en.ctx, "me")
	if acct.Status != store.StatusNeedsAuth {
		t.Fatalf("account = %+v", acct)
	}
	if d := en.detail(1); d.Result != nil || d.RetryAt == nil {
		t.Fatalf("blocked job committed: %+v", d)
	}
	if started := en.dispatch(); started != 0 {
		t.Fatalf("dispatched %d jobs for a needs_auth account", started)
	}
}

func TestDispatchHonorsPauseAndWorkerLimit(t *testing.T) {
	en := newEnv(t, Config{Workers: 1})
	release := make(chan struct{})
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{obs(1, "a"), obs(2, "b")}
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			<-release
			return collectedOutcome(in)
		}
	})
	en.mustPoll()
	n, err := en.eng.DispatchDue(en.ctx)
	if err != nil || n != 1 {
		t.Fatalf("started = %d, %v; want 1 with one worker", n, err)
	}
	if again, _ := en.eng.DispatchDue(en.ctx); again != 0 {
		t.Fatalf("reserved PR dispatched twice: %d", again)
	}
	close(release)
	en.eng.Wait()
	en.eng.ObserveRate("me", github.Rate{Resource: "core", Limit: 5000, Remaining: 1, Reset: t0.Add(time.Hour)})
	if started := en.dispatch(); started != 0 {
		t.Fatalf("dispatched %d jobs while paused", started)
	}
}

func TestRulesChanged(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			out := collectedOutcome(in)
			out.Record.MyReviews = []classify.Review{{State: "CHANGES_REQUESTED", SubmittedAt: t0, Commit: "h"}}
			out.Record.MyLastCommentAt = &t0
			return out
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	en.clock.Advance(5 * 24 * time.Hour)
	if b := en.detail(1).Result.Bucket; b != string(classify.WaitingOnAuthor) {
		t.Fatalf("bucket = %s", b)
	}
	staleSoon := rules.Default()
	staleSoon.StaleDays = 3
	upd, err := en.db.UpdateRepoRules(en.ctx, en.repo.ID, staleSoon, en.clock.Now())
	if err != nil || upd.CollectionChanged {
		t.Fatalf("stale-only update = %+v, %v", upd, err)
	}
	if err := en.eng.RulesChanged(en.ctx, en.repo.ID, upd.CollectionChanged); err != nil {
		t.Fatal(err)
	}
	if b := en.detail(1).Result.Bucket; b != string(classify.StaleChangesRequested) {
		t.Fatalf("stale_days edit not reclassified: %s", b)
	}
	collectRules := staleSoon
	collectRules.TestApproval = rules.TestApproval{MarkerChecks: []string{"tests"}, Command: "/ok-to-test"}
	upd, err = en.db.UpdateRepoRules(en.ctx, en.repo.ID, collectRules, en.clock.Now())
	if err != nil || !upd.CollectionChanged {
		t.Fatalf("collection update = %+v, %v", upd, err)
	}
	if err := en.eng.RulesChanged(en.ctx, en.repo.ID, true); err != nil {
		t.Fatal(err)
	}
	en.dispatch()
	if in := en.src.inputs[len(en.src.inputs)-1]; in.Rules.CollectionHash() != collectRules.CollectionHash() {
		t.Fatal("re-collection used the old rules")
	}
	if b := en.detail(1).Result.Bucket; b == string(classify.Unclassified) {
		t.Fatal("refresh placeholder not replaced")
	}
}

func TestReclassifyIsTimeDependentAndGuarded(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			out := collectedOutcome(in)
			out.Record.MyReviews = []classify.Review{{State: "CHANGES_REQUESTED", SubmittedAt: t0, Commit: "h"}}
			out.Record.MyLastCommentAt = &t0
			return out
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	items, err := en.db.Reclassifiable(en.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a2"))
	en.clock.Advance(8 * 24 * time.Hour)
	if err := en.eng.reclassifyItems(en.ctx, items); err != nil {
		t.Fatalf("stale pass: %v", err)
	}
	if b := en.detail(1).Result.Bucket; b != string(classify.WaitingOnAuthor) {
		t.Fatalf("stale reclassification overwrote newer evidence: %s", b)
	}
	if err := en.eng.Reclassify(en.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if b := en.detail(1).Result.Bucket; b != string(classify.StaleChangesRequested) {
		t.Fatalf("time-dependent rule not applied: %s", b)
	}
}

func TestBaseAdvanceInvalidatesWithoutHeadChange(t *testing.T) {
	en := newEnv(t, Config{})
	fields := collect.FingerprintFields{UpdatedAt: t0, BaseOID: "base1", HeadOID: "head1", MergeState: "CLEAN"}
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{{Number: 1, Fingerprint: collect.Fingerprint(fields), HeadOID: "head1"}}
	})
	en.mustPoll()
	if _, err := en.eng.DispatchDue(en.ctx); err != nil {
		t.Fatal(err)
	}
	en.eng.Wait()
	before := en.state(1)
	fields.BaseOID = "base2"
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{{Number: 1, Fingerprint: collect.Fingerprint(fields), HeadOID: "head1"}}
	})
	en.clock.Advance(2 * time.Minute)
	en.mustPoll()
	after := en.state(1)
	if after.Revision != before.Revision+1 || after.RetryAt == nil {
		t.Fatalf("base advance did not invalidate: before %+v after %+v", before, after)
	}
	if d := en.detail(1); d.HeadChangedAt != nil {
		t.Fatalf("base advance recorded a head change at %v", d.HeadChangedAt)
	}
}

func TestRetryDelay(t *testing.T) {
	en := newEnv(t, Config{})
	for failures, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 9: 2 * time.Minute} {
		if got := en.eng.retryDelay(failures); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}
```

Create `internal/syncer/run_test.go`:

```go
package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func TestRunPollsEnrichesAndStopsCleanly(t *testing.T) {
	en := newEnv(t, Config{PollInterval: time.Hour, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	en.eng.d.Now = time.Now
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{obs(1, "a")} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- en.eng.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := en.db.Queue(context.Background(), store.QueueFilter{})
		if err == nil && len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no result within 5s: %+v, %v", rows, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunStartsPollerForNewRepo(t *testing.T) {
	en := newEnv(t, Config{PollInterval: time.Hour, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	en.eng.d.Now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- en.eng.Run(ctx) }()
	fpCalls := func() int { en.src.mu.Lock(); defer en.src.mu.Unlock(); return en.src.fpCalls }
	waitFor(t, func() bool { return fpCalls() == 1 })
	acct, err := en.db.Account(context.Background(), "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := en.db.AddRepo(context.Background(), acct.ID, "o", "r2", rules.Default(), t0); err != nil {
		t.Fatal(err)
	}
	en.eng.ReposChanged()
	waitFor(t, func() bool { return fpCalls() == 2 })
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
```

`TestRunStartsPollerForNewRepo` checks that `ReposChanged` starts a poller for a newly added repository, whose first poll is immediate.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/syncer/`
Expected: FAIL to compile (`undefined: (*Engine).RunJob`, `Reclassify`, `RulesChanged`, `Run`, `retryDelay`, `reclassifyItems`).

- [ ] **Step 3: Implement enrichment jobs**

Create `internal/syncer/job.go`:

```go
package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/store"
)

// DispatchDue starts enrichment for due work, at most one job per PR and
// Workers per account; paused accounts wait (§7).
func (e *Engine) DispatchDue(ctx context.Context) (int, error) {
	works, err := e.d.Store.DueWork(ctx, e.now(), dispatchBatch)
	if err != nil {
		return 0, err
	}
	queued := map[string]int{}
	started := 0
	for _, w := range works {
		if e.isPaused(w.Account) {
			queued[w.Account]++
			continue
		}
		key := prKey{w.RepoID, w.Number}
		jctx, ok := e.reserve(ctx, key)
		if !ok {
			continue
		}
		sem := e.semaphore(w.Account)
		select {
		case sem <- struct{}{}:
		default:
			e.release(key)
			queued[w.Account]++
			continue
		}
		started++
		e.wg.Add(1)
		go func(w store.Work) {
			defer e.wg.Done()
			defer signal(e.wake)
			defer func() { <-sem }()
			defer e.release(key)
			e.RunJob(jctx, w)
		}(w)
	}
	e.mu.Lock()
	accounts := make([]string, 0, len(e.sems))
	for account := range e.sems {
		accounts = append(accounts, account)
	}
	e.mu.Unlock()
	for _, account := range accounts {
		e.d.Metrics.EnrichQueue.WithLabelValues(account).Set(float64(queued[account]))
	}
	return started, nil
}

// RunJob performs one enrichment attempt and commits its outcome under the §7 guards.
func (e *Engine) RunJob(ctx context.Context, w store.Work) {
	log := e.d.Log.With("repo", w.Owner+"/"+w.Name, "pr", w.Number)
	src, err := e.d.Sources(w.Account)
	if err != nil {
		e.accountBlocked(ctx, w.Account, fmt.Errorf("%w: %w", ErrNoToken, err))
		return
	}
	out := src.Collect(ctx, collect.Input{
		RepoID: w.RepoID, Owner: w.Owner, Name: w.Name, Viewer: w.Account, Number: w.Number, Rules: w.Rules,
		QueuedFingerprint: w.Fingerprint, HeadChangedAt: w.HeadChangedAt, Now: e.now(),
	})
	if ctx.Err() != nil {
		log.Debug("enrichment cancelled")
		return
	}
	now := e.now()
	switch out.Kind {
	case collect.Mismatch:
		err := e.d.Store.CommitObservation(ctx, store.DetailObservation{
			RepoID: w.RepoID, RepoRevision: w.RepoRevision, Number: w.Number, PRRevision: w.PRRevision,
			Now: now, Fingerprint: out.Fingerprint, HeadOID: out.HeadOID,
		})
		switch {
		case errors.Is(err, store.ErrStale):
			log.Debug("detail observation discarded: newer revision")
		case err != nil:
			log.Error("commit detail observation", "err", err)
		default:
			log.Debug("detail observation committed", "head", out.HeadOID)
			signal(e.wake)
		}
	case collect.Collected:
		e.commitRecord(ctx, log, w, out.Record, out.Caches, now, true, "", out.RetryableEvidence)
	case collect.Failed:
		if e.accountBlocked(ctx, w.Account, out.Err) {
			log.Info("enrichment deferred: account blocked", "err", out.Err)
			return
		}
		msg := out.Err.Error()
		if out.Retryable {
			if prior, ok := e.usablePrior(w, now); ok {
				since := now
				if prior.CoreRetry != nil {
					since = prior.CoreRetry.Since
				}
				prior.CoreRetry = &classify.CoreRetry{Message: msg, Since: since}
				e.commitRecord(ctx, log, w, prior, store.Caches{}, now, false, msg, true)
				return
			}
		}
		rec := out.Record
		rec.Number, rec.Version, rec.CollectionRulesHash, rec.Error = w.Number, classify.RecordVersion, w.Rules.CollectionHash(), msg
		e.commitRecord(ctx, log, w, rec, out.Caches, now, false, msg, out.Retryable)
	}
}

// usablePrior returns the stored record when it may stand in for a retryable
// core failure: same version and collection hash, no error, collected within 30m (§7).
func (e *Engine) usablePrior(w store.Work, now time.Time) (classify.Record, bool) {
	if len(w.Record) == 0 || w.RecordAt == nil || now.Sub(*w.RecordAt) > staleEvidenceLimit {
		return classify.Record{}, false
	}
	var prior classify.Record
	if err := json.Unmarshal(w.Record, &prior); err != nil {
		return classify.Record{}, false
	}
	if prior.Error != "" || prior.Version != classify.RecordVersion || prior.CollectionRulesHash != w.Rules.CollectionHash() {
		return classify.Record{}, false
	}
	return prior, true
}

func (e *Engine) commitRecord(ctx context.Context, log *slog.Logger, w store.Work, rec classify.Record, caches store.Caches,
	now time.Time, advance bool, errMsg string, retry bool) {
	res := classify.Classify(rec, w.Rules, w.Account, now)
	data, err := json.Marshal(rec)
	if err != nil {
		log.Error("encode record", "err", err)
		return
	}
	c := store.EnrichCommit{
		RepoID: w.RepoID, RepoRevision: w.RepoRevision, Number: w.Number, PRRevision: w.PRRevision, Now: now,
		Record: data, AdvanceRecordAt: advance, Title: rec.Title, URL: rec.URL, Author: rec.Author, Error: errMsg,
		Result: resultRow(res), Caches: caches,
	}
	if retry {
		c.EnrichFailures = w.EnrichFailures + 1
		next := now.Add(e.retryDelay(c.EnrichFailures))
		c.RetryAt = &next
	}
	tr, err := e.d.Store.CommitEnrichment(ctx, c)
	switch {
	case errors.Is(err, store.ErrStale):
		log.Debug("enrichment discarded: newer revision or lifecycle change")
		return
	case err != nil:
		if ctx.Err() == nil {
			log.Error("commit enrichment", "err", err)
		}
		return
	}
	log.Debug("enrichment committed", "head", rec.HeadOID, "bucket", res.Bucket, "retry_at", c.RetryAt, "error", errMsg)
	e.published(log, w.RepoID, w.Number, tr)
}

func resultRow(r classify.Result) store.ResultRow {
	return store.ResultRow{Bucket: string(r.Bucket), Reason: r.Reason, Action: r.Action, WaitingSince: r.WaitingSince, Also: r.Also, Notes: r.Notes}
}

// retryDelay is exponential from 30s, capped at 2m, with ±20% jitter (§7).
func (e *Engine) retryDelay(failures int) time.Duration {
	d := retryBase
	for i := 1; i < failures && d < retryCap; i++ {
		d *= 2
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*e.d.Rand()))
	return min(d, retryCap)
}
```

- [ ] **Step 4: Implement reclassification, rules changes, and the run loop**

Create `internal/syncer/reclassify.go`:

```go
package syncer

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

// Reclassify re-runs classification on stored open records without GitHub
// reads; repoID 0 means every enabled repository. Passes are serialized (§7).
func (e *Engine) Reclassify(ctx context.Context, repoID int64) error {
	e.reclassMu.Lock()
	defer e.reclassMu.Unlock()
	items, err := e.d.Store.Reclassifiable(ctx, repoID)
	if err != nil {
		return err
	}
	return e.reclassifyItems(ctx, items)
}

func (e *Engine) reclassifyItems(ctx context.Context, items []store.Classifiable) error {
	now := e.now()
	changed := 0
	for _, it := range items {
		var rec classify.Record
		if err := json.Unmarshal(it.Record, &rec); err != nil {
			e.d.Log.Warn("skip undecodable record", "repo_id", it.RepoID, "pr", it.Number, "err", err)
			continue
		}
		if rec.Version != classify.RecordVersion || rec.CollectionRulesHash != it.Rules.CollectionHash() {
			continue // waiting for enrichment to replace the refreshing result
		}
		res := classify.Classify(rec, it.Rules, it.Account, now)
		tr, ok, err := e.d.Store.CommitResult(ctx, it.RepoID, it.RepoRevision, it.Number, it.PRRevision, resultRow(res), now)
		if errors.Is(err, store.ErrStale) {
			continue
		}
		if err != nil {
			return err
		}
		if ok {
			changed++
			e.published(e.d.Log.With("repo_id", it.RepoID), it.RepoID, it.Number, tr)
		}
	}
	e.d.Log.Debug("reclassify pass", "records", len(items), "changed", changed)
	return nil
}

// RulesChanged reacts to a rules edit: a collection change cancels obsolete
// work and wakes the dispatcher; a stale_days-only edit reclassifies now (§6).
func (e *Engine) RulesChanged(ctx context.Context, repoID int64, collectionChanged bool) error {
	if collectionChanged {
		e.cancelRepo(repoID)
		signal(e.wake)
		return nil
	}
	return e.Reclassify(ctx, repoID)
}
```

Create `internal/syncer/run.go`:

```go
package syncer

import (
	"context"
	"errors"
	gosync "sync"
	"time"

	"github.com/thingzio/devbox/internal/store"
)

// Run starts a poller per enabled repository, the dispatcher, and the
// reclassify ticker. It blocks until ctx is cancelled and every poller and
// enrichment job has stopped.
func (e *Engine) Run(ctx context.Context) error {
	pollers := map[int64]context.CancelFunc{}
	var pollWG gosync.WaitGroup
	reconcile := func() {
		repos, err := e.d.Store.Repos(ctx)
		if err != nil {
			if ctx.Err() == nil {
				e.d.Log.Error("list repositories", "err", err)
			}
			return
		}
		enabled := map[int64]bool{}
		for _, r := range repos {
			if !r.Enabled {
				continue
			}
			enabled[r.ID] = true
			if _, running := pollers[r.ID]; running {
				continue
			}
			pctx, cancel := context.WithCancel(ctx)
			pollers[r.ID] = cancel
			pollWG.Add(1)
			go func(id int64) {
				defer pollWG.Done()
				e.pollLoop(pctx, id)
			}(r.ID)
		}
		for id, cancel := range pollers {
			if !enabled[id] {
				cancel()
				delete(pollers, id)
				e.cancelRepo(id)
			}
		}
	}
	reconcile()
	dispatch := time.NewTicker(e.cfg.DispatchEvery)
	defer dispatch.Stop()
	reclassify := time.NewTicker(e.cfg.ReclassifyEvery)
	defer reclassify.Stop()
	resync := time.NewTicker(e.cfg.ResyncEvery)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, cancel := range pollers {
				cancel()
			}
			pollWG.Wait()
			e.wg.Wait()
			return nil
		case <-e.resync:
			reconcile()
		case <-resync.C:
			reconcile()
		case <-dispatch.C:
			e.dispatch(ctx)
		case <-e.wake:
			e.dispatch(ctx)
		case <-reclassify.C:
			if err := e.Reclassify(ctx, 0); err != nil && ctx.Err() == nil {
				e.d.Log.Error("reclassify", "err", err)
			}
		}
	}
}

func (e *Engine) dispatch(ctx context.Context) {
	if _, err := e.DispatchDue(ctx); err != nil && ctx.Err() == nil {
		e.d.Log.Error("dispatch enrichment", "err", err)
	}
}

func (e *Engine) pollLoop(ctx context.Context, repoID int64) {
	for {
		failures, err := e.PollRepo(ctx, repoID)
		if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) {
			return
		}
		timer := time.NewTimer(e.pollDelay(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race -count=3 ./internal/syncer/ -v`
Expected: PASS on all three runs (repeated to shake out ordering races).

- [ ] **Step 6: Lint and commit**

```bash
make fmt lint
git add internal/syncer
git commit -S -m "feat(syncer): enrich due work, reclassify, and run pollers"
```

---

### Task 13: Authentication — keychain, device flow, classic tokens

**Files:**
- Create: `internal/auth/secrets.go`, `internal/auth/device.go`, `internal/auth/manager.go`
- Test: `internal/auth/secrets_test.go`, `internal/auth/device_test.go`, `internal/auth/manager_test.go`

**Interfaces:**
- Consumes: Task 6 `*github.Client` (`Get`), `github.Options`, `github.New`; Task 7 `store.DB` (`UpsertAccount`, `DeleteAccount`), `store.Account`.
- Produces:
  - `type Secrets interface { Get(login string) (string, error); Set(login, token string) error; Delete(login string) error }`; `ErrNoSecret`; `type Keyring struct{}`; `func NewMemSecrets() *MemSecrets`
  - `var DefaultClientID string` (set by `-ldflags`); `const ClientIDEnv = "DEVBOX_GITHUB_CLIENT_ID"`; `const Scopes = "repo read:org"`; `func ClientID() string`
  - `type DeviceCode struct { DeviceCode, UserCode, VerificationURI string; ExpiresIn, Interval int }`; `type DeviceClient struct { BaseURL, ClientID string; HTTP *http.Client; Sleep func(context.Context, time.Duration) error; Now func() time.Time }`; `(*DeviceClient) Start(ctx) (DeviceCode, error)`, `Wait(ctx, DeviceCode) (string, error)`; `ErrExpired`, `ErrDenied`
  - `type Identity struct { Login string; Scopes []string }`; `func Identify(ctx, *github.Client) (Identity, error)`; `ErrFineGrained`, `ErrMissingScope`
  - `type ManagerOptions struct { Store *store.DB; Secrets Secrets; Device *DeviceClient; NewClient func(token string) (*github.Client, error); Log *slog.Logger; Now func() time.Time }`
  - `type FlowState string` (`FlowPending`, `FlowDone`, `FlowFailed`); `type FlowView struct { ID, UserCode, VerificationURI string; ExpiresAt time.Time; State FlowState; Login, Error string }`
  - `func NewManager(ctx context.Context, o ManagerOptions) *Manager`; methods `StartDevice(ctx) (FlowView, error)`, `Flow(id string) (FlowView, bool)`, `AddPAT(ctx, token string) (store.Account, error)`, `Remove(ctx, login string) error`, `DeviceConfigured() bool`, `Close()`

- [ ] **Step 1: Add the keyring dependency**

```bash
go get github.com/zalando/go-keyring@v0.2.8
```

- [ ] **Step 2: Write the failing tests**

Create `internal/auth/secrets_test.go`:

```go
package auth

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func exerciseSecrets(t *testing.T, s Secrets) {
	t.Helper()
	if _, err := s.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("missing secret err = %v", err)
	}
	if err := s.Set("me", "tok"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := s.Get("me"); err != nil || got != "tok" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Delete("me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("second Delete err = %v", err)
	}
}

func TestMemSecrets(t *testing.T) { exerciseSecrets(t, NewMemSecrets()) }

func TestKeyringSecrets(t *testing.T) {
	keyring.MockInit()
	exerciseSecrets(t, Keyring{})
}

func TestKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	if _, err := (Keyring{}).Get("me"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("unavailable keychain err = %v", err)
	}
	keyring.MockInit()
}

func TestClientID(t *testing.T) {
	old := DefaultClientID
	DefaultClientID = "built-in"
	defer func() { DefaultClientID = old }()
	t.Setenv(ClientIDEnv, "")
	if ClientID() != "built-in" {
		t.Fatalf("ClientID = %q", ClientID())
	}
	t.Setenv(ClientIDEnv, "override")
	if ClientID() != "override" {
		t.Fatalf("ClientID = %q", ClientID())
	}
}
```

Create `internal/auth/device_test.go`:

```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type deviceServer struct {
	mu        sync.Mutex
	responses []string
	polls     int
	server    *httptest.Server
}

func newDeviceServer(t *testing.T, responses ...string) *deviceServer {
	t.Helper()
	ds := &deviceServer{responses: responses}
	ds.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		switch r.URL.Path {
		case "/login/device/code":
			if r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("scope") != Scopes {
				t.Errorf("device code form = %v", r.PostForm)
			}
			fmt.Fprint(w, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
		case "/login/oauth/access_token":
			if r.PostForm.Get("device_code") != "dc" || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("token form = %v", r.PostForm)
			}
			ds.mu.Lock()
			resp := ds.responses[min(ds.polls, len(ds.responses)-1)]
			ds.polls++
			ds.mu.Unlock()
			fmt.Fprint(w, resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ds.server.Close)
	return ds
}

func newDeviceClient(base string, sleeps *[]time.Duration) *DeviceClient {
	return &DeviceClient{BaseURL: base, ClientID: "cid", Sleep: func(ctx context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return ctx.Err()
	}}
}

func TestDeviceFlowSucceedsAfterPendingAndSlowDown(t *testing.T) {
	ds := newDeviceServer(t, `{"error":"authorization_pending"}`, `{"error":"slow_down"}`, `{"access_token":"gho_x","token_type":"bearer","scope":"repo,read:org"}`)
	var sleeps []time.Duration
	dc := newDeviceClient(ds.server.URL, &sleeps)
	code, err := dc.Start(context.Background())
	if err != nil || code.UserCode != "ABCD-1234" || code.VerificationURI != "https://github.com/login/device" {
		t.Fatalf("Start = %+v, %v", code, err)
	}
	token, err := dc.Wait(context.Background(), code)
	if err != nil || token != "gho_x" {
		t.Fatalf("Wait = %q, %v", token, err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}
	if fmt.Sprint(sleeps) != fmt.Sprint(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
}

func TestDeviceFlowFailures(t *testing.T) {
	cases := map[string]error{
		`{"error":"expired_token"}`: ErrExpired,
		`{"error":"access_denied"}`: ErrDenied,
	}
	for resp, want := range cases {
		ds := newDeviceServer(t, resp)
		var sleeps []time.Duration
		dc := newDeviceClient(ds.server.URL, &sleeps)
		code, _ := dc.Start(context.Background())
		if _, err := dc.Wait(context.Background(), code); !errors.Is(err, want) {
			t.Errorf("Wait(%s) err = %v, want %v", resp, err, want)
		}
	}
	ds := newDeviceServer(t, `{"error":"incorrect_client_credentials","error_description":"bad"}`)
	var sleeps []time.Duration
	dc := newDeviceClient(ds.server.URL, &sleeps)
	code, _ := dc.Start(context.Background())
	if _, err := dc.Wait(context.Background(), code); err == nil {
		t.Fatal("unknown error accepted")
	}
}

func TestDeviceFlowDeadline(t *testing.T) {
	ds := newDeviceServer(t, `{"error":"authorization_pending"}`)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	dc := &DeviceClient{BaseURL: ds.server.URL, ClientID: "cid", Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }}
	code, _ := dc.Start(context.Background())
	if _, err := dc.Wait(context.Background(), code); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestDeviceStartRequiresClientID(t *testing.T) {
	if _, err := (&DeviceClient{}).Start(context.Background()); err == nil {
		t.Fatal("started without a client id")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":"device_flow_disabled","error_description":"Device flow is not enabled"}`)
	}))
	defer srv.Close()
	if _, err := (&DeviceClient{BaseURL: srv.URL, ClientID: "cid"}).Start(context.Background()); err == nil {
		t.Fatal("disabled device flow accepted")
	}
}
```

Create `internal/auth/manager_test.go`:

```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/store"
)

type managerEnv struct {
	mgr     *Manager
	db      *store.DB
	secrets *MemSecrets
}

func newManagerEnv(t *testing.T, scopes string, device *DeviceClient) *managerEnv {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer revoked" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("X-OAuth-Scopes", scopes)
		fmt.Fprint(w, `{"login":"me"}`)
	}))
	t.Cleanup(api.Close)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	secrets := NewMemSecrets()
	mgr := NewManager(context.Background(), ManagerOptions{
		Store: db, Secrets: secrets, Device: device,
		NewClient: func(token string) (*github.Client, error) {
			return github.New(github.Options{APIURL: api.URL, Token: token})
		},
	})
	t.Cleanup(mgr.Close)
	return &managerEnv{mgr: mgr, db: db, secrets: secrets}
}

func TestAddPAT(t *testing.T) {
	ctx := context.Background()
	env := newManagerEnv(t, "repo, read:org", nil)
	if _, err := env.mgr.AddPAT(ctx, "github_pat_abc"); !errors.Is(err, ErrFineGrained) {
		t.Fatalf("fine-grained err = %v", err)
	}
	if _, err := env.mgr.AddPAT(ctx, "  "); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := env.mgr.AddPAT(ctx, "revoked"); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("revoked token err = %v", err)
	}
	acct, err := env.mgr.AddPAT(ctx, " ghp_good ")
	if err != nil || acct.Login != "me" || acct.Kind != "pat" || acct.Status != store.StatusOK {
		t.Fatalf("AddPAT = %+v, %v", acct, err)
	}
	if tok, _ := env.secrets.Get("me"); tok != "ghp_good" {
		t.Fatalf("stored token = %q", tok)
	}
	if err := env.db.SetAccountStatus(ctx, "me", store.StatusNeedsAuth, "rejected"); err != nil {
		t.Fatal(err)
	}
	if acct, _ := env.mgr.AddPAT(ctx, "ghp_new"); acct.Status != store.StatusOK {
		t.Fatalf("re-auth did not restore status: %+v", acct)
	}
}

func TestAddPATRequiresRepoScope(t *testing.T) {
	env := newManagerEnv(t, "read:org, notifications", nil)
	if _, err := env.mgr.AddPAT(context.Background(), "ghp_x"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("err = %v", err)
	}
	if _, err := env.secrets.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatal("token stored despite missing scope")
	}
}

func TestDeviceFlowAddsAccount(t *testing.T) {
	ds := newDeviceServer(t, `{"error":"authorization_pending"}`, `{"access_token":"gho_dev"}`)
	device := &DeviceClient{BaseURL: ds.server.URL, ClientID: "cid", Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
	env := newManagerEnv(t, "repo, read:org", device)
	if !env.mgr.DeviceConfigured() {
		t.Fatal("device flow not configured")
	}
	first, err := env.mgr.StartDevice(context.Background())
	if err != nil || first.State != FlowPending || first.UserCode != "ABCD-1234" {
		t.Fatalf("StartDevice = %+v, %v", first, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		view, ok := env.mgr.Flow(first.ID)
		if !ok {
			t.Fatal("flow vanished")
		}
		if view.State == FlowDone {
			if view.Login != "me" {
				t.Fatalf("view = %+v", view)
			}
			break
		}
		if view.State == FlowFailed || time.Now().After(deadline) {
			t.Fatalf("flow = %+v", view)
		}
		time.Sleep(10 * time.Millisecond)
	}
	acct, err := env.db.Account(context.Background(), "me")
	if err != nil || acct.Kind != "oauth" {
		t.Fatalf("account = %+v, %v", acct, err)
	}
	if tok, _ := env.secrets.Get("me"); tok != "gho_dev" {
		t.Fatalf("token = %q", tok)
	}
	second, err := env.mgr.StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env.mgr.Flow(first.ID); ok {
		t.Fatal("previous flow still addressable after a new one started")
	}
	if _, ok := env.mgr.Flow(second.ID); !ok {
		t.Fatal("new flow missing")
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	env := newManagerEnv(t, "repo", nil)
	if _, err := env.mgr.AddPAT(ctx, "ghp_x"); err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.Remove(ctx, "me"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := env.db.Account(ctx, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account survived: %v", err)
	}
	if _, err := env.secrets.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatal("token survived")
	}
	if err := env.mgr.Remove(ctx, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Remove err = %v", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/auth/`
Expected: FAIL to compile (`undefined: Secrets`, `DeviceClient`, `NewManager`).

- [ ] **Step 4: Implement secrets**

Create `internal/auth/secrets.go`:

```go
// Package auth handles GitHub account authentication: the OAuth device flow,
// classic personal access tokens, and token storage in the OS keychain (§8).
package auth

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/zalando/go-keyring"
)

// DefaultClientID is the thingzio OAuth App client ID, set at build time with
// -ldflags "-X github.com/thingzio/devbox/internal/auth.DefaultClientID=...".
var DefaultClientID = ""

// ClientIDEnv overrides DefaultClientID.
const ClientIDEnv = "DEVBOX_GITHUB_CLIENT_ID"

// Scopes requested by the device flow; repo also covers notifications (§8).
const Scopes = "repo read:org"

// ClientID returns the OAuth client ID in effect, or "" when none is configured.
func ClientID() string {
	if v := os.Getenv(ClientIDEnv); v != "" {
		return v
	}
	return DefaultClientID
}

// ErrNoSecret means no token is stored for the account.
var ErrNoSecret = errors.New("auth: no stored token")

// Secrets stores account tokens outside the database.
type Secrets interface {
	Get(login string) (string, error)
	Set(login, token string) error
	Delete(login string) error
}

const keyringService = "devbox"

func keyringUser(login string) string { return "account:" + login }

// Keyring stores tokens in the OS keychain; there is no plaintext fallback.
type Keyring struct{}

// Get returns the account's token.
func (Keyring) Get(login string) (string, error) {
	tok, err := keyring.Get(keyringService, keyringUser(login))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoSecret
	}
	if err != nil {
		return "", fmt.Errorf("auth: keychain unavailable: %w", err)
	}
	return tok, nil
}

// Set stores the account's token.
func (Keyring) Set(login, token string) error {
	if err := keyring.Set(keyringService, keyringUser(login), token); err != nil {
		return fmt.Errorf("auth: keychain unavailable: %w", err)
	}
	return nil
}

// Delete removes the account's token.
func (Keyring) Delete(login string) error {
	err := keyring.Delete(keyringService, keyringUser(login))
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNoSecret
	}
	if err != nil {
		return fmt.Errorf("auth: keychain unavailable: %w", err)
	}
	return nil
}

// MemSecrets keeps tokens in memory, for tests.
type MemSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemSecrets returns an empty in-memory store.
func NewMemSecrets() *MemSecrets { return &MemSecrets{m: map[string]string{}} }

// Get returns the account's token.
func (s *MemSecrets) Get(login string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.m[login]
	if !ok {
		return "", ErrNoSecret
	}
	return tok, nil
}

// Set stores the account's token.
func (s *MemSecrets) Set(login, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[login] = token
	return nil
}

// Delete removes the account's token.
func (s *MemSecrets) Delete(login string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[login]; !ok {
		return ErrNoSecret
	}
	delete(s.m, login)
	return nil
}
```

- [ ] **Step 5: Implement the device flow client**

Create `internal/auth/device.go`:

```go
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Device flow terminal errors (§8).
var (
	ErrExpired = errors.New("code expired, start again")
	ErrDenied  = errors.New("authorization denied")
)

// DeviceCode is GitHub's answer to a device authorization request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceClient runs GitHub's OAuth device flow.
type DeviceClient struct {
	BaseURL  string // default https://github.com
	ClientID string
	HTTP     *http.Client
	Sleep    func(context.Context, time.Duration) error
	Now      func() time.Time
}

// Start requests a device and user code.
func (d *DeviceClient) Start(ctx context.Context) (DeviceCode, error) {
	if d.ClientID == "" {
		return DeviceCode{}, fmt.Errorf("auth: no OAuth client ID configured; set %s or add a classic token", ClientIDEnv)
	}
	var resp struct {
		DeviceCode
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := d.post(ctx, "login/device/code", url.Values{"client_id": {d.ClientID}, "scope": {Scopes}}, &resp); err != nil {
		return DeviceCode{}, err
	}
	if resp.Error != "" {
		return DeviceCode{}, fmt.Errorf("auth: device flow unavailable: %s %s", resp.Error, resp.ErrorDescription)
	}
	if resp.DeviceCode.DeviceCode == "" || resp.UserCode == "" {
		return DeviceCode{}, errors.New("auth: incomplete device code response")
	}
	if resp.Interval <= 0 {
		resp.Interval = 5
	}
	return resp.DeviceCode, nil
}

// Wait polls for the access token until it is granted, denied, or expired.
func (d *DeviceClient) Wait(ctx context.Context, dc DeviceCode) (string, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := d.now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	form := url.Values{
		"client_id":   {d.ClientID},
		"device_code": {dc.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	for {
		if err := d.sleep(ctx, interval); err != nil {
			return "", err
		}
		if dc.ExpiresIn > 0 && !d.now().Before(deadline) {
			return "", ErrExpired
		}
		var resp struct {
			AccessToken      string `json:"access_token"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err := d.post(ctx, "login/oauth/access_token", form, &resp); err != nil {
			return "", err
		}
		switch resp.Error {
		case "":
			if resp.AccessToken == "" {
				return "", errors.New("auth: empty token response")
			}
			return resp.AccessToken, nil
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "expired_token":
			return "", ErrExpired
		case "access_denied":
			return "", ErrDenied
		default:
			return "", fmt.Errorf("auth: device flow failed: %s %s", resp.Error, resp.ErrorDescription)
		}
	}
}

func (d *DeviceClient) post(ctx context.Context, path string, form url.Values, out any) error {
	base := strings.TrimSuffix(d.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("auth: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("auth: %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: %s: HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("auth: %s: invalid JSON: %w", path, err)
	}
	return nil
}

func (d *DeviceClient) sleep(ctx context.Context, wait time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, wait)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *DeviceClient) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
```

- [ ] **Step 6: Implement the manager**

Create `internal/auth/manager.go`:

```go
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/store"
)

// Token validation errors (§8).
var (
	ErrFineGrained  = errors.New("fine-grained tokens cannot read notifications; use a classic token with the repo scope")
	ErrMissingScope = errors.New("the token lacks the repo scope")
)

// Identity is a token's owner and granted scopes.
type Identity struct {
	Login  string
	Scopes []string
}

// Identify reads the token owner's login and the scopes GitHub reports.
func Identify(ctx context.Context, c *github.Client) (Identity, error) {
	var u struct {
		Login string `json:"login"`
	}
	h, err := c.Get(ctx, "user", &u)
	if err != nil {
		return Identity{}, err
	}
	if u.Login == "" {
		return Identity{}, errors.New("auth: GitHub returned no login")
	}
	var scopes []string
	for _, s := range strings.Split(h.Get("X-OAuth-Scopes"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	return Identity{Login: u.Login, Scopes: scopes}, nil
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Store     *store.DB
	Secrets   Secrets
	Device    *DeviceClient
	NewClient func(token string) (*github.Client, error)
	Log       *slog.Logger
	Now       func() time.Time
}

// FlowState is a device flow's progress.
type FlowState string

// Device flow states.
const (
	FlowPending FlowState = "pending"
	FlowDone    FlowState = "done"
	FlowFailed  FlowState = "failed"
)

// FlowView is what the UI shows about a device flow; device_code never leaves the server.
type FlowView struct {
	ID              string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	State           FlowState
	Login           string
	Error           string
}

type flow struct {
	view   FlowView
	cancel context.CancelFunc
}

// Manager adds, verifies, and removes accounts.
type Manager struct {
	o    ManagerOptions
	ctx  context.Context
	mu   sync.Mutex
	flow *flow
	wg   sync.WaitGroup
}

// NewManager returns a manager whose device flows live until ctx ends or Close.
func NewManager(ctx context.Context, o ManagerOptions) *Manager {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{o: o, ctx: ctx}
}

// DeviceConfigured reports whether a client ID is available for the device flow.
func (m *Manager) DeviceConfigured() bool { return m.o.Device != nil && m.o.Device.ClientID != "" }

// StartDevice begins a device flow, cancelling any previous one (§8).
func (m *Manager) StartDevice(ctx context.Context) (FlowView, error) {
	if m.o.Device == nil {
		return FlowView{}, errors.New("auth: device flow is not configured")
	}
	dc, err := m.o.Device.Start(ctx)
	if err != nil {
		return FlowView{}, err
	}
	expires := time.Duration(dc.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 15 * time.Minute
	}
	fctx, cancel := context.WithTimeout(m.ctx, expires+time.Minute)
	f := &flow{view: FlowView{
		ID: rand.Text(), UserCode: dc.UserCode, VerificationURI: dc.VerificationURI,
		ExpiresAt: m.o.Now().Add(expires), State: FlowPending,
	}, cancel: cancel}
	m.mu.Lock()
	if m.flow != nil {
		m.flow.cancel()
	}
	m.flow = f
	m.mu.Unlock()
	m.o.Log.Info("device flow started", "expires_at", f.view.ExpiresAt)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		token, err := m.o.Device.Wait(fctx, dc)
		var acct store.Account
		if err == nil {
			acct, err = m.complete(fctx, token, "oauth")
		}
		m.finish(f, acct.Login, err)
	}()
	return f.view, nil
}

func (m *Manager) finish(f *flow, login string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		f.view.State, f.view.Error = FlowFailed, err.Error()
		m.o.Log.Info("device flow ended", "err", err)
		return
	}
	f.view.State, f.view.Login = FlowDone, login
}

// Flow returns the current flow's view if id matches it.
func (m *Manager) Flow(id string) (FlowView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow == nil || m.flow.view.ID != id {
		return FlowView{}, false
	}
	return m.flow.view, true
}

// AddPAT verifies and stores a classic personal access token (§8).
func (m *Manager) AddPAT(ctx context.Context, token string) (store.Account, error) {
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return store.Account{}, errors.New("auth: token is empty")
	case strings.HasPrefix(token, "github_pat_"):
		return store.Account{}, ErrFineGrained
	}
	return m.complete(ctx, token, "pat")
}

func (m *Manager) complete(ctx context.Context, token, kind string) (store.Account, error) {
	c, err := m.o.NewClient(token)
	if err != nil {
		return store.Account{}, err
	}
	id, err := Identify(ctx, c)
	if err != nil {
		return store.Account{}, fmt.Errorf("auth: verify token: %w", err)
	}
	if !slices.Contains(id.Scopes, "repo") {
		return store.Account{}, ErrMissingScope
	}
	if err := m.o.Secrets.Set(id.Login, token); err != nil {
		return store.Account{}, err
	}
	acct, err := m.o.Store.UpsertAccount(ctx, id.Login, kind, id.Scopes, m.o.Now())
	if err != nil {
		return store.Account{}, err
	}
	m.o.Log.Info("account verified", "account", id.Login, "kind", kind, "scopes", strings.Join(id.Scopes, ","))
	return acct, nil
}

// Remove deletes the account's token and the account with its repositories.
func (m *Manager) Remove(ctx context.Context, login string) error {
	if err := m.o.Secrets.Delete(login); err != nil && !errors.Is(err, ErrNoSecret) {
		return err
	}
	if err := m.o.Store.DeleteAccount(ctx, login); err != nil {
		return err
	}
	m.o.Log.Info("account removed", "account", login)
	return nil
}

// Close cancels any running device flow and waits for it to stop.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.flow != nil {
		m.flow.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 8: Lint and commit**

```bash
go mod tidy
make fmt lint
git add go.mod go.sum internal/auth
git commit -S -m "feat(auth): add keychain secrets, device flow, and classic tokens"
```

---

### Task 14: Web UI — views, security middleware, SSE

**Files:**
- Create: `internal/web/server.go`, `internal/web/views.go`, `internal/web/handlers.go`, `internal/web/templates/layout.html`, `internal/web/templates/partials.html`, `internal/web/templates/queue.html`, `internal/web/templates/repo.html`, `internal/web/templates/pr.html`, `internal/web/static/app.js`, `internal/web/static/style.css`, `internal/web/static/htmx.min.js` (vendored)
- Test: `internal/web/helpers_test.go`, `internal/web/views_test.go`

**Interfaces:**
- Consumes: Task 8 store views (`Queue`, `QueueFilter`, `QueueRow`, `PRDetails`, `RepoStatuses`, `ReposByName`, `Accounts`, `Repos`, `Transitions`); Task 11 `hub.Hub`; Task 13 `auth.Manager`; Task 5 `classify.Compare`, `RankKey`, `IsFirstTime`, `SizeBadge`, `Handoff`, `ActionBuckets`.
- Produces:
  - `type Engine interface { ReposChanged(); RulesChanged(ctx context.Context, repoID int64, collectionChanged bool) error; RateStatus() map[string]time.Time }` (satisfied by `*syncer.Engine`)
  - `type Options struct { Store *store.DB; Hub *hub.Hub; Auth *auth.Manager; Engine Engine; Metrics http.Handler; Log *slog.Logger; Addr string; AllowRemote bool; PollInterval time.Duration; ImportDir string; Now func() time.Time; Location *time.Location }`
  - `func New(Options) (*Server, error)`; `(*Server) Handler() http.Handler`
  - Unexported for Task 15: `viewRoutes`, `render`, `renderFragment`, `buildStatus`, `githubURL`, `pageData`, `funcs`.

- [ ] **Step 1: Vendor htmx and verify its checksum**

```bash
mkdir -p internal/web/static internal/web/templates
curl -fsSL https://cdn.jsdelivr.net/npm/htmx.org@2.0.11/dist/htmx.min.js -o internal/web/static/htmx.min.js
echo "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717  internal/web/static/htmx.min.js" | shasum -a 256 -c -
```

Expected: `internal/web/static/htmx.min.js: OK`. Stop if the checksum differs.

- [ ] **Step 2: Write the static assets**

Create `internal/web/static/app.js`:

```js
// Refreshes the queue on server change events and copies handoff lines.
(function () {
  "use strict";
  var queue = document.getElementById("queue");
  if (queue && window.EventSource && window.htmx) {
    var source = new EventSource("/events");
    source.addEventListener("change", function () {
      window.htmx.trigger(queue, "refresh");
    });
  }
  document.addEventListener("click", function (event) {
    var button = event.target.closest("button.copy");
    if (!button || !navigator.clipboard) {
      return;
    }
    navigator.clipboard.writeText(button.getAttribute("data-copy")).then(function () {
      button.textContent = "Copied";
    });
  });
})();
```

Create `internal/web/static/style.css`:

```css
:root { color-scheme: light dark; --fg: #1f2328; --muted: #656d76; --bg: #ffffff; --line: #d0d7de; --warn: #9a6700; --accent: #0969da; }
@media (prefers-color-scheme: dark) { :root { --fg: #e6edf3; --muted: #8d96a0; --bg: #0d1117; --line: #30363d; --warn: #d29922; --accent: #4493f8; } }
* { box-sizing: border-box; }
body { margin: 0; font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; color: var(--fg); background: var(--bg); }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
header.top { display: flex; gap: 1.5rem; align-items: center; padding: 0.6rem 1.25rem; border-bottom: 1px solid var(--line); }
header.top nav { display: flex; gap: 1rem; }
.brand { font-weight: 600; color: var(--fg); }
main { padding: 1rem 1.25rem; max-width: 72rem; }
.status { padding: 0.5rem 1.25rem 0; }
.banner { margin: 0.25rem 0; padding: 0.4rem 0.6rem; border: 1px solid var(--line); border-radius: 6px; }
.banner.warn { border-color: var(--warn); }
.filters { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: end; margin-bottom: 0.5rem; }
.sortkeys, .meta, .counts, .also, .totals { color: var(--muted); }
.bucket h2 { font-size: 1rem; margin: 1.25rem 0 0.25rem; }
.bucket ul { list-style: none; padding: 0; margin: 0; }
.row { padding: 0.35rem 0; border-bottom: 1px solid var(--line); }
.also { font-size: 0.9em; padding-left: 1rem; }
.note + .note::before, .also .note:first-child::before { content: " · "; }
.badge { display: inline-block; margin-left: 0.3rem; padding: 0 0.4rem; border: 1px solid var(--line); border-radius: 999px; font-size: 0.8em; color: var(--muted); }
.badge.first { border-color: var(--accent); color: var(--accent); }
.handoff code { font-size: 0.85em; word-break: break-all; }
.empty { padding: 2rem 0; color: var(--muted); }
table { border-collapse: collapse; width: 100%; }
th, td { text-align: left; padding: 0.3rem 0.5rem; border-bottom: 1px solid var(--line); vertical-align: top; }
form.inline { display: inline-flex; gap: 0.5rem; align-items: center; margin: 0.25rem 0.5rem 0.25rem 0; }
textarea { width: 100%; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 1.2em; letter-spacing: 0.1em; }
details.repo { padding: 0.5rem 0; border-bottom: 1px solid var(--line); }
```

- [ ] **Step 3: Write the templates**

Create `internal/web/templates/layout.html`:

```html
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="htmx-config" content='{"includeIndicatorStyles":false,"allowEval":false,"allowScriptTags":false,"selfRequestsOnly":true}'>
<title>{{.Title}} · devbox</title>
<link rel="stylesheet" href="/static/style.css">
<script src="/static/htmx.min.js"></script>
<script src="/static/app.js" defer></script>
</head>
<body>
<header class="top"><a class="brand" href="/">devbox</a><nav><a href="/">Queue</a><a href="/settings">Settings</a></nav></header>
{{template "status" .Status}}
<main>{{template "content" .Body}}</main>
</body>
</html>{{end}}
```

Create `internal/web/templates/partials.html`:

```html
{{define "status"}}{{if or .Accounts .Paused .Repos}}<section class="status" aria-label="status">
{{range .Accounts}}<p class="banner warn">@{{.Login}} needs re-authentication: {{.Detail}} <a href="/settings#accounts">Re-authenticate</a></p>{{end}}
{{range .Paused}}<p class="banner">@{{.Account}} is paused for rate limits until {{.Until}}</p>{{end}}
{{range .Repos}}<p class="banner {{.Level}}"><strong>{{.Name}}</strong>: {{join .Messages "; "}}</p>{{end}}
</section>{{end}}{{end}}

{{define "prlink"}}{{if .URL}}<a href="{{.URL}}">{{.Repo}}#{{.Number}}</a>{{else}}{{.Repo}}#{{.Number}}{{end}}{{end}}

{{define "row"}}<li class="row">
<div>{{template "prlink" .}} <a class="title" href="{{.ExplainURL}}">{{.Title}}</a> · @{{.Author}} · {{.Age}} · {{.Reason}}{{if .Action}} → <strong>{{.Action}}</strong>{{end}}
<span class="badges"><span class="badge account">@{{.Account}}</span>{{if .FirstTime}}<span class="badge first">first-time</span>{{end}}{{if .Size}}<span class="badge size">{{.Size}}</span>{{end}}{{if .Milestone}}<span class="badge milestone">{{.Milestone}}</span>{{end}}</span></div>
{{if or .Also .Notes}}<div class="also">{{if .Also}}also: {{join .Also ", "}}{{end}}{{range .Notes}}<span class="note">{{if .URL}}<a href="{{.URL}}">{{.Text}}</a>{{else}}{{.Text}}{{end}}</span>{{end}}</div>{{end}}
</li>{{end}}

{{define "queue-body"}}
{{if .EmptyMessage}}<p class="empty">{{.EmptyMessage}}</p>{{end}}
{{if .Totals}}<p class="totals">{{.Totals}}</p>{{end}}
{{range .Buckets}}<section class="bucket" id="{{.ID}}"><h2>{{.Title}} ({{len .Rows}})</h2>
<ul>{{range .Rows}}{{template "row" .}}{{end}}</ul>
{{if .Handoff}}<p class="handoff"><code>{{.Handoff}}</code> <button type="button" class="copy" data-copy="{{.Handoff}}">Copy</button></p>{{end}}
</section>{{end}}
{{if .Ready}}<p class="ready">Ready to merge: {{range .Ready}}{{template "prlink" .}} {{end}}</p>{{end}}
<p class="counts">Waiting on author: {{.Counts.WaitingOnAuthor}} · own: {{.Counts.Own}} · drafts: {{.Counts.Draft}}</p>
{{if .Unclassified}}<section class="bucket"><h2>Unclassified ({{len .Unclassified}})</h2><ul>{{range .Unclassified}}<li class="row">{{template "prlink" .}} <a href="{{.ExplainURL}}">{{.Title}}</a>: {{.Reason}}</li>{{end}}</ul></section>{{end}}
{{end}}
```

Create `internal/web/templates/queue.html`:

```html
{{define "content"}}
<form class="filters" method="get" action="/">
<label>Account <select name="account"><option value="">all</option>{{range .Accounts}}<option value="{{.}}"{{if eq . $.Filter.Account}} selected{{end}}>@{{.}}</option>{{end}}</select></label>
<label>Bucket <select name="bucket"><option value="">all</option>{{range .BucketOptions}}<option value="{{.ID}}"{{if eq .ID $.Filter.Bucket}} selected{{end}}>{{.Title}}</option>{{end}}</select></label>
<label>Repository <input name="repo" value="{{.RepoFilter}}" placeholder="owner/name"></label>
<button type="submit">Filter</button>
</form>
<p class="sortkeys">Buckets in precedence order; within a bucket: milestone due soonest, first-time contributors, waiting longest, PR number.</p>
<div id="queue" hx-get="{{.FragmentURL}}" hx-trigger="refresh delay:1s" hx-swap="innerHTML">{{template "queue-body" .}}</div>
{{end}}
```

Create `internal/web/templates/repo.html`:

```html
{{define "content"}}
<h1>{{.Name}}</h1>
<p class="meta">Monitored by {{range $i, $a := .Accounts}}{{if $i}}, {{end}}@{{$a}}{{end}} · <a href="{{.GitHubURL}}">open pull requests on GitHub</a></p>
<div id="queue" hx-get="{{.Queue.FragmentURL}}" hx-trigger="refresh delay:1s" hx-swap="innerHTML">{{template "queue-body" .Queue}}</div>
{{end}}
```

Create `internal/web/templates/pr.html`:

```html
{{define "content"}}
{{range .}}
<article class="explain">
<h1>{{template "prlink" .Link}} {{.Title}}</h1>
<p class="meta">@{{.Account}} · {{.State}} · revision {{.Revision}}</p>
{{with .Result}}<section><h2>Result</h2><p><strong>{{.Bucket}}</strong>: {{.Reason}}{{if .Action}} → {{.Action}}{{end}}</p>
{{if .Also}}<p>also: {{join .Also ", "}}</p>{{end}}{{range .Notes}}<p class="note">{{.}}</p>{{end}}</section>{{else}}<p>Not classified yet.</p>{{end}}
<section><h2>Evidence</h2><table>{{range .Evidence}}<tr><th>{{.Label}}</th><td>{{.Value}}</td></tr>{{end}}</table></section>
<section><h2>History</h2><ol>{{range .Transitions}}<li>{{.At}}: {{.From}} → {{.To}} ({{.Reason}})</li>{{else}}<li>No transitions yet.</li>{{end}}</ol></section>
</article>
{{end}}
{{end}}
```

- [ ] **Step 4: Write the failing tests**

Create `internal/web/helpers_test.go`:

```go
package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type rulesCall struct {
	repoID            int64
	collectionChanged bool
}

type fakeEngine struct {
	mu           sync.Mutex
	reposChanged int
	rulesCalls   []rulesCall
	paused       map[string]time.Time
}

func (f *fakeEngine) ReposChanged() { f.mu.Lock(); f.reposChanged++; f.mu.Unlock() }
func (f *fakeEngine) RulesChanged(_ context.Context, repoID int64, changed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rulesCalls = append(f.rulesCalls, rulesCall{repoID, changed})
	return nil
}
func (f *fakeEngine) RateStatus() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}
func (f *fakeEngine) reposChangedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reposChanged
}
func (f *fakeEngine) rules() []rulesCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rulesCall(nil), f.rulesCalls...)
}
func (f *fakeEngine) setPaused(p map[string]time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = p
}

type testServer struct {
	mu        sync.Mutex
	t         *testing.T
	ctx       context.Context
	db        *store.DB
	hub       *hub.Hub
	engine    *fakeEngine
	secrets   *auth.MemSecrets
	now       time.Time
	http      *httptest.Server
	client    *http.Client
	importDir string
	apiScopes string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{t: t, ctx: context.Background(), hub: hub.New(), engine: &fakeEngine{}, secrets: auth.NewMemSecrets(),
		now: t0, importDir: t.TempDir(), apiScopes: "repo, read:org"}
	db, err := store.Open(ts.ctx, filepath.Join(t.TempDir(), "d.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ts.db = db
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`)
		case "/login/oauth/access_token":
			_, _ = io.WriteString(w, `{"access_token":"gho_dev"}`)
		default:
			ts.mu.Lock()
			scopes := ts.apiScopes
			ts.mu.Unlock()
			w.Header().Set("X-OAuth-Scopes", scopes)
			_, _ = io.WriteString(w, `{"login":"me"}`)
		}
	}))
	t.Cleanup(api.Close)
	mgr := auth.NewManager(ts.ctx, auth.ManagerOptions{
		Store: db, Secrets: ts.secrets,
		Device: &auth.DeviceClient{BaseURL: api.URL, ClientID: "cid", Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }},
		NewClient: func(token string) (*github.Client, error) {
			return github.New(github.Options{APIURL: api.URL, Token: token})
		},
	})
	t.Cleanup(mgr.Close)
	ts.http = httptest.NewUnstartedServer(nil)
	s, err := New(Options{
		Store: db, Hub: ts.hub, Auth: mgr, Engine: ts.engine, Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "devbox_up 1\n") }),
		Addr: ts.http.Listener.Addr().String(), PollInterval: 2 * time.Minute, ImportDir: ts.importDir,
		Now: ts.clock, Location: time.UTC,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts.http.Config.Handler = s.Handler()
	ts.http.Start()
	t.Cleanup(ts.http.Close)
	ts.client = ts.http.Client()
	ts.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return ts
}

func (ts *testServer) clock() time.Time {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.now
}

func (ts *testServer) setNow(now time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.now = now
}

func (ts *testServer) setScopes(scopes string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.apiScopes = scopes
}

func (ts *testServer) get(path string) (int, string) {
	ts.t.Helper()
	resp, err := ts.client.Get(ts.http.URL + path)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (ts *testServer) post(path string, form url.Values) (int, string, http.Header) {
	ts.t.Helper()
	req, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.http.URL)
	resp, err := ts.client.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func (ts *testServer) account(login string) store.Account {
	ts.t.Helper()
	a, err := ts.db.UpsertAccount(ts.ctx, login, "oauth", []string{"repo"}, t0)
	if err != nil {
		ts.t.Fatal(err)
	}
	return a
}

func (ts *testServer) repo(a store.Account, owner, name string) store.Repo {
	ts.t.Helper()
	r, err := ts.db.AddRepo(ts.ctx, a.ID, owner, name, rules.Default(), t0)
	if err != nil {
		ts.t.Fatal(err)
	}
	return r
}

// seed stores one classified PR with the given title, URL, bucket, and record JSON.
func (ts *testServer) seed(r store.Repo, number int, title, prURL, bucket, record string) {
	ts.t.Helper()
	if _, err := ts.db.ApplyPoll(ts.ctx, store.PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: r.Owner, Name: r.Name, Now: t0,
		Upserts: []store.Observation{{Number: number, Fingerprint: "fp", HeadOID: "h"}}}); err != nil {
		ts.t.Fatal(err)
	}
	states, _ := ts.db.PRStates(ts.ctx, r.ID)
	var rev int64
	for _, s := range states {
		if s.Number == number {
			rev = s.Revision
		}
	}
	since := t0.Add(-49 * time.Hour)
	if _, err := ts.db.CommitEnrichment(ts.ctx, store.EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: number, PRRevision: rev,
		Now: t0, Record: []byte(record), AdvanceRecordAt: true, Title: title, URL: prURL, Author: "alice",
		Result: store.ResultRow{Bucket: bucket, Reason: "not reviewed by you", Action: "review", WaitingSince: &since,
			Notes: []string{"https://github.com/o/r/pull/1#discussion_r1", "approved by @bob"}}}); err != nil {
		ts.t.Fatal(err)
	}
}
```

Create `internal/web/views_test.go`:

```go
package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/hub"
)

func TestQueueRendersRanksAndEscapes(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "<script>alert(1)</script>", "https://github.com/o/r/pull/1", "needs_initial_review",
		`{"number":1,"author_association":"FIRST_TIMER","additions":300,"deletions":10,"milestone":{"title":"v1","due_on":"2026-10-20T00:00:00Z"}}`)
	ts.seed(r, 2, "Plain title", "javascript:alert(1)", "waiting_on_me", `{"number":2}`)
	code, body := ts.get("/")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt;", "first-time", ">L<", ">v1<", "2 days", "@me",
		`href="https://github.com/o/r/pull/1#discussion_r1"`, "approved by @bob", "2 actions: 1 Waiting on me, 1 Needs initial review",
		`hx-get="/fragments/queue"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for _, banned := range []string{"<script>alert(1)", "javascript:alert", `href="#ZgotmplZ"`} {
		if strings.Contains(body, banned) {
			t.Errorf("body contains %q", banned)
		}
	}
	if strings.Index(body, "Waiting on me (1)") > strings.Index(body, "Needs initial review (1)") {
		t.Error("buckets not in precedence order")
	}
}

func TestQueueEmptyStates(t *testing.T) {
	ts := newTestServer(t)
	if _, body := ts.get("/"); !strings.Contains(body, "Add a GitHub account in Settings") {
		t.Fatalf("no-account message missing:\n%s", body)
	}
	a := ts.account("me")
	if _, body := ts.get("/"); !strings.Contains(body, "Add a repository in Settings") {
		t.Fatal("no-repo message missing")
	}
	ts.repo(a, "o", "r")
	if _, body := ts.get("/"); !strings.Contains(body, "Nothing is waiting on you.") {
		t.Fatal("all-clear message missing")
	}
}

func TestQueueFiltersAndFragment(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "First", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	ts.seed(r, 2, "Second", "https://github.com/o/r/pull/2", "waiting_on_me", `{}`)
	_, body := ts.get("/?bucket=waiting_on_me")
	if strings.Contains(body, "First") || !strings.Contains(body, "Second") || !strings.Contains(body, `hx-get="/fragments/queue?bucket=waiting_on_me"`) {
		t.Fatalf("bucket filter failed:\n%s", body)
	}
	code, frag := ts.get("/fragments/queue?repo=o/r")
	if code != http.StatusOK || strings.Contains(frag, "<html") || !strings.Contains(frag, "First") {
		t.Fatalf("fragment = %d\n%s", code, frag)
	}
}

func TestStatusBarShowsStaleRepo(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, t0, errorString("dial tcp: network is unreachable")); err != nil {
		t.Fatal(err)
	}
	ts.setNow(t0.Add(10 * time.Minute))
	_, body := ts.get("/")
	if !strings.Contains(body, "no successful poll yet") || !strings.Contains(body, "network is unreachable") {
		t.Fatalf("stale status missing:\n%s", body)
	}
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, ts.clock(), nil); err != nil {
		t.Fatal(err)
	}
	if _, body := ts.get("/"); strings.Contains(body, "no successful poll yet") || strings.Contains(body, "network is unreachable") {
		t.Fatal("status not cleared after a successful poll")
	}
	ts.setNow(ts.clock().Add(4 * time.Minute))
	if _, body := ts.get("/"); !strings.Contains(body, "last successful poll 4m ago") {
		t.Fatal("staleness not reported after 3 minutes")
	}
}

func TestStatusBarShowsAuthAndRateProblems(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	if err := ts.db.SetAccountStatus(ts.ctx, "me", "needs_auth", "token rejected by GitHub"); err != nil {
		t.Fatal(err)
	}
	ts.engine.setPaused(map[string]time.Time{"me": t0.Add(30 * time.Minute)})
	_, body := ts.get("/")
	if !strings.Contains(body, "@me needs re-authentication: token rejected by GitHub") || !strings.Contains(body, "paused for rate limits until 12:30") {
		t.Fatalf("auth/rate banners missing:\n%s", body)
	}
}

func TestRepoPageShowsBothAccounts(t *testing.T) {
	ts := newTestServer(t)
	me, work := ts.account("me"), ts.account("work")
	ts.seed(ts.repo(me, "NVIDIA", "aicr"), 7, "Mine", "https://github.com/NVIDIA/aicr/pull/7", "needs_initial_review", `{}`)
	ts.seed(ts.repo(work, "NVIDIA", "aicr"), 7, "Theirs", "https://github.com/NVIDIA/aicr/pull/7", "waiting_on_me", `{}`)
	code, body := ts.get("/r/nvidia/AICR")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"@me", "@work", "/reviewing-pull-requests https://github.com/NVIDIA/aicr/pull/7", `class="copy"`, "Waiting on me (1)", "Needs initial review (1)"} {
		if !strings.Contains(body, want) {
			t.Errorf("repo page missing %q", want)
		}
	}
	if code, _ := ts.get("/r/nobody/nothing"); code != http.StatusNotFound {
		t.Fatalf("unknown repo status = %d", code)
	}
}

func TestExplainPage(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 3, "Explain me", "https://github.com/o/r/pull/3", "needs_initial_review",
		`{"number":3,"author":"alice","fork":true,"merge_state":"CLEAN","signature_policy":"absent","evidence_errors":[{"field":"diff","message":"timeout","retryable":true}]}`)
	code, body := ts.get("/r/o/r/3")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"Explain me", "needs_initial_review", "Fork", "yes", "diff: timeout (retry pending)", "→ needs_initial_review"} {
		if !strings.Contains(body, want) {
			t.Errorf("explain page missing %q", want)
		}
	}
	if code, _ := ts.get("/r/o/r/999"); code != http.StatusNotFound {
		t.Fatalf("unknown PR status = %d", code)
	}
	if code, _ := ts.get("/r/o/r/abc"); code != http.StatusNotFound {
		t.Fatalf("non-numeric PR status = %d", code)
	}
}

func TestSecurityHeadersHostAndOrigin(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.client.Get(ts.http.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'", "X-Content-Type-Options": "nosniff",
		"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	req, _ := http.NewRequestWithContext(ts.ctx, http.MethodGet, ts.http.URL+"/", nil)
	req.Host = "evil.example"
	if resp, err := ts.client.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding host status = %v, %v", resp.StatusCode, err)
	}
	cross, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+"/repos", strings.NewReader(url.Values{"repo": {"o/r"}}.Encode()))
	cross.Header.Set("Origin", "https://evil.example")
	if resp, err := ts.client.Do(cross); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %v, %v", resp.StatusCode, err)
	}
	bare, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+"/repos", nil)
	if resp, err := ts.client.Do(bare); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST without Origin status = %v, %v", resp.StatusCode, err)
	}
}

func TestEventsStream(t *testing.T) {
	ts := newTestServer(t)
	ctx, cancel := context.WithCancel(ts.ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.http.URL+"/events", nil)
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}
	deadline := time.Now().Add(2 * time.Second)
	for ts.hub.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ts.hub.Publish(hub.Event{RepoID: 4, Number: 9})
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		if lines.Text() == `data: {"repo":4,"number":9}` {
			return
		}
	}
	t.Fatal("event not delivered")
}

func TestStaticAssetsMetricsHealth(t *testing.T) {
	ts := newTestServer(t)
	if code, body := ts.get("/static/app.js"); code != http.StatusOK || !strings.Contains(body, "EventSource") {
		t.Fatalf("app.js = %d", code)
	}
	if code, body := ts.get("/metrics"); code != http.StatusOK || !strings.Contains(body, "devbox_up") {
		t.Fatalf("metrics = %d %q", code, body)
	}
	if code, body := ts.get("/healthz"); code != http.StatusOK || body != "ok" {
		t.Fatalf("healthz = %d %q", code, body)
	}
	data, err := os.ReadFile("static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717" {
		t.Fatal("vendored htmx.min.js does not match the pinned 2.0.11 checksum")
	}
}

func TestAge(t *testing.T) {
	now := t0
	cases := map[time.Duration]string{0: "today", 23 * time.Hour: "today", 24 * time.Hour: "1 day", 49 * time.Hour: "2 days", -time.Hour: "today"}
	for d, want := range cases {
		since := now.Add(-d)
		if got := age(now, &since); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
	if age(now, nil) != "—" {
		t.Error("nil since")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
```

- [ ] **Step 5: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/web/`
Expected: FAIL to compile (`undefined: New`, `age`).

- [ ] **Step 6: Implement the server core**

Create `internal/web/server.go`:

```go
// Package web serves the devbox UI (§9).
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Engine is the part of the sync engine the UI drives; *syncer.Engine implements it.
type Engine interface {
	ReposChanged()
	RulesChanged(ctx context.Context, repoID int64, collectionChanged bool) error
	RateStatus() map[string]time.Time
}

// Options configures the server.
type Options struct {
	Store        *store.DB
	Hub          *hub.Hub
	Auth         *auth.Manager
	Engine       Engine
	Metrics      http.Handler
	Log          *slog.Logger
	Addr         string // actual listen address, for Host and Origin checks
	AllowRemote  bool
	PollInterval time.Duration
	ImportDir    string
	Now          func() time.Time
	Location     *time.Location // display time zone, default time.Local
}

// Server renders pages and handles UI actions.
type Server struct {
	o            Options
	pages        map[string]*template.Template
	allowedHosts map[string]bool
}

var funcs = template.FuncMap{"join": strings.Join}

// New parses templates and prepares the Host allow-list.
func New(o Options) (*Server, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"queue", "repo", "pr", "settings"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("web: parse %s templates: %w", name, err)
		}
		pages[name] = t
	}
	_, port, err := net.SplitHostPort(o.Addr)
	if err != nil {
		return nil, fmt.Errorf("web: listen address %q: %w", o.Addr, err)
	}
	hosts := map[string]bool{}
	for _, h := range []string{"localhost", "127.0.0.1", "[::1]"} {
		hosts[h+":"+port] = true
	}
	return &Server{o: o, pages: pages, allowedHosts: hosts}, nil
}

// Handler returns the full HTTP handler with security and logging middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.viewRoutes(mux)
	return s.logRequests(s.secure(mux))
}

func (s *Server) viewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleQueue)
	mux.HandleFunc("GET /fragments/queue", s.handleQueueFragment)
	mux.HandleFunc("GET /r/{owner}/{name}", s.handleRepo)
	mux.HandleFunc("GET /fragments/r/{owner}/{name}", s.handleRepoFragment)
	mux.HandleFunc("GET /r/{owner}/{name}/{number}", s.handleExplain)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.o.Metrics != nil {
		mux.Handle("GET /metrics", s.o.Metrics)
	}
	static, _ := fs.Sub(staticFS, "static") // the embedded directory always exists
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
}

// secure applies §9.1: security headers, Host allow-listing, and same-origin POSTs.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if !s.o.AllowRemote && !s.allowedHosts[r.Host] {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == "http://"+r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.o.Log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.o.Log.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
	})
}

type pageData struct {
	Title  string
	Status statusView
	Body   any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, status int, data pageData) {
	data.Status = s.buildStatus(r.Context())
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.o.Log.Error("render page", "page", page, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderFragment(w http.ResponseWriter, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		s.o.Log.Error("render fragment", "fragment", name, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.o.Log.Error("request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// githubURL returns u only when it is a https://github.com/ URL (§9.1).
func githubURL(u string) string {
	if strings.HasPrefix(u, "https://github.com/") {
		return u
	}
	return ""
}
```

Create a placeholder `internal/web/templates/settings.html` so all page sets parse until Task 15 replaces it:

```html
{{define "content"}}<p>Settings are added in the next task.</p>{{end}}
```

- [ ] **Step 7: Implement view models and handlers**

Create `internal/web/views.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

const staleAfter = 3 * time.Minute

type noteView struct{ Text, URL string }

type rowView struct {
	Repo       string
	Account    string
	Number     int
	Title      string
	URL        string
	ExplainURL string
	Author     string
	Age        string
	Reason     string
	Action     string
	Also       []string
	Notes      []noteView
	FirstTime  bool
	Size       string
	Milestone  string
	rank       classify.Rankable
}

type bucketView struct {
	ID, Title, Handoff string
	Rows               []rowView
}

type bucketOption struct{ ID, Title string }

type countsView struct{ WaitingOnAuthor, Own, Draft int }

type queueView struct {
	Buckets       []bucketView
	Ready         []rowView
	Unclassified  []rowView
	Counts        countsView
	Totals        string
	EmptyMessage  string
	FragmentURL   string
	Filter        store.QueueFilter
	RepoFilter    string
	Accounts      []string
	BucketOptions []bucketOption
}

func (s *Server) buildQueue(ctx context.Context, f store.QueueFilter, perRepo bool) (queueView, error) {
	rows, err := s.o.Store.Queue(ctx, f)
	if err != nil {
		return queueView{}, err
	}
	now := s.o.Now()
	byBucket := map[classify.Bucket][]rowView{}
	for _, q := range rows {
		var rec classify.Record
		_ = json.Unmarshal(q.Record, &rec) // an undecodable record only loses badges
		bucket := classify.Bucket(q.Result.Bucket)
		repo := q.Owner + "/" + q.Name
		rv := rowView{
			Repo: repo, Account: q.Account, Number: q.Number, Title: titleOr(q.Title, q.Number), URL: githubURL(q.URL),
			ExplainURL: explainURL(q.Owner, q.Name, q.Number), Author: q.Author, Age: age(now, q.Result.WaitingSince),
			Reason: q.Result.Reason, Action: q.Result.Action, Also: q.Result.Also, Notes: notes(q.Result.Notes),
			FirstTime: classify.IsFirstTime(rec.AuthorAssociation),
			rank:      classify.RankKey(rec, bucket, q.Result.WaitingSince, repo),
		}
		rv.rank.Number = q.Number
		if rec.Additions+rec.Deletions > 0 {
			rv.Size = classify.SizeBadge(rec.Additions, rec.Deletions)
		}
		if rec.Milestone != nil {
			rv.Milestone = rec.Milestone.Title
		}
		byBucket[bucket] = append(byBucket[bucket], rv)
	}
	for b := range byBucket {
		slices.SortFunc(byBucket[b], func(x, y rowView) int { return classify.Compare(x.rank, y.rank) })
	}
	view := queueView{Filter: f}
	var totals []string
	actions := 0
	for _, b := range classify.ActionBuckets() {
		view.BucketOptions = append(view.BucketOptions, bucketOption{ID: string(b), Title: b.Title()})
		rs := byBucket[b]
		if len(rs) == 0 {
			continue
		}
		bv := bucketView{ID: string(b), Title: b.Title(), Rows: rs}
		if perRepo {
			var urls []string
			for _, r := range rs {
				if r.URL != "" {
					urls = append(urls, r.URL)
				}
			}
			bv.Handoff = classify.Handoff(b, urls)
		}
		view.Buckets = append(view.Buckets, bv)
		totals = append(totals, fmt.Sprintf("%d %s", len(rs), b.Title()))
		actions += len(rs)
	}
	for _, b := range []classify.Bucket{classify.ReadyToMerge, classify.WaitingOnAuthor, classify.Own, classify.Draft, classify.Unclassified} {
		view.BucketOptions = append(view.BucketOptions, bucketOption{ID: string(b), Title: b.Title()})
	}
	if actions > 0 {
		view.Totals = fmt.Sprintf("%d actions: %s", actions, strings.Join(totals, ", "))
	}
	view.Ready = byBucket[classify.ReadyToMerge]
	view.Unclassified = byBucket[classify.Unclassified]
	view.Counts = countsView{
		WaitingOnAuthor: len(byBucket[classify.WaitingOnAuthor]),
		Own:             len(byBucket[classify.Own]),
		Draft:           len(byBucket[classify.Draft]),
	}
	if actions == 0 {
		view.EmptyMessage, err = s.emptyMessage(ctx)
		if err != nil {
			return queueView{}, err
		}
	}
	return view, nil
}

func (s *Server) emptyMessage(ctx context.Context) (string, error) {
	accounts, err := s.o.Store.Accounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "Add a GitHub account in Settings to get started.", nil
	}
	repos, err := s.o.Store.Repos(ctx)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(repos, func(r store.Repo) bool { return r.Enabled }) {
		return "Add a repository in Settings to start monitoring.", nil
	}
	return "Nothing is waiting on you.", nil
}

func titleOr(title string, number int) string {
	if title == "" {
		return fmt.Sprintf("(loading PR #%d)", number)
	}
	return title
}

func explainURL(owner, name string, number int) string {
	return "/r/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + strconv.Itoa(number)
}

func notes(items []string) []noteView {
	out := make([]noteView, 0, len(items))
	for _, n := range items {
		out = append(out, noteView{Text: n, URL: githubURL(n)})
	}
	return out
}

// age renders whole days since t, "today" under one day (§9).
func age(now time.Time, since *time.Time) string {
	if since == nil {
		return "—"
	}
	days := int(now.Sub(*since) / (24 * time.Hour))
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

func minutesAgo(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

type accountStatus struct{ Login, Detail string }

type pausedView struct{ Account, Until string }

type repoStatusView struct {
	Name     string
	Level    string
	Messages []string
}

type statusView struct {
	Accounts []accountStatus
	Paused   []pausedView
	Repos    []repoStatusView
}

// buildStatus feeds the header bar: auth problems, rate pauses, staleness,
// and enrichment backlog (§9). Disabled repositories are omitted.
func (s *Server) buildStatus(ctx context.Context) statusView {
	var v statusView
	now := s.o.Now()
	if accounts, err := s.o.Store.Accounts(ctx); err == nil {
		for _, a := range accounts {
			if a.Status != store.StatusOK {
				v.Accounts = append(v.Accounts, accountStatus{Login: a.Login, Detail: a.StatusDetail})
			}
		}
	}
	if s.o.Engine != nil {
		for account, until := range s.o.Engine.RateStatus() {
			v.Paused = append(v.Paused, pausedView{Account: account, Until: until.In(s.o.Location).Format("15:04")})
		}
		slices.SortFunc(v.Paused, func(a, b pausedView) int { return strings.Compare(a.Account, b.Account) })
	}
	statuses, err := s.o.Store.RepoStatuses(ctx)
	if err != nil {
		s.o.Log.Error("repo statuses", "err", err)
		return v
	}
	for _, st := range statuses {
		if !st.Repo.Enabled {
			continue
		}
		var msgs []string
		level := "info"
		last := st.Sync.LastSuccessAt
		switch {
		case last == nil && now.Sub(st.Repo.AddedAt) > staleAfter:
			msgs, level = append(msgs, "no successful poll yet"), "warn"
		case last != nil && now.Sub(*last) > staleAfter:
			msgs, level = append(msgs, fmt.Sprintf("last successful poll %s ago", minutesAgo(now.Sub(*last)))), "warn"
		}
		if st.Sync.LastError != "" {
			msgs, level = append(msgs, "last error: "+st.Sync.LastError), "warn"
		}
		if st.Pending > 0 {
			msgs = append(msgs, fmt.Sprintf("%d awaiting enrichment", st.Pending))
		}
		if st.Errored > 0 {
			msgs, level = append(msgs, fmt.Sprintf("%d with evidence errors", st.Errored)), "warn"
		}
		if st.Retrying > 0 {
			detail := fmt.Sprintf("%d retrying", st.Retrying)
			if st.NextRetryAt != nil {
				detail += " (next " + st.NextRetryAt.In(s.o.Location).Format("15:04:05")
				if st.LastAttemptAt != nil {
					detail += ", last attempt " + st.LastAttemptAt.In(s.o.Location).Format("15:04:05")
				}
				detail += ")"
			}
			msgs = append(msgs, detail)
		}
		if len(msgs) > 0 {
			v.Repos = append(v.Repos, repoStatusView{Name: st.Repo.FullName() + " @" + st.Repo.Account, Level: level, Messages: msgs})
		}
	}
	return v
}
```

Create `internal/web/handlers.go`:

```go
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

func queueFilter(r *http.Request) (store.QueueFilter, string) {
	q := r.URL.Query()
	f := store.QueueFilter{Account: q.Get("account"), Bucket: q.Get("bucket")}
	repo := strings.TrimSpace(q.Get("repo"))
	if owner, name, ok := strings.Cut(repo, "/"); ok {
		f.Owner, f.Name = owner, name
	}
	return f, repo
}

func fragmentURL(path string, f store.QueueFilter, repo string) string {
	v := url.Values{}
	for k, val := range map[string]string{"account": f.Account, "bucket": f.Bucket, "repo": repo} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func (s *Server) queueForRequest(r *http.Request) (queueView, error) {
	f, repo := queueFilter(r)
	view, err := s.buildQueue(r.Context(), f, false)
	if err != nil {
		return queueView{}, err
	}
	view.RepoFilter = repo
	view.FragmentURL = fragmentURL("/fragments/queue", f, repo)
	accounts, err := s.o.Store.Accounts(r.Context())
	if err != nil {
		return queueView{}, err
	}
	for _, a := range accounts {
		view.Accounts = append(view.Accounts, a.Login)
	}
	return view, nil
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	view, err := s.queueForRequest(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "queue", http.StatusOK, pageData{Title: "Queue", Body: view})
}

func (s *Server) handleQueueFragment(w http.ResponseWriter, r *http.Request) {
	view, err := s.queueForRequest(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "queue", "queue-body", view)
}

type repoView struct {
	Name      string
	GitHubURL string
	Accounts  []string
	Queue     queueView
}

func (s *Server) repoForRequest(r *http.Request) (repoView, bool, error) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	repos, err := s.o.Store.ReposByName(r.Context(), owner, name)
	if err != nil || len(repos) == 0 {
		return repoView{}, false, err
	}
	f := store.QueueFilter{Owner: owner, Name: name}
	q, err := s.buildQueue(r.Context(), f, true)
	if err != nil {
		return repoView{}, false, err
	}
	q.FragmentURL = "/fragments/r/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	v := repoView{Name: repos[0].FullName(), GitHubURL: "https://github.com/" + repos[0].FullName() + "/pulls", Queue: q}
	for _, repo := range repos {
		v.Accounts = append(v.Accounts, repo.Account)
	}
	return v, true, nil
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	v, ok, err := s.repoForRequest(r)
	switch {
	case err != nil:
		s.serverError(w, err)
	case !ok:
		http.NotFound(w, r)
	default:
		s.render(w, r, "repo", http.StatusOK, pageData{Title: v.Name, Body: v})
	}
}

func (s *Server) handleRepoFragment(w http.ResponseWriter, r *http.Request) {
	v, ok, err := s.repoForRequest(r)
	switch {
	case err != nil:
		s.serverError(w, err)
	case !ok:
		http.NotFound(w, r)
	default:
		s.renderFragment(w, "repo", "queue-body", v.Queue)
	}
}

type linkView struct {
	Repo   string
	Number int
	URL    string
}

type kv struct{ Label, Value string }

type transitionView struct{ At, From, To, Reason string }

type explainView struct {
	Link        linkView
	Title       string
	Account     string
	State       string
	Revision    int64
	Result      *store.ResultRow
	Evidence    []kv
	Transitions []transitionView
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		http.NotFound(w, r)
		return
	}
	details, err := s.o.Store.PRDetails(r.Context(), r.PathValue("owner"), r.PathValue("name"), number)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(details) == 0 {
		http.NotFound(w, r)
		return
	}
	var views []explainView
	for _, d := range details {
		trs, err := s.o.Store.Transitions(r.Context(), d.Repo.ID, number)
		if err != nil {
			s.serverError(w, err)
			return
		}
		v := explainView{
			Link:  linkView{Repo: d.Repo.FullName(), Number: number, URL: githubURL(d.URL)},
			Title: titleOr(d.Title, number), Account: d.Repo.Account, State: d.State, Revision: d.Revision, Result: d.Result,
			Evidence: s.evidence(d),
		}
		for _, tr := range trs {
			from := "(new)"
			if tr.From != nil {
				from = *tr.From
			}
			v.Transitions = append(v.Transitions, transitionView{At: s.when(&tr.At), From: from, To: tr.To, Reason: tr.Reason})
		}
		views = append(views, v)
	}
	s.render(w, r, "pr", http.StatusOK, pageData{Title: fmt.Sprintf("%s#%d", details[0].Repo.FullName(), number), Body: views})
}

func yesNo(b *bool) string {
	switch {
	case b == nil:
		return "unknown"
	case *b:
		return "yes"
	}
	return "no"
}

func (s *Server) when(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.In(s.o.Location).Format(time.DateTime)
}

// evidence summarizes the stored record for the explain view (§9).
func (s *Server) evidence(d store.PRDetail) []kv {
	out := []kv{
		{"First seen", s.when(&d.FirstSeenAt)},
		{"Last collected", s.when(d.RecordAt)},
		{"Last attempt", s.when(d.LastEnrichAttemptAt)},
		{"Next retry", s.when(d.RetryAt)},
		{"Head", d.HeadOID},
		{"Head changed (observed)", s.when(d.HeadChangedAt)},
	}
	if d.Error != "" {
		out = append(out, kv{"Error", d.Error})
	}
	var rec classify.Record
	if len(d.Record) == 0 || json.Unmarshal(d.Record, &rec) != nil {
		return out
	}
	fork := rec.Fork
	out = append(out,
		kv{"Author", rec.Author}, kv{"Fork", yesNo(&fork)}, kv{"Merge state", rec.MergeState},
		kv{"Review decision", rec.ReviewDecision}, kv{"My reviews", strconv.Itoa(len(rec.MyReviews))},
		kv{"My last hand-off", s.when(rec.MyLastCommentAt)}, kv{"Author last active", s.when(rec.AuthorUpdatedAt)},
		kv{"Diff changed since my review", yesNo(rec.DiffChanged)}, kv{"Awaiting workflow runs", strconv.Itoa(rec.AwaitingRuns)},
		kv{"Test marker present", yesNo(rec.MarkerPresent)}, kv{"Touches CI paths", yesNo(rec.CIPathsTouched)},
		kv{"Signature policy", rec.SignaturePolicy}, kv{"Unverified commits", strconv.Itoa(len(rec.UnverifiedCommits))},
	)
	for _, e := range rec.EvidenceErrors {
		msg := e.Field + ": " + e.Message
		if e.Retryable {
			msg += " (retry pending)"
		}
		out = append(out, kv{"Evidence error", msg})
	}
	if rec.CoreRetry != nil {
		out = append(out, kv{"Stale evidence", fmt.Sprintf("since %s: %s", s.when(&rec.CoreRetry.Since), rec.CoreRetry.Message)})
	}
	return out
}

// handleEvents streams change events (§9) until the client or server goes away.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	events, unsubscribe := s.o.Hub.Subscribe()
	defer unsubscribe()
	s.o.Log.Debug("sse subscriber connected", "subscribers", s.o.Hub.Subscribers())
	defer s.o.Log.Debug("sse subscriber disconnected")
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil || rc.Flush() != nil {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: change\ndata: {\"repo\":%d,\"number\":%d}\n\n", ev.RepoID, ev.Number); err != nil || rc.Flush() != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 9: Lint and commit**

```bash
make fmt lint
git add internal/web
git commit -S -m "feat(web): render queue, repo, and explain views with SSE refresh"
```

---

### Task 15: Web settings — accounts, repositories, rules, import

**Files:**
- Create: `internal/web/settings.go`
- Modify: `internal/web/server.go` (register settings routes in `Handler`), `internal/web/templates/settings.html` (replace the placeholder)
- Test: `internal/web/settings_test.go`

**Interfaces:**
- Consumes: Task 14 server internals (`render`, `renderFragment`, `serverError`, `githubURL`, `pageData`); Task 13 `auth.Manager` (`StartDevice`, `Flow`, `AddPAT`, `Remove`, `DeviceConfigured`), `auth.FlowView`, `auth.FlowPending`, `auth.FlowDone`; Task 7/8 store (`Account`, `Accounts`, `AddRepo`, `SetRepoEnabled`, `DeleteRepo`, `UpdateRepoRules`, `RepoStatuses`, `ErrExists`, `ErrNotFound`); Task 2 `rules.Parse`, `rules.Default`; Task 11 `hub.Event`.
- Produces: `func parseRepoReference(input string) (owner, name string, err error)`; routes `GET /settings`, `POST /accounts/device`, `GET /accounts/device/{flow}`, `POST /accounts/pat`, `POST /accounts/{login}/delete`, `POST /repos`, `POST /repos/{id}/{enable|disable|delete|rules}`, `POST /import`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/settings_test.go`:

```go
package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/rules"
)

func TestParseRepoReference(t *testing.T) {
	ok := map[string]string{
		"NVIDIA/aicr":                               "NVIDIA/aicr",
		"  NVIDIA/aicr  ":                           "NVIDIA/aicr",
		"https://github.com/NVIDIA/aicr":            "NVIDIA/aicr",
		"https://github.com/NVIDIA/aicr/":           "NVIDIA/aicr",
		"http://www.github.com/NVIDIA/aicr.git":     "NVIDIA/aicr",
		"github.com/NVIDIA/aicr/pull/12":            "NVIDIA/aicr",
		"https://github.com/NVIDIA/aicr/pulls?q=is": "NVIDIA/aicr",
		"o/repo.with_dots-and_underscores":          "o/repo.with_dots-and_underscores",
	}
	for in, want := range ok {
		owner, name, err := parseRepoReference(in)
		if err != nil || owner+"/"+name != want {
			t.Errorf("parseRepoReference(%q) = %s/%s, %v; want %s", in, owner, name, err, want)
		}
	}
	for _, in := range []string{"", "NVIDIA", "gitlab.com/o/r", "https://gitlab.com/o/r", "-bad/r", "o/r r", "o/..", "git@github.com:o/r.git", "o/" + strings.Repeat("x", 101)} {
		if owner, name, err := parseRepoReference(in); err == nil {
			t.Errorf("parseRepoReference(%q) = %s/%s, want error", in, owner, name)
		}
	}
}

func TestAddRepoFlow(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	code, _, hdr := ts.post("/repos", url.Values{"account": {"me"}, "repo": {"https://github.com/NVIDIA/aicr/pull/12"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings" {
		t.Fatalf("add repo = %d %v", code, hdr)
	}
	repos, _ := ts.db.Repos(ts.ctx)
	if len(repos) != 1 || repos[0].FullName() != "NVIDIA/aicr" || ts.engine.reposChangedCount() != 1 {
		t.Fatalf("repos = %+v, reposChanged = %d", repos, ts.engine.reposChangedCount())
	}
	code, body, _ := ts.post("/repos", url.Values{"account": {"me"}, "repo": {"nvidia/AICR"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "already monitored") {
		t.Fatalf("duplicate = %d\n%s", code, body)
	}
	code, body, _ = ts.post("/repos", url.Values{"account": {"me"}, "repo": {"gitlab.com/o/r"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "not a github.com repository") {
		t.Fatalf("foreign host = %d\n%s", code, body)
	}
	if code, _, _ := ts.post("/repos", url.Values{"account": {"nobody"}, "repo": {"o/r"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown account = %d", code)
	}
}

func TestRepoActions(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "One", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	id := "/repos/" + itoa(r.ID)
	if code, _, _ := ts.post(id+"/disable", nil); code != http.StatusSeeOther {
		t.Fatalf("disable = %d", code)
	}
	if got, _ := ts.db.Repo(ts.ctx, r.ID); got.Enabled {
		t.Fatal("repo still enabled")
	}
	if code, _, _ := ts.post(id+"/enable", nil); code != http.StatusSeeOther {
		t.Fatalf("enable = %d", code)
	}
	events, unsubscribe := ts.hub.Subscribe()
	defer unsubscribe()
	code, body, _ := ts.post(id+"/rules", url.Values{"rules": {`{"stale": 3}`}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "unknown field") {
		t.Fatalf("invalid rules = %d\n%s", code, body)
	}
	code, _, _ = ts.post(id+"/rules", url.Values{"rules": {`{"test_approval":{"marker_checks":["tests"],"command":"/ok-to-test"},"stale_days":3}`}})
	if code != http.StatusSeeOther {
		t.Fatalf("valid rules = %d", code)
	}
	if calls := ts.engine.rules(); len(calls) != 1 || !calls[0].collectionChanged || calls[0].repoID != r.ID {
		t.Fatalf("rules calls = %+v", calls)
	}
	select {
	case ev := <-events:
		if ev != (hub.Event{RepoID: r.ID, Number: 1}) {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("rules edit did not publish the refresh transition")
	}
	if code, _, _ := ts.post(id+"/frobnicate", nil); code != http.StatusNotFound {
		t.Fatalf("unknown action = %d", code)
	}
	if code, _, _ := ts.post(id+"/delete", nil); code != http.StatusSeeOther {
		t.Fatalf("delete = %d", code)
	}
	if repos, _ := ts.db.Repos(ts.ctx); len(repos) != 0 {
		t.Fatalf("repo survived delete: %+v", repos)
	}
	if code, _, _ := ts.post(id+"/enable", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("action on deleted repo = %d", code)
	}
}

func TestAccountFlows(t *testing.T) {
	ts := newTestServer(t)
	code, body, _ := ts.post("/accounts/pat", url.Values{"token": {"github_pat_123"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "fine-grained tokens cannot read notifications") || strings.Contains(body, "github_pat_123") {
		t.Fatalf("fine-grained = %d\n%s", code, body)
	}
	if code, _, _ := ts.post("/accounts/pat", url.Values{"token": {"ghp_ok"}}); code != http.StatusSeeOther {
		t.Fatalf("classic token = %d", code)
	}
	_, settings := ts.get("/settings")
	if !strings.Contains(settings, "@me") || !strings.Contains(settings, "pat") || !strings.Contains(settings, "Sign in with GitHub") {
		t.Fatalf("settings page:\n%s", settings)
	}
	if code, _, _ := ts.post("/accounts/me/delete", nil); code != http.StatusSeeOther {
		t.Fatalf("remove = %d", code)
	}
	if accounts, _ := ts.db.Accounts(ts.ctx); len(accounts) != 0 {
		t.Fatalf("account survived removal: %+v", accounts)
	}
	ts.setScopes("read:org")
	if code, body, _ := ts.post("/accounts/pat", url.Values{"token": {"ghp_noscope"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "repo scope") {
		t.Fatalf("missing scope = %d\n%s", code, body)
	}
}

var flowIDPattern = regexp.MustCompile(`hx-get="/accounts/device/([A-Z2-7]+)"`)

func TestDeviceFlowFragments(t *testing.T) {
	ts := newTestServer(t)
	code, frag, _ := ts.post("/accounts/device", nil)
	if code != http.StatusOK || !strings.Contains(frag, "ABCD-1234") || !strings.Contains(frag, `href="https://github.com/login/device"`) {
		t.Fatalf("start fragment = %d\n%s", code, frag)
	}
	m := flowIDPattern.FindStringSubmatch(frag)
	if m == nil {
		t.Fatalf("no polling URL in fragment:\n%s", frag)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, status := ts.get("/accounts/device/" + m[1])
		if strings.Contains(status, "Added @me") {
			if strings.Contains(status, "hx-trigger") {
				t.Fatal("finished fragment keeps polling")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("device flow did not finish:\n%s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, body := ts.get("/accounts/device/UNKNOWN"); !strings.Contains(body, "start again") {
		t.Fatalf("unknown flow:\n%s", body)
	}
}

func TestImportFlow(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	dir := filepath.Join(ts.importDir, "NVIDIA")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	overlay := `{"test_approval":{"marker_checks":["tests / Test","tests-skip"],"command":"/ok-to-test","ci_paths":[".github/"],"ci_paths_command":"/ok-to-test confirm-ci"},"stale_days":7}`
	if err := os.WriteFile(filepath.Join(dir, "aicr.json"), []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, settings := ts.get("/settings")
	if !strings.Contains(settings, "NVIDIA/aicr.json") || strings.Contains(settings, "notes.txt") {
		t.Fatalf("import candidates:\n%s", settings)
	}
	if code, _, _ := ts.post("/import", url.Values{"file": {"NVIDIA/aicr.json"}, "account": {"me"}}); code != http.StatusSeeOther {
		t.Fatalf("import = %d", code)
	}
	repos, _ := ts.db.Repos(ts.ctx)
	want, _ := rules.Parse([]byte(overlay))
	if len(repos) != 1 || repos[0].FullName() != "NVIDIA/aicr" || repos[0].Rules.CollectionHash() != want.CollectionHash() {
		t.Fatalf("imported repos = %+v", repos)
	}
	for _, bad := range []string{"../etc/passwd.json", "NVIDIA/missing.json", "NVIDIA/notes.txt"} {
		if code, _, _ := ts.post("/import", url.Values{"file": {bad}, "account": {"me"}}); code != http.StatusUnprocessableEntity {
			t.Errorf("import %q = %d, want 422", bad, code)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/web/ -run 'TestParseRepoReference|TestAddRepoFlow|TestRepoActions|TestAccountFlows|TestDeviceFlowFragments|TestImportFlow'`
Expected: FAIL to compile (`undefined: parseRepoReference`).

- [ ] **Step 3: Write the settings template**

Replace `internal/web/templates/settings.html` with:

```html
{{define "content"}}
<h1>Settings</h1>
{{if .Error}}<p class="banner warn" role="alert">{{.Error}}</p>{{end}}

<section id="accounts"><h2>Accounts</h2>
{{if .Accounts}}<table><thead><tr><th>Account</th><th>Kind</th><th>Scopes</th><th>Status</th><th></th></tr></thead><tbody>
{{range .Accounts}}<tr><td>@{{.Login}}</td><td>{{.Kind}}</td><td>{{.Scopes}}</td>
<td>{{.Status}}{{if .Detail}}: {{.Detail}}{{end}}{{if .PausedUntil}} (rate-limit pause until {{.PausedUntil}}){{end}}</td>
<td><form method="post" action="/accounts/{{.Login}}/delete" class="inline"><button type="submit">Remove</button></form>
<a href="https://github.com/settings/applications">Revoke on GitHub</a></td></tr>{{end}}
</tbody></table>{{else}}<p>No accounts yet.</p>{{end}}
<h3>Add an account</h3>
{{if .ClientIDConfigured}}<div id="device"><button type="button" hx-post="/accounts/device" hx-target="#device" hx-swap="innerHTML">Sign in with GitHub</button></div>
{{else}}<p>Device sign-in needs an OAuth client ID (set <code>DEVBOX_GITHUB_CLIENT_ID</code>); add a classic token instead.</p>{{end}}
<form method="post" action="/accounts/pat" class="inline"><label>Classic token <input type="password" name="token" autocomplete="off" required></label><button type="submit">Add token</button></form>
</section>

<section id="repos"><h2>Repositories</h2>
{{if .AccountLogins}}<form method="post" action="/repos" class="inline">
<label>Repository <input name="repo" placeholder="owner/name or GitHub URL" required></label>
<label>Account <select name="account">{{range .AccountLogins}}<option value="{{.}}">@{{.}}</option>{{end}}</select></label>
<button type="submit">Add</button></form>{{end}}
{{range .Repos}}<details class="repo"><summary><strong>{{.Name}}</strong> @{{.Account}} · {{if .Enabled}}enabled{{else}}disabled{{end}}{{if .LastError}} · last error: {{.LastError}}{{end}}</summary>
<form method="post" action="/repos/{{.ID}}/{{if .Enabled}}disable{{else}}enable{{end}}" class="inline"><button type="submit">{{if .Enabled}}Disable{{else}}Enable{{end}}</button></form>
<form method="post" action="/repos/{{.ID}}/delete" class="inline"><button type="submit">Remove</button></form>
<form method="post" action="/repos/{{.ID}}/rules"><label>Rules (maintainer-sweep overlay JSON)<textarea name="rules" rows="10">{{.RulesJSON}}</textarea></label><button type="submit">Save rules</button></form>
</details>{{else}}<p>No repositories yet.</p>{{end}}
</section>

{{if .Imports}}<section id="import"><h2>Import from maintainer-sweep</h2>
{{range .Imports}}<form method="post" action="/import" class="inline"><input type="hidden" name="file" value="{{.}}"><span>{{.}}</span>
<select name="account">{{range $.AccountLogins}}<option value="{{.}}">@{{.}}</option>{{end}}</select><button type="submit">Import</button></form>{{end}}
</section>{{end}}

<section><h2>Polling</h2><p>Every {{.PollInterval}} (set with <code>--poll-interval</code>).</p></section>
{{end}}

{{define "device"}}<div id="device-flow"{{if .Pending}} hx-get="/accounts/device/{{.ID}}" hx-trigger="every 2s" hx-swap="outerHTML"{{end}}>
{{if .Pending}}<p>Enter code <strong class="code">{{.UserCode}}</strong> at {{if .VerificationURL}}<a href="{{.VerificationURL}}" target="_blank" rel="noopener noreferrer">{{.VerificationURL}}</a>{{else}}{{.VerificationURI}}{{end}}. Waiting for authorization…</p>
{{else if .Done}}<p>Added @{{.Login}}. <a href="/settings">Refresh</a></p>
{{else}}<p class="banner warn">{{.Error}}</p>{{end}}
</div>{{end}}
```

- [ ] **Step 4: Implement settings handlers**

Create `internal/web/settings.go`:

```go
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /accounts/device", s.handleDeviceStart)
	mux.HandleFunc("GET /accounts/device/{flow}", s.handleDeviceStatus)
	mux.HandleFunc("POST /accounts/pat", s.handleAddPAT)
	mux.HandleFunc("POST /accounts/{login}/delete", s.handleRemoveAccount)
	mux.HandleFunc("POST /repos", s.handleAddRepo)
	mux.HandleFunc("POST /repos/{id}/{action}", s.handleRepoAction)
	mux.HandleFunc("POST /import", s.handleImport)
}

var (
	ownerPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	importPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100})\.json$`)
)

// parseRepoReference accepts owner/name or any github.com URL inside the repository.
func parseRepoReference(input string) (string, string, error) {
	s := strings.TrimSpace(input)
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimPrefix(s, "www.")
	if rest, ok := strings.CutPrefix(s, "github.com/"); ok {
		s = rest
	} else if first, _, _ := strings.Cut(s, "/"); strings.ContainsAny(first, ".:@") {
		return "", "", fmt.Errorf("%q is not a github.com repository", input)
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("%q is not owner/name", input)
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !ownerPattern.MatchString(owner) || !namePattern.MatchString(name) || name == "." || name == ".." {
		return "", "", fmt.Errorf("%q is not a valid owner/name", input)
	}
	return owner, name, nil
}

type accountView struct {
	Login, Kind, Scopes, Status, Detail, PausedUntil string
}

type repoSettingsView struct {
	ID        int64
	Name      string
	Account   string
	Enabled   bool
	RulesJSON string
	LastError string
}

type settingsView struct {
	Error              string
	Accounts           []accountView
	AccountLogins      []string
	Repos              []repoSettingsView
	Imports            []string
	PollInterval       string
	ClientIDConfigured bool
}

func (s *Server) buildSettings(ctx context.Context) (settingsView, error) {
	v := settingsView{PollInterval: s.o.PollInterval.String(), Imports: s.importCandidates()}
	v.ClientIDConfigured = s.o.Auth != nil && s.o.Auth.DeviceConfigured()
	accounts, err := s.o.Store.Accounts(ctx)
	if err != nil {
		return v, err
	}
	paused := map[string]string{}
	if s.o.Engine != nil {
		for account, until := range s.o.Engine.RateStatus() {
			paused[account] = until.In(s.o.Location).Format("15:04")
		}
	}
	for _, a := range accounts {
		v.Accounts = append(v.Accounts, accountView{Login: a.Login, Kind: a.Kind, Scopes: strings.Join(a.Scopes, ", "),
			Status: a.Status, Detail: a.StatusDetail, PausedUntil: paused[a.Login]})
		v.AccountLogins = append(v.AccountLogins, a.Login)
	}
	statuses, err := s.o.Store.RepoStatuses(ctx)
	if err != nil {
		return v, err
	}
	for _, st := range statuses {
		v.Repos = append(v.Repos, repoSettingsView{ID: st.Repo.ID, Name: st.Repo.FullName(), Account: st.Repo.Account,
			Enabled: st.Repo.Enabled, RulesJSON: st.Repo.Rules.JSON(), LastError: st.Sync.LastError})
	}
	return v, nil
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, message string) {
	v, err := s.buildSettings(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Error = message
	s.render(w, r, "settings", status, pageData{Title: "Settings", Body: v})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, http.StatusOK, "")
}

// reject re-renders settings with a validation message (never echoing tokens).
func (s *Server) reject(w http.ResponseWriter, r *http.Request, err error) {
	s.o.Log.Info("settings change rejected", "path", r.URL.Path, "err", err)
	msg := err.Error()
	switch {
	case errors.Is(err, store.ErrExists):
		msg = "That repository is already monitored by that account."
	case errors.Is(err, store.ErrNotFound):
		msg = "That account or repository no longer exists; refresh the page."
	}
	s.renderSettings(w, r, http.StatusUnprocessableEntity, msg)
}

func (s *Server) backToSettings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleAddPAT(w http.ResponseWriter, r *http.Request) {
	acct, err := s.o.Auth.AddPAT(r.Context(), r.PostFormValue("token"))
	if err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Log.Info("account added", "account", acct.Login, "kind", acct.Kind)
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Auth.Remove(r.Context(), r.PathValue("login")); err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}

type deviceView struct {
	ID, UserCode, VerificationURI, VerificationURL, Login, Error string
	Pending, Done                                                bool
}

func newDeviceView(f auth.FlowView) deviceView {
	return deviceView{ID: f.ID, UserCode: f.UserCode, VerificationURI: f.VerificationURI, VerificationURL: githubURL(f.VerificationURI),
		Login: f.Login, Error: f.Error, Pending: f.State == auth.FlowPending, Done: f.State == auth.FlowDone}
}

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	f, err := s.o.Auth.StartDevice(r.Context())
	if err != nil {
		s.renderFragment(w, "settings", "device", deviceView{Error: err.Error()})
		return
	}
	s.renderFragment(w, "settings", "device", newDeviceView(f))
}

func (s *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	f, ok := s.o.Auth.Flow(r.PathValue("flow"))
	if !ok {
		s.renderFragment(w, "settings", "device", deviceView{Error: "This sign-in expired or was replaced; start again."})
		return
	}
	if f.State == auth.FlowDone {
		s.o.Engine.ReposChanged()
	}
	s.renderFragment(w, "settings", "device", newDeviceView(f))
}

func (s *Server) handleAddRepo(w http.ResponseWriter, r *http.Request) {
	acct, err := s.o.Store.Account(r.Context(), r.PostFormValue("account"))
	if err != nil {
		s.reject(w, r, err)
		return
	}
	owner, name, err := parseRepoReference(r.PostFormValue("repo"))
	if err != nil {
		s.reject(w, r, err)
		return
	}
	if _, err := s.o.Store.AddRepo(r.Context(), acct.ID, owner, name, rules.Default(), s.o.Now()); err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Log.Info("repository added", "repo", owner+"/"+name, "account", acct.Login)
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}

func (s *Server) handleRepoAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	switch action := r.PathValue("action"); action {
	case "enable", "disable":
		if _, err := s.o.Store.SetRepoEnabled(ctx, id, action == "enable"); err != nil {
			s.reject(w, r, err)
			return
		}
		s.o.Log.Info("repository "+action+"d", "repo_id", id)
		s.o.Engine.ReposChanged()
	case "delete":
		if err := s.o.Store.DeleteRepo(ctx, id); err != nil {
			s.reject(w, r, err)
			return
		}
		s.o.Log.Info("repository removed", "repo_id", id)
		s.o.Engine.ReposChanged()
	case "rules":
		rl, err := rules.Parse([]byte(r.PostFormValue("rules")))
		if err != nil {
			s.reject(w, r, err)
			return
		}
		upd, err := s.o.Store.UpdateRepoRules(ctx, id, rl, s.o.Now())
		if err != nil {
			s.reject(w, r, err)
			return
		}
		for _, tr := range upd.Transitions {
			s.o.Hub.Publish(hub.Event{RepoID: id, Number: tr.Number})
		}
		if err := s.o.Engine.RulesChanged(ctx, id, upd.CollectionChanged); err != nil {
			s.serverError(w, err)
			return
		}
		s.o.Log.Info("repository rules updated", "repo", upd.Repo.FullName(), "collection_changed", upd.CollectionChanged)
	default:
		http.NotFound(w, r)
		return
	}
	s.backToSettings(w, r)
}

// importCandidates lists <owner>/<repo>.json overlays under the import directory.
func (s *Server) importCandidates() []string {
	if s.o.ImportDir == "" {
		return nil
	}
	owners, err := os.ReadDir(s.o.ImportDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.o.ImportDir, owner.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if rel := owner.Name() + "/" + f.Name(); !f.IsDir() && importPattern.MatchString(rel) {
				out = append(out, rel)
			}
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	file := r.PostFormValue("file")
	m := importPattern.FindStringSubmatch(file)
	if m == nil || m[2] == "." || m[2] == ".." || s.o.ImportDir == "" {
		s.reject(w, r, fmt.Errorf("%q is not an importable overlay", file))
		return
	}
	data, err := os.ReadFile(filepath.Join(s.o.ImportDir, m[1], m[2]+".json")) //nolint:gosec // owner and name are validated and joined under the import directory
	if err != nil {
		s.reject(w, r, fmt.Errorf("read %s: %w", file, err))
		return
	}
	rl, err := rules.Parse(data)
	if err != nil {
		s.reject(w, r, fmt.Errorf("%s: %w", file, err))
		return
	}
	acct, err := s.o.Store.Account(r.Context(), r.PostFormValue("account"))
	if err != nil {
		s.reject(w, r, err)
		return
	}
	if _, err := s.o.Store.AddRepo(r.Context(), acct.ID, m[1], m[2], rl, s.o.Now()); err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Log.Info("repository imported", "repo", m[1]+"/"+m[2], "account", acct.Login, "file", file)
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}
```

In `internal/web/server.go`, register the settings routes in `Handler`:

```go
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.viewRoutes(mux)
	s.settingsRoutes(mux)
	return s.logRequests(s.secure(mux))
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/web/ -v`
Expected: PASS for all web tests.

- [ ] **Step 6: Lint and commit**

```bash
make fmt lint
git add internal/web
git commit -S -m "feat(web): manage accounts, repositories, rules, and imports"
```

---

### Task 16: Wiring — logging, `devbox serve`, shutdown, end-to-end test

**Files:**
- Create: `internal/logging/logging.go`, `internal/app/app.go`
- Modify: `cmd/devbox/main.go` (add `serve`)
- Test: `internal/logging/logging_test.go`, `internal/app/app_test.go`, `cmd/devbox/main_test.go` (extend)

**Interfaces:**
- Consumes: every earlier package: `store.Open`, `(*store.DB).Purge`, `hub.New`, `metrics.New`, `metrics.RegisterPRGauge`, `github.New`, `collect.NewSource`, `collect.GitHubReader`, `syncer.New`, `syncer.Config`, `syncer.Deps`, `syncer.Source`, `auth.NewManager`, `auth.Keyring`, `auth.DeviceClient`, `auth.ClientID`, `web.New`, `web.Options`, `version.Version`.
- Produces:
  - `logging.New(w io.Writer, format string, debug bool) (*slog.Logger, error)`
  - `app.Config{Addr, DBPath string; PollInterval time.Duration; AllowRemote bool; GitHubAPIURL, GitHubURL, ImportDir string; Logger *slog.Logger; Secrets auth.Secrets; Ready func(addr string); EngineConfig syncer.Config}`; `(app.Config) Validate() error`; `app.Run(ctx, Config) error`; `app.MinPollInterval`
  - CLI: `devbox serve [--addr] [--db] [--poll-interval] [--debug] [--log-format] [--allow-remote]` plus hidden `--github-api-url`, `--github-url`; exit codes 0/1/2 (§11)

- [ ] **Step 1: Write the failing logging tests**

Create `internal/logging/logging_test.go`:

```go
package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "json", true)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hello", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hello"`) || !strings.Contains(buf.String(), `"level":"DEBUG"`) {
		t.Fatalf("json debug output = %q", buf.String())
	}
	buf.Reset()
	log, _ = New(&buf, "text", false)
	log.Debug("hidden")
	log.Info("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "msg=shown") {
		t.Fatalf("text info output = %q", buf.String())
	}
	buf.Reset()
	log, _ = New(&buf, "auto", false)
	log.Info("auto")
	if !strings.HasPrefix(buf.String(), "{") {
		t.Fatalf("auto on a non-terminal should be JSON: %q", buf.String())
	}
	if _, err := New(&buf, "xml", false); err == nil {
		t.Fatal("unknown format accepted")
	}
}
```

- [ ] **Step 2: Implement logging**

Create `internal/logging/logging.go`:

```go
// Package logging configures devbox's structured logger (§10).
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// New returns a logger writing to w. format is auto, text, or json; auto picks
// text on a terminal and JSON otherwise. debug enables debug level and source locations.
func New(w io.Writer, format string, debug bool) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if debug {
		opts.Level, opts.AddSource = slog.LevelDebug, true
	}
	if format == "auto" {
		format = "json"
		if isTerminal(w) {
			format = "text"
		}
	}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("unknown log format %q (want auto, text, or json)", format)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
```

Run: `CGO_ENABLED=1 go test -race ./internal/logging/`
Expected: PASS.

- [ ] **Step 3: Write the failing app tests**

Create `internal/app/app_test.go`:

```go
package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/syncer"
)

const fingerprintsJSON = `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
{"number":7,"updatedAt":"2026-10-01T09:30:00Z","baseRefOid":"b1","headRefOid":"h1","mergeStateStatus":"CLEAN","reviewDecision":null,"isDraft":false,
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}]}}}}`

const detailJSON = `{"data":{"repository":{"pullRequest":{"number":7,"title":"Fix flaky test","url":"https://github.com/o/r/pull/7",
"isDraft":false,"isCrossRepository":false,"createdAt":"2026-10-01T08:00:00Z","updatedAt":"2026-10-01T09:30:00Z",
"authorAssociation":"CONTRIBUTOR","additions":10,"deletions":2,"milestone":null,"author":{"login":"alice","__typename":"User"},
"baseRefName":"main","baseRefOid":"b1","headRefOid":"h1","mergeStateStatus":"CLEAN","reviewDecision":null,"autoMergeRequest":null,
"reviewRequests":{"nodes":[]},"myReviews":{"nodes":[]},"latestOpinionatedReviews":{"nodes":[]},
"reviewThreads":{"totalCount":0,"nodes":[]},"comments":{"nodes":[]},
"commits":{"totalCount":1,"nodes":[{"commit":{"oid":"h1","signature":{"state":"VALID"}}}]},
"head":{"nodes":[{"commit":{"oid":"h1","committedDate":"2026-10-01T09:00:00Z","statusCheckRollup":{"state":"SUCCESS"}}}]}}}}}`

func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo, read:org")
			fmt.Fprint(w, `{"login":"me"}`)
		case "/graphql":
			body, _ := io.ReadAll(r.Body)
			switch {
			case strings.Contains(string(body), "query Fingerprints"):
				fmt.Fprint(w, fingerprintsJSON)
			case strings.Contains(string(body), "query PullRequestDetail"):
				fmt.Fprint(w, detailJSON)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/repos/o/r/rules/branches/main":
			fmt.Fprint(w, `[]`)
		case "/repos/o/r/branches/main/protection/required_signatures":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Branch not protected"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func baseConfig(t *testing.T) Config {
	return Config{
		Addr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "devbox.db"), PollInterval: time.Minute,
		GitHubAPIURL: github.DefaultAPIURL, GitHubURL: "https://github.com", Secrets: auth.NewMemSecrets(),
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Config){
		"poll interval below minimum": func(c *Config) { c.PollInterval = 30 * time.Second },
		"non-loopback without opt-in": func(c *Config) { c.Addr = "0.0.0.0:8484" },
		"malformed address":           func(c *Config) { c.Addr = "8484" },
		"bad API URL":                 func(c *Config) { c.GitHubAPIURL = "ftp://x" },
		"missing database path":       func(c *Config) { c.DBPath = "" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig(t)
			mod(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	cfg := baseConfig(t)
	cfg.Addr, cfg.AllowRemote = "0.0.0.0:8484", true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("allow-remote rejected: %v", err)
	}
	cfg = baseConfig(t)
	cfg.Addr = "localhost:8484"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("localhost rejected: %v", err)
	}
}

func TestRunFailsWhenPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := baseConfig(t)
	cfg.Addr = ln.Addr().String()
	start := time.Now()
	err = Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v, want a listen error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("port conflict was not reported promptly")
	}
}

func TestRunEndToEnd(t *testing.T) {
	gh := fakeGitHub(t)
	cfg := baseConfig(t)
	cfg.GitHubAPIURL, cfg.GitHubURL = gh.URL, gh.URL
	cfg.EngineConfig = syncer.Config{DispatchEvery: 20 * time.Millisecond, ResyncEvery: 50 * time.Millisecond}
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr
	case err := <-done:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server not ready")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", base)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s = %d", path, resp.StatusCode)
		}
	}
	post("/accounts/pat", url.Values{"token": {"ghp_e2e"}})
	post("/repos", url.Values{"account": {"me"}, "repo": {"o/r"}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "Fix flaky test") && strings.Contains(string(body), "Needs initial review (1)") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue never showed the PR:\n%s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not shut down")
	}
}
```

Run: `CGO_ENABLED=1 go test -race ./internal/app/`
Expected: FAIL to compile (`undefined: Config`, `Run`).

- [ ] **Step 4: Implement the app**

Create `internal/app/app.go`:

```go
// Package app wires devbox's components and runs the server with an orderly
// shutdown (§3.3).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/store"
	"github.com/thingzio/devbox/internal/syncer"
	"github.com/thingzio/devbox/internal/version"
	"github.com/thingzio/devbox/internal/web"
)

// MinPollInterval is the shortest allowed fingerprint poll interval (§7).
const MinPollInterval = time.Minute

// Config is everything `devbox serve` needs.
type Config struct {
	Addr         string
	DBPath       string
	PollInterval time.Duration
	AllowRemote  bool
	GitHubAPIURL string
	GitHubURL    string
	ImportDir    string
	Logger       *slog.Logger
	Secrets      auth.Secrets      // nil uses the OS keychain
	Ready        func(addr string) // called with the bound address once serving
	EngineConfig syncer.Config     // zero values take engine defaults; PollInterval is overridden
}

// Validate rejects configurations that are unsafe or unusable.
func (c Config) Validate() error {
	if c.PollInterval < MinPollInterval {
		return fmt.Errorf("poll interval %s is below the %s minimum", c.PollInterval, MinPollInterval)
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", c.Addr, err)
	}
	if !c.AllowRemote && !isLoopback(host) {
		return fmt.Errorf("address %s is not loopback; pass --allow-remote to accept remote connections", c.Addr)
	}
	for _, raw := range []string{c.GitHubAPIURL, c.GitHubURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("invalid GitHub URL %q", raw)
		}
	}
	if c.DBPath == "" {
		return errors.New("database path is required")
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run serves until ctx is cancelled, then stops HTTP (10s grace), pollers,
// in-flight enrichment, the hub, and the database, in that order.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg.AllowRemote {
		log.Warn("accepting non-loopback connections; the UI has no login", "addr", cfg.Addr)
	}
	db, err := store.Open(ctx, cfg.DBPath, log)
	if err != nil {
		return err
	}
	defer db.Close()
	if n, err := db.Purge(ctx, time.Now()); err != nil {
		return err
	} else if n > 0 {
		log.Info("purged expired cache entries", "rows", n)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	h := hub.New()
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := metrics.New(reg)
	metrics.RegisterPRGauge(reg, db)
	secrets := cfg.Secrets
	if secrets == nil {
		secrets = auth.Keyring{}
	}

	var engine *syncer.Engine
	newClient := func(token, account string) (*github.Client, error) {
		return github.New(github.Options{
			APIURL: cfg.GitHubAPIURL, Token: token, UserAgent: "devbox/" + version.Version, Logger: log,
			OnRequest: m.ObserveRequest,
			OnRate: func(r github.Rate) {
				if account != "" {
					engine.ObserveRate(account, r)
				}
			},
		})
	}
	sources := func(account string) (syncer.Source, error) {
		token, err := secrets.Get(account)
		if err != nil {
			return nil, err
		}
		c, err := newClient(token, account)
		if err != nil {
			return nil, err
		}
		return collect.NewSource(collect.GitHubReader{Client: c}, db, log), nil
	}
	ecfg := cfg.EngineConfig
	ecfg.PollInterval = cfg.PollInterval
	engine = syncer.New(ecfg, syncer.Deps{Store: db, Hub: h, Metrics: m, Log: log, Sources: sources})

	mgrCtx, cancelMgr := context.WithCancel(context.Background())
	defer cancelMgr()
	mgr := auth.NewManager(mgrCtx, auth.ManagerOptions{
		Store: db, Secrets: secrets, Log: log,
		Device:    &auth.DeviceClient{BaseURL: cfg.GitHubURL, ClientID: auth.ClientID()},
		NewClient: func(token string) (*github.Client, error) { return newClient(token, "") },
	})
	srv, err := web.New(web.Options{
		Store: db, Hub: h, Auth: mgr, Engine: engine, Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
		Log: log, Addr: ln.Addr().String(), AllowRemote: cfg.AllowRemote, PollInterval: cfg.PollInterval, ImportDir: cfg.ImportDir,
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	httpSrv.RegisterOnShutdown(cancelBase) // ends SSE streams so Shutdown can finish

	engineCtx, cancelEngine := context.WithCancel(context.Background())
	defer cancelEngine()
	engineDone := make(chan error, 1)
	go func() { engineDone <- engine.Run(engineCtx) }()
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpSrv.Serve(ln) }()

	accounts, _ := db.Accounts(ctx)
	repos, _ := db.Repos(ctx)
	log.Info("devbox started", "version", version.Version, "url", "http://"+ln.Addr().String(), "db", cfg.DBPath,
		"poll_interval", cfg.PollInterval, "accounts", len(accounts), "repos", len(repos), "device_flow", mgr.DeviceConfigured())
	if cfg.Ready != nil {
		cfg.Ready(ln.Addr().String())
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("http server: %w", err)
		}
	}
	log.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown incomplete", "err", err)
	}
	cancelShutdown()
	cancelEngine()
	if err := <-engineDone; err != nil {
		log.Warn("engine stopped with error", "err", err)
	}
	mgr.Close()
	h.Close()
	log.Info("stopped")
	return runErr
}
```

- [ ] **Step 5: Run the app tests to verify they pass**

Run: `go mod tidy && CGO_ENABLED=1 go test -race ./internal/app/ -v`
Expected: PASS for `TestValidate`, `TestRunFailsWhenPortInUse`, `TestRunEndToEnd`.

- [ ] **Step 6: Write the failing CLI tests**

Replace `cmd/devbox/main_test.go` with:

```go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thingzio/devbox/internal/version"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version output = %q, want %q", got, version.Version)
	}
}

func TestRunUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":      nil,
		"unknown command": {"frobnicate"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !strings.Contains(errOut.String(), "usage: devbox") {
				t.Fatalf("stderr %q does not contain usage", errOut.String())
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "serve") {
		t.Fatalf("help %q does not list serve", out.String())
	}
}

func TestServeUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":            {"serve", "--nope"},
		"poll interval too short": {"serve", "--poll-interval", "10s"},
		"bad log format":          {"serve", "--log-format", "xml"},
		"non-loopback address":    {"serve", "--addr", "0.0.0.0:8484"},
		"unexpected argument":     {"serve", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr %q)", code, errOut.String())
			}
		})
	}
}

func TestServeHelpHidesTestFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "--help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	help := errOut.String()
	if !strings.Contains(help, "--poll-interval") || strings.Contains(help, "github-api-url") || strings.Contains(help, "github-url") {
		t.Fatalf("help = %q", help)
	}
}

func TestDefaultPaths(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	t.Setenv("XDG_CONFIG_HOME", "/config")
	t.Setenv("MAINTAINER_SWEEP_CONFIG_DIR", "")
	if got := defaultDBPath(); got != filepath.Join("/data", "devbox", "devbox.db") {
		t.Fatalf("defaultDBPath = %s", got)
	}
	if got := defaultImportDir(); got != filepath.Join("/config", "maintainer-sweep", "repos") {
		t.Fatalf("defaultImportDir = %s", got)
	}
	t.Setenv("MAINTAINER_SWEEP_CONFIG_DIR", "/sweep")
	if got := defaultImportDir(); got != filepath.Join("/sweep", "repos") {
		t.Fatalf("override import dir = %s", got)
	}
}
```

Run: `CGO_ENABLED=1 go test -race ./cmd/devbox/`
Expected: FAIL (`serve` is an unknown command; `defaultDBPath` undefined).

- [ ] **Step 7: Implement `devbox serve`**

Replace `cmd/devbox/main.go` with:

```go
// Command devbox runs the maintainer queue server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thingzio/devbox/internal/app"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/logging"
	"github.com/thingzio/devbox/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "devbox: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: devbox <command> [flags]

commands:
  serve     run the maintainer queue server
  version   print the version
`)
}

// hiddenFlags are for tests against fake GitHub servers and are omitted from help.
var hiddenFlags = map[string]bool{"github-api-url": true, "github-url": true}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8484", "listen address")
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	poll := fs.Duration("poll-interval", 2*time.Minute, "fingerprint poll interval (minimum 1m)")
	debug := fs.Bool("debug", false, "log at debug level, including every GitHub request")
	logFormat := fs.String("log-format", "auto", "log format: auto, text, or json")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback --addr")
	apiURL := fs.String("github-api-url", github.DefaultAPIURL, "GitHub API base URL")
	webURL := fs.String("github-url", "https://github.com", "GitHub web base URL")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: devbox serve [flags]")
		fs.VisitAll(func(f *flag.Flag) {
			if !hiddenFlags[f.Name] {
				fmt.Fprintf(stderr, "  --%s\t%s (default %q)\n", f.Name, f.Usage, f.DefValue)
			}
		})
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "devbox serve: unexpected arguments %v\n", fs.Args())
		return 2
	}
	log, err := logging.New(stderr, *logFormat, *debug)
	if err != nil {
		fmt.Fprintln(stderr, "devbox serve:", err)
		return 2
	}
	cfg := app.Config{
		Addr: *addr, DBPath: *dbPath, PollInterval: *poll, AllowRemote: *allowRemote,
		GitHubAPIURL: *apiURL, GitHubURL: *webURL, ImportDir: defaultImportDir(), Logger: log,
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "devbox serve:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		log.Error("devbox stopped with an error", "err", err)
		return 1
	}
	return 0
}

func defaultDBPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "devbox.db"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "devbox", "devbox.db")
}

func defaultImportDir() string {
	if dir := os.Getenv("MAINTAINER_SWEEP_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "repos")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "maintainer-sweep", "repos")
}
```

- [ ] **Step 8: Run all tests and a manual smoke check**

Run: `make test`
Expected: PASS across all packages.

Run: `make build && ./bin/devbox serve --db "$TMPDIR/devbox-smoke.db" --debug` in one terminal, then in another: `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8484/healthz`
Expected: `200`; the server log shows `devbox started` with `url=http://127.0.0.1:8484`; Ctrl-C prints `shutting down` and `stopped` and exits 0.

- [ ] **Step 9: Lint and commit**

```bash
make fmt lint
git add go.mod go.sum cmd/devbox internal/app internal/logging
git commit -S -m "feat: wire devbox serve with logging, metrics, and orderly shutdown"
```

---

### Task 17: Golden fixtures, live test, coverage floor, README

**Files:**
- Create: `internal/collect/golden_test.go`, `internal/collect/live_test.go`, `hack/record-fixtures.sh`, `internal/collect/testdata/golden/<recorded-name>/{fixture.json,expected.json}`, `README.md`
- Modify: `.coverage-floor`

**Interfaces:**
- Consumes: Task 9/10 `GitHubReader`, `Collector`, `Input`, `Outcome`; Task 6 `github.New`, `github.Options`; Task 2 `rules.Parse`.
- Produces: `make fixtures REPO=owner/name PR=n` records a replayable golden fixture; `go test ./internal/collect -run TestGolden -args -update` rewrites expectations; opt-in live test `DEVBOX_LIVE_TEST=1`.

- [ ] **Step 1: Write the golden replay and recording tests**

Create `internal/collect/golden_test.go`:

```go
package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
)

var (
	updateGolden = flag.Bool("update", false, "rewrite golden expected.json files")
	recordGolden = flag.Bool("record", false, "record a golden fixture from the live GitHub API (needs GITHUB_TOKEN)")
	recordRepo   = flag.String("record-repo", "", "owner/name to record")
	recordPR     = flag.Int("record-pr", 0, "pull request number to record")
	recordViewer = flag.String("record-viewer", "", "viewer login")
	recordName   = flag.String("record-name", "", "fixture directory name")
	recordRules  = flag.String("record-rules", "", "optional rules overlay JSON file")
)

const goldenBase = "BASE"

var goldenNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type exchange struct {
	Key    string `json:"key"`
	Status int    `json:"status"`
	Link   string `json:"link,omitempty"`
	Body   string `json:"body"`
}

type goldenFixture struct {
	Repo      string          `json:"repo"`
	Number    int             `json:"number"`
	Viewer    string          `json:"viewer"`
	Rules     json.RawMessage `json:"rules,omitempty"`
	Exchanges []exchange      `json:"exchanges"`
}

type goldenResult struct {
	Kind   Kind            `json:"kind"`
	Error  string          `json:"error,omitempty"`
	Record classify.Record `json:"record"`
}

type noCache struct{}

func (noCache) CachedDiff(context.Context, int64, string, string, string) (bool, bool, error) {
	return false, false, nil
}
func (noCache) CachedComment(context.Context, int64, int64, time.Time) (bool, bool, error) {
	return false, false, nil
}
func (noCache) CachedPolicy(context.Context, int64, string, time.Time, time.Duration) (string, bool, error) {
	return "", false, nil
}

// requestKey identifies a request independent of host: GraphQL by operation
// name and variables, REST by path and query.
func requestKey(r *http.Request, body []byte) string {
	if strings.HasSuffix(r.URL.Path, "/graphql") {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		op := ""
		if fields := strings.Fields(req.Query); len(fields) > 1 {
			op, _, _ = strings.Cut(fields[1], "(")
		}
		vars, _ := json.Marshal(req.Variables)
		return "POST graphql " + op + " " + string(vars)
	}
	return r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
}

type recorder struct {
	base      http.RoundTripper
	apiBase   string
	mu        sync.Mutex
	exchanges []exchange
}

func (rec *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := rec.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	rec.mu.Lock()
	rec.exchanges = append(rec.exchanges, exchange{
		Key: requestKey(req, body), Status: resp.StatusCode,
		Link: strings.ReplaceAll(resp.Header.Get("Link"), rec.apiBase, goldenBase), Body: string(data),
	})
	rec.mu.Unlock()
	return resp, nil
}

func replayServer(t *testing.T, fx goldenFixture) *httptest.Server {
	t.Helper()
	byKey := map[string]exchange{}
	for _, ex := range fx.Exchanges {
		byKey[ex.Key] = ex
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ex, ok := byKey[requestKey(r, body)]
		if !ok {
			t.Errorf("unrecorded request %s", requestKey(r, body))
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		if ex.Link != "" {
			w.Header().Set("Link", strings.ReplaceAll(ex.Link, goldenBase, srv.URL))
		}
		w.WriteHeader(ex.Status)
		_, _ = io.WriteString(w, ex.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collectGolden(ctx context.Context, c *github.Client, fx goldenFixture) (goldenResult, error) {
	owner, name, ok := strings.Cut(fx.Repo, "/")
	if !ok {
		return goldenResult{}, fmt.Errorf("repo %q is not owner/name", fx.Repo)
	}
	rl, err := rules.Parse(fx.Rules)
	if err != nil {
		return goldenResult{}, err
	}
	reader := GitHubReader{Client: c}
	observed, err := reader.Fingerprints(ctx, owner, name)
	if err != nil {
		return goldenResult{}, err
	}
	fingerprint := ""
	for _, o := range observed {
		if o.Number == fx.Number {
			fingerprint = o.Fingerprint
		}
	}
	if fingerprint == "" {
		return goldenResult{}, fmt.Errorf("PR #%d is not open in %s", fx.Number, fx.Repo)
	}
	out := (&Collector{Reader: reader, Cache: noCache{}}).Collect(ctx, Input{
		RepoID: 1, Owner: owner, Name: name, Viewer: fx.Viewer, Number: fx.Number, Rules: rl,
		QueuedFingerprint: fingerprint, Now: goldenNow,
	})
	res := goldenResult{Kind: out.Kind, Record: out.Record}
	if out.Err != nil {
		res.Error = out.Err.Error()
	}
	return res, nil
}

func TestGolden(t *testing.T) {
	paths, err := filepath.Glob("testdata/golden/*/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden fixtures; record one with make fixtures REPO=owner/name PR=n")
	}
	for _, path := range paths {
		dir := filepath.Dir(path)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fx goldenFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			srv := replayServer(t, fx)
			c, err := github.New(github.Options{APIURL: srv.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			got, err := collectGolden(context.Background(), c, fx)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			expectedPath := filepath.Join(dir, "expected.json")
			if *updateGolden {
				if err := os.WriteFile(expectedPath, append(gotJSON, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(gotJSON)) {
				t.Fatalf("golden mismatch in %s; rerun with -args -update and review the diff\n%s", dir, gotJSON)
			}
		})
	}
}

func TestRecordGolden(t *testing.T) {
	if !*recordGolden {
		t.Skip("recording is opt-in: go test -run TestRecordGolden -args -record ...")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" || *recordRepo == "" || *recordPR == 0 || *recordViewer == "" || *recordName == "" {
		t.Fatal("recording needs GITHUB_TOKEN, -record-repo, -record-pr, -record-viewer, and -record-name")
	}
	var rulesJSON json.RawMessage
	if *recordRules != "" {
		data, err := os.ReadFile(*recordRules)
		if err != nil {
			t.Fatal(err)
		}
		rulesJSON = data
	}
	rec := &recorder{base: http.DefaultTransport, apiBase: strings.TrimSuffix(github.DefaultAPIURL, "/")}
	c, err := github.New(github.Options{Token: token, HTTPClient: &http.Client{Transport: rec}})
	if err != nil {
		t.Fatal(err)
	}
	fx := goldenFixture{Repo: *recordRepo, Number: *recordPR, Viewer: *recordViewer, Rules: rulesJSON}
	got, err := collectGolden(context.Background(), c, fx)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	fx.Exchanges = rec.exchanges
	dir := filepath.Join("testdata", "golden", *recordName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.MarshalIndent(fx, "", "  ")
	expected, _ := json.MarshalIndent(got, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), append(fixture, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "expected.json"), append(expected, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d exchanges into %s", len(fx.Exchanges), dir)
}
```

Create `internal/collect/live_test.go`:

```go
package collect

import (
	"cmp"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
)

// TestLiveCollect reads one open PR from a public repository with the real
// API. It is opt-in (DEVBOX_LIVE_TEST=1) and never runs in CI.
func TestLiveCollect(t *testing.T) {
	if os.Getenv("DEVBOX_LIVE_TEST") != "1" {
		t.Skip("set DEVBOX_LIVE_TEST=1 to run against the live GitHub API")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Fatal("DEVBOX_LIVE_TEST=1 needs GITHUB_TOKEN")
	}
	owner, name, _ := strings.Cut(cmp.Or(os.Getenv("DEVBOX_LIVE_REPO"), "NVIDIA/aicr"), "/")
	ctx := context.Background()
	c, err := github.New(github.Options{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	var me struct {
		Login string `json:"login"`
	}
	if _, err := c.Get(ctx, "user", &me); err != nil {
		t.Fatalf("identify: %v", err)
	}
	reader := GitHubReader{Client: c}
	observed, err := reader.Fingerprints(ctx, owner, name)
	if err != nil {
		t.Fatalf("fingerprints: %v", err)
	}
	if len(observed) == 0 {
		t.Skipf("%s/%s has no open pull requests", owner, name)
	}
	out := (&Collector{Reader: reader, Cache: noCache{}}).Collect(ctx, Input{
		RepoID: 1, Owner: owner, Name: name, Viewer: me.Login, Number: observed[0].Number, Rules: rules.Default(),
		QueuedFingerprint: observed[0].Fingerprint, Now: time.Now(),
	})
	if out.Kind == Failed {
		t.Fatalf("live collection failed: %v", out.Err)
	}
	t.Logf("%s/%s#%d kind=%d evidence_errors=%d", owner, name, observed[0].Number, out.Kind, len(out.Record.EvidenceErrors))
}
```

- [ ] **Step 2: Add the recording script**

Create `hack/record-fixtures.sh`:

```bash
#!/usr/bin/env bash
# Records a golden collect fixture from the live GitHub API (read-only) into
# internal/collect/testdata/golden/<name>. Review expected.json before committing.
set -euo pipefail
usage="usage: hack/record-fixtures.sh <owner/name> <pr-number> [viewer] [name] [rules.json]"
repo=${1:?$usage}
pr=${2:?$usage}
viewer=${3:-$(gh api user --jq .login)}
name=${4:-$(printf '%s-%s' "${repo//\//-}" "$pr" | tr '[:upper:]' '[:lower:]')}
rules=${5:-}
GITHUB_TOKEN="${GITHUB_TOKEN:-$(gh auth token)}"
export GITHUB_TOKEN
args=(-record -record-repo "$repo" -record-pr "$pr" -record-viewer "$viewer" -record-name "$name")
if [ -n "$rules" ]; then
  args+=(-record-rules "$rules")
fi
CGO_ENABLED=0 go test ./internal/collect -run '^TestRecordGolden$' -count=1 -v -args "${args[@]}"
echo "recorded internal/collect/testdata/golden/$name; review expected.json before committing"
```

```bash
chmod +x hack/record-fixtures.sh
```

- [ ] **Step 3: Record and verify one golden fixture**

Pick an open PR in a public repository, record it with the existing sweep overlay, and replay it:

```bash
PR=$(gh pr list -R NVIDIA/aicr --state open --limit 1 --json number --jq '.[0].number')
RULES="${XDG_CONFIG_HOME:-$HOME/.config}/maintainer-sweep/repos/NVIDIA/aicr.json"
./hack/record-fixtures.sh NVIDIA/aicr "$PR" "" nvidia-aicr "$( [ -f "$RULES" ] && echo "$RULES" )"
CGO_ENABLED=1 go test -race ./internal/collect -run TestGolden -v
```

Expected: the record step logs `recorded N exchanges into testdata/golden/nvidia-aicr`; `TestGolden/nvidia-aicr` PASSES from the replay with no network. Read `internal/collect/testdata/golden/nvidia-aicr/expected.json` and confirm the record matches the PR on GitHub (author, head OID, merge state, review state). The fixture contains only public data from a public repository and no token (the `Authorization` header is never recorded).

- [ ] **Step 4: Write the README**

Create `README.md`:

````markdown
# devbox

A local, single-user maintainer queue for GitHub. `devbox` polls the repositories
you maintain, works out which open pull requests are waiting on you and why, and
serves a live queue at http://127.0.0.1:8484. State lives in SQLite; tokens live
in your OS keychain.

Design: [docs/superpowers/specs/2026-10-12-devbox-phase1-design.md](docs/superpowers/specs/2026-10-12-devbox-phase1-design.md)

## Build

```bash
make build CLIENT_ID=<thingzio OAuth App client id>   # CLIENT_ID optional; classic tokens work without it
./bin/devbox serve
```

`CGO_ENABLED=0` builds are the default; `make cross` builds darwin, linux, and windows binaries.

## Accounts

- **Sign in with GitHub** (device flow) needs the `thingzio` OAuth App client ID, baked in with
  `make build CLIENT_ID=...` or set at runtime with `DEVBOX_GITHUB_CLIENT_ID`. The app requests
  `repo read:org`. Organizations that restrict OAuth Apps need an owner's approval for private repositories.
- **Classic token**: paste a classic personal access token with the `repo` scope. Fine-grained
  tokens are refused because they cannot read notifications.

## Run

```
devbox serve [--addr 127.0.0.1:8484] [--db PATH] [--poll-interval 2m]
             [--debug] [--log-format auto|text|json] [--allow-remote]
devbox version
```

- Database: `${XDG_DATA_HOME:-~/.local/share}/devbox/devbox.db`
- maintainer-sweep overlays offered for import: `${XDG_CONFIG_HOME:-~/.config}/maintainer-sweep/repos/<owner>/<repo>.json`
  (or `$MAINTAINER_SWEEP_CONFIG_DIR/repos`)
- `--debug` logs every GitHub request (never tokens, bodies, or diffs).
- Metrics: `GET /metrics` (Prometheus). Health: `GET /healthz`.

## Develop

```bash
make test      # race tests (CGO_ENABLED=1)
make lint      # golangci-lint, version from .versions.yaml
make vuln      # govulncheck
make cover     # tests + coverage floor from .coverage-floor
make fixtures REPO=owner/name PR=123   # record a golden collect fixture (read-only, uses gh)
DEVBOX_LIVE_TEST=1 GITHUB_TOKEN=$(gh auth token) go test ./internal/collect -run TestLiveCollect -v
```
````

- [ ] **Step 5: Set the coverage floor**

```bash
make cover || true
TOTAL=$(go tool cover -func=coverage.out | awk '/^total:/ {gsub("%", "", $3); print $3}')
python3 -c "import math,sys; print(f'{math.floor(float(sys.argv[1])*10)/10:.1f}')" "$TOTAL" > .coverage-floor
cat .coverage-floor
make cover
```

Expected: `.coverage-floor` holds the measured total rounded down to one decimal (for example `78.4`), and the final `make cover` prints `coverage: X% (floor Y%)` and exits 0.

- [ ] **Step 6: Run every gate**

Run: `make test && make lint && make vuln && make build && make cross && make cover`
Expected: all succeed; `govulncheck` reports no vulnerabilities in called code.

- [ ] **Step 7: Commit**

```bash
git add internal/collect hack/record-fixtures.sh README.md .coverage-floor
git commit -S -m "test: add golden collect fixtures, live test, coverage floor, and README"
```

---
