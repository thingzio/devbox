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
