import argparse
import hashlib
import json
import os
import pathlib
import shutil
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.request

FIXTURE = pathlib.Path(__file__).resolve().parent
DATABASE = 'brine-fixture-sdk'


def available_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


class Probe:
    def __init__(self, root, observer):
        self.root = root
        self.observer = observer
        self.state = root / 'smoke-private'
        self.state.mkdir(mode=0o700)
        self.keys = self.state / 'keys'
        self.keys.mkdir(mode=0o700)
        self.port = available_port()
        self.endpoint = f'http://127.0.0.1:{self.port}/'
        self.result = {'version': 1, 'runtime': '2.11.0', 'sdk': '2.11.0', 'canonicalizer': 'spacetimedb-v10-expanded-v2'}
        self.result['observer_binary_sha256'] = hashlib.sha256(observer.read_bytes()).hexdigest()
        subprocess.run(['openssl', 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-out', str(self.keys / 'private.pem')], check=True, capture_output=True)
        subprocess.run(['openssl', 'pkey', '-in', str(self.keys / 'private.pem'), '-pubout', '-out', str(self.keys / 'public.pem')], check=True, capture_output=True)
        (self.keys / 'private.pem').chmod(0o600)

    def request(self, path, data=None, method=None, token=None, timeout=10):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        request = urllib.request.Request(self.endpoint + 'v1/' + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                body = response.read(2 * 1024 * 1024)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f'private fixture API rejected request, HTTP {error.code}') from None
        return json.loads(body) if body else None

    def start(self, tree, label):
        log = (self.state / (label + '.log')).open('w')
        process = subprocess.Popen([
            str(self.root / 'runtime/spacetimedb-standalone'), 'start', '--listen-addr', f'127.0.0.1:{self.port}',
            '--data-dir', str(tree), '--jwt-pub-key-path', str(self.keys / 'public.pem'),
            '--jwt-priv-key-path', str(self.keys / 'private.pem'), '--page_pool_max_size', '67108864', '--non-interactive',
        ], stdout=log, stderr=log, env=dict(os.environ, TOKIO_WORKER_THREADS='2'))
        try:
            return self.wait_start(process)
        except Exception:
            process.kill()
            process.wait()
            raise

    def wait_start(self, process):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError('private fixture runtime exited during startup')
            if self.ready():
                return process
            time.sleep(.1)
        raise RuntimeError('private runtime startup deadline')

    def ready(self):
        try:
            return self.request('health', timeout=1)['version'] == '2.11.0'
        except (RuntimeError, urllib.error.URLError):
            return False

    def stop(self, process, strict=False):
        process.send_signal(signal.SIGINT)
        try:
            code = process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise RuntimeError('unknown fixture stop outcome, no exact capture eligible')
        if strict and code != 0:
            raise RuntimeError('nonzero fixture stop, no exact capture eligible')
        return code

    def token(self, name, identity):
        path = self.state / (name + '.token')
        path.write_text(identity['token'])
        path.chmod(0o600)
        return path

    def client(self, variant, action, token, value=None, expected_error=None):
        args = ['bun', str(self.root / 'clients' / variant / 'drill.ts'), self.endpoint, DATABASE, action]
        if value is not None:
            args.append(str(value))
        run = subprocess.run(args, env=dict(os.environ, BRINE_USER_TOKEN_FILE=str(token)), capture_output=True, text=True, timeout=20)
        if expected_error:
            assert run.returncode != 0 and expected_error in run.stderr
            return {'rejected': expected_error}
        if run.returncode:
            raise RuntimeError('official SDK drill failed')
        return json.loads(run.stdout)

    def admin(self, action, value, expected_error=None):
        args = ['bun', str(self.root / 'clients/v1/admin-drill.ts'), self.endpoint, DATABASE, action, str(value)]
        run = subprocess.run(args, env=dict(os.environ, BRINE_PUBLISHER_TOKEN_FILE=str(self.owner_token)), capture_output=True, text=True, timeout=20)
        if expected_error:
            assert run.returncode != 0 and expected_error in run.stderr
            return
        if run.returncode:
            raise RuntimeError('typed publisher reducer failed')

    def observe(self):
        info = self.request('database/' + DATABASE, token=self.owner['token'])
        private = {'host': '127.0.0.1', 'port': self.port, 'database_identity': info['database_identity']['__identity__'].removeprefix('0x').lower(), 'publisher_identity': info['owner_identity']['__identity__'].removeprefix('0x').lower(), 'names': [DATABASE], 'publisher_credential': self.owner['token']}
        run = subprocess.run([str(self.observer), '--mode', 'observe'], input=json.dumps(private), capture_output=True, text=True, timeout=20)
        if run.returncode:
            raise RuntimeError('shared production observer refused fixture')
        return json.loads(run.stdout)

    def publish(self, variant, policy=None, token=None):
        query = 'database/' + DATABASE + '?host_type=Wasm'
        if policy:
            query += '&policy=' + policy + '&token=' + token
        body = (self.root / 'artifacts' / ('module-' + variant + '.wasm')).read_bytes()
        self.request(query, body, 'PUT', self.owner['token'])

    def preflight(self, variant):
        body = (self.root / 'artifacts' / ('module-' + variant + '.wasm')).read_bytes()
        result = self.request('database/' + DATABASE + '/pre_publish?host_type=Wasm&style=NoColor', body, 'POST', self.owner['token'])
        return result

    def initial(self):
        self.owner = self.request('identity', b'', 'POST')
        writer = self.request('identity', b'', 'POST')
        outsider = self.request('identity', b'', 'POST')
        self.owner_token = self.token('publisher', self.owner)
        self.writer_token = self.token('writer', writer)
        self.outsider_token = self.token('outsider', outsider)
        self.publish('v1')
        self.admin('authorize', writer['identity'])
        self.result['denied_writer'] = self.client('v1', 'denied', self.outsider_token)
        self.result['confirmed_write'] = self.client('v1', 'write', self.writer_token, 1)
        self.admin('schedule', 21, 'one schedule of 1..20 writes required')
        self.admin('schedule', 3)
        self.client('v1', 'write', self.writer_token, 2, 'scheduled writer active')
        time.sleep(4)
        scheduled = self.client('v1', 'verify', self.writer_token, 4)
        assert sum(row['scheduled'] for row in scheduled['rows']) == 3
        self.result['scheduled_without_clients'] = scheduled
        self.result['browser'] = self.browser(4)
        self.expected = self.client('v1', 'verify', self.writer_token, 5)
        self.original_observation = self.observe()

    def browser(self, rows):
        port = available_port()
        log = (self.state / 'app.log').open('w')
        env = dict(os.environ, BRINE_FIXTURE_LISTEN=f'127.0.0.1:{port}', BRINE_DB_MAIN_ENGINE='spacetimedb', BRINE_DB_MAIN_URL=self.endpoint, BRINE_DB_MAIN_NAME=DATABASE, BRINE_DB_MAIN_ACCEPTS='brine-recovery-v1', EXTRA_SECRET='must-not-appear')
        process = subprocess.Popen([str(FIXTURE / 'bin/amd64/spacetimedb-app')], env=env, stdout=log, stderr=log)
        try:
            app_url = f'http://127.0.0.1:{port}/'
            self.wait_app(process, app_url)
            public = json.load(urllib.request.urlopen(app_url + 'config.json', timeout=10))
            assert public == {'endpoint': self.endpoint, 'database': DATABASE}
            output = self.root / 'artifacts/browser-evidence.json'
            run = subprocess.run(['bun', str(FIXTURE / 'browser-test/browser.mjs'), app_url, str(self.writer_token), str(rows), str(output)], capture_output=True, text=True, timeout=40)
            if run.returncode:
                raise RuntimeError('actual browser subscription/write proof failed')
            return json.loads(output.read_text())
        finally:
            process.terminate()
            process.wait(timeout=5)

    def wait_app(self, process, url):
        for _ in range(50):
            if process.poll() is not None:
                raise RuntimeError('static fixture app exited')
            try:
                urllib.request.urlopen(url + 'health', timeout=1).close()
                return
            except urllib.error.URLError:
                time.sleep(.1)
        raise RuntimeError('static fixture readiness deadline')

    def restored(self, source):
        restored = self.state / 'restored'
        shutil.copytree(source, restored)
        process = self.start(restored, 'restored')
        try:
            recovered = self.client('v1', 'verify', self.writer_token, 5)
            assert recovered == self.expected
            observed = self.observe()
            assert observed == self.original_observation
            self.result['stopped_copy_recovered'] = recovered
            self.result['shared_observer_matches_replay'] = True
        finally:
            self.stop(process, strict=True)

    def unconfirmed(self, source):
        process = self.start(source, 'unconfirmed')
        try:
            args = ['bun', str(self.root / 'clients/v1/unconfirmed-drill.ts'), self.endpoint, DATABASE, '6']
            run = subprocess.run(args, env=dict(os.environ, BRINE_USER_TOKEN_FILE=str(self.writer_token)), capture_output=True, text=True, timeout=20)
            assert run.returncode == 0
            acknowledgement = json.loads(run.stdout)
            assert acknowledgement['durabilityEvidence'] is False and acknowledgement['confirmedReads'] is False
            self.expected = self.client('v1', 'verify', self.writer_token, 6)
            self.result['unconfirmed_acknowledgement'] = acknowledgement
            self.result['independent_confirmed_read'] = self.expected
            self.preflight_checks()
        finally:
            self.stop(process, strict=True)

    def preflight_checks(self):
        results = {}
        before = self.client('v1', 'read', self.writer_token)
        for variant in ['additive', 'destructive', 'default-column', 'access-change', 'observer-hooks']:
            result = self.preflight(variant)
            results[variant] = scrub_preflight(result)
        assert results['additive']['AutoMigrate']['break_clients'] is False
        assert results['destructive']['AutoMigrate']['break_clients'] is True
        assert 'recovery_right' in results['destructive']['AutoMigrate']['migrate_plan']
        assert self.client('v1', 'read', self.writer_token) == before
        self.result['preflight'] = results
        self.result['destructive_requires_independent_brine_rejection'] = True

    def variant(self, source, variant):
        tree = self.state / ('candidate-' + variant)
        shutil.copytree(source, tree)
        process = self.start(tree, variant)
        try:
            return self.variant_running(variant)
        finally:
            code = self.stop(process)
            self.result[variant + '_shutdown'] = {'exit': code, 'clean_exit_observed': code == 0, 'exact_capture_receipt': False}

    def variant_running(self, variant):
        if variant == 'destructive':
            self.reject_destructive()
            return
        preflight = self.preflight(variant)
        automatic = preflight.get('AutoMigrate')
        if automatic is None:
            raise RuntimeError('pinned fixture candidate unexpectedly requires manual migration')
        if automatic['break_clients']:
            self.publish(variant, 'BreakClients', automatic['token'])
        else:
            self.publish(variant)
        if variant == 'observer-hooks':
            self.client('v1', 'read', self.writer_token, expected_error='fixed schema marker differs')
            self.result[variant] = {'lifecycle_marker_change_refused': True}
            return
        if variant == 'default-column':
            self.default_client()
            return
        base = self.client('v1', 'read', self.writer_token)
        matched = self.client('additive', 'read', self.writer_token)
        assert base == self.expected and matched == base
        self.result[variant] = {'base_and_matched_clients_preserved_markers': True}

    def reject_destructive(self):
        before = self.observe()
        refused = False
        try:
            self.publish('destructive')
        except RuntimeError:
            refused = True
        after = self.observe()
        unchanged = self.client('v1', 'read', self.writer_token) == self.expected
        assert refused and before == after and unchanged
        self.result['destructive'] = {'compatible_policy_rejected': True, 'shared_observation_and_markers_unchanged': True}

    def default_client(self):
        client = self.root / 'clients/default-column'
        shutil.copy(FIXTURE / 'checks/default-column.ts', client / 'default-client-check.ts')
        subprocess.run([str(client / 'node_modules/.bin/tsc'), '-p', str(client)], check=True, capture_output=True, timeout=20)
        args = ['bun', str(client / 'default-client-check.ts'), self.endpoint, DATABASE, '6']
        run = subprocess.run(args, env=dict(os.environ, BRINE_USER_TOKEN_FILE=str(self.writer_token)), capture_output=True, text=True, timeout=20)
        if run.returncode:
            raise RuntimeError('regenerated default-column SDK client failed')
        self.result['default-column'] = json.loads(run.stdout)

    def run(self):
        source = self.state / 'source'
        source.mkdir(mode=0o700)
        (source / 'config.toml').write_text('[module-http]\nenabled = false\n[commitlog]\nmax-segment-size = 32768\nwrite-buffer-size = 4096\n[v8-heap-policy]\nheap-limit-mb = 64\n')
        process = self.start(source, 'source')
        try:
            self.initial()
        finally:
            self.stop(process, strict=True)
        self.restored(source)
        self.unconfirmed(source)
        for variant in ['additive', 'default-column', 'destructive', 'observer-hooks']:
            self.variant(source, variant)
        (self.root / 'artifacts/smoke-evidence.json').write_text(json.dumps(self.result, indent=2) + '\n')
        print('Pinned fixture SDK/browser/scheduler/replay and reviewed candidate evidence passed.')


def scrub_preflight(result):
    clean = json.loads(json.dumps(result))
    for contents in clean.values():
        if isinstance(contents, dict):
            contents.pop('token', None)
    return clean


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('build_root', type=pathlib.Path)
    parser.add_argument('--observer', type=pathlib.Path)
    args = parser.parse_args()
    observer = args.observer or args.build_root / 'runtime/spacetime-runtime'
    if not observer.is_file():
        raise RuntimeError('merged production observer helper is required')
    probe = Probe(args.build_root, observer)
    try:
        probe.run()
    finally:
        shutil.rmtree(probe.state)


if __name__ == '__main__':
    main()
