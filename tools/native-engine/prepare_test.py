import importlib.util
import pathlib
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('prepare', HERE / 'prepare.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


def add_member(writer, path, source):
    info = writer.gettarinfo(str(path), 'SpacetimeDB-fixture/' + str(path.relative_to(source)))
    info.mtime = 1
    if not path.is_file():
        writer.addfile(info)
        return
    with path.open('rb') as data:
        writer.addfile(info, data)


def create_archive(source, archive):
    with tarfile.open(archive, 'w:gz') as writer:
        for path in source.rglob('*'):
            add_member(writer, path, source)


class PreparationTest(unittest.TestCase):
    def test_old_archive_mtimes_cannot_reuse_stale_cargo_objects(self):
        with tempfile.TemporaryDirectory(prefix='native-engine-prepare-') as scratch:
            root = pathlib.Path(scratch)
            source = root / 'SpacetimeDB-fixture'
            (source / 'src').mkdir(parents=True)
            (source / 'src/main.rs').write_text('before\n')
            (source / 'Cargo.lock').write_text('pinned lock\n')
            (source / 'LICENSE.txt').write_text('fixture license\n')
            archive = root / 'source.tar.gz'
            create_archive(source, archive)
            inputs = root / 'inputs'
            inputs.mkdir()
            for name in ('pins.json', 'Dockerfile'):
                (inputs / name).write_text('fixture\n')
            (inputs / 'graceful-shutdown.patch').write_text('--- a/src/main.rs\n+++ b/src/main.rs\n@@ -1 +1 @@\n-before\n+after\n')
            pins = {'upstream_commit': 'fixture', 'archive_sha256': prepare.file_sha256(archive),
                    'patch_sha256': prepare.file_sha256(inputs / 'graceful-shutdown.patch')}
            started_ns = time.time_ns()
            with patch.object(prepare, 'HERE', inputs), patch.object(prepare, 'PINS', pins):
                upstream = prepare.prepare(root / 'output', archive)
            self.assertEqual((upstream / 'src/main.rs').read_text(), 'after\n')
            self.assertEqual((upstream / 'Cargo.lock').read_text(), 'pinned lock\n')
            self.assertGreaterEqual((upstream / 'src/main.rs').stat().st_mtime_ns, started_ns)
            self.assertGreaterEqual((upstream / 'Cargo.lock').stat().st_mtime_ns, started_ns)


if __name__ == '__main__':
    unittest.main()
