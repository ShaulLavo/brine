import hashlib
import json
import pathlib
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
fixture = pathlib.Path(sys.argv[2])

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

variants = ['v1', 'additive', 'destructive', 'observer-hooks', 'default-column', 'access-change']
modules = {name: digest(root / 'artifacts' / ('module-' + name + '.wasm')) for name in variants}
assert len(set(modules.values())) == len(variants), 'fixture variants are not distinct'
clients = {name: {'browser_sha256': digest(root / 'artifacts' / ('browser-' + name + '.js')), 'module_sha256': modules[name]} for name in ['v1', 'additive', 'default-column']}
inputs = ['module/Cargo.toml', 'module/Cargo.lock', 'module/rust-toolchain.toml', 'module/src/lib.rs', 'client/package.json', 'client/bun.lock', 'client/session.ts', 'client/browser.ts', 'main.go', 'browser-test/package.json', 'browser-test/bun.lock', 'browser-test/browser.mjs', 'checks/default-column.ts', 'smoke.py']
pinned = json.loads((fixture / 'module/artifact-pins.json').read_text())
assert modules == pinned['modules'] and clients == pinned['clients'], 'rebuild differs from retained module/client pins'
manifest = {
    'version': 1,
    'source_head': subprocess.check_output(['git', '-C', str(fixture), 'rev-parse', 'HEAD'], text=True).strip(),
    'source_dirty': bool(subprocess.check_output(['git', '-C', str(fixture), 'status', '--porcelain', '--', '.'], text=True).strip()),
    'rust': '1.96.1',
    'spacetimedb': '2.11.0',
    'client_sdk': '2.11.0',
    'typescript': '5.6.3',
    'playwright': '1.64.0',
    'canonicalizer': 'spacetimedb-v10-expanded-v2',
    'modules': modules,
    'clients': clients,
    'serving_variant': 'v1',
    'base_client_compatible_modules': ['v1', 'additive'],
    'rejection_inputs': ['destructive', 'observer-hooks', 'access-change'],
    'input_sha256': {name: digest(fixture / name) for name in inputs},
    'app_binaries': {arch: digest(fixture / 'bin' / arch / 'spacetimedb-app') for arch in ['amd64', 'arm64']},
}
(root / 'artifacts' / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
