#!/usr/bin/env python3
"""Real systemd lifecycle regression, only in a fresh disposable Linux account.

Uses the candidate native CLI, a real user service, and an offline daemon fixture.
No model or email requests. Reboot acceptance remains a separate VM check.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import signal
import socket
import subprocess
import time
import uuid

UNIT = 'dearmachine-concierge.service'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--leave-running', action='store_true', help='Leave the successful fixture enabled for a separate VM reboot check')
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    home = Path.home()
    root = home / '.dearmachine'
    unit = home / '.config/systemd/user' / UNIT
    launcher = home / '.local/bin/dearmachine'
    if os.getuid() == 0 or root.exists() or unit.exists() or launcher.exists():
        raise SystemExit('Requires an ordinary disposable account without Dear Machine state.')
    subprocess.run(['systemctl', '--user', 'show-environment'], check=True, stdout=subprocess.DEVNULL)
    loaded = subprocess.check_output(['systemctl', '--user', 'show', UNIT, '-p', 'LoadState', '--value'], text=True).strip()
    if loaded != 'not-found':
        raise SystemExit('Refusing an existing service.')

    def cli(*args, check=True):
        result = subprocess.run([str(binary), *args], capture_output=True, text=True, timeout=35, cwd=home)
        if check and result.returncode:
            raise AssertionError(f'{args}: {result.stderr}')
        return result

    def request(command):
        with socket.socket(socket.AF_UNIX) as sock:
            sock.settimeout(5)
            sock.connect(str(root / 'run/supervisor.sock'))
            sock.sendall(json.dumps({'version': 1, 'command': command}).encode() + b'\n')
            with sock.makefile('r') as stream:
                result = json.loads(stream.readline())
        assert result['ok'], result
        return result['status']

    def wait(predicate):
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except (OSError, ValueError):
                pass
            time.sleep(.1)
        raise AssertionError('Timed out waiting for lifecycle state')

    root.mkdir(mode=0o700)
    inbox = str(uuid.uuid4())
    (root / 'pairs.toml').write_text(f'''version = 2
[[inboxes]]
id = "{inbox}"
transport = "agentmail"
provider_id = "fixture"
address = "machine@example.invalid"
[[pairs]]
id = "{uuid.uuid4()}"
user_email = "user@example.invalid"
inbox_id = "{inbox}"
''')
    (root / 'pairs.toml').chmod(0o600)
    child = home / 'offline-daemon.py'
    child.write_text('''import os,signal,time
from pathlib import Path
root=Path.home()/'.dearmachine/run'
root.mkdir(parents=True,exist_ok=True)
paths=[root/name for name in ('dearmachine.pid','dearmachine.ready')]
for path in paths:path.write_text(str(os.getpid()))
def stop(*args):raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
signal.signal(signal.SIGINT,stop)
try:
 while True:time.sleep(.1)
finally:
 for path in paths:
  if path.exists() and path.read_text()==str(os.getpid()):path.unlink()
''')
    launcher.parent.mkdir(parents=True)
    launcher.write_text('#!/bin/sh\nif [ "${1:-}" = up ] && [ "${2:-}" = --foreground ]; then\nexec /usr/bin/python3 '
                        + shlex.quote(str(child)) + '\nfi\nexec ' + shlex.quote(str(binary)) + ' "$@"\n')
    launcher.chmod(0o700)
    ordinary = None
    keep_running = False
    try:
        with (home / 'ordinary-supervisor.log').open('wb') as log:
            ordinary = subprocess.Popen([str(binary), '_supervise', '--state-dir', str(root), '--', str(launcher), 'up', '--foreground'],
                                        stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True, cwd=home)
        wait(lambda: request('status')['daemon'] == 'running')
        first = request('status')
        assert cli('systemd', 'on', check=False).returncode != 0, 'Changed a running owner'
        assert request('status')['daemonPid'] == first['daemonPid']
        cli('down')
        assert request('status')['supervisor'] == 'stopped'
        transition = cli('systemd', 'on', check=False)
        assert transition.returncode == 0, 'Stopped-supervisor transition failed: ' + transition.stderr
        ordinary.wait(timeout=10)
        assert str(launcher) in unit.read_text(), 'Service bypasses the stable launcher'
        cli('persistence', 'on')
        cli('up', '--bootstrap')
        first = request('status')
        assert first['daemon'] == 'running'
        main_pid = subprocess.check_output(['systemctl', '--user', 'show', UNIT, '-p', 'MainPID', '--value'], text=True).strip()
        assert int(main_pid) == first['supervisorPid']
        cli('up', '--bootstrap')
        assert request('status')['daemonPid'] == first['daemonPid'], 'Repeated start replaced the daemon'
        os.kill(first['daemonPid'], signal.SIGKILL)
        def recovered():
            current = request('status')
            return current['daemon'] == 'running' and current.get('daemonPid', 0) not in (0, first['daemonPid'])
        wait(recovered)
        cli('down')
        time.sleep(2)
        assert request('status')['daemon'] == 'stopped', 'Service undid explicit down'
        cli('restart')
        assert request('status')['daemon'] == 'running'
        report = cli('persistence', 'status').stdout
        assert 'Managed startup at login: enabled' in report, report
        assert 'Managed startup after reboot (before login): enabled' in report, report
        if args.leave_running:
            (home / 'lifecycle-reboot.json').write_text(json.dumps({
                'bootId': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
                'status': request('status'), 'binary': str(binary),
            }))
            keep_running = True
            print('PASS: lifecycle checks; fixture left running for VM logout/reboot acceptance')
            return
        cli('down')
        cli('persistence', 'off')
        cli('systemd', 'off')
        assert not (root / 'run/supervisor.sock').exists()
        print('PASS: actual systemd ownership transition, stable launcher, singleton, crash recovery, explicit stop, restart, persistence, and disable')
    finally:
        # Only this account's initially absent, run-created service is stopped.
        if unit.exists() and not keep_running:
            subprocess.run(['systemctl', '--user', 'disable', '--now', UNIT], capture_output=True)
        if not keep_running:
            try:
                request('shutdown')
            except (OSError, ValueError):
                pass
        if ordinary is not None:
            ordinary.wait(timeout=10)


if __name__ == '__main__':
    main()
