#!/usr/bin/env python3
"""Card 120: isolated Grok ACP handshake/negative routing probe.

No real session IDs or credentials are read. Live mode creates synthetic sessions
using the existing login; it never starts a login flow or grants tool permissions.
"""
import asyncio
import json
import os
import tempfile
import argparse
from pathlib import Path


class Client:
    def __init__(self):
        self.pending = {}
        self.updates = []
        self.seq = 0
        self.err_bytes = 0

    async def start(self, cwd, args=None):
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(('CLAUDE', 'CODEX', 'MUSE', 'BRIGADE')) and k != 'AI_AGENT'}
        self.proc = await asyncio.create_subprocess_exec(
            'grok', 'agent', *(args or ['--no-leader']), 'stdio', cwd=cwd, env=env,
            stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE)
        self.reader = asyncio.create_task(self.read())
        self.err_reader = asyncio.create_task(self.read_err())
        return await self.call('initialize', {'protocolVersion': 1, 'clientCapabilities': {},
                              'clientInfo': {'name': 'brigade-feasibility', 'version': '0'}})

    async def read_err(self):
        while chunk := await self.proc.stderr.read(4096):
            self.err_bytes += len(chunk)

    async def send(self, msg):
        self.proc.stdin.write((json.dumps(msg) + '\n').encode())
        await self.proc.stdin.drain()

    async def read(self):
        while line := await self.proc.stdout.readline():
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if 'method' in msg and 'id' in msg:
                if msg['method'] == 'session/request_permission':
                    result = {'outcome': {'outcome': 'cancelled'}}
                    await self.send({'jsonrpc': '2.0', 'id': msg['id'], 'result': result})
                else:
                    await self.send({'jsonrpc': '2.0', 'id': msg['id'], 'error':
                                    {'code': -32601, 'message': 'Probe does not implement this client method'}})
            elif msg.get('id') in self.pending:
                future = self.pending.pop(msg['id'])
                if not future.done():
                    future.set_result(msg)
            elif msg.get('method') == 'session/update':
                self.updates.append(msg['params'])

    async def call(self, method, params, timeout=90):
        self.seq += 1
        future = asyncio.get_running_loop().create_future()
        self.pending[self.seq] = future
        await self.send({'jsonrpc': '2.0', 'id': self.seq, 'method': method, 'params': params})
        return await asyncio.wait_for(future, timeout)

    def text(self, sid):
        return ''.join(p.get('update', {}).get('content', {}).get('text', '')
                       for p in self.updates if p.get('sessionId') == sid
                       and p.get('update', {}).get('sessionUpdate') == 'agent_message_chunk')

    async def close(self):
        if self.proc.returncode is None:
            self.proc.stdin.close()
            try:
                await asyncio.wait_for(self.proc.wait(), 5)
            except asyncio.TimeoutError:
                self.proc.terminate()
                await asyncio.wait_for(self.proc.wait(), 5)
        await self.reader
        await self.err_reader


async def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--live', action='store_true', help='attempt one synthetic model turn using the existing login')
    args = parser.parse_args()
    c = Client()
    report = {'schema': 1, 'scope': 'standalone ACP; not proof of existing TUI delivery'}
    sid = None
    with tempfile.TemporaryDirectory(prefix='brigade-grok-probe-') as cwd:
        try:
            init = (await c.start(cwd))['result']
            report['version'] = init.get('_meta', {}).get('agentVersion')
            report['capabilities'] = init.get('agentCapabilities', {})
            report['auth_method_ids'] = [m['id'] for m in init.get('authMethods', [])]
            missing = '00000000-0000-4000-8000-000000000120'
            report['negative_routing'] = {}
            for method in ('x.ai/interject', '_x.ai/interject'):
                response = await c.call(method, {'sessionId': missing, 'text': 'BRIGADE_NO_TARGET'}, timeout=10)
                report['negative_routing'][method] = {'error_code': response.get('error', {}).get('code')}
            if args.live:
                new = await c.call('session/new', {'cwd': cwd, 'mcpServers': []}, timeout=20)
                if 'error' in new:
                    report['live'] = {'status': 'blocked', 'error_code': new['error'].get('code'),
                                      'action': 'Complete a normal Grok prompt in a terminal, then retry. No login flow is automated.'}
                else:
                    sid = new['result']['sessionId']
                    response = await c.call('session/prompt', {'sessionId': sid, 'prompt': [
                        {'type': 'text', 'text': 'Integration test. Do not call tools. Reply exactly BRIGADE_GROK_HELLO.'}]})
                    report['live'] = {'status': 'pass' if c.text(sid).strip() == 'BRIGADE_GROK_HELLO' and 'result' in response else 'fail',
                                      'expected_reply_observed': c.text(sid).strip() == 'BRIGADE_GROK_HELLO',
                                      'stop_reason': response.get('result', {}).get('stopReason'),
                                      'error_code': response.get('error', {}).get('code')}
            else:
                report['live'] = {'status': 'not_run'}
        except (TimeoutError, KeyError, ValueError) as error:
            report['probe_error'] = type(error).__name__
        finally:
            if sid is not None:
                try:
                    closed = await c.call('session/close', {'sessionId': sid}, timeout=10)
                    report['session_close_ok'] = 'result' in closed
                except TimeoutError:
                    report['session_close_ok'] = False
            await c.close()
    print(json.dumps(report, indent=2))
    return 1 if 'probe_error' in report or report['live']['status'] in ('blocked', 'fail') else 0


if __name__ == '__main__':
    raise SystemExit(asyncio.run(main()))
