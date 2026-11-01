#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/brine-docs-commands-XXXXXX")
trap 'rm -rf "$scratch"' EXIT

cat > "$scratch/brine" <<'HELP'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  --help)
    printf 'Available Commands:\n  example  Example command\n\n'
    ;;
  'example --help')
    printf 'Example command\n\nAvailable Commands:\n  inspect     \n\nGlobal Flags:\n      --json  Machine output\n'
    ;;
  'example inspect --help')
    printf 'Inspect fixture\n\nUsage:\n  brine example inspect    \n'
    ;;
  *) exit 2 ;;
esac
HELP
chmod +x "$scratch/brine"
"$root/scripts/docs-commands.sh" "$scratch/brine" > "$scratch/commands.md"

if ! awk '/[ \t]+$/ {bad=1} {last=$0} END {if (bad || last=="") exit 1}' "$scratch/commands.md"; then
  echo 'Generated command help has trailing whitespace or a final blank line.' >&2
  exit 1
fi
grep -Fq '### `brine example`' "$scratch/commands.md"
grep -Fq '### `brine example inspect`' "$scratch/commands.md"
grep -Fq '  brine example inspect' "$scratch/commands.md"
if grep -Eq 'Global Flags:|--json' "$scratch/commands.md"; then
  echo 'Root flags must remain omitted from individual commands.' >&2
  exit 1
fi
