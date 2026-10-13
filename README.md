# devbox

A local, single-user maintainer queue for GitHub. `devbox` polls the repositories
you maintain, works out which open pull requests are waiting on you and why, and
serves a live queue at http://127.0.0.1:8484, opening it in your default browser
on start. State lives in SQLite; tokens live in your OS keychain.

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
devbox serve [--addr 127.0.0.1:8484] [--db PATH] [--poll-interval 2m|1m..15m|manual]
             [--debug] [--log-format auto|text|json] [--allow-remote] [--no-browser]
devbox version
```

- Database: `${XDG_DATA_HOME:-~/.local/share}/devbox/devbox.db`
- maintainer-sweep overlays offered for import: `${XDG_CONFIG_HOME:-~/.config}/maintainer-sweep/repos/<owner>/<repo>.json`
  (or `$MAINTAINER_SWEEP_CONFIG_DIR/repos`)
- `--poll-interval` sets the default check interval (1m–15m, or `manual` to check only when you press
  Refresh). Settings → Polling can override it; the choice is saved in the database.
- `--no-browser` skips opening the queue in the default browser on start (`open`, `xdg-open`, or
  `rundll32`); if the browser can't be opened, the URL is logged instead.
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
