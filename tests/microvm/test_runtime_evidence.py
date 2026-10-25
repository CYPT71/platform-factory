import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

script = Path(__file__).with_name('capture-runtime-evidence.py')
spec = importlib.util.spec_from_file_location('capture_runtime', script)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RuntimeEvidenceTests(unittest.TestCase):
    def test_real_delete_can_remove_state_without_losing_terminal_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state_root = root / 'state'
            state_root.mkdir()
            evidence = root / 'evidence'
            evidence.mkdir()
            identity = 'a' * 64
            record = dict(status='stopped', exitStatus=1, metrics={'exit_status': 1})
            (state_root / (identity + '.json')).write_text(json.dumps(record))
            (state_root / (identity + '.supervisor.log')).write_text('guest output')
            runtime = root / 'runtime'
            runtime.write_text('#!' + sys.executable + '\nimport pathlib,sys\np=pathlib.Path(sys.argv[2])\nfor f in p.iterdir(): f.unlink()\nraise SystemExit(7)\n')
            runtime.chmod(0o700)
            result = subprocess.run([sys.executable, str(script), '--runtime', str(runtime),
                                     '--evidence', str(evidence), '--', '--root', str(state_root), 'delete', identity])
            self.assertEqual(result.returncode, 7)
            self.assertEqual(list(state_root.iterdir()), [])
            self.assertEqual(json.loads((evidence / 'podman-microvm-terminal-state.json').read_text()), record)
            self.assertEqual((evidence / 'podman-microvm-supervisor-final.log').read_text(), 'guest output')

    def test_live_or_unrelated_commands_do_not_supply_terminal_proof(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            identity = 'b' * 64
            (root / (identity + '.json')).write_text('{"status":"running"}')
            for args in (['--root=' + directory, 'state', identity],
                         ['--root=' + directory, 'delete', identity],
                         ['delete', '../escape']):
                module.capture(args, root, os.environ)
            self.assertFalse((root / 'podman-microvm-terminal-state.json').exists())


if __name__ == '__main__':
    unittest.main()
