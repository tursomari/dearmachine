#!/usr/bin/env python3
"""Build the exact candidate source snapshot and run offline authorization gates."""
import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]
GO_IMAGE = 'golang@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--docker-host')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    output = args.output.resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error('output must be a new directory outside the checkout')
    output.mkdir(parents=True, exist_ok=False)
    docker = ['docker'] + (['--host', args.docker_host] if args.docker_host else [])
    image = 'dearmachine-guest-test:' + uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix='dearmachine-guest-source-') as tmp:
        snapshot = Path(tmp)
        names = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).decode().split('\0')
        hashes = []
        for name in sorted(set(filter(None, names))):
            source = ROOT / name
            if not source.exists():
                continue
            if source.is_symlink() or not source.is_file():
                raise SystemExit(f'unsupported snapshot entry: {name}')
            target = snapshot / 'candidate' / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
            hashes.append(hashlib.sha256(source.read_bytes()).hexdigest() + '  ' + name)
        (output / 'source.sha256').write_text('\n'.join(hashes) + '\n')
        (snapshot / 'Dockerfile').write_text(f'''FROM {GO_IMAGE}
WORKDIR /src/dearmachine
COPY candidate/dearmachine/go.mod candidate/dearmachine/go.sum ./
RUN go mod download
COPY candidate /src
ENV GOCACHE=/tmp/go-cache HOME=/tmp/test-home
CMD ["sh", "-c", "mkdir -p /tmp/test-home && go test ./... && go test -race ./... && go vet ./..."]
''')
        try:
            with (output / 'build.log').open('w') as log:
                subprocess.run(docker + ['build', '--tag', image, str(snapshot)], stdout=log, stderr=subprocess.STDOUT, check=True)
            with (output / 'tests.log').open('w') as log:
                subprocess.run(docker + ['run', '--rm', '--network', 'none', '--read-only', '--tmpfs', '/tmp:exec,size=4g', image], stdout=log, stderr=subprocess.STDOUT, check=True)
        finally:
            subprocess.run(docker + ['image', 'rm', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    print(f'Offline Go, race and vet gates passed; source hashes and logs: {output}')


if __name__ == '__main__':
    main()
