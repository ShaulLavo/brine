#!/usr/bin/env python3
"""Verify native engine publication inputs and emit their immutable receipt."""
import argparse
import hashlib
import json
import pathlib
import re

HERE = pathlib.Path(__file__).resolve().parent
FILES = {'spacetimedb-standalone', 'pins.json', 'graceful-shutdown.patch',
         'upstream-LICENSE.txt', 'Cargo.lock'}


def sha256(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def verify_arch(bundle, source_head, pins):
    if (bundle / 'source-head').read_text() != source_head + '\n':
        raise ValueError('bundle source-head differs from the publishing commit')
    if (bundle / 'pins.json').read_bytes() != (HERE / 'pins.json').read_bytes():
        raise ValueError('bundle source pins differ from the publishing commit')
    entries = (bundle / 'SHA256SUMS').read_text().splitlines()
    hashes = {}
    for entry in entries:
        match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9.-]+)', entry)
        if not match or match[2] in hashes:
            raise ValueError('invalid or duplicate bundle checksum entry')
        hashes[match[2]] = match[1]
    if hashes.keys() != FILES:
        raise ValueError('bundle checksum manifest does not cover the exact input files')
    for name, expected in hashes.items():
        path = bundle / name
        if path.is_symlink() or not path.is_file() or sha256(path) != expected:
            raise ValueError('bundle checksum mismatch: ' + name)
    if hashes['graceful-shutdown.patch'] != pins['patch_sha256']:
        raise ValueError('bundle shutdown patch pin mismatch')
    if (bundle / 'graceful-shutdown.patch').read_bytes() != (HERE / 'graceful-shutdown.patch').read_bytes():
        raise ValueError('bundle shutdown patch differs from the publishing commit')
    return hashes['spacetimedb-standalone']


def verify(package, source_head, published_digest=None):
    if not re.fullmatch(r'[0-9a-f]{40}', source_head):
        raise ValueError('source-head must be the full publishing commit')
    pins = json.loads((HERE / 'pins.json').read_text())
    if sha256(HERE / 'graceful-shutdown.patch') != pins['patch_sha256']:
        raise ValueError('repository shutdown patch pin mismatch')
    package_dockerfile = (HERE / 'Package.Dockerfile').read_text()
    if package_dockerfile.splitlines()[0] != 'FROM ' + pins['base_image']:
        raise ValueError('packaging base differs from the pinned original image')
    native_copy = 'COPY --chmod=0555 ${TARGETARCH}/' + pins['artifact'] + ' ' + pins['native_path']
    if native_copy not in package_dockerfile.splitlines():
        raise ValueError('packaging native path or executable mode differs from its pins')
    executables = {arch: verify_arch(package / arch, source_head, pins) for arch in ('amd64', 'arm64')}
    receipt = {'source_head': source_head, 'upstream_commit': pins['upstream_commit'],
               'patch_sha256': pins['patch_sha256'], 'base_image': pins['base_image'],
               'executable_sha256': executables}
    if published_digest is not None:
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', published_digest):
            raise ValueError('published index digest is missing or invalid')
        receipt['image'] = 'ghcr.io/shaullavo/brine-native-engine@' + published_digest
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('package', type=pathlib.Path)
    parser.add_argument('source_head')
    parser.add_argument('--published-digest')
    args = parser.parse_args()
    print(json.dumps(verify(args.package, args.source_head, args.published_digest), indent=2))
