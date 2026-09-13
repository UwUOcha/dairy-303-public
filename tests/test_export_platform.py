import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('export_platform', ROOT / 'scripts/export-platform.py')
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


class ExportTests(unittest.TestCase):
    def test_export_rewrite_and_replace_protect_local_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / 'public'
            exporter.export(out, module='example.org/university/schedule')
            self.assertIn('module example.org/university/schedule', (out / 'go.mod').read_text())
            self.assertIn('example.org/university/schedule/internal/', (out / 'cmd/raspd/main.go').read_text())
            for name in ['adapters', 'local', 'run', 'bin', 'docs/archive', 'profiles/kgu.json', 'cmd/admind', 'scripts/pipeline', 'adapters/kgu/ops', '.github/workflows/ci.yml']:
                self.assertFalse((out / name).exists(), name)
            self.assertNotIn('tksu', (out / '.gitignore').read_text())
            self.assertTrue((out / 'LICENSE').is_file())
            self.assertIn('MIT License', (out / 'pkg/provider/LICENSE').read_text())
            self.assertIn('https://github.com/UwUOcha/dairy-303-public', (out / 'internal/buildinfo/info.go').read_text())
            exporter.export(out, replace=True)
            readme = out / 'README.md'
            readme.write_text('local changes')
            with self.assertRaisesRegex(ValueError, 'changed'):
                exporter.export(out, replace=True)
            self.assertEqual(readme.read_text(), 'local changes')

    def test_refuses_unmanaged_directories_and_source_symlinks(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / 'public'
            out.mkdir()
            with self.assertRaisesRegex(ValueError, 'not a managed'):
                exporter.export(out, replace=True)
            original = exporter.ROOT
            fake = Path(tmp) / 'source'
            fake.mkdir()
            (fake / 'go.mod').symlink_to(ROOT / 'go.mod')
            exporter.ROOT = fake
            try:
                with self.assertRaisesRegex(ValueError, 'Symlink'):
                    list(exporter.sources())
            finally:
                exporter.ROOT = original

    def test_no_untracked_go_packages_in_ignored_runtime(self):
        self.assertEqual(list((ROOT / 'run').rglob('*.go')), [])


if __name__ == '__main__':
    unittest.main()
