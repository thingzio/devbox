# Security policy

## Reporting a vulnerability

Report suspected vulnerabilities privately through
[GitHub Security Advisories](https://github.com/thingzio/devbox/security/advisories/new).
Please do not open a public issue for a suspected vulnerability.

Include, where you can: the affected version or commit, a description of the
impact, and the smallest reproduction you have. A pull request title, request,
or page that triggers the behavior is more useful than a description of it.
Never include a real token; a revoked one or a placeholder is enough.

You should get an acknowledgement within three business days. We will tell you
whether we consider the report in scope and keep you updated as a fix
progresses. Credit is offered by default in the advisory unless you ask
otherwise.

## Supported versions

Only the latest release receives fixes. Until 1.0, a fix ships as a new patch
or minor version rather than being backported, and a defective release is
superseded, never withdrawn.

## Scope

DevBox is a local, single-user server. It holds GitHub credentials for the
person running it, reads pull request data from GitHub, and renders that data,
which other people author, in a browser UI on the same machine. Its security
claims follow from that:

- tokens never leave the OS keychain except in requests to GitHub;
- nothing GitHub returns can run script in, or act through, the UI;
- other websites and other machines cannot read the UI or drive its actions;
- DevBox never writes to GitHub.

The following are in scope for a report:

- disclosure of a token through logs (including `--debug`), the database,
  error messages, URLs, metrics, or responses;
- script injection, or any link that leaves GitHub or the app, from pull
  request titles, bodies, author names, notes, or any other GitHub-supplied
  value;
- a cross-site request that changes settings, adds or removes accounts or
  repositories, or triggers a refresh;
- DNS rebinding or any other way for a website to read the UI while it is
  bound to loopback;
- a redirect from DevBox to a location outside it;
- with `--allow-remote`, reaching any page other than `/healthz` without the
  access token printed at startup;
- pull request text that runs as AppleScript or as an `osascript` option
  through a desktop notification;
- any write to GitHub, or a request to a host other than GitHub's API and
  sign-in endpoints; and
- resource exhaustion from GitHub data that bounded reads should have limited.

The following are **not** vulnerabilities in DevBox:

- eavesdropping on `--allow-remote` traffic. The UI is plain HTTP, so the
  access token and session cookie travel unencrypted; put it behind TLS (an
  SSH tunnel or a reverse proxy) on any network you do not trust;
- anything that requires an attacker who can already run code as your user or
  read your keychain and home directory;
- the content of pull requests, or the behavior of GitHub, its API, or an
  organization's app installation policy, when DevBox reported it correctly; and
- what an agent does with a hand-off command you copy into it.

## Security posture

- GitHub access is read-only. Sign-in uses a GitHub App whose permissions are
  all read-only, so GitHub enforces it; its user tokens expire after 8 hours
  and are renewed with a single-use refresh token kept in the keychain. Classic
  tokens are validated for the `repo` and `read:org` scopes; `repo` cannot be narrowed to
  reads, so for them read-only is DevBox's own guarantee.
- Tokens live in the OS keychain, never in the SQLite database, and are never
  logged, including authorization headers, device codes, pull request and
  comment bodies, and diffs.
- The server binds to `127.0.0.1` by default, allows only loopback host names,
  and refuses state-changing requests that are not same-origin.
- Pages send `Content-Security-Policy: default-src 'self'`,
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
  `Referrer-Policy: no-referrer`. Templates escape all GitHub data, and only
  `https://github.com/` URLs are rendered as outbound links.
- Release binaries carry a keyless Sigstore signature over their checksums,
  SLSA Build Level 3 provenance, and an SBOM; see [RELEASING.md](RELEASING.md).
