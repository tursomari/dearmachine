#!/usr/bin/env python3
"""Exercise a real macOS LaunchAgent with a candidate CLI and an offline child.

No provider, inbox, real HOME, logout, reboot, or unrelated service is used.
The login plist is explicitly bootstrapped to simulate a new login; this is not
an actual logout/reboot test. Run in an existing graphical macOS user session.
"""
import argparse
import json
import os
from pathlib import Path
import platform
import plistlib
import shlex
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        parser.error('requires a real macOS host with a graphical login session')
    binary = args.binary.resolve(strict=True)
    domain = 'gui/' + str(os.getuid())
    subprocess.run(['/bin/launchctl', 'print', domain], check=True, stdout=subprocess.DEVNULL)
    home = Path(tempfile.mkdtemp(prefix='dm-ld-', dir='/tmp')).resolve()
    root = home / '.dearmachine'
    env = dict(HOME=str(home), PATH='/usr/bin:/bin:/usr/sbin:/sbin')
    target = None
    foreground = None
    passed = False

    def cli(*command, check=True):
        return subprocess.run([str(binary), *command], env=env, text=True,
                              capture_output=True, check=check, timeout=35)

    def status():
        with socket.socket(socket.AF_UNIX) as connection:
            connection.settimeout(3)
            connection.connect(str(root / 'run/supervisor.sock'))
            connection.sendall(b'{"version":1,"command":"status"}\n')
            with connection.makefile('r') as reader:
                response = json.loads(reader.readline())
        assert response['ok'], response
        return response['status']

    def wait_for(predicate):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.1)
        raise AssertionError('timed out waiting for lifecycle state')

    try:
        root.mkdir(mode=0o700)
        inbox, pair = str(uuid.uuid4()), str(uuid.uuid4())
        registry = root / 'pairs.toml'
        registry.write_text(f'''version = 2
[[inboxes]]
id = "{inbox}"
transport = "agentmail"
provider_id = "fixture"
address = "machine@example.invalid"
[[pairs]]
id = "{pair}"
user_email = "user@example.invalid"
inbox_id = "{inbox}"
''')
        registry.chmod(0o600)
        child = home / 'fixture.py'
        child.write_text('''import os,signal,time
from pathlib import Path
root=Path(os.environ['HOME'])/'.dearmachine/run'
root.mkdir(parents=True,exist_ok=True,mode=0o700)
paths=[root/name for name in ('dearmachine.pid','dearmachine.ready')]
for path in paths:
 path.write_text(str(os.getpid()))
 path.chmod(0o600)
def stop(*args):raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
signal.signal(signal.SIGINT,stop)
try:
 while True:time.sleep(0.1)
finally:
 for path in paths:path.unlink(missing_ok=True)
''')
        launcher = home / '.local/bin/dearmachine'
        launcher.parent.mkdir(parents=True)
        launcher.write_text('#!/bin/sh\nif [ "${1:-}" = up ] && [ "${2:-}" = --foreground ]; then\n'
                            'exec /usr/bin/python3 ' + shlex.quote(str(child)) + '\nfi\nexec '
                            + shlex.quote(str(binary)) + ' "$@"\n')
        launcher.chmod(0o700)
        # A foreground owner must be stopped before a service choice can alter
        # supervision. Use the same offline child with its actual live PID.
        foreground = subprocess.Popen([str(launcher), 'up', '--foreground'], env=env,
                                      stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL)
        wait_for(lambda: (root / 'run/dearmachine.pid').read_text().strip() == str(foreground.pid))
        refused = cli('launchd', 'on', check=False)
        assert refused.returncode != 0, 'Changed supervision beside a foreground owner'
        assert 'another foreground or service owner' in refused.stderr, refused.stderr
        assert not (root / 'supervision.json').exists(), 'Refused choice saved consent'
        assert foreground.poll() is None, 'Refused choice stopped the foreground owner'
        foreground.terminate()
        foreground.wait(timeout=10)
        foreground = None
        cli('launchd', 'on')
        definition = next((root / 'launchd').glob('*.plist'))
        plist = plistlib.loads(definition.read_bytes())
        assert plist['ProgramArguments'][0] == str(launcher)
        assert plist['EnvironmentVariables']['HOME'] == str(home)
        assert not (home / 'Library/LaunchAgents').exists()
        target = domain + '/' + plist['Label']
        assert subprocess.run(['/bin/launchctl', 'print', target], capture_output=True).returncode == 113
        cli('up', '--bootstrap')
        first = status()
        assert first['daemon'] == 'running'
        cli('up', '--bootstrap')
        assert status()['daemonPid'] == first['daemonPid']
        os.kill(first['daemonPid'], signal.SIGKILL)
        def child_recovered():
            observed = status()
            return (observed['daemon'] == 'running' and observed.get('daemonPid', 0) > 0
                    and observed['daemonPid'] != first['daemonPid'])
        wait_for(child_recovered)
        first = status()
        assert cli('launchd', 'off', check=False).returncode != 0
        cli('persistence', 'on')
        login = home / 'Library/LaunchAgents' / definition.name
        assert login.read_bytes() == definition.read_bytes()
        report = cli('persistence', 'status').stdout
        assert 'Managed startup at login: enabled' in report, report
        assert 'after reboot (before login): not configured' in report, report
        cli('persistence', 'off')
        assert status()['daemonPid'] == first['daemonPid']
        assert not login.exists()
        cli('restart')
        assert status()['daemonPid'] != first['daemonPid']
        cli('down')
        assert status()['daemon'] == 'stopped'
        time.sleep(2)
        assert status()['daemon'] == 'stopped', 'launchd undid explicit down'
        cli('up', '--bootstrap')
        assert status()['daemon'] == 'running'
        cli('persistence', 'on')
        # Only this run's known service is unloaded/reloaded. The real user's
        # login session and all other jobs stay untouched.
        subprocess.run(['/bin/launchctl', 'bootout', target], check=True)
        wait_for(lambda: not (root / 'run/supervisor.sock').exists())
        subprocess.run(['/bin/launchctl', 'bootstrap', domain, str(login)], check=True)
        wait_for(lambda: status()['daemon'] == 'running')
        cli('down')
        cli('persistence', 'off')
        cli('launchd', 'off')
        assert subprocess.run(['/bin/launchctl', 'print', target], capture_output=True).returncode == 113
        assert not definition.exists() and not login.exists()
        passed = True
        print('PASS: foreground ownership refusal, launchd startup, singleton ownership, child crash recovery, restart, explicit stop, login configuration, simulated login, disable, and cleanup')
    finally:
        if foreground is not None and foreground.poll() is None:
            foreground.terminate()
            foreground.wait(timeout=10)
        if target:
            cli('down', check=False)
            cli('persistence', 'off', check=False)
            cli('launchd', 'off', check=False)
            remaining = subprocess.run(['/bin/launchctl', 'print', target], capture_output=True)
            if remaining.returncode != 113:
                raise RuntimeError('owned job cleanup is unconfirmed; retained ' + str(home))
        if passed:
            shutil.rmtree(home)
        else:
            print('Failure evidence retained at', home)


if __name__ == '__main__':
    main()
