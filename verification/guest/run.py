#!/usr/bin/env python3
"""Verify a source-only snapshot. No credentials, Git mounts, or runtime state."""
import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile
import urllib.request

IMAGE = 'ghcr.io/viperproject/gobra@sha256:d9dc17cdb3725818943a6224872628610e130c349e059ad393ef4f88878cca19'
TLC_URL = 'https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar'
TLC_SHA = '936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88'
ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--docker-host', help='Explicit Docker socket, if needed')
    parser.add_argument('--tlc-jar', type=Path, help='Pre-downloaded pinned tla2tools.jar')
    parser.add_argument('--output', type=Path, required=True, help='New artifact directory outside the checkout')
    parser.add_argument('--expand', action='store_true', help='Explore two threads sharing one provider entry')
    args = parser.parse_args()
    output = args.output.resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error('artifacts must be outside the checkout')
    output.mkdir(parents=True, exist_ok=False)
    docker = ['docker'] + (['--host', args.docker_host] if args.docker_host else [])
    subprocess.run(docker + ['image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
    with tempfile.TemporaryDirectory(prefix='dearmachine-guest-proof-') as name:
        snapshot = Path(name)
        data = args.tlc_jar.read_bytes() if args.tlc_jar else urllib.request.urlopen(TLC_URL, timeout=60).read()
        if hashlib.sha256(data).hexdigest() != TLC_SHA:
            raise SystemExit('TLC checksum mismatch')
        (snapshot / 'tla2tools.jar').write_bytes(data)
        for file in ('Guest.tla', 'Guest.cfg'):
            shutil.copyfile(ROOT / 'verification/guest' / file, snapshot / file)
        source = (ROOT / 'dearmachine/internal/client/guest_policy.go').read_text()
        (output / 'source.sha256').write_text(hashlib.sha256(source.encode()).hexdigest() + '\n')
        # Gobra receives the exact production declarations and bodies; only
        # annotation comment prefixes are removed. No independent proof copy.
        (snapshot / 'policy.gobra').write_text(source.replace('// @ ', ''))
        common = docker + ['run', '--rm', '--network', 'none', '--mount', f'type=bind,src={snapshot},dst=/proof,readonly']
        def run(label, command, expected=0, marker=None):
            result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=600)
            (output / (label + '.log')).write_text(result.stdout)
            if result.returncode != expected or (marker and marker not in result.stdout):
                raise SystemExit(f'{label} failed: exit {result.returncode}; inspect {output / (label + ".log")}')
            print(f'{label}: expected exit {expected}', flush=True)
        run('gobra', common + [IMAGE, '-i', '/proof/policy.gobra'], marker='Gobra found 0 errors')
        tlc = common + ['--entrypoint', 'java', IMAGE, '-XX:+UseParallelGC', '-Xmx2g', '-cp', '/proof/tla2tools.jar', 'tlc2.TLC', '-workers', '2', '-metadir', '/tmp/tlc']
        config = (snapshot / 'Guest.cfg').read_text()
        for mutant in ('KnownOnly', 'StaleDecision', 'ReplayInvitation', 'DeleteUnowned'):
            (snapshot / (mutant + '.cfg')).write_text(config.replace(mutant + ' = FALSE', mutant + ' = TRUE'))
            run(mutant, tlc + ['-config', '/proof/' + mutant + '.cfg', '/proof/Guest.tla'], 12, 'Invariant Safety is violated')
        run('tlc', tlc + ['-config', '/proof/Guest.cfg', '/proof/Guest.tla'], marker='Model checking completed. No error has been found.')
        if args.expand:
            (snapshot / 'Expanded.cfg').write_text(config.replace('Threads = {t1}', 'Threads = {t1, t2}'))
            run('tlc-expanded', tlc + ['-config', '/proof/Expanded.cfg', '/proof/Guest.tla'], marker='Model checking completed. No error has been found.')
            matrix = config.replace('Sparse = FALSE', 'Sparse = TRUE')
            for dimension, first, second in [('Pairs', 'p1', 'p2'), ('Inboxes', 'i1', 'i2'), ('Guests', 'g1', 'g2'), ('Threads', 't1', 't2')]:
                matrix = matrix.replace(f'{dimension} = {{{first}}}', f'{dimension} = {{{first}, {second}}}')
            (snapshot / 'Matrix.cfg').write_text(matrix)
            run('tlc-matrix', tlc + ['-config', '/proof/Matrix.cfg', '/proof/Guest.tla'], marker='Model checking completed. No error has been found.')



if __name__ == '__main__':
    main()
