"""Package a selected, pinned browser from an already verified fixture bundle."""
import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tempfile

FIXTURE = pathlib.Path(__file__).resolve().parent
VARIANTS = ('v1', 'default-column')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def serving_client(manifest, variant):
    if variant not in VARIANTS:
        raise ValueError('unsupported serving variant')
    client = manifest['clients'][variant]
    if client['module_sha256'] != manifest['modules'][variant]:
        raise ValueError('selected browser and module are not paired')
    return client


def package(release, output, variant, input_source):
    subprocess.run(['python3', str(FIXTURE / 'check-release.py'), str(release), input_source],
                   check=True, timeout=30)
    manifest = json.loads((release / 'artifacts/manifest.json').read_text())
    client = serving_client(manifest, variant)
    source = subprocess.check_output(['git', '-C', str(FIXTURE), 'rev-parse', 'HEAD'], text=True).strip()
    dirty = subprocess.check_output(['git', '-C', str(FIXTURE), 'status', '--porcelain', '--', '.'], text=True).strip()
    if dirty:
        raise ValueError('fixture package source is not clean')
    output.mkdir(parents=True, exist_ok=False)
    shutil.copytree(release / 'artifacts', output / 'artifacts')
    (output / 'artifacts/image-evidence.json').unlink(missing_ok=True)
    with tempfile.TemporaryDirectory(prefix='brine-fixture-variant-') as scratch:
        app = pathlib.Path(scratch)
        shutil.copyfile(FIXTURE / 'main.go', app / 'main.go')
        shutil.copytree(FIXTURE / 'web', app / 'web', ignore=shutil.ignore_patterns('browser.js'))
        shutil.copyfile(release / 'artifacts' / ('browser-' + variant + '.js'), app / 'web/browser.js')
        if digest(app / 'web/browser.js') != client['browser_sha256']:
            raise ValueError('selected embedded browser hash differs from the pinned client')
        (app / 'go.mod').write_text('module brine-fixture-assets\n\ngo 1.26.0\n')
        inputs = {str(path.relative_to(app)): digest(path) for path in app.rglob('*') if path.is_file()}
        binaries = {}
        for arch in ('amd64', 'arm64'):
            binary = output.resolve() / 'bin' / arch / 'spacetimedb-app'
            binary.parent.mkdir(parents=True)
            subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(binary), '.'],
                           cwd=app, env=dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=arch),
                           check=True, timeout=120)
            binaries[arch] = digest(binary)
    manifest.update(source_head=source, source_dirty=False, artifact_source_head=input_source,
                    serving_variant=variant, app_input_sha256=inputs, app_binaries=binaries)
    (output / 'artifacts/manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    subprocess.run(['python3', str(FIXTURE / 'check-release.py'), str(output), source, variant], check=True, timeout=30)
    return manifest


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('release', type=pathlib.Path)
    parser.add_argument('output', type=pathlib.Path)
    parser.add_argument('variant', choices=VARIANTS)
    parser.add_argument('--input-source', required=True)
    args = parser.parse_args()
    package(args.release, args.output, args.variant, args.input_source)
