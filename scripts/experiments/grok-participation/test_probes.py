"""Offline only: no Grok, trust changes, credentials or live model calls."""
import asyncio
from contextlib import redirect_stdout
from copy import deepcopy
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

import monitor_fixture as fixture
import probe
import summarize_evidence as evidence
import tui_probe


def chunk(sid='owned', pid='current', text='EXPECTED', kind='agent_message_chunk'):
    return {'sessionId': sid, '_meta': {'promptId': pid},
            'update': {'sessionUpdate': kind, 'content': {'text': text}}}


def response(pid='current'):
    return {'result': {'stopReason': 'end_turn', '_meta': {'promptId': pid}}}


class ClientTests(unittest.IsolatedAsyncioTestCase):
    async def test_passive_never_answers_reverse_interactions(self):
        for passive in (True, False):
            client = probe.Client(passive=passive)
            stream = asyncio.StreamReader()
            for method in ('session/request_permission', 'fs/read_text_file', 'terminal/create'):
                stream.feed_data((json.dumps({'method': method, 'id': method})+'\n').encode())
            stream.feed_eof()
            client.proc = SimpleNamespace(stdout=stream)
            client.send = AsyncMock()
            await client.read()
            self.assertEqual(client.send.await_count, 0 if passive else 3)
            if not passive:
                first = client.send.await_args_list[0].args[0]
                self.assertEqual(first['result']['outcome']['outcome'], 'cancelled')

    def test_replay_user_echo_sibling_and_missing_correlation_cannot_supply_content(self):
        client = probe.Client(passive=True)
        client.updates = [chunk(pid='replay', text='READY_A'),
                          chunk(sid='sibling'), chunk(kind='user_message_chunk'),
                          chunk(pid=None), chunk(text='EX'), chunk(text='PECTED')]
        self.assertEqual(client.text('owned', 'current'), 'EXPECTED')
        client.updates = client.updates[:-2]
        self.assertEqual(client.text('owned', 'current'), '')
        self.assertFalse(tui_probe.completed(response('replay'), 'current'))
        for mutation in ({'stopReason': 'cancelled'}, {'_meta': {}},
                         {'_meta': {'promptId': 'current', 'cancellationCategory': 'HookDenied'}}):
            r = response()
            r['result'].update(mutation)
            self.assertFalse(tui_probe.completed(r, 'current'))
        self.assertTrue(tui_probe.completed(response(), 'current'))


class ScorerTests(unittest.TestCase):
    def baseline(self):
        checks = {
            'tui_readiness_A': {'observed': True}, 'tui_readiness_B': {'observed': True},
            'producer_initialize': {'ok': True}, 'attach_A': {'ok': True}, 'attach_B': {'ok': True},
            'idle_A': {'ok': True, 'correlated': True, 'tui_witness': True, 'stop': 'end_turn',
                       'sibling_agent_text': False, 'reply': 'GROK120_IDLE_A'},
            'busy_A': {'correlated': True, 'request_in_flight': True, 'original_completed': True,
                       'nonce_observed': True, 'tui_witness': True, 'sibling_agent_text': False,
                       'reply': 'GROK120_BUSY_A', 'original_stop': 'end_turn', 'followup_stop': 'end_turn'},
            'no_send_control': {'quiet': True},
            'idle_B_after_hold': {'ok': True, 'correlated': True, 'tui_witness': True, 'stop': 'end_turn',
                                  'sibling_agent_text': False, 'reply': 'GROK120_IDLE_B'},
            'producer_disconnect': {'tuis_alive': True},
            'cleanup': {'owned_tuis_closed': True, 'leader_reaped': True}}
        return {'scorer_schema': 2, 'checks': [{'name': k, **v} for k, v in checks.items()]}

    def test_required_boolean_content_stop_and_tui_mutations_fail(self):
        base = self.baseline()
        self.assertTrue(tui_probe.score(base))
        mutations = 0
        for i, check in enumerate(base['checks']):
            for key, value in check.items():
                if key == 'name':
                    continue
                damaged = deepcopy(base)
                damaged['checks'][i][key] = not value if type(value) is bool else 'WRONG'
                self.assertFalse(tui_probe.score(damaged), (check['name'], key))
                mutations += 1
            missing = deepcopy(base)
            del missing['checks'][i]
            self.assertFalse(tui_probe.score(missing))
        self.assertEqual(mutations, 30)
        contaminated = deepcopy(base)
        next(c for c in contaminated['checks'] if c['name'] == 'idle_A')['reply'] = 'READY_AGROK120_IDLE_A'
        self.assertFalse(tui_probe.score(contaminated))
        base['scorer_schema'] = 1
        self.assertFalse(tui_probe.score(base))


class ExportTests(unittest.TestCase):
    def records(self):
        private = '/Users/PRIVATE/account-secret-signature'
        direct = {
            'live_info': {'result': {'result': {'sessionId': private, 'cwd': private}}},
            'missing_info': {'result': {'result': {}}},
            'direct_result': {'result': {'stopReason': 'end_turn', '_meta': {
                'promptId': private, 'requestId': private, 'usage': {'modelCalls': 1, 'private': private}}}},
            'direct_text': 'DIRECT_LIVE_120',
            'messages': [{'method': 'session/update', 'params': chunk(pid=private)}]}
        rejected = deepcopy(direct)
        rejected['direct_result']['result']['stopReason'] = 'cancelled'
        rejected['direct_result']['result']['_meta']['cancellationCategory'] = 'HookDenied'
        rejected['direct_text'], rejected['messages'] = '', []
        return {'direct-report.json': direct, 'rejected-report.json': rejected,
                'liveness-normal-report.json': {'before_present': True, 'after_normal_exit_present': True,
                    'closed_prompt_error_code': None, 'closed_prompt_stop': 'end_turn', 'ghost_reply': True},
                'monitor-summary.json': {'hook_parent_equals_tui': True, 'socket_env_present': False,
                    'native_identity_matches': True, 'auto_nonce_visible': True},
                'monitor-tool-busy-report.json': {'tool_in_flight': True, 'tool_completed': True,
                    'nonce_observed': True, 'completion_seconds': 18.23, 'status': 'pass'},
                'monitor-random-tail-report.json': {'status': 'fail', 'cases': [
                    {'body_bytes': 2048, 'kind': 'ascii', 'tail_reply_observed': False, 'completion_seconds': 121.95}]},
                private: {'unexpected': private}}

    def test_allowlist_excludes_private_values_extra_keys_and_raw_content(self):
        output = evidence.summarize(self.records())
        text = json.dumps(output)
        for private in ('/Users/', 'account-secret', 'signature', 'DIRECT_LIVE_120', 'EXPECTED'):
            self.assertNotIn(private, text)
        self.assertEqual(output['sources'], list(evidence.NAMES))
        self.assertTrue(output['rejected_prompt']['usage_matches_preceding_turn'])
        self.assertTrue(output['direct_transport']['agent_chunks_correlated'])

    def test_unexpected_enums_and_nonboolean_measurements_rejected(self):
        records = self.records()
        records['monitor-summary.json']['auto_nonce_visible'] = 'true'
        with self.assertRaises(ValueError):
            evidence.summarize(records)
        records = self.records()
        records['rejected-report.json']['direct_result']['result']['stopReason'] = '/private/path'
        with self.assertRaises(ValueError):
            evidence.summarize(records)

    def test_reused_tail_and_unverified_size_pass_cannot_enter_allowlist(self):
        records = self.records()
        records['monitor-payload-report.json'] = {'cases': [
            {'body_bytes': n, 'tail_reply_observed': True, 'tail': 'END120'}
            for n in (64, 2048, 8192, 16384)]}
        with self.assertRaises(ValueError):
            evidence.summarize(records)
        records = self.records()
        records['monitor-random-tail-report.json']['status'] = 'pass'
        records['monitor-random-tail-report.json']['cases'][0]['tail_reply_observed'] = True
        with self.assertRaises(ValueError):
            evidence.summarize(records)
        records = self.records()
        records['monitor-random-tail-report.json']['cases'][0]['tail_reply_observed'] = True
        with self.assertRaises(ValueError):
            evidence.summarize(records)


class FixtureTests(unittest.TestCase):
    def test_prepare_only_creates_private_fixture(self):
        with tempfile.TemporaryDirectory() as root:
            with patch.object(fixture.tempfile, 'mkdtemp', return_value=root), redirect_stdout(io.StringIO()):
                fixture.prepare()
            hooks = json.loads((Path(root)/'plugin/hooks/hooks.json').read_text())['hooks']
            self.assertEqual(set(hooks), {'SessionStart', 'Stop', 'SessionEnd'})
            self.assertTrue(hooks['Stop'][0]['hooks'][0]['command'].startswith('exec python3 '))
            self.assertTrue((Path(root)/'workspace/.monitor-fixture/trigger.txt').exists())

    def test_wrong_workspace_never_reads_stdin(self):
        stdin = SimpleNamespace(buffer=SimpleNamespace(read=lambda _: self.fail('read unrelated input')))
        with tempfile.TemporaryDirectory() as root, patch.object(fixture.sys, 'stdin', stdin):
            self.assertEqual(fixture.hook(Path(root).resolve()), {})

    def test_stop_once_recursion_teardown_identity_and_end(self):
        with tempfile.TemporaryDirectory() as root:
            workspace = Path(root).resolve()
            (workspace/'.monitor-fixture').mkdir()
            original = Path.cwd()
            try:
                os.chdir(workspace)
                def invoke(**overrides):
                    data = {'sessionId': 'synthetic', 'hookEventName': 'stop', 'reason': 'end_turn'}
                    data.update(overrides)
                    stdin = SimpleNamespace(buffer=io.BytesIO(json.dumps(data).encode()))
                    with patch.object(fixture.sys, 'stdin', stdin):
                        return fixture.hook(workspace)
                with patch.dict(os.environ, {'GROK_SESSION_ID': 'synthetic'}, clear=True), \
                     patch.object(fixture.os, 'getppid', return_value=123), \
                     patch.object(fixture, 'birth', return_value='synthetic-start'):
                    for invalid in ({'stopHookActive': True}, {'reason': 'shutdown'},
                                    {'subagentType': 'worker'}, {'sessionId': 'sibling'}):
                        self.assertEqual(invoke(**invalid), {})
                    self.assertEqual(invoke()['decision'], 'block')
                    self.assertEqual(invoke(), {})
                    self.assertEqual(invoke(hookEventName='session_end'), {})
                    self.assertEqual(len(list((workspace/'.monitor-fixture').glob('*.ended'))), 1)
            finally:
                os.chdir(original)

    def test_dead_or_reused_parent_emits_no_pending_trigger(self):
        with tempfile.TemporaryDirectory() as root:
            workspace = Path(root)
            state = workspace/'.monitor-fixture'
            state.mkdir()
            key = 'a'*64
            (state/(key+'.owner.json')).write_text(json.dumps({'pid': 123, 'started': 'old', 'native_id': 'synthetic'}))
            (state/'trigger.txt').write_text('SHOULD_NOT_EMIT\n')
            out = io.StringIO()
            with patch.dict(os.environ, {'GROK_SESSION_ID': 'synthetic'}, clear=True), \
                 patch.object(fixture, 'birth', return_value='new'), redirect_stdout(out):
                fixture.follow(workspace, key)
            self.assertEqual(out.getvalue(), '')


if __name__ == '__main__':
    unittest.main()
