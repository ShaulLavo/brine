import hashlib
import importlib.util
import json
import pathlib
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parent


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, HERE / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


package = load('package_variant', 'package-variant.py')
image = load('check_image', 'check-image.py')


class VariantTest(unittest.TestCase):
    def setUp(self):
        self.browsers = {'v1': b'base browser', 'default-column': b'default browser'}
        self.manifest = {'serving_variant': 'default-column', 'modules': {'v1': 'base module', 'default-column': 'default module'},
                         'clients': {variant: {'browser_sha256': hashlib.sha256(browser).hexdigest(),
                                              'module_sha256': 'base module' if variant == 'v1' else 'default module'}
                                     for variant, browser in self.browsers.items()}}

    def test_default_selection_binds_its_own_browser_and_module(self):
        client = package.serving_client(self.manifest, 'default-column')
        self.assertEqual(client['module_sha256'], 'default module')
        self.assertEqual(client['browser_sha256'], hashlib.sha256(self.browsers['default-column']).hexdigest())
        self.assertNotEqual(client, package.serving_client(self.manifest, 'v1'))

    def test_wrong_pair_or_unsupported_selection_refuses(self):
        self.manifest['clients']['default-column']['module_sha256'] = 'base module'
        with self.assertRaises(ValueError):
            package.serving_client(self.manifest, 'default-column')
        with self.assertRaises(ValueError):
            package.serving_client(self.manifest, 'destructive')

    def response(self, path, timeout):
        from io import BytesIO
        if path.endswith('config.json'):
            return BytesIO(json.dumps({'endpoint': 'https://fixture.example/', 'database': 'recovery-fixture'}).encode())
        return BytesIO(self.served)

    def test_served_default_client_passes_and_v1_substitution_refuses(self):
        self.served = self.browsers['default-column']
        with patch.object(image.urllib.request, 'urlopen', side_effect=self.response):
            evidence = image.inspect_http('http://127.0.0.1/', 'amd64', self.manifest)
            self.assertEqual(evidence['serving_variant'], 'default-column')
            self.served = self.browsers['v1']
            with self.assertRaises(AssertionError):
                image.inspect_http('http://127.0.0.1/', 'amd64', self.manifest)


if __name__ == '__main__':
    unittest.main()
