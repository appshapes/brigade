#!/usr/bin/env python3
"""Owned synthetic monitor fixture. Prepare only; never installs or launches Grok."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import time


def birth(pid):
    """Read only the selected process's start identity; never signal it."""
    try:
        result = subprocess.run(['ps', '-p', str(pid), '-o', 'lstart='],
                                capture_output=True, text=True, timeout=1)
    except (OSError, subprocess.TimeoutExpired):
        return ''
    return result.stdout.strip() if result.returncode == 0 else ''


def incarnation(sid, pid, started):
    return hashlib.sha256(f'{sid}\n{pid}\n{started}'.encode()).hexdigest()


def prepare():
    root = Path(tempfile.mkdtemp(prefix='brigade-monitor-')).resolve()
    workspace, plugin = root / 'workspace', root / 'plugin'
    workspace.mkdir(mode=0o700)
    state = workspace / '.monitor-fixture'
    state.mkdir(mode=0o700)
    (state / 'trigger.txt').touch(mode=0o600)
    (plugin / 'hooks').mkdir(parents=True)
    shutil.copyfile(__file__, plugin / 'fixture.py')
    name = 'brigade-monitor-' + root.name.removeprefix('brigade-monitor-')
    (plugin / 'plugin.json').write_text(json.dumps({'name': name, 'version': '0.0.1'}))
    command = ('exec python3 "${GROK_PLUGIN_ROOT}/fixture.py" hook --workspace '
               + shlex.quote(str(workspace)))
    hooks = {event: [{'hooks': [{'type': 'command', 'command': command,
                                'timeout': 1 if event == 'SessionEnd' else 10}]}]
             for event in ('SessionStart', 'Stop', 'SessionEnd')}
    (plugin / 'hooks' / 'hooks.json').write_text(json.dumps({'hooks': hooks}, indent=2))
    # These private operator paths are never exported as evidence.
    print(json.dumps({'root': str(root), 'workspace': str(workspace),
                      'plugin': str(plugin), 'plugin_name': name}, indent=2))


def hook(workspace):
    # Installed globally but inert outside this exact disposable workspace.
    # This guard deliberately precedes reading stdin, which can contain prompts.
    if Path.cwd().resolve() != workspace or os.environ.get('GROK_LEADER_SOCKET'):
        return {}
    sid = os.environ.get('GROK_SESSION_ID', '')
    if not sid:
        return {}
    raw = sys.stdin.buffer.read(131073)
    if len(raw) > 131072:
        return {}
    event = json.loads(raw)
    if event.get('sessionId', event.get('session_id')) != sid or event.get('subagentType'):
        return {}
    kind = event.get('hookEventName', event.get('hook_event_name'))
    state = workspace / '.monitor-fixture'
    if kind in ('session_start', 'SessionStart'):
        return {'systemMessage': 'Synthetic monitor fixture: setup follows one completed turn.'}
    pid = os.getppid()
    # End-hook cleanup is bounded: the follower also checks parent birth and TTL.
    if kind in ('session_end', 'SessionEnd'):
        for path in list(state.glob('*.owner.json'))[:16]:
            owner = json.loads(path.read_text())
            if owner['pid'] == pid and owner['native_id'] == sid:
                path.with_suffix('.ended').touch(mode=0o600)
        return {}
    if (kind not in ('stop', 'Stop') or event.get('reason') != 'end_turn'
            or event.get('stopHookActive', event.get('stop_hook_active', False))):
        return {}
    started = birth(pid)
    if not started:
        return {}
    key = incarnation(sid, pid, started)
    owner_path = state / (key + '.owner.json')
    try:
        with owner_path.open('x') as out:
            json.dump({'pid': pid, 'started': started, 'native_id': sid}, out)
    except FileExistsError:
        return {}
    command = shlex.join(['python3', str(Path(__file__).resolve()), 'follow',
                         '--workspace', str(workspace), '--incarnation', key])
    return {'decision': 'block', 'reason':
            'Synthetic test: call the native monitor tool once with persistent:true and command '
            + command + '. Use normal permissions. After it starts reply AUTO_MONITOR_ARMED. '
            'If it fails or permission is denied, explain the failure and stop; do not retry automatically.'}


def follow(workspace, key):
    if len(key) != 64 or any(c not in '0123456789abcdef' for c in key):
        raise ValueError('invalid fixture incarnation')
    state = workspace / '.monitor-fixture'
    owner_path = state / (key + '.owner.json')
    owner = json.loads(owner_path.read_text())
    if os.environ.get('GROK_SESSION_ID') != owner['native_id'] or os.environ.get('GROK_LEADER_SOCKET'):
        return
    # Finite experiment only: no indefinite polling, heartbeats or diagnostic stream.
    deadline = time.monotonic() + 3600
    with (state / 'trigger.txt').open() as stream:
        while time.monotonic() < deadline:
            if (owner_path.with_suffix('.ended').exists()
                    or birth(owner['pid']) != owner['started']):
                return
            position = stream.tell()
            line = stream.readline(16385)
            if line:
                if not line.endswith('\n'):
                    stream.seek(position)
                    if len(line) > 16384:
                        return
                else:
                    print(line.rstrip('\n'), flush=True)
            time.sleep(.25)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('prepare', 'hook', 'follow'))
    parser.add_argument('--workspace', type=Path)
    parser.add_argument('--incarnation')
    args = parser.parse_args()
    if args.mode == 'prepare':
        prepare()
    elif args.workspace is None:
        parser.error('--workspace is required')
    elif args.mode == 'hook':
        print(json.dumps(hook(args.workspace.resolve())))
    elif args.incarnation is None:
        parser.error('--incarnation is required')
    else:
        follow(args.workspace.resolve(), args.incarnation)


if __name__ == '__main__':
    main()
