#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import ast
import json
import pathlib
import re

root = pathlib.Path('tools/spacetime-runtime')
for path in root.glob('*.py'):
    ast.parse(path.read_text(), filename=str(path))
pins = json.loads((root / 'pins.json').read_text())
assert pins['runtime_version'] == pins['module_sdk'] == pins['client_sdk'] == '2.11.0'
assert pins['container_uid'] == pins['container_gid'] == 1000
assert pins['rust_toolchain'] == '1.96.1' and pins['bun'] == '1.4.2'
assert set(pins['platforms']) == {'linux/amd64', 'linux/arm64'}
digests = [pins['image'].split('@')[1]]
for platform in pins['platforms'].values():
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', platform['manifest'])
    assert re.fullmatch(r'[a-f0-9]{64}', platform['archive_sha256'])
    assert re.fullmatch(r'[a-f0-9]{64}', platform['standalone_sha256'])
    digests.append(platform['manifest'])
assert len(set(digests)) == 3
print('Runtime probe syntax and pinned metadata contract passed; no host or network used.')
PY
