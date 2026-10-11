import argparse
import hashlib
import json
import pathlib
import shutil
import stat
import subprocess
import time
import urllib.request
import urllib.error

FIXTURE = pathlib.Path(__file__).resolve().parent


def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()


def flatten_transfer(root, manifest):
    for arch, expected in manifest['app_binaries'].items():
        binary = root / 'bin' / arch / 'spacetimedb-app'
        binary.chmod(0o644)
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == expected
        assert stat.S_IMODE(binary.stat().st_mode) == 0o644
    binary = root / 'bin/amd64/spacetimedb-app'
    try:
        subprocess.run([str(binary)], check=True, capture_output=True, timeout=5)
    except PermissionError:
        return
    raise AssertionError('artifact transfer mode regression did not reproduce EACCES')


def qualify(root, arch, manifest):
    image = 'brine-fixture-mode-proof:' + arch
    subprocess.run(['docker', 'buildx', 'build', '--load', '--platform', 'linux/' + arch, '-t', image, str(root)], check=True, timeout=120)
    container = docker('run', '--detach', '--platform', 'linux/' + arch, '--publish', '127.0.0.1::8080',
                       '--env', 'BRINE_DB_MAIN_ENGINE=spacetimedb', '--env', 'BRINE_DB_MAIN_URL=https://fixture.example/',
                       '--env', 'BRINE_DB_MAIN_NAME=recovery-fixture', '--env', 'BRINE_DB_MAIN_ACCEPTS=brine-recovery-v1', image)
    try:
        return inspect_running(container, root, arch, manifest)
    finally:
        docker('rm', '--force', container)
        docker('image', 'rm', image)


def inspect_running(container, root, arch, manifest):
    state = json.loads(docker('inspect', container))[0]
    assert state['State']['Running'] and state['Config']['User'] == '10001:10001'
    entrypoint = root / ('entrypoint-' + arch)
    docker('cp', container + ':/spacetimedb-app', str(entrypoint))
    assert stat.S_IMODE(entrypoint.stat().st_mode) == 0o555
    assert hashlib.sha256(entrypoint.read_bytes()).hexdigest() == manifest['app_binaries'][arch]
    binding = state['NetworkSettings']['Ports']['8080/tcp'][0]
    base = 'http://127.0.0.1:' + binding['HostPort'] + '/'
    for _ in range(50):
        try:
            return inspect_http(base, arch, manifest)
        except urllib.error.URLError:
            time.sleep(.1)
    raise RuntimeError('container HTTP readiness deadline')


def inspect_http(base, arch, manifest):
    with urllib.request.urlopen(base + 'health', timeout=1) as response:
        assert response.status == 200
    with urllib.request.urlopen(base + 'config.json', timeout=1) as response:
        config = json.load(response)
    assert config == {'endpoint': 'https://fixture.example/', 'database': 'recovery-fixture'}
    with urllib.request.urlopen(base + 'browser.js', timeout=1) as response:
        browser = response.read()
    assert hashlib.sha256(browser).hexdigest() == manifest['clients']['v1']['browser_sha256']
    return {'platform': 'linux/' + arch, 'transferred_binary_mode': '0644', 'image_entrypoint_mode': '0555',
            'container_user': '10001:10001', 'entrypoint_running': True, 'http_and_browser_identity': True}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('release', type=pathlib.Path)
    parser.add_argument('scratch', type=pathlib.Path)
    parser.add_argument('--transfer-only', action='store_true')
    args = parser.parse_args()
    args.scratch.mkdir(mode=0o700)
    context = args.scratch / 'context'
    shutil.copytree(args.release / 'bin', context / 'bin')
    shutil.copy(FIXTURE / 'Dockerfile', context / 'Dockerfile')
    manifest = json.loads((args.release / 'artifacts/manifest.json').read_text())
    flatten_transfer(context, manifest)
    if args.transfer_only:
        print('Documented artifact modes preserve hashes and reproduce EACCES before image assembly.')
        return
    result = {arch: qualify(context, arch, manifest) for arch in ['amd64', 'arm64']}
    (args.scratch / 'image-evidence.json').write_text(json.dumps(result, indent=2) + '\n')
    print('Both image entrypoints run after flattened artifact transfer; byte and browser identities match.')


if __name__ == '__main__':
    main()
