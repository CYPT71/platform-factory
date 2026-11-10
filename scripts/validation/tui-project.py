#!/usr/bin/env python3
"""Exercise project build confirmation in a real POSIX PTY, without downloads."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import pty
import select
import shutil
import signal
import struct
import subprocess
import tempfile
import termios
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pf', type=Path, required=True)
    parser.add_argument('--binary', type=Path, required=True, help='Prebuilt Linux ARM64 fixture')
    parser.add_argument('--results', type=Path, required=True)
    args = parser.parse_args()
    args.pf, args.binary = args.pf.resolve(strict=True), args.binary.resolve(strict=True)
    args.results.mkdir(parents=True, exist_ok=False)
    report = {'host': platform.platform(), 'pf_sha256': hashlib.sha256(args.pf.read_bytes()).hexdigest(),
              'scope': 'Prepared compiled fixture, 40x20 PTY, keyboard and NO_COLOR; not a novice study', 'trials': []}
    for scenario in ['escape', 'interrupt', 'invalid-and-retry']:
        work = Path(tempfile.mkdtemp(prefix='pf-tui-'))
        master, slave = pty.openpty()
        process = None
        raw = bytearray()
        trial = {'scenario': scenario, 'passed': False, 'sent_keys_hex': []}
        try:
            shutil.copy2(args.binary, work / 'service')
            (work / 'pf.yaml').write_text('language: compiled\nartifact: service\nplatform: linux/arm64\nimage: local/tui-fixture\n')
            initial = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in work.iterdir()}
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 20, 40, 0, 0))

            def child_setup():
                os.setsid()
                fcntl.ioctl(0, termios.TIOCSCTTY, 0)

            process = subprocess.Popen([str(args.pf), 'project', 'build'], cwd=work,
                                       env={**os.environ, 'TERM': 'xterm-256color', 'NO_COLOR': '1'},
                                       stdin=slave, stdout=slave, stderr=slave, preexec_fn=child_setup)
            os.close(slave)
            slave = None
            started = time.monotonic()

            def send(keys):
                trial['sent_keys_hex'].append(keys.hex())
                os.write(master, keys)

            def receive_until(needle=None, timeout=15):
                deadline = time.monotonic() + timeout
                while time.monotonic() < deadline:
                    readable, _, _ = select.select([master], [], [], .1)
                    if readable:
                        try:
                            chunk = os.read(master, 65536)
                        except OSError:
                            break
                        if not chunk:
                            break
                        raw.extend(chunk)
                        # Answer terminal probes locally; never query any external terminal.
                        if b'\x1b[6n' in chunk:
                            os.write(master, b'\x1b[1;1R')
                        if b'\x1b]11;' in chunk:
                            os.write(master, b'\x1b]11;rgb:0000/0000/0000\x1b\\')
                    if needle and needle in raw:
                        return
                    if process.poll() is not None and not readable:
                        break
                if needle and needle not in raw:
                    raise RuntimeError('Expected terminal prompt not observed: ' + repr(needle))

            receive_until(b'Confirm image reference')
            trial['prompt_seconds'] = round(time.monotonic() - started, 4)
            if scenario == 'invalid-and-retry':
                send(b'\x01\x0b\r')  # Ctrl+A, Ctrl+K, Enter: empty reference.
                receive_until(b'image must be')
                if (work / '.platform-factory/image').exists():
                    raise RuntimeError('Invalid reference produced an image')
                send(b'local/pty-api\r')
            else:
                send(b'\t')  # Exercise keyboard focus before cancellation.
                time.sleep(.15)
                send(b'\x1b' if scenario == 'escape' else b'\x03')
            receive_until(timeout=30)
            code = process.wait(timeout=5)
            trial['exit_code'] = code
            trial['seconds'] = round(time.monotonic() - started, 4)
            for name, checksum in initial.items():
                if hashlib.sha256((work / name).read_bytes()).hexdigest() != checksum:
                    raise RuntimeError('Input changed: ' + name)
            if scenario == 'invalid-and-retry':
                if code != 0:
                    raise RuntimeError('Corrected reference did not build')
                result = subprocess.run([str(args.pf), 'verify', str(work / '.platform-factory/image')],
                                        capture_output=True, text=True, timeout=30)
                trial['verification'] = {'exit_code': result.returncode, 'stdout': result.stdout, 'stderr': result.stderr}
                if result.returncode or b'local/pty-api:latest' not in raw:
                    raise RuntimeError('Verified image does not match confirmed reference')
            elif code == 0 or (work / '.platform-factory').exists():
                raise RuntimeError('Cancellation reported success or wrote build state')
            trial['root_entries'] = sorted(p.name for p in work.iterdir())
            trial['passed'] = True
        except Exception as error:
            trial['error'] = str(error)
        finally:
            if process is not None and process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            if slave is not None:
                os.close(slave)
            os.close(master)
            shutil.rmtree(work)
            trial['owned_workspace_removed'] = not work.exists()
            (args.results / (scenario + '.terminal')).write_bytes(raw)
            report['trials'].append(trial)
            (args.results / 'report.json').write_text(json.dumps(report, indent=2))
        print(scenario, 'PASS' if trial['passed'] else 'FAIL', trial.get('error', ''), flush=True)
    return 0 if all(t['passed'] and t['owned_workspace_removed'] for t in report['trials']) else 1


if __name__ == '__main__':
    raise SystemExit(main())
