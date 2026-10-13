# Releasing

Releases are cut by pushing a signed semver tag. Everything after that is
automated.

This document is for maintainers. Contributors do not need it; see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Cutting a release

```bash
make release-tag VERSION=v0.1.0
```

`hack/release-tag.sh` refuses a tag that is not semver, refuses to tag from a
branch other than `main`, refuses a dirty tree, refuses when `main` differs from
`origin/main`, refuses a version that already exists, and runs `make qualify`
before it tags anything. It then creates a signed, annotated tag and pushes
`main` and the tag with `git push --atomic`, so the remote never carries a tag
that points at a commit nobody can fetch. `SKIP_QUALIFY=1` exists for tagging a
commit already known to be green; it is not for saving time.

## What happens on the tag

`.github/workflows/release.yaml` runs on any tag matching `v*`:

1. **Gate.** The tag must be semver, annotated, signed, and on `main`.
2. **Verify.** `make qualify` runs on Linux, and the race tests run on macOS.
   A failure aborts the release before anything is built.
3. **Release.** Everything else is
   [`release-go.yaml`](https://github.com/thingzio/actions/blob/main/.github/workflows/release-go.md)
   in `thingzio/actions`, pinned by commit SHA:
   - **Build.** goreleaser builds `devbox` for macOS and Linux (amd64 and arm64,
     as `.tar.gz`) and Windows (amd64, as `.zip`), a `checksums.txt` covering
     every artifact, and an SPDX SBOM per archive. This job holds no signing
     identity.
   - **Sign and attest.** A separate job that runs no code from this repository
     re-downloads every release asset, re-derives its digest, signs
     `checksums.txt` with a keyless Sigstore signature, and attests build
     provenance. Because the signing identity is unreachable from goreleaser
     hooks in this repository, the provenance reaches SLSA Build Level 3.
   - **Verify and publish.** The new signature is verified against the shared
     workflow's identity, and only then is the draft release published.

The release notes carry the exact `cosign` and `gh attestation verify` commands.
The signer is `thingzio/actions`, not this repository.

**Artifact attestations need a public repository** on the `thingzio` plan, so
the first release must come after the repository is made public.

## The Homebrew tap

The cask is pushed to `thingzio/homebrew-tap` during the build, so
`brew install thingzio/tap/devbox` picks up the new version. It is published
only when the repository has a `HOMEBREW_DEPLOY_KEY` secret, a token with
`contents: write` on the tap; without it goreleaser skips the cask rather than
failing.

The cask is pushed while the release is still a draft, because publication
waits for signature verification. If verification fails, the cask points at a
tag that never publishes; revert that commit in the tap.

## Release candidates

A prerelease tag (`v0.1.0-rc.1`) exercises the whole path without publishing
the cask: goreleaser marks it a prerelease and skips the tap. Cut one before
the first release and before any release that changes the pipeline. Put the
final tag on a later commit than its candidate.

## Versioning

[Semantic versioning](https://semver.org). Until 1.0, minor versions may carry
breaking changes; patch versions do not. Database migrations are forward-only
and apply on start, so upgrading keeps your accounts, repositories, and history.

## If a release goes wrong

**The signing job failed.** Re-run only the failed jobs, not the whole
workflow. Nothing is public while this is true: the release stays a draft
until its signature verifies.

**The tag exists but the workflow failed.** Fix the problem on `main` and cut
a new patch version. Do not delete and re-push a tag someone may have fetched;
version numbers are free. If the run never created the release, deleting the
draft and the tag is fine.

**A released version has a serious defect.** Cut a new patch release. There is
no yank; only the latest release is supported (see [SECURITY.md](SECURITY.md)).

## Release checklist

Nothing here is enforced by tooling, which is why it is written down:

- [ ] CI is green on the commit being tagged
- [ ] Anything user-visible is reflected in `README.md`
- [ ] `THIRD_PARTY_NOTICES.md` is current (`make notices`; CI checks it)
