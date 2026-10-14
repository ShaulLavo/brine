#!/usr/bin/env python3
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
PINS = json.loads((HERE / 'pins.json').read_text())


def file_sha256(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def prepare(output, supplied):
    output.mkdir(parents=True, exist_ok=False)
    archive = output / 'upstream.tar.gz'
    if supplied:
        shutil.copyfile(supplied, archive)
    else:
        url = 'https://codeload.github.com/clockworklabs/SpacetimeDB/tar.gz/' + PINS['upstream_commit']
        with urllib.request.urlopen(url, timeout=45) as response, archive.open('wb') as writer:
            copy_bounded(response, writer)
    if file_sha256(archive) != PINS['archive_sha256']:
        raise ValueError('upstream archive pin mismatch')
    with tarfile.open(archive) as source:
        members = source.getmembers()
        if len(members) > 10000 or sum(member.size for member in members) > 256 << 20:
            raise ValueError('upstream expansion quota')
        source.extractall(output, filter='data')
    upstream = output / ('SpacetimeDB-' + PINS['upstream_commit'])
    patch = HERE / 'graceful-shutdown.patch'
    if file_sha256(patch) != PINS['patch_sha256']:
        raise ValueError('native shutdown patch pin mismatch')
    subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(patch.resolve())], cwd=upstream, check=True, timeout=15)
    prepared_ns = time.time_ns()
    for path in upstream.rglob('*'):
        if path.is_file():
            os.utime(path, ns=(prepared_ns, prepared_ns))
    shutil.copyfile(HERE / 'pins.json', output / 'pins.json')
    shutil.copyfile(patch, output / 'graceful-shutdown.patch')
    shutil.copyfile(HERE / 'Dockerfile', output / 'Dockerfile')
    shutil.copyfile(upstream / 'LICENSE.txt', output / 'upstream-LICENSE.txt')
    print(upstream)
    return upstream


def copy_bounded(response, writer):
    while chunk := response.read(1 << 20):
        if writer.tell() + len(chunk) > 32 << 20:
            raise ValueError('upstream archive quota')
        writer.write(chunk)


if __name__ == '__main__':
    if len(sys.argv) not in [2, 3]:
        raise SystemExit('usage: prepare.py NEW_OUTPUT [PINNED_ARCHIVE]')
    prepare(pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]) if len(sys.argv) == 3 else None)
