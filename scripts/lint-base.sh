#!/usr/bin/env bash
set -euo pipefail

# GitHub checks out a synthetic PR merge, whose first parent is current main.
# The event's PR base SHA can lag behind that parent and invent new findings.
case "${1:?event name is required}" in
  pull_request) git rev-parse HEAD^1 ;;
  push) git rev-parse "${2:?previous push SHA is required}^{commit}" ;;
  *) echo 'Expected pull_request or push.' >&2; exit 2 ;;
esac
