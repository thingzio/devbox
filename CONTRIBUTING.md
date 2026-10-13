# Contributing to DevBox

Contributions are welcome, from typo fixes to new capabilities. This document
covers what you need to know to build, test, and change DevBox.

## Table of Contents

- [Project governance](#project-governance)
- [Getting started](#getting-started)
- [Design principles](#design-principles)
- [Architecture](#architecture)
- [Testing](#testing)
- [Working on the UI](#working-on-the-ui)
- [How to contribute](#how-to-contribute)
- [Pull request process](#pull-request-process)
- [Code style](#code-style)
- [Commits](#commits)

## Project governance

This is a single-maintainer project, maintained on a best-effort basis.
Decisions about what gets merged, what ships, and what is in scope are the
maintainer's. There is no review-time commitment: a pull request may be reviewed
the same day or sit for weeks.

- **Small, focused pull requests get reviewed fastest.**
- **Open an issue before a large change.** Finding out a direction is wrong after
  a week of work is worse for you than for the project.
- **A closed pull request is not a judgment on you.** Scope is the most common
  reason.

## Getting started

You need Go (the version in `.versions.yaml`) and `make`. Pinned linters and
scanners run through `go run`, so there is nothing else to install. You do not
need any credentials from the maintainer.

```bash
git clone https://github.com/thingzio/devbox && cd devbox
make build     # bin/devbox, CGO_ENABLED=0
make test      # race tests (CGO_ENABLED=1)
make lint      # golangci-lint
make vuln      # govulncheck
make cover     # tests plus the coverage floor
make cross     # darwin, linux, and windows binaries without cgo
```

`make build CLIENT_ID=<id>` bakes in the OAuth App client ID that enables
**Sign in with GitHub**; without it, use a classic token, or set
`DEVBOX_GITHUB_CLIENT_ID` at runtime.

To try a change against real data, run `./bin/devbox serve --debug --db /tmp/devbox.db`
so you don't touch your everyday database.

## Design principles

- **Local development equals CI.** `.versions.yaml` is the single source of
  truth for the Go, golangci-lint, and govulncheck versions; the Makefile and CI
  both read it. If `make test lint vuln cover cross` passes locally, CI passes.
- **Correctness must be reproducible.** The classifier is a pure function of
  the stored record and the repository's rules: the same inputs always produce
  the same bucket, reason, and action.
- **Partial failure is the steady state.** Polls retry with bounded backoff,
  rate limits pause an account rather than fail it, and a PR whose evidence
  could not be read keeps its last result and says why.
- **Read-only and private.** DevBox never writes to GitHub. Never log tokens,
  `Authorization` headers, device codes, pull request or comment bodies, or
  diffs, including at `--debug`.
- **Boring first.** Pure-Go SQLite (`modernc.org/sqlite`), server-rendered
  `html/template`, and vendored htmx; no JavaScript build step.

The full design is in
[docs/superpowers/specs/2026-10-12-devbox-phase1-design.md](docs/superpowers/specs/2026-10-12-devbox-phase1-design.md).

## Architecture

| Package | Responsibility |
|---------|----------------|
| `cmd/devbox` | CLI: flags, logging, signals, opening the browser |
| `internal/app` | Wires the store, auth, sync engine, and web server; owns startup and shutdown |
| `internal/github` | Read-only GitHub client: REST and GraphQL, retries, typed errors, rate observation |
| `internal/auth` | Device flow, classic tokens, and the OS keychain |
| `internal/collect` | Turns GitHub data into a normalized record: fingerprints, details, follow-up evidence |
| `internal/classify` | Pure classifier: buckets in precedence order, reasons, actions, ranking |
| `internal/rules` | Per-repository rules (stale days, test-approval markers) and their validation |
| `internal/store` | SQLite: revision-guarded writes, the transition log, caches, settings |
| `internal/syncer` | Per-repository pollers, enrichment dispatch with per-account fairness, polling modes |
| `internal/hub` | In-process publish/subscribe for change and poll events |
| `internal/web` | HTTP UI: templates, htmx fragments, server-sent events, security headers |
| `internal/metrics`, `internal/logging`, `internal/version` | Prometheus metrics, `slog` setup, build version |

A poll fetches cheap fingerprints; only changed pull requests are enriched,
classified, and committed under a revision guard. Every commit publishes a hub
event, which the web server forwards over server-sent events so open pages
refresh only what changed.

## Testing

- **Race tests** run everywhere: `make test`.
- **Coverage floor**: `make cover` fails below the value in `.coverage-floor`.
  The floor only ratchets up; raise it when coverage rises.
- **Golden fixtures**: `make fixtures REPO=owner/name PR=123` records a read-only
  collect fixture with `gh` for regression tests.
- **Live check** (optional, reads real GitHub data):
  `DEVBOX_LIVE_TEST=1 GITHUB_TOKEN=$(gh auth token) go test ./internal/collect -run TestLiveCollect -v`
- Web tests run the real handler against a temporary database and a fake
  GitHub server; prefer them over testing templates in isolation.

Write the failing test first, and table-driven tests where there is more than one
case.

## Working on the UI

The UI follows the thingz.io app style shared with DevRadar: light by default,
dark with the OS, tokens defined at the top of `internal/web/static/style.css`.

- Templates live in `internal/web/templates`; `partials.html` holds the shared
  pieces (status, rows, the queue body).
- The Content Security Policy is `default-src 'self'`: no inline scripts, inline
  styles, or `style=""` attributes. Put behavior in `static/app.js` and styling in
  `static/style.css`.
- htmx is vendored at a pinned version; `TestStaticAssetsMetricsHealth` checks
  its checksum, so update both together.
- Live updates: the queue re-fetches its fragment on a `change` event, and the
  header re-fetches `/fragments/status` on a `poll` event.
- Check both light and dark themes and a narrow window before sending a change.

## How to contribute

### Reporting bugs

Open an [issue](https://github.com/thingzio/devbox/issues/new) with the smallest
reproduction you can manage: a failing test is ideal, a command with its output
(run with `--debug`) is good. Never paste tokens.

### Suggesting enhancements

Lead with the problem you are trying to solve rather than the solution you have
in mind; the problem statement is the part most likely to change the design.

### Contributing code

Read the surrounding code first and match it. Consistency with what is already
there beats an individually better pattern applied in one place. Add Go
dependencies at their latest release unless there is a concrete reason not to,
and say why next to the pin when there is.

## Pull request process

1. **Make the gates pass:** `make test lint vuln cover cross`.
2. **Update documentation** if you changed behavior someone depends on: user
   behavior in `README.md`, developer workflow here, and the design spec.
3. **Open the pull request against `main`** with a clear summary, referencing
   related issues (`Fixes #123`).

Review covers correctness, test coverage, and consistency with existing
patterns. Address feedback with new commits; squashing happens at merge.

## Code style

- Wrap errors with context: `fmt.Errorf("context: %w", err)`.
- Log with `log/slog` and structured attributes, never `fmt.Println`.
- Thread `context.Context` through every call that can block.
- Every outbound HTTP call uses a client with a timeout.
- Parameterized SQL only.
- Prefer self-documenting code over comments; comment the why, not the what.

## Commits

Commits on `main` must be signed (`git commit -S`); the branch rules reject
unsigned commits. Messages follow [Conventional
Commits](https://www.conventionalcommits.org): `feat:`, `fix:`, `docs:`,
`refactor:`, `test:`, `chore:`, `ci:`. The subject says what changed; the body
says why.
