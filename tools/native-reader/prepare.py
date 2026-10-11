#!/usr/bin/env python3
import hashlib
import json
import pathlib
import re
import shutil
import sys
import tarfile
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
PINS = json.loads((HERE / 'pins.json').read_text())


def archive(output, supplied):
    target = output / 'upstream.tar.gz'
    if supplied:
        shutil.copyfile(supplied, target)
    else:
        url = 'https://codeload.github.com/clockworklabs/SpacetimeDB/tar.gz/' + PINS['upstream_commit']
        with urllib.request.urlopen(url, timeout=45) as response, target.open('wb') as writer:
            while chunk := response.read(1 << 20):
                writer.write(chunk)
                if writer.tell() > 32 << 20:
                    raise ValueError('upstream archive quota')
    if hashlib.sha256(target.read_bytes()).hexdigest() != PINS['archive_sha256']:
        raise ValueError('upstream archive pin mismatch')
    return target


def prepare(output, supplied):
    output.mkdir(parents=True, exist_ok=False)
    downloaded = archive(output, supplied)
    with tarfile.open(downloaded) as source:
        members = source.getmembers()
        if len(members) > 10000 or sum(member.size for member in members) > 256 << 20:
            raise ValueError('upstream expansion quota')
        source.extractall(output, filter='data')
    upstream = output / ('SpacetimeDB-' + PINS['upstream_commit'])
    root = upstream / 'Cargo.toml'
    text = root.read_text()
    text = re.sub(r'^members = \[.*?\]', 'members = ["crates/brine-native-reader"]', text, flags=re.S | re.M)
    text = re.sub(r'^default-members = \[.*?\]', 'default-members = ["crates/brine-native-reader"]', text, flags=re.S | re.M)
    root.write_text(text)
    for manifest in (upstream / 'crates').glob('*/Cargo.toml'):
        content = re.sub(r'\[dev-dependencies(?:\.[^\]]+)?\]\n.*?(?=\n\[|\Z)', '', manifest.read_text(), flags=re.S)
        manifest.write_text(content)
    package = upstream / 'crates/brine-native-reader'
    package.mkdir()
    shutil.copyfile(HERE / 'Cargo.toml', package / 'Cargo.toml')
    shutil.copytree(HERE / 'src', package / 'src')
    shutil.copyfile(HERE / 'Cargo.lock', upstream / 'Cargo.lock')
    shutil.copyfile(HERE / 'pins.json', output / 'pins.json')
    for filename in ['LICENSE.txt', 'README.md']:
        shutil.copyfile(upstream / filename, output / ('upstream-' + filename))
    shutil.copyfile(HERE / 'Dockerfile', output / 'Dockerfile')
    print(upstream)


if __name__ == '__main__':
    if len(sys.argv) not in [2, 3]:
        raise SystemExit('usage: prepare.py NEW_OUTPUT [PINNED_ARCHIVE]')
    prepare(pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]) if len(sys.argv) == 3 else None)
