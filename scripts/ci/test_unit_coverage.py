from pathlib import Path
import subprocess
import tempfile
import unittest


class UnitCoverageScopeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.binary = str(Path(cls.temporary.name) / 'coverage-scope')
        subprocess.run(['go', 'build', '-o', cls.binary, 'scripts/ci/unit-coverage.go'], check=True)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def run_filter(self, root, blocks):
        profile = root / 'coverage.out'
        profile.write_text('mode: set\n' + blocks)
        return subprocess.run([self.binary, '-root', str(root), '-profile', str(profile)], capture_output=True, text=True)

    def test_entrypoint_is_excluded_but_application_logic_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'cmd/demo/main.go'
            source.parent.mkdir(parents=True)
            source.write_text('package main\n\nfunc main() {\n helper()\n}\n\nfunc helper() {\n println("product behavior")\n}\n')
            prefix = 'github.com/CYPT71/platform-factory/cmd/demo/main.go:'
            entry = prefix + '3.13,5.2 1 0\n'
            behavior = prefix + '7.15,9.2 1 1\n'
            result = self.run_filter(root, entry + behavior)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, 'mode: set\n' + behavior)

    def test_exec_helper_is_excluded_but_permission_logic_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'internal/plugin/sandbox_linux.go'
            source.parent.mkdir(parents=True)
            source.write_text('package plugin\n\nfunc execPlugin() {\n panic("exec helper")\n}\n\nfunc hostNetworkGranted() bool {\n return false\n}\n')
            prefix = 'github.com/CYPT71/platform-factory/internal/plugin/sandbox_linux.go:'
            helper = prefix + '3.19,5.2 1 0\n'
            policy = prefix + '7.32,9.2 1 1\n'
            result = self.run_filter(root, helper + policy)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, 'mode: set\n' + policy)

    def test_missing_or_invalid_source_and_malformed_profiles_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for blocks in ('malformed\n', 'external/module/file.go:1.1,2.1 1 0\n',
                           'github.com/CYPT71/platform-factory/cmd/missing.go:1.1,2.1 1 0\n'):
                result = self.run_filter(root, blocks)
                self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
