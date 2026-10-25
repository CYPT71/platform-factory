import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.parse

HERE = Path(__file__).parent / "assets"
def load(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module
trigger = load("trigger")
runner = load("runner")

class TriggerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "trigger.json"
        self.cfg = dict(version=1, url="https://gitlab.example", project=42, ref="main", events=["command.failed"])
        self.write()
        self.event = dict(id="incident-123", kind="command.failed", command="build")
        self.env = patch.dict(os.environ, {"PF_GITLAB_TRIGGER_TOKEN": "private-token", "CI": "false"})
        self.env.start()
        self.addCleanup(self.env.stop)
    def write(self):
        self.path.write_text(json.dumps(self.cfg))
        self.path.chmod(0o600)
    def test_request_and_secret_location(self):
        def send(request, timeout):
            self.assertEqual(timeout, 15)
            self.assertEqual(request.full_url, "https://gitlab.example/api/v4/projects/42/trigger/pipeline")
            self.assertNotIn("private-token", request.full_url)
            fields = urllib.parse.parse_qs(request.data.decode())
            self.assertEqual(fields["token"], ["private-token"])
            self.assertEqual(fields["variables[PF_EVENT_ID]"], ["incident-123"])
            response = io.BytesIO(b'{"id": 123}')
            response.status = 201
            return response
        self.assertTrue(trigger.trigger(str(self.path), self.event, send))
    def test_fail_closed_config(self):
        for url in ("http://gitlab.example", "https://user:pass@gitlab.example", "https://gitlab.example/path", "https://gitlab.example/?token=leak"):
            self.cfg["url"] = url
            self.write()
            with self.assertRaises(ValueError):
                trigger.trigger(str(self.path), self.event)
        self.path.chmod(0o644)
        with self.assertRaises(ValueError):
            trigger.trigger(str(self.path), self.event)
    def test_recursion_and_events(self):
        self.event["command"] = "ci"
        self.assertFalse(trigger.trigger(str(self.path), self.event))
        self.event["command"] = "build"
        self.event["kind"] = "command.started"
        self.assertFalse(trigger.trigger(str(self.path), self.event))
    def test_redirect_refused(self):
        with self.assertRaises(ValueError):
            trigger.NoRedirect().redirect_request(None, None, 302, "", {}, "https://other.example")
    def test_bad_event_and_response(self):
        self.event["id"] = "bad\nvalue"
        with self.assertRaises(ValueError):
            trigger.trigger(str(self.path), self.event)
        self.event["id"] = "valid"
        def send(request, timeout):
            response = io.BytesIO(b'{"id":123}')
            response.status = 403
            return response
        with self.assertRaises(ValueError):
            trigger.trigger(str(self.path), self.event, send)

class RunnerTests(unittest.TestCase):
    def test_traversal_and_invalid_config(self):
        with tempfile.TemporaryDirectory() as directory:
            old = os.getcwd()
            os.chdir(directory)
            try:
                for name in ("../config", "/tmp/config", "a/../config", "x\\y"):
                    with self.assertRaises(ValueError):
                        runner.run("pf", name, "build")
                Path("config").write_text('{"version":1,"unknown":true}')
                with self.assertRaises(ValueError):
                    runner.run("pf", "config", "build")
            finally:
                os.chdir(old)


class OrchestrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        previous = Path.cwd()
        os.chdir(self.tmp.name)
        self.addCleanup(os.chdir, previous)
        Path('boot').mkdir()
        Path('.platform-factory').mkdir()
        for name in ('pf.yaml', 'pf.lock', '.platform-factory/freeze.lock.json'):
            Path(name).write_text('{}')
        Path('boot/kernel').write_bytes(b'kernel')
        Path('boot/init').write_bytes(b'init')
        self.cfg = dict(version=1, project='pf.yaml', inputs_lock='pf.inputs.lock.json', layout='dist/oci', output='dist/microvm', platform='linux/amd64', kernel='boot/kernel', kernel_digest=runner.digest('boot/kernel'), init='boot/init', init_digest=runner.digest('boot/init'), ready='APPLICATION_READY')
        Path('pf.ci.json').write_text(json.dumps(self.cfg))
        self.selected = ['pf.yaml', 'pf.lock', '.platform-factory/freeze.lock.json', 'pf.ci.json', 'boot']
        self.write_lock()
        self.project = dict(profile='static', user='65532', include_project=False, output='dist/oci', platform='linux/amd64')
        self.commands = []
    def write_lock(self):
        Path('pf.inputs.lock.json').write_text(json.dumps(dict(paths=self.selected)))
    def call(self, argv, **kwargs):
        self.commands.append(argv[1:3])
        if argv[1:3] == ['project', 'show']:
            return type('Result', (), {'stdout': json.dumps(dict(config=self.project))})()
        if argv[1:3] == ['project', 'build']:
            Path('dist/oci').mkdir(parents=True)
        if argv[1:3] == ['ci', 'package']:
            Path('dist/microvm/initramfs.cpio.gz').write_bytes(b'packed-image')
            return type('Result', (), {'stdout': '{"manifest_digest":"sha256:example","rootfs_digest":"sha256:root"}'})()
        return type('Result', (), {'stdout': ''})()
    def test_three_phase_contract_and_evidence_tamper(self):
        with patch.object(runner.subprocess, 'run', self.call):
            for phase in ('build', 'package', 'boot'):
                runner.run('/trusted/pf', 'pf.ci.json', phase)
            evidence = json.loads(Path('dist/microvm/package.json').read_text())
            self.assertEqual(evidence['initramfs_digest'], runner.digest('dist/microvm/initramfs.cpio.gz'))
            self.assertTrue(json.loads(Path('dist/microvm/boot.json').read_text())['native_boot'])
            self.assertIn(['ci', 'boot'], self.commands)
            Path('dist/microvm/boot.json').unlink()
            evidence['inputs_lock_digest'] = 'wrong'
            Path('dist/microvm/package.json').write_text(json.dumps(evidence))
            with self.assertRaisesRegex(ValueError, 'evidence'):
                runner.run('/trusted/pf', 'pf.ci.json', 'boot')
    def test_modified_boot_inputs_fail_before_build(self):
        Path('boot/kernel').write_bytes(b'tampered')
        with patch.object(runner.subprocess, 'run', self.call):
            with self.assertRaisesRegex(ValueError, 'digest mismatch'):
                runner.run('/trusted/pf', 'pf.ci.json', 'build')
        self.assertNotIn(['project', 'build'], self.commands)
    def test_unlocked_config_and_output_overlap_rejected(self):
        self.selected.remove('pf.ci.json')
        self.write_lock()
        with self.assertRaisesRegex(ValueError, 'must be locked'):
            runner.run('/trusted/pf', 'pf.ci.json', 'build')
        self.selected.append('dist')
        self.write_lock()
        with self.assertRaisesRegex(ValueError, 'overlap'):
            runner.run('/trusted/pf', 'pf.ci.json', 'build')
    def test_rootful_or_extra_files_rejected(self):
        with patch.object(runner.subprocess, 'run', self.call):
            for key, value in [('user', '0'), ('profile', 'dynamic'), ('include', [{'source':'secrets'}])]:
                original = self.project.copy()
                self.project[key] = value
                with self.assertRaisesRegex(ValueError, 'distroless'):
                    runner.run('/trusted/pf', 'pf.ci.json', 'build')
                self.project = original
    def test_lock_changed_by_build_rejected(self):
        def malicious_build(argv, **kwargs):
            result = self.call(argv, **kwargs)
            if argv[1:3] == ['project', 'build']:
                Path('pf.inputs.lock.json').write_text('{}')
            return result
        with patch.object(runner.subprocess, 'run', malicious_build):
            with self.assertRaisesRegex(ValueError, 'lock changed'):
                runner.run('/trusted/pf', 'pf.ci.json', 'build')


if __name__ == "__main__":
    unittest.main()
