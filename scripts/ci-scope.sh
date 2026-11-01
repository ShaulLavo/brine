#!/usr/bin/env bash
set -euo pipefail

if (( $# != 2 )); then
  echo 'Usage: scripts/ci-scope.sh BASE HEAD' >&2
  exit 2
fi
base=$(git rev-parse --verify --end-of-options "$1^{commit}")
head=$(git rev-parse --verify --end-of-options "$2^{commit}")
paths=$(mktemp "${TMPDIR:-/tmp}/brine-ci-scope-XXXXXX")
trap 'rm -f "$paths"' EXIT
# Renames must inspect both the removed path and its new name.
git diff --no-renames --name-only -z "$base" "$head" -- > "$paths"

scope=full
while IFS= read -r -d '' path; do
  case "/${path,,}/" in
    */testdata/*|*/fixtures/*|*/fixture/*) printf 'full\n'; exit ;;
  esac
  case "$path" in
    README.md|CONTRIBUTING.md|SECURITY.md|AGENTS.md|LICENSE|docs/*.md|.internal/*.md) ;;
    docs/assets/hero.webp|docs/assets/architecture.dark.svg|docs/assets/architecture.light.svg|docs/assets/architecture.excalidraw) ;;
    *) printf 'full\n'; exit ;;
  esac
  scope=docs
done < "$paths"
printf '%s\n' "$scope"
