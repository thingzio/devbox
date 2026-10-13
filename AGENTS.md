# AGENTS.md

Repository guidance for coding agents working on DevBox. Human contributors
should read [CONTRIBUTING.md](CONTRIBUTING.md); the two must agree.

## Local overlay

If `AGENTS.local.md` exists at the repository root, read it too: it is
gitignored, so check the exact path rather than relying on ignore-aware search.
Follow it where it does not conflict with this file. Local-only design notes,
when present, live under `docs/superpowers/` (also gitignored); code is
authoritative where they differ.

## Role

Act as a principal distributed-systems engineer fluent in Go. Optimize for
correctness, resiliency, and operational simplicity. Write production-grade
code, never illustrative pseudo-code.

## Project overview

DevBox is a local, single-user maintainer queue for GitHub: one Go binary
(`devbox serve`) that polls the repositories a maintainer watches, classifies
each open pull request by whose turn it is, and serves a live, server-rendered
UI on loopback.

```
poll fingerprints ─▶ enrich changed PRs ─▶ classify ─▶ commit (revision-guarded) ─▶ hub event ─▶ SSE ─▶ UI
```

- `README.md` is for users; `CONTRIBUTING.md` for developers; `RELEASING.md`
  for the maintainer; `SECURITY.md` states the security claims.
- `.versions.yaml` is the single source of truth for the Go toolchain and every
  pinned tool. `.coverage-floor` is the coverage gate; it only ratchets up.

## Commands

```bash
make build      # bin/devbox (CGO_ENABLED=0)
make test       # race tests (CGO_ENABLED=1)
make lint       # golangci-lint
make qualify    # the full CI gate: lint, actionlint, vuln, cover, cross, notices
make snapshot   # local goreleaser dry run
make notices    # regenerate THIRD_PARTY_NOTICES.md after a dependency change
make leaks      # gitleaks over the full history
go test ./internal/web -run TestName -v    # one test
```

Run `make qualify` at every stopping point and before every commit. Fix every
failure, including ones that look pre-existing.

## Non-negotiable rules

1. **Read before writing.** Never modify code you have not read.
2. **Test first.** Write the failing test, watch it fail, then implement. Never
   skip, disable, or weaken a test to get green.
3. **Use existing patterns.** Learn the surrounding package before inventing.
4. **Three strikes.** After three failed fix attempts, stop and reassess the design.
5. **Never lower `.coverage-floor`.** Raise it when coverage rises.
6. **Latest dependencies.** Add Go modules and tools at their latest release
   unless there is a concrete reason not to; state the reason next to the pin.

## Product boundaries

- **Read-only GitHub.** DevBox never comments, approves, merges, or otherwise
  writes to GitHub. Requests go only to GitHub's API and sign-in endpoints.
- **Secrets stay secret.** Tokens live in the OS keychain (`internal/auth`),
  never in SQLite. Never log tokens, `Authorization` headers, device codes,
  pull request or comment bodies, or diffs, at any level including `--debug`.
- **GitHub data is attacker-controlled.** Titles, names, and notes are authored
  by strangers. Render them only through `html/template`; emit outbound links
  only through `githubURL` or `profileURL`, which allow `https://github.com/`
  alone; redirect only through `localPath`.
- **The local server is a security boundary.** Keep `default-src 'self'` CSP
  (no inline scripts, inline styles, or `style=""` attributes), the loopback
  Host allow-list, and same-origin checks on every state-changing request.
- **Classification is pure.** `internal/classify` does no I/O and never reads
  the clock; the same record, rules, and time always produce the same result.

## Architecture

| Package | Responsibility |
|---------|----------------|
| `cmd/devbox` | Entry point only: flags, logging, signals |
| `internal/app` | Wires everything; owns startup and orderly shutdown |
| `internal/github` | Read-only REST and GraphQL client: bounded retries, typed errors, rate observation |
| `internal/auth` | Device flow, classic tokens, keychain |
| `internal/collect` | GitHub data to a normalized `classify.Record`: fingerprints, details, follow-up evidence |
| `internal/classify` | Buckets in precedence order, reasons, actions, ranking |
| `internal/rules` | Per-repository rules and their collection hash |
| `internal/store` | SQLite (`modernc.org/sqlite`, WAL, one writer): revision guards, transitions log, caches, settings |
| `internal/syncer` | Per-repository pollers, enrichment dispatch with per-account fairness, polling modes |
| `internal/hub` | In-process pub/sub: `PRChanged` and `Polled` events |
| `internal/web` | `html/template` UI, htmx fragments, SSE, security middleware |
| `internal/browser` | Opens the UI in the default browser |
| `internal/metrics`, `internal/logging`, `internal/version` | Prometheus, `slog`, build version |

## Invariants

- Bucket precedence lives in `classify.ActionBuckets()`; the UI, tiles, and
  sorting all derive from it. Do not hardcode bucket order elsewhere.
- Every store write that commits enrichment checks the repository and pull
  request revisions inside the transaction; a stale writer gets `ErrStale` and
  drops its result. Never bypass the guard.
- Transitions are append-only.
- A rules change that alters the collection hash re-collects the repository; a
  `stale_days`-only change reclassifies without GitHub calls.
- Rate limits pause an account rather than fail it; polls back off with a cap.
  Partial failure is the steady state: keep the last good result and say why.
- Every recorded poll publishes `hub.Polled`; every PR change publishes
  `hub.PRChanged`. The UI depends on both for live updates.

## Code rules

- **Errors:** wrap with context, `fmt.Errorf("doing X: %w", err)`; compare
  with `errors.Is`/`errors.As`, never `==`.
- **Context:** thread `context.Context` through every call that can block;
  `context.Background()` only in `main`, tests, and detached work that must
  outlive a request.
- **HTTP clients:** never `http.DefaultClient`; every client has a timeout.
- **Logging:** `log/slog` with structured attributes, never `fmt.Println`.
- **SQL:** parameterized only; schema changes are new forward-only migrations
  under `internal/store/migrations`.
- **Tests:** table-driven where there is more than one case; web tests run the
  real handler against a temporary database and a fake GitHub server.
- **Comments:** prefer self-documenting code; a comment states what the code
  cannot show (a constraint, an absence, the provenance of a value).
- **UI:** styles in `internal/web/static/style.css` using the existing tokens;
  behavior in `static/app.js`. Check light and dark themes.
- **Dependencies:** after adding or upgrading one, run `make notices` and
  commit `THIRD_PARTY_NOTICES.md`.

## Git

- Commit to `main`. Sign every commit with `git commit -S`; the branch rules
  reject unsigned commits. Do not add `Signed-off-by` (`-s`).
- Never add `Co-Authored-By` or any agent attribution to commits or pull
  requests.
- Messages follow Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`,
  `test:`, `chore:`, `ci:`): the subject says what changed, the body says why.
- Never force-push `main`, and never move or delete a published tag.

## Secrets

Never commit a secret, in source, fixtures, or messages, including tokens you
only use locally. Test fixtures use obviously fake values. Never paste a token
into an issue, log, or transcript. If one is committed, revoke it first, then
clean the history.

## Releases

Do not tag, release, push to the Homebrew tap, or change repository settings
without the maintainer's explicit approval. The process is in
[RELEASING.md](RELEASING.md).

## Working style

- Prefer incremental, independently testable slices over big-bang rewrites.
- Decide by testability, then readability, consistency, simplicity, and
  reversibility, in that order.
- Be concise. At the end of a plan, list unresolved questions.
