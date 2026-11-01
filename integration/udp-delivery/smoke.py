import contextlib
import json
import os
import pathlib
import platform
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request


def interrupted(signum, _frame):
    raise SystemExit(128 + signum)


def unused_port(kind):
    with socket.socket(socket.AF_INET, kind) as connection:
        connection.bind(('127.0.0.1', 0))
        return connection.getsockname()[1]


def stop(process, http_port, udp_port):
    if process.poll() is None:
        process.terminate()
    try:
        process.wait(timeout=4)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=1)
        raise AssertionError('fixture ignored the shutdown bound')
    for kind, port in [(socket.SOCK_STREAM, http_port), (socket.SOCK_DGRAM, udp_port)]:
        with socket.socket(socket.AF_INET, kind) as connection:
            connection.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            connection.bind(('127.0.0.1', port))


def http(port, path, expected_status):
    request = urllib.request.Request(f'http://127.0.0.1:{port}{path}')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        response = opener.open(request, timeout=.3)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        assert response.status == expected_status
        body = response.read(2049)
        assert len(body) <= 2048
        return json.loads(body)


def start(stack, binary, failed_health=False):
    http_port = unused_port(socket.SOCK_STREAM)
    udp_port = unused_port(socket.SOCK_DGRAM)
    environment = dict(os.environ, UDP_ECHO_HTTP_ADDR=f'127.0.0.1:{http_port}',
                       UDP_ECHO_UDP_ADDR=f'127.0.0.1:{udp_port}',
                       UDP_ECHO_FAIL_HEALTH=str(failed_health).lower())
    process = subprocess.Popen([str(binary)], env=environment, stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL)
    stack.callback(stop, process, http_port, udp_port)
    deadline = time.monotonic() + 3
    status = 503 if failed_health else 200
    while time.monotonic() < deadline:
        assert process.poll() is None, 'fixture exited before HTTP startup'
        try:
            return http_port, udp_port, http(http_port, '/healthz', status)
        except (OSError, urllib.error.URLError):
            time.sleep(.02)
    raise AssertionError('fixture HTTP startup exceeded three seconds')


def probe(binary, port, release, expected_outcome):
    result = subprocess.run([str(binary), '-endpoint', f'127.0.0.1:{port}',
                             '-release', release, '-vantage', 'local-subprocess',
                             '-attempts', '2', '-attempt-timeout', '150ms',
                             '-overall-timeout', '500ms'],
                            capture_output=True, check=False, timeout=2)
    assert len(result.stdout) <= 2048 and result.stderr == b''
    receipt = json.loads(result.stdout)
    assert receipt['outcome'] == expected_outcome, receipt
    assert result.returncode == (0 if expected_outcome == 'ok' else 1)
    assert receipt['endpoint'] == f'127.0.0.1:{port}'
    assert receipt['vantage'] == 'local-subprocess'
    assert receipt['started_at'] and receipt['finished_at'] and receipt['local_address']
    return receipt


def main():
    if os.geteuid() == 0:
        raise RuntimeError('run the local fixture as a non-root user')
    release_a, release_b, evidence = map(pathlib.Path, sys.argv[1:])
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}[platform.machine()]
    client = release_a / arch / 'udp-echo-client'
    receipts = {'execution': 'native-local-subprocess', 'architecture': arch,
                'uid': os.geteuid(), 'brine_remote_readiness': 'unverified'}
    with contextlib.ExitStack() as stack:
        a_http, a_udp, a_health = start(stack, release_a / arch / 'udp-echo-server')
        b_http, b_udp, b_health = start(stack, release_b / arch / 'udp-echo-server')
        assert a_health['release'] == 'release-a' and b_health['release'] == 'release-b'
        receipts['release-a'] = probe(client, a_udp, 'release-a', 'ok')
        receipts['release-b'] = probe(client, b_udp, 'release-b', 'ok')
        receipts['mismatch'] = probe(client, b_udp, 'release-a', 'protocol_error')
        receipts['stats-a'] = http(a_http, '/stats', 200)
        receipts['stats-b'] = http(b_http, '/stats', 200)
        assert receipts['stats-a']['stats']['replied'] >= 1
        assert receipts['stats-b']['stats']['replied'] >= 2
        failed_http, failed_udp, failed_health = start(stack, release_b / arch / 'udp-echo-server', True)
        assert failed_health['ok'] is False and failed_health['release'] == 'release-b'
        receipts['failed-health'] = http(failed_http, '/healthz', 503)
        receipts['failed-health-udp'] = probe(client, failed_udp, 'release-b', 'ok')
    receipts['cleanup'] = 'all fixture processes reaped and their TCP/UDP ports rebound'
    (evidence / 'subprocess-evidence.json').write_text(json.dumps(receipts, indent=2) + '\n')
    print('Native non-root HTTP/UDP subprocess checks and cleanup passed.')


if __name__ == '__main__':
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    main()
