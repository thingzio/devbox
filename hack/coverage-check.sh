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
