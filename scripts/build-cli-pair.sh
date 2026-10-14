#!/usr/bin/env bash
set -euo pipefail

if (( $# != 1 )); then
  printf 'usage: build-cli-pair.sh OUTPUT_DIRECTORY\n' >&2
  exit 2
fi

cd "$(dirname "${BASH_SOURCE[0]}")/.."
if [[ $(go env GOHOSTOS) != linux || $(go env GOHOSTARCH) != amd64 ]]; then
  printf 'build verification requires a Linux amd64 host\n' >&2
  exit 2
fi
source_root=$(pwd -P)
output_root=$(realpath -m -- "$1")
case "$output_root/" in
  "$source_root/"*) printf 'build output must be outside the checkout\n' >&2; exit 2 ;;
esac
if [[ -n $(git status --porcelain) ]]; then
  printf 'build requires a clean checkout\n' >&2
  exit 2
fi

source_head=$(git rev-parse HEAD)
source_tree=$(git rev-parse HEAD^{tree})
mkdir -- "$output_root"
for architecture in amd64 arm64; do
  binary="$output_root/brine-$architecture"
  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -o "$binary" ./cmd/brine
  go version -m "$binary" > "$output_root/$architecture.metadata.txt"
done
"$output_root/brine-amd64" version --json > "$output_root/amd64.version.json"

python3 - "$output_root" "$source_head" "$source_tree" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

root = Path(sys.argv[1])
version = json.loads((root / "amd64.version.json").read_text())
assert version["ok"] and isinstance(version["data"]["version"], str)
receipt = {"source_head": sys.argv[2], "source_tree": sys.argv[3],
           "version": version["data"]["version"], "vcs_modified": False, "artifacts": {}}
for architecture, machine in (("amd64", 62), ("arm64", 183)):
    metadata = (root / (architecture + ".metadata.txt")).read_text().splitlines()
    settings = {line.strip().split("\t", 1)[1] for line in metadata if line.strip().startswith("build\t")}
    assert {"vcs.revision=" + sys.argv[2], "vcs.modified=false", "GOOS=linux", "GOARCH=" + architecture} <= settings
    binary = root / ("brine-" + architecture)
    with binary.open("rb") as stream:
        header = stream.read(20)
        assert header[:6] == b"\x7fELF\x02\x01" and int.from_bytes(header[18:20], "little") == machine
        stream.seek(0)
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    receipt["artifacts"][architecture] = {"path": binary.name, "sha256": digest,
                                           "bytes": binary.stat().st_size, "os": "linux", "architecture": architecture}
(root / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
PY
