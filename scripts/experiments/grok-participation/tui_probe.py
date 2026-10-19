import asyncio
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
import uuid
import sys

spec = importlib.util.spec_from_file_location('probe', Path(__file__).with_name('probe.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


def tmux(*args):
    return subprocess.check_output(['tmux', *args], text=True).strip()


async def main(report_path, scratch):
    root = Path(tempfile.mkdtemp(prefix='bg120-', dir='/tmp')).resolve()
    cwd = root / 'workspace'
    cwd.mkdir()
    sock = root / 'leader.sock'
    owned = []
    clients = []
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(('CLAUDE', 'CODEX', 'MUSE', 'BRIGADE', 'GROK')) and k != 'AI_AGENT'}
    report = {'scorer_schema': 2, 'version': subprocess.check_output(['grok','--version'],text=True).strip(), 'topology': 'two TUIs plus independent ACP producer on an owned leader', 'checks': []}
    ids = {label: str(uuid.uuid4()) for label in ('A', 'B')}
    scratch.mkdir(parents=True, exist_ok=True)
    paths = {'root': str(root), 'socket': str(sock), 'ids': ids}
    (scratch / 'owned-state.json').write_text(json.dumps(paths))
    def record(name, **data):
        entry = {'name': name, **data}
        report['checks'].append(entry)
        print(json.dumps(entry), flush=True)
        report_path.write_text(json.dumps(report, indent=2)+'\n')
    leader = await asyncio.create_subprocess_exec('grok', 'agent', '--leader-socket', str(sock),
        'leader', '--relay-on-demand', '--no-auto-update', cwd=str(cwd), env=env,
        stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL)
    try:
        for _ in range(100):
            if sock.exists(): break
            await asyncio.sleep(.1)
        if not sock.exists(): raise RuntimeError('owned leader socket did not appear')
        for label, sid in ids.items():
            name = 'bg120-'+root.name+'-'+label.lower()
            launch = root / ('launch-'+label+'.py')
            args = ['grok', '--leader', '--leader-socket', str(sock), '--session-id', sid,
                    '--permission-mode', 'default', '--no-subagents', '--reasoning-effort', 'low', '--rules',
                    'This is a disposable integration test. Do not call tools or delegate. Keep replies short unless the prompt requests a busy test.',
                    'Remember your test label is '+label+'. Reply exactly READY_'+label+'. Do not call tools.']
            launch.write_text('import os\ne={k:v for k,v in os.environ.items() if not k.startswith(("CLAUDE","CODEX","MUSE","BRIGADE","GROK")) and k!="AI_AGENT"}\nos.execvpe("grok",'+repr(args)+',e)\n')
            tmux('new-session','-d','-s',name,'-x','140','-y','45','-c',str(cwd), shlex.join(['python3',str(launch)]))
            owned.append(name)
            record('launch_'+label, created=True)
        for label in ids:
            name = owned[0 if label=='A' else 1]
            ready = False
            for _ in range(120):
                screen = tmux('capture-pane','-p','-S','-150','-t',name)
                # A prompt echo alone is not a content witness. This is readiness only.
                if screen.count('READY_'+label) >= 2 and ('esc to interrupt' not in screen.lower()):
                    ready=True
                    break
                await asyncio.sleep(1)
            record('tui_readiness_'+label, observed=ready)
            if not ready:
                (scratch / ('screen-'+label+'.txt')).write_text(screen)
                raise RuntimeError('TUI readiness not observed; inspect owned screen')
        c = probe.Client(passive=True); clients.append(c)
        init = await c.start(str(cwd), ['--leader','--leader-socket',str(sock)])
        record('producer_initialize', ok='result' in init)
        for label,sid in ids.items():
            response=await c.call('session/resume',{'sessionId':sid,'cwd':str(cwd),'mcpServers':[]},timeout=20)
            record('attach_'+label, ok='result' in response,error_code=response.get('error',{}).get('code'))
            if 'error' in response:
                for tag,name in zip(ids,owned): (scratch / ('screen-'+tag+'.txt')).write_text(tmux('capture-pane','-p','-S','-200','-t',name))
                raise RuntimeError('attach failed')
        # Replay may arrive after resume returns. Drain before the no-send window;
        # prompt-ID correlation below remains mandatory even after this quiet wait.
        await asyncio.sleep(5)
        c.updates.clear()
        idle_id, busy_id, follow_id, hold_id = [str(uuid.uuid4()) for _ in range(4)]
        start=time.monotonic()
        response=await c.call('session/prompt', prompt_params(ids['A'], idle_id,
            'Do not call tools. Join these two pieces with no separator and reply only with the result: GROK120_ and IDLE_A.'), timeout=120)
        text=c.text(ids['A'], idle_id).strip()
        record('idle_A', ok=completed(response, idle_id) and text=='GROK120_IDLE_A',
               correlated=completed(response, idle_id), stop=response.get('result',{}).get('stopReason'),
               elapsed_seconds=round(time.monotonic()-start,2), sibling_agent_text=bool(c.text(ids['B'])),
               reply=text[:300], tui_witness='GROK120_IDLE_A' in tmux('capture-pane','-p','-S','-200','-t',owned[0]))
        c.updates.clear()
        busy=asyncio.create_task(c.call('session/prompt',prompt_params(ids['A'],busy_id,
            'Do not call tools. This is a bounded busy test. Write the numbers 1 through 180, one per line, then finish with ORIGINAL_DONE.'),timeout=120))
        for _ in range(300):
            if c.text(ids['A'], busy_id) or busy.done(): break
            await asyncio.sleep(.05)
        was_busy=not busy.done()
        response=await c.call('session/prompt',prompt_params(ids['A'],follow_id,
            'Do not call tools. Reply only with the joined pieces: GROK120_ and BUSY_A.'),timeout=120)
        original=await busy
        text=c.text(ids['A'], follow_id).strip()
        record('busy_A', request_in_flight=was_busy,
               correlated=completed(original,busy_id) and completed(response,follow_id),
               original_completed=c.text(ids['A'],busy_id).rstrip().endswith('ORIGINAL_DONE'),
               nonce_observed=text=='GROK120_BUSY_A', reply=text[:300],
               original_stop=original.get('result',{}).get('stopReason'), followup_stop=response.get('result',{}).get('stopReason'),
               tui_witness='GROK120_BUSY_A' in tmux('capture-pane','-p','-S','-300','-t',owned[0]), sibling_agent_text=bool(c.text(ids['B'])))
        c.updates.clear()
        record('idle_hold_start', seconds=75)
        await asyncio.sleep(75)
        record('no_send_control', quiet=not c.text(ids['A']) and not c.text(ids['B']))
        response=await c.call('session/prompt',prompt_params(ids['B'],hold_id,
            'Do not call tools. Reply only with the joined pieces: GROK120_ and IDLE_B.'),timeout=120)
        text=c.text(ids['B'],hold_id).strip()
        record('idle_B_after_hold',ok=completed(response,hold_id) and text=='GROK120_IDLE_B',
               correlated=completed(response,hold_id), stop=response.get('result',{}).get('stopReason'),
               sibling_agent_text=bool(c.text(ids['A'])),reply=text[:300],
               tui_witness='GROK120_IDLE_B' in tmux('capture-pane','-p','-S','-200','-t',owned[1]))
        await c.close(); clients.remove(c)
        record('producer_disconnect',tuis_alive=all(tmux('display-message','-p','-t',n,'#{pane_dead}')=='0' for n in owned))
    except Exception as error:
        record('probe_error', kind=type(error).__name__)
    finally:
        for c in clients:
            await c.close()
        for name in owned:
            subprocess.run(['tmux','kill-session','-t',name],capture_output=True)
        if leader.returncode is None:
            leader.terminate()
            try: await asyncio.wait_for(leader.wait(),5)
            except asyncio.TimeoutError:
                leader.kill();await asyncio.wait_for(leader.wait(),5)
        closed = all(subprocess.run(['tmux','has-session','-t',n],capture_output=True).returncode != 0 for n in owned)
        record('cleanup', owned_tuis_closed=closed, leader_reaped=leader.returncode is not None)
    report['status'] = 'pass' if score(report) else 'fail'
    report_path.write_text(json.dumps(report, indent=2)+'\n')
    return 0 if report['status']=='pass' else 1


def prompt_params(sid, prompt_id, text):
    return {'sessionId': sid, '_meta': {'promptId': prompt_id},
            'prompt': [{'type': 'text', 'text': text}]}


def completed(response, prompt_id):
    result = response.get('result', {})
    meta = result.get('_meta', {})
    return ('error' not in response and result.get('stopReason') == 'end_turn'
            and meta.get('promptId') == prompt_id
            and not meta.get('cancellationCategory'))


def score(report):
    checks = {c['name']: c for c in report['checks']}
    required = {
        'tui_readiness_A': ['observed'], 'tui_readiness_B': ['observed'],
        'producer_initialize': ['ok'], 'attach_A': ['ok'], 'attach_B': ['ok'],
        'idle_A': ['ok', 'correlated', 'tui_witness'],
        'busy_A': ['correlated', 'request_in_flight', 'original_completed', 'nonce_observed', 'tui_witness'],
        'no_send_control': ['quiet'], 'idle_B_after_hold': ['ok', 'correlated', 'tui_witness'],
        'producer_disconnect': ['tuis_alive'], 'cleanup': ['owned_tuis_closed', 'leader_reaped'],
    }
    if report.get('scorer_schema') != 2 or 'probe_error' in checks:
        return False
    if not all(checks.get(name, {}).get(key) is True for name, keys in required.items() for key in keys):
        return False
    if any(checks[name].get('sibling_agent_text') is not False for name in ('idle_A','busy_A','idle_B_after_hold')):
        return False
    return (checks['idle_A'].get('reply') == 'GROK120_IDLE_A'
            and checks['idle_B_after_hold'].get('reply') == 'GROK120_IDLE_B'
            and checks['busy_A'].get('reply') == 'GROK120_BUSY_A'
            and checks['idle_A'].get('stop') == 'end_turn'
            and checks['idle_B_after_hold'].get('stop') == 'end_turn'
            and checks['busy_A'].get('original_stop') == 'end_turn'
            and checks['busy_A'].get('followup_stop') == 'end_turn')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Live model test: two owned Grok TUIs and an ACP producer; consumes quota.')
    parser.add_argument('--scratch', type=Path, required=True, help='Private scratch directory for synthetic session IDs and failure screens')
    parser.add_argument('--report', type=Path, required=True, help='JSON result path; parent must exist')
    args = parser.parse_args()
    sys.exit(asyncio.run(main(args.report, args.scratch)))
