import hashlib
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
expected_revision = sys.argv[2]
manifest = json.loads((root / 'artifacts/manifest.json').read_text())
assert manifest['source_head'] == expected_revision and manifest['source_dirty'] is False
for variant, expected in manifest['modules'].items():
    actual = hashlib.sha256((root / 'artifacts' / ('module-' + variant + '.wasm')).read_bytes()).hexdigest()
    assert actual == expected, 'module bytes differ from verified publication manifest'
for variant, client in manifest['clients'].items():
    actual = hashlib.sha256((root / 'artifacts' / ('browser-' + variant + '.js')).read_bytes()).hexdigest()
    assert actual == client['browser_sha256'], 'client bytes differ from verified publication manifest'
for arch, expected in manifest['app_binaries'].items():
    actual = hashlib.sha256((root / 'bin' / arch / 'spacetimedb-app').read_bytes()).hexdigest()
    assert actual == expected, 'app binary differs from verified publication manifest'
print('Downloaded source revision, module/client pairs and both app binaries match verified identities.')
