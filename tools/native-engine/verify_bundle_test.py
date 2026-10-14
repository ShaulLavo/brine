import importlib.util
import pathlib
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('verify_bundle', HERE / 'verify_bundle.py')
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)
HEAD = '1' * 40


class BundleTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix='native-engine-bundle-')
        self.addCleanup(self.scratch.cleanup)
        self.package = pathlib.Path(self.scratch.name)
        for arch in ('amd64', 'arm64'):
            bundle = self.package / arch
            bundle.mkdir()
            for name in verifier.FILES:
                source = HERE / name
                (bundle / name).write_bytes(source.read_bytes() if source.exists() else (arch + name).encode())
            (bundle / 'source-head').write_text(HEAD + '\n')
            self.manifest(bundle)

    def manifest(self, bundle):
        entries = [verifier.sha256(bundle / name) + '  ' + name for name in sorted(verifier.FILES)]
        (bundle / 'SHA256SUMS').write_text('\n'.join(entries) + '\n')

    def test_both_architectures_and_actual_digest_receipt(self):
        digest = 'sha256:' + '2' * 64
        receipt = verifier.verify(self.package, HEAD, digest)
        self.assertEqual(receipt['image'], 'ghcr.io/shaullavo/brine-native-engine@' + digest)
        for arch in ('amd64', 'arm64'):
            self.assertEqual(receipt['executable_sha256'][arch], verifier.sha256(self.package / arch / 'spacetimedb-standalone'))

    def test_wrong_head_or_tampered_executable_refuses(self):
        with self.assertRaises(ValueError):
            verifier.verify(self.package, '3' * 40)
        (self.package / 'arm64/spacetimedb-standalone').write_bytes(b'tampered')
        with self.assertRaises(ValueError):
            verifier.verify(self.package, HEAD)

    def test_rehashed_foreign_pins_or_patch_refuses(self):
        for name in ('pins.json', 'graceful-shutdown.patch'):
            with self.subTest(name=name):
                bundle = self.package / 'amd64'
                original = (bundle / name).read_bytes()
                (bundle / name).write_bytes(original + b' ')
                self.manifest(bundle)
                with self.assertRaises(ValueError):
                    verifier.verify(self.package, HEAD)
                (bundle / name).write_bytes(original)
                self.manifest(bundle)

    def test_missing_duplicate_or_unexpected_manifest_entry_refuses(self):
        path = self.package / 'arm64/SHA256SUMS'
        original = path.read_text()
        for content in (original.split('\n', 1)[1], original + original.splitlines()[0] + '\n',
                        original + '0' * 64 + '  ../outside\n'):
            path.write_text(content)
            with self.assertRaises(ValueError):
                verifier.verify(self.package, HEAD)

    def test_missing_actual_digest_refuses(self):
        for digest in ('', 'latest', 'sha256:bad'):
            with self.assertRaises(ValueError):
                verifier.verify(self.package, HEAD, digest)


if __name__ == '__main__':
    unittest.main()
