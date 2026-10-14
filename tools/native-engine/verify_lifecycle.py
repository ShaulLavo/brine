#!/usr/bin/env python3
"""Check a supplied native engine and supplied v1 Wasm in a caller-isolated network."""
import argparse
import copy
import hashlib
import json
import os
import pathlib
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

DATABASE = 'brine-native-lifecycle'


def sha256(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def require_isolation():
    if {name for _, name in socket.if_nameindex()} != {'lo'}:
        raise ValueError('caller must supply a loopback-only network namespace')
    if pathlib.Path('/proc/net/route').read_text().splitlines()[1:]:
        raise ValueError('caller network namespace has an external route')


def normalize_schema_declaration_order(schema):
    """Order only the collections emitted from upstream HashMap iteration."""
    normalized = copy.deepcopy(schema)
    key = lambda value: json.dumps(value, sort_keys=True, separators=(',', ':'))
    for section in normalized['sections']:
        if 'Types' in section:
            section['Types'].sort(key=key)
        if 'Tables' in section:
            section['Tables'].sort(key=key)
        if 'ExplicitNames' in section:
            section['ExplicitNames']['entries'].sort(key=key)
    return normalized


class Probe:
    def __init__(self, binary, root, report):
        self.binary, self.root, self.report = binary, root, report
        self.processes, self.readers = [], []
        self.panic = False
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(self, path, data=None, method=None, token=None, timeout=10):
        headers = {'Content-Type': 'application/octet-stream', 'Connection': 'close'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        request = urllib.request.Request(f'http://127.0.0.1:{self.port}/v1/' + path,
                                         data=data, method=method, headers=headers)
        with self.http.open(request, timeout=timeout) as response:
            body = response.read((1 << 20) + 1)
        if len(body) > 1 << 20:
            raise ValueError('native response exceeds lifecycle quota')
        return json.loads(body) if body else None

    def drain(self, pipe, label):
        tail, size = b'', 0
        with (self.report / (label + '-private.log')).open('wb') as log:
            while chunk := pipe.read(4096):
                window = tail + chunk
                self.panic |= b'panicked at' in window or b'fatal runtime error:' in window
                tail = window[-64:]
                kept = chunk[:max(0, (64 << 10) - size)]
                log.write(kept)
                size += len(kept)
        pipe.close()

    def start(self, label):
        args = [str(self.binary), 'start', '--listen-addr', f'127.0.0.1:{self.port}',
                '--data-dir', str(self.root / 'state'), '--jwt-pub-key-path', str(self.root / 'public.pem'),
                '--jwt-priv-key-path', str(self.root / 'private.pem'), '--page_pool_max_size',
                '134217728', '--non-interactive']
        process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                   start_new_session=True, cwd=self.root,
                                   env={'PATH': '/usr/bin:/bin', 'HOME': str(self.root),
                                        'TMPDIR': str(self.root / 'tmp'), 'TOKIO_WORKER_THREADS': '2'})
        self.processes.append(process)
        reader = threading.Thread(target=self.drain, args=(process.stdout, label))
        self.readers.append(reader)
        reader.start()
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError('native runtime exited during startup')
            ready = False
            try:
                ready = self.request('health', timeout=1)['version'] == '2.11.0'
            except (urllib.error.URLError, TimeoutError):
                pass
            if ready:
                return process
            time.sleep(.1)
        raise TimeoutError('native startup deadline')

    def observe(self, token):
        prefix = 'database/' + DATABASE
        info = self.request(prefix, token=token)
        environment = self.request(prefix + '/environment', token=token)
        if info['initial_program'] != environment['module_hash']:
            raise ValueError('initial and active programs differ')
        rows = {}
        cases = [('schema_marker', 'SELECT id, revision FROM schema_marker', [[1, 'brine-recovery-v1']]),
                 ('sequence_state', 'SELECT id, sequence FROM sequence_state', [[1, 0]])]
        for name, query, expected in cases:
            rows[name] = self.request(prefix + '/sql?confirmed=true', query.encode(), 'POST', token)[0]['rows']
            if rows[name] != expected:
                raise ValueError('confirmed fixed fixture rows differ')
        return {'info': info, 'environment': environment, 'rows': rows,
                'schema': normalize_schema_declaration_order(self.request(prefix + '/schema?version=10', token=token)),
                'names': self.request(prefix + '/names', token=token)}

    def stop(self, process):
        process.send_signal(signal.SIGINT)
        code = process.wait(timeout=30)
        if code != 0:
            raise RuntimeError('native shutdown returned a nonzero exit')
        return {'exit': code, 'forced': False}

    def cleanup(self):
        for process in self.processes:
            if process.poll() is not None:
                continue
            os.killpg(process.pid, signal.SIGINT)
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
        for reader in self.readers:
            reader.join(timeout=5)
        if any(reader.is_alive() for reader in self.readers):
            raise RuntimeError('native log capture did not terminate')


def exercise(probe, wasm):
    root = probe.root
    result = {}
    (root / 'state').mkdir()
    (root / 'tmp').mkdir()
    (root / 'state/config.toml').write_text('[commitlog]\nmax-segment-size = 32768\nwrite-buffer-size = 4096\n[module-http]\nenabled = false\n[v8-heap-policy]\nheap-limit-mb = 64\n')
    subprocess.run(['openssl', 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-out', str(root / 'private.pem')], check=True, capture_output=True, timeout=15)
    subprocess.run(['openssl', 'pkey', '-in', str(root / 'private.pem'), '-pubout', '-out', str(root / 'public.pem')], check=True, capture_output=True, timeout=15)
    process = probe.start('fresh')
    owner = probe.request('identity', b'', 'POST')['token']
    created = probe.request('database/' + DATABASE + '?host_type=Wasm&policy=Compatible&num_replicas=1', wasm.read_bytes(), 'PUT', owner)
    if created['Success']['op'] != 'created':
        raise ValueError('native publication did not create the fresh fixture')
    before = probe.observe(owner)
    result['fresh_shutdown'] = probe.stop(process)
    process = probe.start('reopen')
    if probe.observe(owner) != before:
        raise ValueError('immediate reopen differs from confirmed state')
    result['reopen_shutdown'] = probe.stop(process)
    if probe.panic:
        raise RuntimeError('native panic marker observed')
    result['state_sha256'] = hashlib.sha256(json.dumps(before, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    result['status'] = 'PASS'
    return result


def run(args):
    require_isolation()
    binary, wasm = args.binary.resolve(), args.wasm.resolve()
    expected = args.wasm_sha256
    if sha256(binary) != args.binary_sha256 or sha256(wasm) != expected:
        raise ValueError('supplied executable or supplied v1 Wasm hash mismatch')
    args.report.mkdir(mode=0o700, parents=True, exist_ok=False)
    result = {'binary_sha256': args.binary_sha256, 'wasm_sha256': expected, 'loopback_only': True}
    with tempfile.TemporaryDirectory(prefix='brine-native-lifecycle-') as scratch:
        root = pathlib.Path(scratch)
        probe = Probe(binary, root, args.report)
        try:
            result.update(exercise(probe, wasm))
        except BaseException as error:
            result.update(status='FAIL', failure_type=type(error).__name__)
        finally:
            probe.cleanup()
            result['processes_reaped'] = all(process.poll() is not None for process in probe.processes)
            result['panic_marker'] = probe.panic
    result['scratch_removed'] = not root.exists()
    result['inputs_unchanged'] = sha256(binary) == args.binary_sha256 and sha256(wasm) == expected
    if result['panic_marker'] or not result['inputs_unchanged']:
        result['status'] = 'FAIL'
    (args.report / 'receipt.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))
    return 0 if result['status'] == 'PASS' else 1


def interrupt(signum, frame):
    raise KeyboardInterrupt


if __name__ == '__main__':
    os.umask(0o077)
    signal.signal(signal.SIGTERM, interrupt)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=pathlib.Path)
    parser.add_argument('wasm', type=pathlib.Path)
    parser.add_argument('report', type=pathlib.Path)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--wasm-sha256', required=True)
    raise SystemExit(run(parser.parse_args()))
