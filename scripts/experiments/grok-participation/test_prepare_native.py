"""Preparation/send orchestration tests using fake CLI results, never live registration."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import prepare_native as native


class PrepareTests(unittest.TestCase):
    def run_fixture(self, base):
        root = base/'brigade-native-offline'
        root.mkdir()
        package = base/'package'
        (package/'hooks').mkdir(parents=True)
        (package/'hooks/hooks.json').write_text('{}')
        bins = []
        for name in ('dev', 'fs'):
            path = base/name
            path.write_bytes(b'\x7fELFsynthetic-test-not-executable-code')
            path.chmod(0o700)
            bins.append(path)
        self.calls = []
        self.roster = {'team_ref': 'synthetic-team', 'sessions': [
            {'session_id': 'own-receiver', 'harness': 'grok-build', 'state': 'idle'}]}
        self.sent_count = 0
        def runner(argv, *, root, document=None, protocol=True):
            self.calls.append((argv, document))
            if 'create' in argv:
                secret = Path(argv[argv.index('--secret-file')+1])
                native.private_write(secret, 'brg1.' + os.urandom(24).hex() + '\n')
                native.private_write(root/'workspace/.brigade.json', {
                    'adapter':'fs','url':'http://127.0.0.1:1','team_ref':'synthetic-team'})
                native.private_write(root/'home/.config/brigade/projects.json', {
                    'projects':{str(root/'workspace'):{'team_ref':'synthetic-team'}}})
            elif 'list' in argv:
                return self.roster
            elif 'register' in argv:
                self.roster['sessions'].append({
                    'session_id': 'own-sender', 'harness': 'shell', 'state': 'active'})
                return {'session_id':'own-sender'}
            elif 'send' in argv:
                self.sent_count += 1
                return {'message_id': 'own-message' if self.sent_count == 1 else f'own-message-{self.sent_count}'}
            elif 'close' in argv:
                return {'state':'offline'}
            else:
                self.fail('unexpected command')
        # Nonexistent GROK_HOME proves preparer does not inspect it.
        grok_home = str(base/'never-open-normal-grok-home')
        with patch.object(native.tempfile, 'mkdtemp', return_value=str(root)):
            actual = native.prepare(*bins, grok_home, package, runner=runner)
        return actual, runner

    def test_setup_uses_cli_consent_no_receiver_map_or_native_process(self):
        with tempfile.TemporaryDirectory() as scratch:
            root, runner = self.run_fixture(Path(scratch))
            self.assertEqual(len(self.calls), 1)
            argv, doc = self.calls[0]
            self.assertEqual(argv[1:3], ['team','create'])
            self.assertNotIn('brg1.', ' '.join(argv))
            self.assertIsNone(doc)
            self.assertFalse((root/'home/.local/state/brigade/sessions').exists())
            self.assertFalse(any('hook' in args or 'grok' == args[0] for args,_ in self.calls))
            self.assertEqual((root/'messages/16k.txt').stat().st_size,16384)
            state = json.loads((root/'acceptance.json').read_text())
            self.assertFalse(Path(state['grok_home']).exists())
            hints = native.instructions(root)
            self.assertIn('--permission-mode default', hints['operator_only_launch'])
            self.assertIn('--no-leader', hints['operator_only_launch'])
            self.assertIn('--trust', hints['operator_only_install'])
            self.assertNotIn('--plugin-dir', hints['operator_only_install'])
            self.assertFalse(any(k.startswith(('BRIGADE','GROK','CLAUDE','CODEX','MUSE','XDG'))
                                 for k in native.fixture_env(root)))

    def test_send_only_explicit_owned_roster_receiver_body_on_stdin(self):
        with tempfile.TemporaryDirectory() as scratch:
            root, runner = self.run_fixture(Path(scratch))
            result = native.send(root,'own-receiver',root/'messages/16k.txt',runner=runner)
            self.assertEqual(result['body_bytes'],16384)
            self.assertEqual(result['local_injection_ack'],'not_checked')
            self.assertEqual(result['model_delivery'],'not_checked')
            self.assertEqual(result['sender_session_id'],'own-sender')
            send_args, payload = next((a,d) for a,d in self.calls if 'send' in a)
            self.assertEqual(payload['recipient_session_id'],'own-receiver')
            self.assertNotIn(payload['body'], ' '.join(send_args))
            self.assertFalse(any('ack' in a or 'hook' in a for a,_ in self.calls))
            self.assertFalse(native.receipt(root,'own-receiver','own-message')['local_injection_ack_record_present'])
            state = json.loads((root/'acceptance.json').read_text())
            record = root/'store/teams'/state['team_ref']/'acked/own-receiver/0001.own-message.json'
            native.private_write(record, {'message_id':'own-message','acked_at':'synthetic-time'})
            self.assertTrue(native.receipt(root,'own-receiver','own-message')['local_injection_ack_record_present'])
            with self.assertRaises(native.FixtureError):
                native.receipt(root,'sibling','own-message')
            with self.assertRaises(native.FixtureError):
                native.send(root,'unowned',root/'messages/short.txt',runner=runner)
            with self.assertRaises(native.FixtureError):
                native.send(root,'../escape',root/'messages/short.txt',runner=runner)
            with self.assertRaises(native.FixtureError):
                native.send(root,'own-receiver',root/'expected-private.json',runner=runner)

    def test_changed_binary_and_bootstrap_are_refused(self):
        with tempfile.TemporaryDirectory() as scratch:
            root, _ = self.run_fixture(Path(scratch))
            state = json.loads((root/'acceptance.json').read_text())
            Path(state['dev_bin']).write_bytes(b'\x7fELFchanged')
            with self.assertRaises(native.FixtureError):
                native.instructions(root)
            shell = Path(scratch)/'bootstrap'
            shell.write_text('#!/bin/sh\nexit 0\n'); shell.chmod(0o700)
            with self.assertRaises(native.FixtureError):
                native.native_binary(shell)

    def test_repeat_send_selects_exact_receiver_among_sender_and_siblings(self):
        with tempfile.TemporaryDirectory() as scratch:
            root, runner = self.run_fixture(Path(scratch))
            self.roster['sessions'].insert(0, {
                'session_id': 'sibling', 'harness': 'grok-build', 'state': 'offline'})
            first = native.send(root, 'own-receiver', root/'messages/short.txt', runner=runner)
            # The first call registered a synthetic sender, now present in the roster.
            self.assertEqual(len(self.roster['sessions']), 3)
            self.roster['sessions'][1].update(harness='grok', state='active')
            second = native.send(root, 'own-receiver', root/'messages/short.txt', runner=runner)
            self.assertNotEqual(first['message_id'], second['message_id'])
            payloads = [doc for args, doc in self.calls if 'send' in args]
            self.assertEqual(len(payloads), 2)
            self.assertTrue(all(doc['recipient_session_id'] == 'own-receiver' for doc in payloads))
            self.assertTrue(first['accepted_by_adapter'] and second['accepted_by_adapter'])

    def test_receiver_refusals_precede_registration_or_send(self):
        receiver = {'session_id': 'own-receiver', 'harness': 'grok-build', 'state': 'active'}
        sibling = {'session_id': 'sibling', 'harness': 'grok-build', 'state': 'idle'}
        cases = (
            ('missing', 'synthetic-team', [sibling]),
            ('duplicate', 'synthetic-team', [receiver, sibling, dict(receiver)]),
            ('duplicate-invalid', 'synthetic-team', [receiver, {**receiver, 'state': 'offline'}]),
            ('wrong-state', 'synthetic-team', [sibling, {**receiver, 'state': 'offline'}]),
            ('wrong-harness', 'synthetic-team', [sibling, {**receiver, 'harness': 'shell'}]),
            ('wrong-team', 'other-team', [sibling, receiver]),
        )
        with tempfile.TemporaryDirectory() as scratch:
            root, runner = self.run_fixture(Path(scratch))
            for name, team, entries in cases:
                with self.subTest(name=name):
                    self.roster = {'team_ref': team, 'sessions': entries}
                    before = len(self.calls)
                    with self.assertRaises(native.FixtureError):
                        native.send(root, 'own-receiver', root/'messages/short.txt', runner=runner)
                    self.assertEqual(len(self.calls), before+1)
                    self.assertIn('list', self.calls[-1][0])
                    self.assertFalse((root/'sent').exists())


if __name__ == '__main__':
    unittest.main()
