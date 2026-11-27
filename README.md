# DevBox

Triage your pull request queue at a glance. Rank, explain, and surface the
reviews waiting on you before they go stale.

DevBox is a local, single-user maintainer queue for GitHub. It watches the
repositories you maintain, works out which open pull requests are waiting on
you and why, and serves a live queue in your browser.

## Why not GitHub notifications?

- **Whose turn it is:** pull requests are sorted by state (waiting on me, needs
  re-review, stale), not by "someone commented."
- **Explains itself:** every pull request shows the evidence and history behind
  its bucket.
- **Maintainer-first ranking:** milestones and first-time contributors come
  first, then whoever has waited longest.
- **Surfaces hidden gates:** fork pull requests whose tests need your approval,
  pull requests touching CI files, and bot pull requests in their own bucket.
- **One live, local view:** all your accounts in one queue that updates itself,
  with one-click hand-off commands for your agent.

## Install

DevBox is a single binary for macOS, Linux, and Windows.

```bash
brew install thingzio/tap/devbox
```

Or with Go:

```bash
go install github.com/thingzio/devbox/cmd/devbox@latest
```

Or download the archive for your platform from the
[latest release](https://github.com/thingzio/devbox/releases/latest). Every
release is signed and carries SLSA build provenance and an SBOM; its notes show
how to verify them. To build from source, see [CONTRIBUTING.md](CONTRIBUTING.md).

## Get started

```bash
devbox serve
```

DevBox starts on http://127.0.0.1:8484 and opens it in your default browser.

1. **Add an account** in Settings: sign in with GitHub, or paste a classic
   personal access token with the `repo` and `read:org` scopes.
2. **Add the repositories you maintain**: `owner/name`, any GitHub URL inside
   the repository, or its SSH remote. Not sure which? **Suggest repositories**
   proposes up to five you can push to, ranked by open pull requests that
   request your review and by your reviews in the last 30 days.
3. **Open the Queue.** Tiles count each bucket and filter to it; the sidebar
   lists pull requests ready to merge and every repository you watch. Select a
   pull request's title to see why it is where it is.
4. **Snooze** a pull request you cannot act on yet: for a day, three days, a
   week, or until it changes. It returns early if it moves to another bucket or
   gets new commits.
5. **Use the keyboard**: <kbd>j</kbd> / <kbd>k</kbd> select a pull request,
   <kbd>o</kbd> opens it on GitHub, <kbd>Enter</kbd> explains it, <kbd>s</kbd>
   opens its snooze menu, and <kbd>Esc</kbd> clears the selection.
6. **Get notified** (macOS): turn on desktop notifications in Settings →
   Notifications to hear when a pull request becomes new work for you. They
   are off by default, arrive as one notification per burst, and skip Bot PRs.
   macOS shows them as coming from Script Editor, so allow Script Editor in
   System Settings → Notifications; clicking one opens Script Editor, not the
   pull request.

If you use the maintainer-sweep skill, Settings offers its per-repository rule
files (`~/.config/maintainer-sweep/repos/<owner>/<repo>.json`) for one-click
import.

## Accounts

- **Sign in with GitHub** uses the device flow with the DevBox GitHub App,
  whose permissions are read-only: GitHub itself prevents DevBox from writing.
  Install the app on each organization or account whose repositories you
  watch (an organization owner may need to approve it). Its tokens expire after
  8 hours and DevBox renews them; if renewal fails, sign in again.
- **Classic token**: a classic personal access token with the `repo` and
  `read:org` scopes (`read:org` lets DevBox see review requests sent to your
  teams).
  Classic scopes cannot be read-only, so prefer signing in with GitHub.
  Fine-grained tokens are not supported.
- **Organizations with SAML SSO** also need the token authorized for SSO. When
  a repository shows **Authorize for SSO**, open that link while signed in to
  the organization's identity provider. If an organization will not install
  the GitHub App, add a classic token authorized for its SSO instead.

You can add several accounts, for example personal and work. Each repository is
watched by one of them, and the queue shows them all.

## Options

```
devbox serve [--addr 127.0.0.1:8484] [--db PATH] [--poll-interval 2m|1m..15m|manual]
             [--debug] [--log-format auto|pretty|text|json] [--allow-remote] [--no-browser]
devbox version
```

| Flag | Default | What it does |
|------|---------|--------------|
| `--addr` | `127.0.0.1:8484` | Listen address. A non-loopback address also needs `--allow-remote`. |
| `--db` | `~/.local/share/devbox/devbox.db` | SQLite database (honors `XDG_DATA_HOME`). |
| `--poll-interval` | `2m` | How often repositories are checked: 1m to 15m, or `manual` to check only when you press Refresh. Settings → Polling can override it. |
| `--no-browser` | off | Don't open the queue in the default browser on start; the URL is logged either way. |
| `--debug` | off | Log every GitHub request (never tokens, bodies, or diffs). |
| `--log-format` | `auto` | `pretty` (short lines, colored by level: info green, warnings blue, errors red), `text` (the full `key=value` record with level and time), `json`, or `auto` (pretty on a terminal, JSON otherwise). Set `NO_COLOR` to turn colors off. |
| `--allow-remote` | off | Accept connections from other machines. DevBox prints a URL with a one-time access token at startup; open it once to get a session cookie, which lasts until DevBox restarts. Traffic is plain HTTP, so use TLS (an SSH tunnel or a proxy) on networks you do not trust. |

## Privacy and security

- Everything runs on your machine. DevBox only reads from GitHub; it never
  comments, approves, or merges.
- Tokens live in your OS keychain, never in the database, and are never logged.
- The UI listens on loopback by default and rejects requests from other sites
  and unexpected host names.
- Closed and merged pull requests, with their history, are removed from the
  database 90 days after they close.
- Desktop notifications, when you turn them on, include pull request titles;
  macOS may show them on the lock screen depending on your preview settings.

Prometheus metrics are at `/metrics` and a health check at `/healthz`.

## Contributing

Bug reports, ideas, and pull requests are welcome. See
[CONTRIBUTING.md](CONTRIBUTING.md) for how to build, test, and change DevBox.

## License

[Apache 2.0](LICENSE)
