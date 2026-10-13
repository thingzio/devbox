#!/usr/bin/env bash
# Cuts a release: verifies the tree, runs the full gate, then creates a signed,
# annotated tag and pushes it with main atomically. A tag names bytes other
# people verify against and cannot be taken back, so the checks are enforced.
set -euo pipefail

version="${1:-}"
if [[ ! "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: make release-tag VERSION=vX.Y.Z[-rc.N]" >&2
  exit 2
fi
if [[ "$(git branch --show-current)" != "main" ]]; then
  echo "release-tag: tag from main only" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "release-tag: the working tree has uncommitted or untracked files" >&2
  exit 1
fi
git fetch --quiet origin main --tags
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "release-tag: main differs from origin/main; push or pull first" >&2
  exit 1
fi
if git rev-parse --quiet --verify "refs/tags/${version}" >/dev/null; then
  echo "release-tag: ${version} already exists; version numbers are free, pick the next one" >&2
  exit 1
fi

if [[ "${SKIP_QUALIFY:-}" != "1" ]]; then
  make qualify
fi

git tag -s "${version}" -m "${version}"
git push --atomic origin main "refs/tags/${version}"
echo "pushed ${version}; follow the release at https://github.com/thingzio/devbox/actions/workflows/release.yaml"
