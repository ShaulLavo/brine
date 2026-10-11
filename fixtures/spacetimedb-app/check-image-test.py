import importlib.util
import json
import pathlib
import socket
import socketserver
import struct
import threading
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('check_image', pathlib.Path(__file__).with_name('check-image.py'))
image = importlib.util.module_from_spec(spec)
spec.loader.exec_module(image)

RUNNING = {'Running': True, 'Status': 'running', 'ExitCode': 0, 'OOMKilled': False}


class HealthHandler(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.recv(4096)
        self.server.attempts += 1
        if self.server.always_reset or self.server.attempts == 1:
            self.request.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack('ii', 1, 0))
            return
        self.request.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 12\r\nConnection: close\r\n\r\n{"ok":true}\n')


class ImageReadinessTest(unittest.TestCase):
    def setUp(self):
        self.server = socketserver.TCPServer(('127.0.0.1', 0), HealthHandler)
        self.server.attempts = 0
        self.server.always_reset = False
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': .01})
        self.thread.start()
        self.base = 'http://127.0.0.1:' + str(self.server.server_address[1]) + '/'

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=1)
        self.server.server_close()

    def docker(self, *args):
        if args[0] == 'inspect':
            return json.dumps([{'State': RUNNING}])
        self.assertEqual(args, ('logs', '--tail', '20', 'fixture-container'))
        return 'fixture log receipt'

    def test_reset_then_actual_health_succeeds(self):
        with patch.object(image, 'docker', self.docker):
            image.wait_http(self.base, 'fixture-container', timeout=1)
        self.assertEqual(self.server.attempts, 2)

    def test_persistent_resets_fail_at_deadline_with_state(self):
        self.server.always_reset = True
        started = time.monotonic()
        with patch.object(image, 'docker', self.docker):
            with self.assertRaisesRegex(RuntimeError, 'state=.*running.*fixture log receipt'):
                image.wait_http(self.base, 'fixture-container', timeout=.2)
        self.assertLess(time.monotonic() - started, .5)
        self.assertGreaterEqual(self.server.attempts, 2)

    def test_stopped_container_fails_without_http_retry(self):
        stopped = dict(RUNNING, Running=False, Status='exited', ExitCode=1)
        with patch.object(image, 'container_state', return_value=stopped), patch.object(image, 'docker', self.docker):
            with self.assertRaisesRegex(RuntimeError, 'exited.*ExitCode.*1'):
                image.wait_http(self.base, 'fixture-container')
        self.assertEqual(self.server.attempts, 0)

    def test_wrong_health_is_terminal(self):
        with patch.object(image.urllib.request, 'urlopen') as request:
            request.return_value.__enter__.return_value.status = 200
            request.return_value.__enter__.return_value.read.return_value = b'wrong health'
            with self.assertRaises(AssertionError):
                image.health_ready(self.base, 1)
        request.assert_called_once()

    def test_http_error_is_terminal(self):
        error = image.urllib.error.HTTPError(self.base, 500, 'failed', {}, None)
        with patch.object(image.urllib.request, 'urlopen', side_effect=error) as request:
            with self.assertRaises(image.urllib.error.HTTPError):
                image.health_ready(self.base, 1)
        error.close()
        request.assert_called_once()


if __name__ == '__main__':
    unittest.main()
