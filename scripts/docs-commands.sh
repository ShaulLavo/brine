#!/usr/bin/env bash
# Prints the command reference body (docs/reference/commands.md) from the CLI's own --help.
# Usage: scripts/docs-commands.sh [path-to-brine-binary]
# The page header is hand-written. See docs/reference/commands.md for the regenerate command.
set -euo pipefail
brine=${1:-brine}

emit() {
  local path=$1 help
  # $path holds space-separated command words, so it stays unquoted.
  # shellcheck disable=SC2086
  help=$("$brine" $path --help 2>&1)
  printf '### `brine%s`\n\n' "${path:+ $path}"
  # The three root flags are listed once at the top of the page. Drop them per command,
  # and drop the "Global Flags:" header when no inherited flag remains.
  printf '```text\n%s\n```\n\n' "$(printf '%s\n' "$help" | awk '
    /^Global Flags:/ {held = $0; inherited = 0; next}
    held != "" && /^      --(json|jsonl|no-input) / {next}
    held != "" && /^ +-/ {if (!inherited) print held; inherited = 1; print; next}
    held != "" && /^$/ {held = ""; if (inherited) print ""; next}
    {print}')"
  printf '%s\n' "$help" | awk '/^Available Commands:/{on=1; next} on && /^$/{exit} on && $1!="" {print $1}' | while read -r sub; do
    [[ $sub == help || $sub == completion ]] && continue
    emit "${path:+$path }$sub"
  done
}

printf '\n'
for top in $("$brine" --help | awk '/^Available Commands:/{on=1; next} on && /^$/{exit} on && $1!="" {print $1}'); do
  [[ $top == help || $top == completion ]] && continue
  emit "$top"
done
