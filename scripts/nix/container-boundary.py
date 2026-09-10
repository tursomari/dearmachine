#!/usr/bin/env python3
"""Validate an explicit container workspace and isolation from native state."""
import argparse
import os
from pathlib import Path, PurePosixPath
import sys
import tomllib


def overlaps(left, right):
    return left == right or left in right.parents or right in left.parents


def inboxes(path):
    if not path.exists():
        return set()
    with path.open('rb') as source:
        registry = tomllib.load(source)
    if registry.get('version') != 2:
        raise ValueError('unsupported pair registry version')
    identities = set()
    for inbox in registry.get('inboxes', []):
        transport = inbox['transport']
        identities.add((transport, 'id', inbox['provider_id']))
        if inbox.get('id'):
            identities.add((transport, 'local-id', inbox['id']))
        identities.add((transport, 'address', inbox['address'].lower()))
    return identities


def validate(native_home, environment, create_inbox=None):
    native_home = Path(native_home).resolve()
    client_home = Path(environment['DEARMACHINE_CLIENT_HOME']).resolve()
    protected = [native_home / '.dearmachine', native_home / '.machtiani']
    names = ['DEARMACHINE_CLIENT_HOME', 'DEARMACHINE_CLIENT_CONFIG_DIR',
             'DEARMACHINE_CLIENT_STATE_DIR', 'DEARMACHINE_CLIENT_RUN_DIR',
             'DEARMACHINE_CLIENT_LOG_DIR', 'DEARMACHINE_CLIENT_AGENT_MANAGER_DIR',
             'DEARMACHINE_MACHTIANI_DIR']
    for name in names:
        if not environment.get(name):
            continue
        path = Path(environment[name]).resolve()
        if any(overlaps(path, boundary.resolve()) for boundary in protected):
            raise ValueError(f'{name} overlaps native state; select a private container state directory')
    if not environment.get('DEARMACHINE_PROJECT_DIR'):
        raise ValueError('DEARMACHINE_PROJECT_DIR is required')
    root = Path(environment['DEARMACHINE_PROJECT_DIR']).resolve(strict=True)
    subdir = PurePosixPath(environment.get('DEARMACHINE_PROJECT_SUBDIR', '.'))
    if subdir.is_absolute() or '..' in subdir.parts:
        raise ValueError('DEARMACHINE_PROJECT_SUBDIR must stay beneath the selected project tree')
    project = (root / str(subdir)).resolve(strict=True)
    if not project.is_dir() or not project.is_relative_to(root):
        raise ValueError('selected project must be a directory beneath DEARMACHINE_PROJECT_DIR')
    native_inboxes = inboxes(native_home / '.dearmachine/pairs.toml')
    if create_inbox and any(create_inbox.lower() == identity[2].lower() for identity in native_inboxes):
        raise ValueError('selected inbox is registered natively; use a separate container inbox')
    if native_inboxes & inboxes(client_home / '.dearmachine/pairs.toml'):
        raise ValueError('container and native registries share an inbox; use a separate inbox and state')
    return str(PurePosixPath('/workspace') / subdir)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--native-home', required=True)
    parser.add_argument('--create-inbox')
    args = parser.parse_args()
    try:
        print(validate(args.native_home, os.environ, args.create_inbox))
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f'container boundary: {error}', file=sys.stderr)
        sys.exit(1)
