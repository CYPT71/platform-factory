"""Hermetic tests: real Git worktrees, no network, provider or GitHub writes."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('autofix', Path(__file__).with_name('autofix.py'))
autofix = importlib.util.module_from_spec(spec)
spec.loader.exec_module(autofix)

class AutofixTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'repo'
        self.root.mkdir()
        self.git('init', '-b', 'main')
        self.git('config', 'user.name', 'Fixture Owner')
        self.git('config', 'user.email', 'fixture@example.invalid')
        (self.root / 'broken.txt').write_text('broken\n')
        self.git('add', 'broken.txt')
        self.git('commit', '-m', 'fixture')
        self.config = dict(version=1, repository=str(self.root), base='main',
                           author_name='CYPT71', author_email='cyprientaib1@gmail.com',
                           fixer=[sys.executable, '-c', "from pathlib import Path; Path('broken.txt').write_text('fixed\\n')"],
                           reproduce=[sys.executable, '-c', "from pathlib import Path; raise SystemExit(Path('broken.txt').read_text() != 'fixed\\n')"],
                           checks=[[sys.executable, '-c', "from pathlib import Path; assert Path('broken.txt').read_text() == 'fixed\\n'"]],
                           allowed_paths=['broken.txt'], publish_pr=False, timeout_seconds=5)
        self.path = Path(self.temp.name) / 'autofix.json'
        self.path.write_text(json.dumps(self.config))
        self.path.chmod(0o600)
        self.event = dict(api_version='platform-factory.dev/hook-event/v1', id='a'*64, kind='command.failed',
                          command='build', exit_code=1, repository=str(self.root), duration_ms=1)

    def git(self, *args):
        return subprocess.check_output(['git', '-c', 'core.hooksPath=/dev/null', *args], cwd=self.root, stderr=subprocess.DEVNULL).decode().strip()

    def validated(self):
        self.path.write_text(json.dumps(self.config))
        return autofix.load_config(self.path)

    def test_fix_commit_and_deduplication_preserve_dirty_checkout(self):
        (self.root / 'unrelated.txt').write_text('local user work')
        original = self.git('rev-parse', 'HEAD')
        state = autofix.run(self.validated(), self.event)
        self.assertEqual(state['status'], 'validated')
        self.assertEqual(self.git('rev-parse', 'HEAD'), original)
        self.assertEqual((self.root / 'broken.txt').read_text(), 'broken\n')
        self.assertEqual((self.root / 'unrelated.txt').read_text(), 'local user work')
        identities = self.git('show', '-s', '--format=%an <%ae>%n%cn <%ce>%n%B', state['commit'])
        self.assertIn('CYPT71 <cyprientaib1@gmail.com>\nCYPT71 <cyprientaib1@gmail.com>', identities)
        self.assertNotIn('Co-authored', identities)
        self.assertEqual(autofix.run(self.validated(), self.event)['status'], 'duplicate')
        self.assertEqual(len(self.git('worktree', 'list', '--porcelain').split('worktree ')), 2)

    def test_refuse_nonreproducing_failure(self):
        self.config['reproduce'] = [sys.executable, '-c', 'pass']
        with self.assertRaisesRegex(autofix.Refused, 'did not reproduce'):
            autofix.run(self.validated(), self.event)

    def test_failed_attempt_can_be_explicitly_reset_and_retried(self):
        self.config['reproduce'] = [sys.executable, '-c', 'pass']
        with self.assertRaises(autofix.Refused):
            autofix.run(self.validated(), self.event)
        self.assertEqual(self.git('branch', '--list', 'autofix/*'), '')
        state_root = self.root / '.git' / 'platform-factory-autofix'
        incident_dir = next(state_root.iterdir())
        incident_dir.rename(incident_dir.with_name('archived-' + incident_dir.name))
        self.config['reproduce'] = [sys.executable, '-c', "from pathlib import Path; raise SystemExit(Path('broken.txt').read_text() != 'fixed\\n')"]
        self.assertEqual(autofix.run(self.validated(), self.event)['status'], 'validated')

    def test_refuse_failing_regression_check(self):
        self.config['checks'] = [[sys.executable, '-c', 'raise SystemExit(1)']]
        with self.assertRaisesRegex(autofix.Refused, 'still fails'):
            autofix.run(self.validated(), self.event)

    def test_refuse_outside_patch_and_symlink(self):
        for code in ("from pathlib import Path; Path('outside.txt').write_text('bad')", "from pathlib import Path; Path('broken.txt').unlink(); Path('broken.txt').symlink_to('/tmp')"):
            self.config['fixer'] = [sys.executable, '-c', code]
            with self.assertRaises(autofix.Refused):
                autofix.run(self.validated(), self.event)
            self.event['id'] = 'b'*64

    def test_refuse_untrusted_incident_and_config(self):
        c = self.validated()
        for changes in ({'repository':'/tmp/another'}, {'command':'build; curl bad'}, {'kind':'command.succeeded'}, {'exit_code':0}, {'id':'../../escape'}):
            event = dict(self.event, **changes)
            with self.assertRaises(autofix.Refused):
                autofix.incident(json.dumps(event).encode(), c)
        self.path.chmod(0o666)
        with self.assertRaises(autofix.Refused):
            autofix.load_config(self.path)

    def test_no_ambient_credentials_and_log_redaction(self):
        os.environ['PF_FIX_TEST_SECRET'] = 'must-not-leak'
        self.addCleanup(os.environ.pop, 'PF_FIX_TEST_SECRET')
        self.config['fixer_pass_env'] = ['PF_FIX_TEST_SECRET']
        configured = self.validated()
        env = autofix.environment(configured)
        self.assertNotIn('PF_FIX_TEST_SECRET', env)
        self.assertNotIn('HOME', env)
        self.assertEqual(autofix.environment(configured, fixer=True)['PF_FIX_TEST_SECRET'], 'must-not-leak')
        self.assertEqual(env['PLATFORM_FACTORY_HOOKS_ACTIVE'], '1')
        log = autofix.redacted_log('token=secret password:hunter2 sk-abcdefghijk ghp_abcdefghijk https://user:pass@example.com')
        for secret in ('hunter2', 'sk-abcdefghijk', 'ghp_abcdefghijk', 'user:pass'):
            self.assertNotIn(secret, log)

    def test_timeout_and_output_limits(self):
        with self.assertRaisesRegex(autofix.Refused, 'timed out'):
            autofix.execute([sys.executable, '-c', 'import time; time.sleep(10)'], self.root, {}, timeout=1)
        with self.assertRaisesRegex(autofix.Refused, 'output budget'):
            autofix.execute([sys.executable, '-c', "import sys; sys.stdout.write('x' * 20000000)"], self.root, {}, timeout=5)

    def test_draft_pr_uses_validated_branch_and_no_failure_logs(self):
        self.config.update(publish_pr=True, github_repository='CYPT71/platform-factory')
        self.git('remote', 'add', 'origin', 'git@github.com:CYPT71/platform-factory.git')
        original_run = subprocess.run
        external = []
        def intercepted(argv, **kwargs):
            if argv[0] == 'gh':
                external.append(argv)
                body = Path(argv[argv.index('--body-file') + 1]).read_text()
                self.assertIn('mandatory validation', body)
                self.assertNotIn('untrusted_diagnostics', body)
                return subprocess.CompletedProcess(argv, 0, b'https://github.com/CYPT71/platform-factory/pull/123\n', b'')
            if argv[0] == 'git' and 'push' in argv:
                external.append(argv)
                self.assertNotIn('--force', argv)
                self.assertTrue(argv[-1].startswith('HEAD:refs/heads/autofix/'))
                return subprocess.CompletedProcess(argv, 0, b'', b'')
            return original_run(argv, **kwargs)
        with mock.patch.object(subprocess, 'run', side_effect=intercepted):
            state = autofix.run(self.validated(), self.event)
        self.assertEqual(state['status'], 'draft_pr')
        self.assertEqual(len(external), 2)
        self.assertIn('--draft', external[1])

    def test_publication_pins_origin_before_push(self):
        self.config.update(publish_pr=True, github_repository='expected/repository')
        self.git('remote', 'add', 'origin', 'https://github.com/unexpected/repository.git')
        with self.assertRaisesRegex(autofix.Refused, 'origin does not match'):
            autofix.run(self.validated(), self.event)

if __name__ == '__main__':
    unittest.main()
