import json
import pathlib
import re
import sys

index = sys.argv[1]
source = sys.argv[2]
raw = pathlib.Path(sys.argv[3])
output = pathlib.Path(sys.argv[4])
manifest = pathlib.Path(sys.argv[5])
assert re.fullmatch(r'sha256:[0-9a-f]{64}', index)
assert re.fullmatch(r'[0-9a-f]{40}', source)
index_data = json.loads(raw.read_text())
platforms = {}
for descriptor in index_data['manifests']:
    platform = descriptor.get('platform', {})
    arch = platform.get('architecture')
    if platform.get('os') != 'linux' or arch not in ['amd64', 'arm64']:
        continue
    assert arch not in platforms, 'duplicate publication platform'
    value = descriptor['digest']
    assert re.fullmatch(r'sha256:[0-9a-f]{64}', value)
    platforms[arch] = value
assert set(platforms) == {'amd64', 'arm64'}
verified = json.loads(manifest.read_text())
assert verified['source_head'] == source and verified['source_dirty'] is False
variant = verified['serving_variant']
assert variant in ('v1', 'default-column')
output.write_text(json.dumps({'source_revision': source, 'artifact_source_revision': verified['artifact_source_head'],
                             'serving_variant': variant, 'image_index': index, 'platforms': platforms,
                             'module_sha256': verified['modules'][variant],
                             'client_sha256': verified['clients'][variant]['browser_sha256'],
                             'app_binaries': verified['app_binaries']}, indent=2) + '\n')
