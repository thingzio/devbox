# devbox — Phase 1 Design: Maintainer Queue Core

- **Status:** Draft for review
- **Date:** 2026-10-12
- **Module:** `github.com/thingzio/devbox`
- **Scope:** Phase 1 of 4 (see [Roadmap](#roadmap-out-of-scope))

## 1. Summary

`devbox` is a local, single-user Go application that tells a GitHub repository
maintainer what is waiting on them. It runs as one binary (`devbox serve`),
stores state in SQLite, and serves a server-rendered web UI on loopback.

GitHub notifications are event-centric: each comment, push, or review is an
item to triage. `devbox` is state-centric: it derives, per open pull request,
which action (if any) the maintainer owes, and a row disappears when the action
is done rather than when it is clicked. The classification rules start from the
`maintainer-sweep` skill (`~/.agents/skills/maintainer-sweep/scripts/classify.py`
and `collect.py`); the server adds continuous freshness, cross-repo and
cross-account aggregation, cached evidence, and a bucket-transition history that
later phases build on.

### Goals

1. Show, for each monitored repository, the open PRs grouped by the action the
   maintainer owes, with reason, action, and age — at most 3 minutes behind
   GitHub.
2. Show one ranked queue across all monitored repositories and accounts.
3. Support multiple GitHub accounts; each monitored repository belongs to one.
4. Spend GitHub rate budget proportional to change, not to repository size.
5. Record every bucket change so later phases can alert on transitions and
   compute response-time metrics.

### Non-goals (Phase 1)

- Any write to GitHub (comments, approvals, reruns, merges, marking
  notifications read). The GitHub client refuses mutations.
- Issues, mentions, security alerts (Phase 4).
- Snooze, alerts, digest, notification auto-clear (Phase 2).
- Interdiff view, co-maintainer awareness, flake memory (Phase 3).
- Multi-user, remote access, hosting, webhooks.
- Behavioral parity with the Python `maintainer-sweep` skill. The two stay
  separate; convergence is revisited after Phase 1 is in daily use.
- Tracing. Structured logging and metrics only.

## 2. Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | Local, single user, loopback only | No hosting or multi-tenant auth surface; matches how the sweep is used |
| D2 | OAuth device flow, classic PAT fallback | In-app login; PAT covers orgs that restrict OAuth apps |
| D3 | OAuth App (not GitHub App), registered under `thingzio` | Notifications API rejects GitHub App user tokens (needed in Phase 2) |
| D4 | Each repo belongs to exactly one account; that login is the viewer | Simple; same repo can be added twice under different accounts |
| D5 | Fingerprint poll every 2m (configurable, min 60s); freshness target ≤ 3m | Cheap change detection, independent of watch settings and notifications |
| D6 | Go classifier separate from the Python skill | Avoid parity burden now; revisit after real use |
| D7 | Server-rendered `html/template` + htmx + SSE, embedded assets | No JS toolchain; one-language testing |
| D8 | `modernc.org/sqlite`; `CGO_ENABLED=0` everywhere | Pure Go; clean cross-platform builds |
| D9 | Store normalized evidence (`Record`) per PR; classification is pure | Free reclassification; explainable rows |
| D10 | Append-only `transitions` table | Phase 2 alerts and Phase 4 metrics without event sourcing |
| D11 | No tracing; `--debug` enables maximal structured logging | Single local process; logs are the diagnostic tool |
| D12 | Coverage floor ratchet; lint and `govulncheck` are hard gates | Measured floor at end of Phase 1, only raised afterwards |

## 3. Architecture

### 3.1 Packages

```
cmd/devbox            CLI entrypoint: serve, version
internal/
  auth      device flow, classic-PAT validation, TokenSource, Secrets (keychain) interface
  github    HTTP client: GraphQL + REST, bounded retries, rate-limit accounting, read-only guard
  collect   GitHub reads -> Record (normalized evidence); cache-aware
  classify  pure: (Record, Rules, viewer, now) -> Result
  sync      per-repo poller, enrichment worker pool, reclassify ticker
  store     SQLite (WAL), embedded migrations, single writer connection
  hub       in-process pub/sub feeding SSE
  web       handlers, embedded templates and static assets, SSE endpoint, security middleware
```

Dependency direction: `web -> store, hub, auth`; `sync -> github, collect,
classify, store, hub`; `collect -> github, store (caches)`; `classify` depends on
nothing but the standard library. `github` knows nothing about `devbox` domain
types beyond request and response shapes.

### 3.2 Data flow

```
every poll interval, per enabled repo:
  poller ──GraphQL Fingerprints──▶ GitHub
    │ compare with stored prs.fingerprint
    ├─ changed or new PR numbers ──▶ enrichment pool (bounded per account)
    │                                  collect.Enrich ──GraphQL PullRequestDetail + REST evidence──▶ GitHub
    │                                  └─▶ Record
    ├─ PRs missing from the open set ──▶ mark closed
    └─ PRs not enriched in the last 30m ──▶ enrichment pool (safety reconcile, staggered)

  Record ──▶ classify.Classify ──▶ Result
  store: one transaction — upsert prs.record + results; if bucket changed, append transitions row
  hub.Publish(repo, number) ──▶ SSE ──▶ htmx swaps the row

every 5m: reclassify all stored open Records (no GitHub reads) — time-dependent rules
```

### 3.3 Concurrency

- One poller goroutine per enabled repository; repos are started and stopped
  when added, enabled, disabled, or removed.
- One enrichment semaphore per account (default 4 in flight).
- Store uses one write connection (`SetMaxOpenConns(1)` on the writer `*sql.DB`)
  and a separate read pool; WAL mode; `busy_timeout` set.
- All goroutines take a root `context.Context` cancelled on SIGINT/SIGTERM.
  Shutdown order: stop accepting HTTP (10s graceful), cancel pollers, wait for
  in-flight enrichment, close hub, close DB.

## 4. GitHub Access

### 4.1 Client

- Base URLs configurable (hidden flags `--github-api-url`, `--github-url`) for
  tests; defaults `https://api.github.com` and `https://github.com`.
- Per-request timeout 30s. Up to 3 attempts for transient failures: HTTP 408,
  429, 500, 502, 503, 504, and network errors (timeout, reset, EOF, TLS
  handshake, refused). Backoff `min(2^attempt, 5)s`; `Retry-After` honored when
  present. Sleep is injected for tests.
- GraphQL: only operations starting with `query` are sent; any `mutation` or
  `subscription` is rejected before the network. A response with a non-empty
  `errors` array is a failure (partial data is unusable). Every query selects
  `rateLimit { limit cost remaining resetAt }`.
- REST: only `GET`. Rate limit read from `X-RateLimit-Limit`,
  `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `X-RateLimit-Resource`.
- Auth errors are typed:
  - `401` → `ErrUnauthorized`.
  - `403` with `X-GitHub-SSO: required; url=...` → `ErrSSORequired{URL}`.
  - `403` whose message states the organization enabled OAuth App access
    restrictions → `ErrOAuthRestricted{Org}`.
  - Secondary rate limit (`403`/`429` with `Retry-After`) → retried as above.
- Diff reads use `Accept: application/vnd.github.diff` and return raw bytes.

### 4.2 Queries

**Fingerprints** (one per repo per poll; 100 PRs per page):

```graphql
query Fingerprints($owner:String!, $name:String!, $endCursor:String) {
  rateLimit { limit cost remaining resetAt }
  repository(owner:$owner, name:$name) {
    pullRequests(states:OPEN, first:100, after:$endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number updatedAt headRefOid mergeStateStatus reviewDecision isDraft
        commits(last:1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}
```

Fingerprint = SHA-256 over `updatedAt|headRefOid|mergeStateStatus|reviewDecision|isDraft|rollupState`.
`baseRefOid` is deliberately excluded: base movement does not change the
three-dot effective diff, and when it changes mergeability `mergeStateStatus`
changes. `mergeStateStatus` of `UNKNOWN` needs no sleep-and-retry: GitHub
computes it lazily, the fingerprint changes on a later poll, and the PR is
re-enriched then. A repeated pagination cursor is an error.

**PullRequestDetail** (one per enriched PR): the fields of the sweep's
`PRS_QUERY` node for a single `pullRequest(number:)`, plus `authorAssociation`,
`additions`, `deletions`, `milestone { title dueOn }`, and `updatedAt` on issue
comments. Concretely: number, title, url, isDraft, isCrossRepository,
createdAt, updatedAt, author (login, `__typename`), baseRefName, baseRefOid,
headRefOid, mergeStateStatus, reviewDecision, autoMergeRequest.enabledAt,
reviewRequests (first 50, user logins), viewer's reviews (last 50: state,
submittedAt, commit oid), latestOpinionatedReviews (first 50: state,
submittedAt, author), reviewThreads (first 100: isResolved, first comment author
and url, last comment author and createdAt, totalCount), comments (last 100:
author, createdAt, updatedAt, databaseId), commits (first 100: oid, signature
state, totalCount), recent commits (last 20: oid, committedDate, author user
login).

**Checks** (when needed, paginated): the sweep's `CHECKS_QUERY` — status check
rollup contexts on the head commit with `isRequired(pullRequestNumber:)`, name,
state or conclusion, and workflow run database id.

**REST evidence** (when needed; see 4.3):

| Read | Endpoint |
|------|----------|
| Awaiting workflow runs | `repos/{r}/actions/runs?head_sha={head}&status=action_required&per_page=1` |
| Marker check runs | `repos/{r}/commits/{head}/check-runs?check_name={name}&per_page=100` |
| Changed files | `repos/{r}/compare/{base}...{head}` (≥ 300 files → unknown) |
| Effective diff | `repos/{r}/compare/{base}...{commit}` with diff media type |
| Comment body | `repos/{r}/issues/comments/{id}` |
| Signature policy (rulesets) | `repos/{r}/rules/branches/{branch}?per_page=100` (paginated) |
| Signature policy (classic) | `repos/{r}/branches/{branch}/protection/required_signatures` |

### 4.3 Enrichment (`collect`)

Input: account client, repo, viewer login, repo rules. Output: `Record`. All
follow-up reads are pinned to the `headRefOid` and `baseRefOid` returned by
`PullRequestDetail`, so a push mid-enrichment yields a stale but consistent
record; the next poll corrects it.

Normalization (from the detail node):

- `author` = login or `ghost`; `is_bot` if `__typename == "Bot"` or login ends in `[bot]`.
- `author_updated_at` = max of: author's issue comment times; for each review
  thread whose last comment is by the author, that comment's time; commit dates
  of the last 20 commits whose author user is the PR author or unlinked.
- `my_threads_waiting` = unresolved threads whose first comment is by the viewer
  and last comment is by the author (url of first comment, time of last).
- `unverified_commits` = commits whose signature state is not `VALID`
  (missing signature = `unsigned`); `signatures_complete` = commits totalCount ≤ fetched.
- `approved_at` = latest `APPROVED` among opinionated reviews.
- `others_decisive` = opinionated `APPROVED`/`CHANGES_REQUESTED` reviews not by the viewer.
- `my_reviews` = viewer reviews excluding `PENDING` and those without `submittedAt`.
- More than 100 review threads → record error "more than 100 review threads; cannot evaluate replies".

Follow-up reads are skipped for drafts and the viewer's own PRs:

1. Fork and not bot: awaiting workflow runs count. If rules define
   `marker_checks`: `marker_present` = any run of any marker check whose
   conclusion is not `skipped`. If rules define `ci_paths` and (awaiting runs or
   marker absent): `ci_paths_touched` from changed files (including
   `previous_filename`), unknown when unreadable or ≥ 300 files.
2. `my_last_comment_at`: walk the viewer's issue comments newest-first, stopping
   at the viewer's latest review time; the first whose body does not start with
   `/` (after left-trim) wins; otherwise the latest review time. Body
   classification cached by `(repo, comment_id, updated_at)`.
3. Review baseline = viewer's latest decisive review (`APPROVED`,
   `CHANGES_REQUESTED`, `DISMISSED`) unless it is `DISMISSED`, else the viewer's
   latest review. If its commit ≠ head: `diff_changed` = effective diff at
   reviewed commit ≠ effective diff at head (both relative to base). Effective
   diff strips `index ` lines unless the diff contains a binary section. Cached
   by `(repo, base_oid, reviewed_oid, head_oid)`; only definite answers cached;
   read failure → unknown.
4. If `reviewDecision == APPROVED` or bot, and merge state is `BLOCKED` or
   `UNSTABLE`: required checks via the Checks query.
5. Signature policy per base branch: `required` if a ruleset rule has type
   `required_signatures`, else classic protection `enabled`; `absent` when the
   classic endpoint 404s with "Branch not protected"; `unavailable` on any other
   read failure. Cached per `(repo, base_ref)` for 1 hour.

Any read failure sets `Record.Error`; one PR's failure never aborts the repo.

`Record` carries a `Version` integer. A stored record with an older version is
treated as having a stale fingerprint and re-enriched on the next poll.

## 5. Classification (`classify`)

Pure function. Inputs: `Record`, `Rules` (`test_approval`, `stale_days`),
viewer login, `now`. Output: `Result{Bucket, Reason, Action, WaitingSince,
Also []string, Notes []string}`. Rules start from `classify.py`; reason and
action strings below are the initial wording.

### 5.1 Buckets

Action buckets in precedence order (first match is primary; later matches go
to `Also` by title):

| id | Title |
|----|-------|
| `waiting_test_approval` | Waiting test approval |
| `waiting_on_me` | Waiting on me |
| `needs_rereview` | Needs re-review |
| `approved_blocked` | Approved, blocked |
| `needs_initial_review` | Needs initial review |
| `stale_changes_requested` | Stale changes requested |
| `bot` | Bot PRs |

Non-action buckets: `ready_to_merge`, `waiting_on_author`, `own`, `draft`,
`unclassified`, and terminal `closed`. Every open PR has exactly one bucket.

### 5.2 Order of evaluation

1. `Record.Error` → `unclassified` (reason = error).
2. Draft → `draft`.
3. Author is viewer → `own`.
4. Compute merge blockers (5.4).
5. Bot → `bot` (5.5).
6. Evaluate candidates in precedence order; first match is primary.
7. No match: `reviewDecision == APPROVED` and no blockers → `ready_to_merge`;
   otherwise `waiting_on_author`.

Non-action bucket results:

| Bucket | Reason | Action | WaitingSince |
|--------|--------|--------|--------------|
| `unclassified` | the record error | "check the PR on GitHub" | none |
| `draft` | "draft" | "" | `createdAt` |
| `own` | "your pull request" | "" | `createdAt` |
| `ready_to_merge` | "approved; ready to merge" | "merge" | `approved_at`, else `updatedAt` |
| `waiting_on_author` | "waiting on author" | "" | `my_last_comment_at`, else `updatedAt` |

Helpers:
- `author_moved` = `my_last_comment_at` is nil, or `author_updated_at` is set and later.
- `reviewable` = merge state ≠ `DIRTY` and `author_moved`.

### 5.3 Candidate rules

- **waiting_test_approval** (never when `DIRTY`):
  - awaiting runs > 0 → reason "N workflow run(s) awaiting approval"; action
    "approve and run workflows after reading the diff"; since head commit time.
  - else fork, `marker_checks` configured, marker absent → reason
    "fork PR; no `A` or `B` check on <sha7>"; command = `command`, or
    `ci_paths_command` when `ci_paths` configured and `ci_paths_touched` is not
    false (reason gains "; changes CI files" or "; file list unreadable");
    action "comment `<command>` after reading the diff"; since head commit time.
- **waiting_on_me** (reviewable): review requested from viewer, or
  `my_threads_waiting` non-empty. Reason parts "your review is requested",
  "author replied in your threads (N)". Action "review" if requested else
  "reply in the linked threads". Since latest waiting-thread time, else
  `updatedAt`. Notes include thread URLs.
- **needs_rereview** (reviewable): baseline exists, baseline commit ≠ head, and
  `diff_changed` is not false. Reason "diff changed since your review" or
  "could not confirm the diff is unchanged since your review", suffixed
  "(<state> at <sha7>)". Action "re-review". Since head commit time.
- **approved_blocked**: `reviewDecision == APPROVED` and blockers non-empty.
  Reason "approved; " + causes joined "; ". Action = distinct actions joined.
  Since `approved_at`, else `updatedAt`.
- **needs_initial_review** (reviewable): viewer has no reviews. Reason "not
  reviewed by you"; action "review"; since `createdAt`.
- **stale_changes_requested**: latest decisive review by viewer is
  `CHANGES_REQUESTED` and not `author_moved`. Handed = earliest viewer review
  submitted after `author_updated_at` (all reviews if never), default
  `my_last_comment_at`. If `now - handed ≥ stale_days` → reason "changes
  requested; no author activity in N days since your feedback"; action "nudge
  the author, or close"; since handed.

### 5.4 Merge blockers

By merge state:
- `DIRTY` → ("conflicts with base", "ask the author to rebase")
- `BEHIND` → ("behind base", "run serial-auto-merge" if auto-merge enabled else "update the branch")
- `UNKNOWN` → ("mergeability unknown", "check the merge box on GitHub")
- `BLOCKED` → per required check not in {SUCCESS, NEUTRAL, SKIPPED}:
  ("required check `name` is <state>", "`gh run rerun <id> --failed` after a
  flake check" when a run id exists and state ∈ {FAILURE, TIMED_OUT, CANCELLED,
  STARTUP_FAILURE}, else "wait for the check, or investigate why it has not passed")
- other states outside {CLEAN, HAS_HOOKS, UNSTABLE} → ("merge state <s>", "check the merge box on GitHub")

Then signatures (policy `absent` → none):
- signatures incomplete → ("signature state unknown", "check commit signatures on GitHub")
- unverified and policy `required` → ("unverified commits: <sha7> (<reason>), ...", "ask the author to sign the listed commits")
- unverified and policy `unavailable` → ("N unverified commits; signature policy unreadable: ...", "confirm the signature policy, then ask the author to sign if required")

`BLOCKED` with no identified cause → ("blocked by branch policy; cause not
identified (N unresolved threads)", "check the merge box on GitHub").

### 5.5 Bot PRs

- Approved, no blockers → reason "approved; ready to merge", action "merge".
- Approved, blockers → summarized like `approved_blocked`.
- Otherwise reason = lower-cased review decision or "no review decision";
  action "review in batch". Since `createdAt` in all cases.

### 5.6 Notes

Appended to rule-specific notes: "approved by @x" / "changes requested by @x"
for others' decisive reviews; "branch updated, diff unchanged" when baseline
commit ≠ head and `diff_changed` is false; "non-required checks failing" when
merge state is `UNSTABLE`.

### 5.7 Handoff lines

Per-repo view only: `needs_rereview`, `needs_initial_review`, `bot` show a copyable
`/reviewing-pull-requests <url> <url> ...`; `waiting_test_approval` shows "post
each command only after reading that PR's diff".

### 5.8 Ranking

Unified queue: bucket precedence, then within a bucket:
1. Milestone with the soonest `dueOn` first (no milestone last).
2. First-time contributors (`authorAssociation` ∈ {FIRST_TIME_CONTRIBUTOR, FIRST_TIMER}) first.
3. Oldest `WaitingSince` first.
4. Lowest PR number.

The sort keys are listed in the UI. Size badge from `additions + deletions`:
S < 50, M < 250, L < 1000, XL ≥ 1000.

## 6. Data Model

SQLite at `${XDG_DATA_HOME:-~/.local/share}/devbox/devbox.db` (override
`--db`). WAL, foreign keys on. Times are INTEGER Unix milliseconds UTC.
Migrations are numbered SQL files embedded in the binary, applied in one
transaction at startup, forward-only, tracked in `schema_migrations`.

```sql
accounts (
  id INTEGER PRIMARY KEY,
  login TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('oauth','pat')),
  scopes TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('ok','needs_auth')),
  status_detail TEXT,
  created_at INTEGER NOT NULL,
  verified_at INTEGER
)

repos (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  rules TEXT NOT NULL DEFAULT '{}',          -- JSON, sweep overlay shape
  added_at INTEGER NOT NULL,
  UNIQUE (account_id, owner, name)
)

repo_sync (
  repo_id INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  last_poll_at INTEGER,
  last_success_at INTEGER,
  last_error TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0
)

prs (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  url TEXT NOT NULL,
  author TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('open','closed')),
  fingerprint TEXT,
  record TEXT,                                -- JSON, versioned
  record_at INTEGER,
  error TEXT,
  first_seen_at INTEGER NOT NULL,
  closed_at INTEGER,
  PRIMARY KEY (repo_id, number)
)

results (
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  bucket TEXT NOT NULL,
  reason TEXT NOT NULL,
  action TEXT NOT NULL,
  waiting_since INTEGER,
  also TEXT NOT NULL DEFAULT '[]',            -- JSON array
  notes TEXT NOT NULL DEFAULT '[]',           -- JSON array
  classified_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, number),
  FOREIGN KEY (repo_id, number) REFERENCES prs(repo_id, number) ON DELETE CASCADE
)

transitions (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  at INTEGER NOT NULL,
  from_bucket TEXT,                           -- NULL on first classification
  to_bucket TEXT NOT NULL,
  reason TEXT NOT NULL,
  FOREIGN KEY (repo_id, number) REFERENCES prs(repo_id, number) ON DELETE CASCADE
)
CREATE INDEX transitions_pr ON transitions (repo_id, number, at);
CREATE INDEX transitions_at ON transitions (at);

cache_diff (
  repo_id INTEGER NOT NULL, base_oid TEXT NOT NULL, reviewed_oid TEXT NOT NULL, head_oid TEXT NOT NULL,
  changed INTEGER NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, base_oid, reviewed_oid, head_oid)
)

cache_comment (
  repo_id INTEGER NOT NULL, comment_id INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  is_command INTEGER NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, comment_id, updated_at)
)

cache_policy (
  repo_id INTEGER NOT NULL, base_ref TEXT NOT NULL,
  policy TEXT NOT NULL CHECK (policy IN ('required','absent','unavailable')),
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, base_ref)
)
```

Rules:
- A `transitions` row is written only when the bucket differs from the stored
  result (or on first classification). Reason and notes changes alone do not
  create a transition.
- Closing a PR writes a transition to `closed` and deletes its `results` row.
- Retention: `prs` and `transitions` kept indefinitely. At startup, delete
  `cache_diff` and `cache_comment` rows older than 30 days.
- Repo `rules` JSON is validated on write: allowed keys are `test_approval`
  (`marker_checks` []string, `command` string, `ci_paths` []string,
  `ci_paths_command` string) and `stale_days` (int ≥ 1, default 7). Unknown
  keys are an error. `ci_paths` requires `ci_paths_command`; `marker_checks`
  requires `command`.

## 7. Sync Engine (`sync`)

Per repo, each tick (interval from `--poll-interval`, default 2m, minimum 60s,
with ±10% jitter):

1. Skip if the account is `needs_auth` or paused for rate limit.
2. Run Fingerprints (all pages). On failure: record `last_error`, increment
   `consecutive_failures`, next attempt after `min(interval × 2^failures, 15m)`.
3. For each open PR: enqueue enrichment if new, fingerprint changed, record
   version stale, or `record_at` older than 30 minutes (safety reconcile; at
   most 10% of the repo's open PRs per tick, minimum 1, oldest first).
4. PRs in the store as `open` but absent from the result → mark `closed`.
5. Each enrichment result is classified and stored in one transaction, then
   published to the hub.
6. On success: `last_success_at = now`, `consecutive_failures = 0`.

Reclassify ticker (every 5m): load all open records, classify with the current
`now`, store changes, publish changed rows. No GitHub reads.

Rate limiting per account: track the latest `remaining`/`reset` for GraphQL and
REST (core). When either drops below 10% of its limit, pause that account's
pollers and enrichment until reset; record it for the UI.

Auth failures: `ErrUnauthorized` sets the account to `needs_auth` and pauses
its repos until re-authentication. `ErrSSORequired` and `ErrOAuthRestricted`
are stored as `repo_sync.last_error` with their actionable detail; the account
stays `ok` (other orgs may work).

## 8. Authentication (`auth`)

- `TokenSource` interface: `Token(ctx, login) (string, error)`.
- `Secrets` interface over the OS keychain (`zalando/go-keyring`; no CGO):
  service `devbox`, user `account:<login>`. In-memory fake for tests. If the
  keychain is unavailable, adding an account fails with a clear error; there is
  no plaintext fallback.
- OAuth client ID: build-time default via
  `-ldflags "-X github.com/thingzio/devbox/internal/auth.DefaultClientID=..."`,
  overridden by `DEVBOX_GITHUB_CLIENT_ID`. The OAuth App is registered under
  `thingzio` with device flow enabled. Requested scopes: `repo read:org`.

**Device flow:**
1. `POST {github}/login/device/code` with `client_id`, `scope`.
2. UI shows `user_code` and a link to `verification_uri`; `device_code` is
   held server-side only and never logged.
3. Server polls `POST {github}/login/oauth/access_token` every `interval`
   seconds: `authorization_pending` → continue; `slow_down` → interval += 5s;
   `expired_token` → fail "code expired, start again"; `access_denied` → fail
   "authorization denied"; success → token.
4. `GET {api}/user` → login and `X-OAuth-Scopes`; store token in keychain and
   upsert the `accounts` row (`kind = oauth`, `status = ok`).

Flows are held in memory keyed by a random flow id returned to the UI, and are
discarded on success, failure, or device-code expiry. At most one flow runs at a
time; starting a new one cancels the previous.

**Classic PAT fallback:** pasted token must not start with `github_pat_`
(rejected: fine-grained tokens cannot read notifications). Validated with
`GET /user`; `X-OAuth-Scopes` must include `repo`. Stored as `kind = pat`.

**Removal:** delete keychain entry, `accounts` row (cascades to repos and their
data). The UI links to the GitHub settings page for token revocation.

## 9. Web UI (`web`)

Server-rendered `html/template`; htmx and CSS vendored and embedded via
`embed.FS`. SSE at `/events` pushes `{repo, number}` change events; the queue
container re-fetches its own fragment on events (debounced 1s), so PRs that
change bucket move correctly. Unified and per-repo views use the ranking in 5.8.

| Route | Content |
|-------|---------|
| `GET /` | Unified queue: action buckets in precedence order with counts; rows: repo · `#n` title · `@author` · age · reason → action; account badge; `also`/notes line; badges: first-time contributor, size, milestone. Then ready-to-merge links; waiting-on-author / own / draft counts; unclassified with errors. Filters: repo, account, bucket (query params). Sort keys displayed. |
| `GET /r/{owner}/{name}` | Single-repo sweep layout: totals line, buckets, handoff lines with copy button, ready to merge, counts, unclassified. |
| `GET /r/{owner}/{name}/{n}` | Explain view: result, evidence summary from the record, transition history. |
| `GET /settings` | Accounts (status, kind, scopes, rate remaining, pause state), repos (add, enable/disable, remove, rules editor with validation errors), import from `~/.config/maintainer-sweep/repos/**.json` (one-time, per file, choose account), effective poll interval (read-only). |
| `POST /accounts/device`, `GET /accounts/device/{flow}` | Start and poll device flow. |
| `POST /accounts/pat`, `POST /accounts/{login}/delete` | PAT add, account removal. |
| `POST /repos`, `POST /repos/{id}/(enable\|disable\|delete\|rules)` | Repo management. |
| `GET /events` | SSE stream. |
| `GET /metrics` | Prometheus exposition. |
| `GET /healthz` | Liveness. |

A header bar shows per-repo staleness when `now - last_success_at > 3m`, account
`needs_auth` banners with a re-authenticate action, and rate-limit pauses with
resume time.

Ages render as whole days since `WaitingSince`, `today` under one day.

### 9.1 Local server security

- Default `--addr 127.0.0.1:8484`. A non-loopback address requires
  `--allow-remote` and logs a warning.
- Host header must be `localhost`, `127.0.0.1`, or `[::1]` with the configured
  port (DNS rebinding).
- State-changing requests are `POST` only and require `Origin` matching the
  server origin or `Sec-Fetch-Site: same-origin`.
- Headers: `Content-Security-Policy: default-src 'self'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`.
- All PR-sourced text (titles, authors, notes, errors) is untrusted data,
  rendered only through `html/template` escaping. Links rendered only when the
  URL is `https://github.com/...`.

## 10. Logging and Metrics

- `log/slog` to stderr. `--log-format auto|text|json` (auto: text on a TTY,
  JSON otherwise). Default level `info`; `--debug` sets `debug`.
- `info`: startup config summary, migrations applied, account and repo
  changes, bucket transitions, poll failures, rate-limit pauses, shutdown.
- `debug` adds: every GitHub request (method, REST path or GraphQL operation
  name, status, duration, attempt, rate remaining, `X-GitHub-Request-Id`),
  poll cycle summary (pages, PRs, changed, enqueued, closed, duration),
  fingerprint diffs per PR, cache hits and misses, enrichment steps,
  classification inputs summary and result, SSE subscriber changes, HTTP
  requests (method, path, status, duration).
- Every log line in a poll cycle carries `repo` and `poll_id`; every
  enrichment line carries `repo`, `pr`, and `head`.
- Never logged: tokens, `Authorization` headers, `device_code`, PR or comment
  bodies, diff contents.
- Metrics (`prometheus/client_golang`): `devbox_poll_duration_seconds{repo}`,
  `devbox_poll_failures_total{repo}`, `devbox_github_requests_total{kind,status}`,
  `devbox_github_rate_remaining{account,resource}`,
  `devbox_enrich_queue_depth{account}`, `devbox_prs{repo,bucket}`,
  `devbox_transitions_total{to_bucket}`.

## 11. CLI

```
devbox serve [--addr 127.0.0.1:8484] [--db PATH] [--poll-interval 2m]
             [--debug] [--log-format auto|text|json] [--allow-remote]
devbox version
```

Hidden test flags: `--github-api-url`, `--github-url`. Environment:
`DEVBOX_GITHUB_CLIENT_ID`. Exit codes: 0 clean shutdown, 1 runtime error, 2
usage error.

## 12. Testing

All tests run with `-race`; none touch the network.

| Package | Approach |
|---------|----------|
| `classify` | Table-driven, pure, injected `now`. Seeded from `test_classify.py` cases: every bucket rule, precedence and `also`, notes, bot results, ready-to-merge, non-action buckets, ranking order, size badges. |
| `collect` | Golden files: recorded GraphQL/REST responses → expected `Record`. Fixtures recorded once from public repositories (e.g. `NVIDIA/aicr`) by `hack/record-fixtures.sh` using `gh api`, then hand-edited for edge cases: forks, unsigned commits, > 100 threads, binary diffs, slash-command comments, unreadable file lists. |
| `github` | `httptest`: retries and backoff (injected sleep), `Retry-After`, rate-limit parsing, GraphQL `errors` → failure, mutation refusal, SSO and OAuth-restriction 403 typing, diff media type. |
| `auth` | Fake device-flow server for pending, slow_down, expired, denied, success; PAT validation (fine-grained rejection, missing scope); in-memory `Secrets`. |
| `store` | Real SQLite in `t.TempDir()`: migrations, transition-on-bucket-change only, close handling, cache semantics, retention purge, rules validation. |
| `sync` | Fake GitHub source + real store + fake clock: only changed PRs enriched (call counts), safety reconcile cap, closed detection, reclassify tick, failure backoff, rate-limit pause, needs_auth pause, clean shutdown. |
| `web` | `httptest`: hostile titles escaped, non-GitHub links not rendered, Host and Origin enforcement, security headers, SSE publish delivery, settings validation errors. |
| e2e | `devbox serve` against a fake GitHub (hidden URL flags): add PAT account → add repo → poll → `/` shows expected rows. |
| live | Opt-in `DEVBOX_LIVE_TEST=1`, read-only against a public repo; never in CI. |

## 13. Tooling and CI

- `.versions.yaml`: single source of truth for Go and `golangci-lint` versions,
  read by the Makefile and CI.
- `Makefile`: `build`, `test`, `lint`, `vuln`, `cover`, `run`, `fixtures`,
  `cross` (all with `CGO_ENABLED=0`).
- Coverage ratchet: `.coverage-floor` holds the total coverage percentage;
  `make cover` fails below it. Set to the measured value at the end of Phase 1;
  only raised afterwards.
- GitHub Actions on push and PR: test (`-race`), `golangci-lint`, `govulncheck`,
  coverage floor, cross-compile matrix (darwin/arm64, darwin/amd64,
  linux/amd64, linux/arm64, windows/amd64) with `CGO_ENABLED=0`. Lint and
  `govulncheck` failures block merge. Actions pinned by commit SHA; workflow
  `permissions: contents: read`.
- Release automation (goreleaser, signing) is deferred past Phase 1.

## 14. Roadmap (out of scope)

- **Phase 2 — Inbox control:** auto-mark GitHub notification threads done when
  the PR leaves all action buckets; conditional snooze (until CI green, author
  push, base update, date); transition-only alerts (desktop/Slack) with quiet
  hours and a daily digest. Builds on `transitions`.
- **Phase 3 — Review depth:** "since you last looked" interdiff robust to
  rebase; co-maintainer awareness (others reviewing; claims via GitHub-native
  self-assign or reviewer request); flaky-check memory from check-outcome
  history.
- **Phase 4 — Breadth:** issues awaiting reply, untriaged issues, mentions,
  Dependabot/code-scanning alerts; guarded write actions (approve workflow
  runs, rerun failed, enable auto-merge, `/ok-to-test`) behind explicit
  confirmation; response-time metrics from `transitions`.

## 15. Resolved Questions

| Question | Resolution |
|----------|-----------|
| Deployment model | Local, single user |
| Token source | Device flow; classic PAT fallback |
| Does NVIDIA restrict OAuth apps? | Not determinable as a member; PAT fallback covers it; public repos readable regardless |
| Multi-account semantics | One account per repo |
| Freshness | ≤ 3m; 2m default poll |
| Relationship to Python skill | Separate; revisit after use |
| Name and location | `devbox`, `github.com/thingzio/devbox` |
| UI approach | Server-rendered single binary |
| CGO | Disallowed; pure-Go SQLite |
| Tracing | Not included; `--debug` logging instead |
| Coverage gate | Ratchet from measured floor |
| OAuth App owner | `thingzio` |
