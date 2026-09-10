#!/usr/bin/env python3
"""Exercise container path and inbox isolation with disposable real files."""
import importlib.util
import os
from pathlib import Path
import tempfile
import sys
import unittest

sys.dont_write_bytecode = True

root = Path(os.environ.get('PROJECT_ROOT', Path(__file__).resolve().parents[2]))
spec = importlib.util.spec_from_file_location('boundary', root / 'scripts/nix/container-boundary.py')
boundary = importlib.util.module_from_spec(spec)
spec.loader.exec_module(boundary)


class ContainerBoundaryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.native = self.root / 'native'
        self.client = self.root / 'container'
        self.projects = self.root / 'projects'
        for path in [self.native, self.client, self.projects / 'entry point']:
            path.mkdir(parents=True)
        self.environment = {'DEARMACHINE_CLIENT_HOME': str(self.client),
                            'DEARMACHINE_PROJECT_DIR': str(self.projects)}

    def test_mount_root_and_nested_working_directory(self):
        self.assertEqual(boundary.validate(self.native, self.environment), '/workspace')
        self.environment['DEARMACHINE_PROJECT_SUBDIR'] = 'entry point'
        self.assertEqual(boundary.validate(self.native, self.environment), '/workspace/entry point')

    def test_rejects_missing_parent_and_symlink_escape(self):
        (self.projects / 'outside').symlink_to(self.native, target_is_directory=True)
        for value in ['../native', '/native', 'outside', 'missing']:
            with self.subTest(value=value), self.assertRaises((ValueError, OSError)):
                boundary.validate(self.native, {**self.environment, 'DEARMACHINE_PROJECT_SUBDIR': value})

    def test_rejects_native_database_and_pid_directory_aliases(self):
        native_state = self.native / '.dearmachine'
        native_state.mkdir()
        alias = self.root / 'alias'
        alias.symlink_to(native_state, target_is_directory=True)
        for name, path in [('DEARMACHINE_CLIENT_HOME', self.native),
                           ('DEARMACHINE_CLIENT_STATE_DIR', alias / 'state'),
                           ('DEARMACHINE_CLIENT_RUN_DIR', native_state / 'run'),
                           ('DEARMACHINE_MACHTIANI_DIR', self.native / '.machtiani')]:
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'overlaps native state'):
                boundary.validate(self.native, {**self.environment, name: str(path)})

    def write_registry(self, home, provider_id, address='inbox@example.test'):
        path = home / '.dearmachine/pairs.toml'
        path.parent.mkdir(exist_ok=True)
        path.write_text(f'version = 2\n[[inboxes]]\ntransport = "agentmail"\nprovider_id = "{provider_id}"\naddress = "{address}"\n')

    def test_distinct_databases_do_not_allow_duplicate_inbox_workers(self):
        self.write_registry(self.native, 'native-inbox')
        self.write_registry(self.client, 'native-inbox', 'alias@example.test')
        with self.assertRaisesRegex(ValueError, 'share an inbox'):
            boundary.validate(self.native, self.environment)
        self.write_registry(self.client, 'other-id', 'INBOX@example.test')
        with self.assertRaisesRegex(ValueError, 'share an inbox'):
            boundary.validate(self.native, self.environment)
        self.write_registry(self.client, 'other-id', 'other@example.test')
        self.assertEqual(boundary.validate(self.native, self.environment), '/workspace')

    def test_creation_cannot_adopt_a_native_inbox_before_its_first_turn(self):
        self.write_registry(self.native, 'native-inbox')
        for selection in ['native-inbox', 'INBOX@example.test']:
            with self.subTest(selection=selection), self.assertRaisesRegex(ValueError, 'registered natively'):
                boundary.validate(self.native, self.environment, selection)
        self.assertEqual(boundary.validate(self.native, self.environment, 'other-inbox'), '/workspace')

    def test_invalid_registry_fails_closed(self):
        self.write_registry(self.native, 'native-inbox')
        (self.native / '.dearmachine/pairs.toml').write_text('version = 999\n')
        with self.assertRaisesRegex(ValueError, 'unsupported'):
            boundary.validate(self.native, self.environment)


if __name__ == '__main__':
    unittest.main()
