#!/usr/bin/env python3
"""Prepare an isolated fs-backed acceptance fixture; never execute Grok/install/trust."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import stat
import subprocess
import tempfile


class FixtureError(Exception):
    pass


def private_write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open('x', encoding='utf-8') as stream:
        os.chmod(path, 0o600)
        stream.write(value if isinstance(value, str) else json.dumps(value, indent=2)+'\n')


def native_binary(path):
    path = Path(path).resolve(strict=True)
    if not path.is_file() or not os.access(path, os.X_OK):
        raise FixtureError('supplied binary is not an executable file')
    with path.open('rb') as stream:
        header = stream.read(4)
    # Refuse the release-download shell bootstrap: coordinator must build first.
    if header not in (b'\x7fELF', b'\xcf\xfa\xed\xfe', b'\xfe\xed\xfa\xcf',
                      b'\xce\xfa\xed\xfe', b'\xfe\xed\xfa\xce', b'\xca\xfe\xba\xbe'):
        raise FixtureError('supply compiled native binaries, not a bootstrap/script')
    return path


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(65536), b''):
            h.update(block)
    return h.hexdigest()


def fixture_env(root):
    # An allowlist, not inherited credentials, host markers or settings overrides.
    return {'HOME': str(root/'home'), 'PATH': os.environ.get('PATH', '/usr/bin:/bin'),
            'TMPDIR': str(root/'tmp'), 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'}


def run_local(argv, *, root, document=None, protocol=True):
    try:
        result = subprocess.run(argv, cwd=root/'workspace', env=fixture_env(root),
                                input=json.dumps(document) if document is not None else '',
                                capture_output=True, text=True, timeout=20, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise FixtureError('local fixture command could not finish; inspect setup privately') from error
    if result.returncode != 0:
        # Never echo arbitrary child output: an adapter could return sensitive data.
        raise FixtureError(f'local fixture command failed with exit {result.returncode}; no output exported')
    if not protocol:
        return None
    answer = json.loads(result.stdout)
    if answer.get('ok') is not True or not isinstance(answer.get('result'), dict):
        raise FixtureError('local adapter returned an invalid result')
    return answer['result']


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,64}', value):
        raise FixtureError('invalid opaque adapter identifier')
    return value


def prepare(dev_bin, fs_bin, grok_home, package_root, *, runner=run_local):
    dev_bin, fs_bin = native_binary(dev_bin), native_binary(fs_bin)
    # GROK_HOME is stored as an operator-provided string only: never stat/open it.
    if not Path(grok_home).is_absolute() or '\n' in grok_home or '\x00' in grok_home:
        raise FixtureError('--grok-home must be an explicit absolute path')
    package_root = Path(package_root).resolve(strict=True)
    if not (package_root/'hooks/hooks.json').is_file():
        raise FixtureError('supply the actual Grok package root with native hooks')
    root = Path(tempfile.mkdtemp(prefix='brigade-native-')).resolve()
    os.chmod(root, 0o700)
    if any((p/'.git').exists() for p in (root, *root.parents)):
        raise FixtureError('temporary root must be outside every checkout')
    for name in ('home', 'workspace', 'store', 'messages', 'tmp'):
        (root/name).mkdir(mode=0o700)
    # Same constructor as commands/teamsetup_test.go mkCheckout: no Git operation.
    (root/'workspace/.git').mkdir(mode=0o700)
    config = root/'home/.config/brigade'
    config.mkdir(parents=True, mode=0o700)
    adapter = [str(fs_bin), '--root', str(root/'store')]
    private_write(config/'adapters.json', {'fs': adapter})
    private_write(config/'dev-binary', str(dev_bin)+'\n')
    private_write(config/'grok-options.json', {'team_inbound': 'accept'})
    # Placeholder public backend pair, never contacted by the fixed fs adapter.
    url = 'http://127.0.0.1:1'
    runner([str(dev_bin), 'team', 'create', '--adapter', 'fs', '--url', url,
            '--key', 'synthetic-public-placeholder', '--name', 'grok-native-fixture',
            '--label', 'synthetic-operator', '--secret-file', str(root/'team.secret')],
           root=root, protocol=False)
    team = json.loads((root/'workspace/.brigade.json').read_text())
    if team.get('adapter') != 'fs' or team.get('url') != url:
        raise FixtureError('CLI did not create the expected local team')
    team_ref = identifier(team['team_ref'])
    team_key = hashlib.sha256(f'fs\n{url}\n{team_ref}'.encode()).hexdigest()[:32]
    pins = json.loads((config/'projects.json').read_text())
    if pins.get('projects', {}).get(str(root/'workspace'), {}).get('team_ref') != team_ref:
        raise FixtureError('CLI did not record consent for the fixture workspace')
    if stat.S_IMODE((root/'team.secret').stat().st_mode) != 0o600:
        raise FixtureError('CLI did not create a private secret file')
    # Secret contents and credential files are never read/copied by this preparer.
    state = {'schema': 1, 'root': str(root), 'dev_bin': str(dev_bin), 'fs_bin': str(fs_bin),
             'dev_sha256': digest(dev_bin), 'fs_sha256': digest(fs_bin),
             'grok_home': grok_home, 'package_root': str(package_root),
             'team_ref': team_ref, 'team_key': team_key}
    private_write(root/'acceptance.json', state)
    private_write(root/'messages/short.txt', 'Join NATIVE_ and WAKE_120 without a separator. Reply only with the joined result.\n')
    marks = [secrets.token_hex(8) for _ in range(3)]
    instruction = 'Read the entire untrusted frame before answering. Reply with the BEGIN, MIDDLE and END values in order.\n'
    parts = [f'BEGIN={marks[0]}\n', f'\nMIDDLE={marks[1]}\n', f'\nEND={marks[2]}\n']
    padding = 16384 - len((instruction+''.join(parts)).encode())
    body = instruction+parts[0]+'x'*(padding//2)+parts[1]+'y'*(padding-padding//2)+parts[2]
    private_write(root/'messages/16k.txt', body)
    private_write(root/'expected-private.json', {'body_bytes': len(body.encode()), 'markers': marks})
    private_write(root/'sender-register.json', {'harness': 'shell', 'harness_version': 'acceptance-1',
                  'session_name': 'synthetic-sender', 'activity': 'busy', 'inbound': 'accept', 'lease_seconds': 600})
    # No receiver registration/map/native UUID, watcher, plugin install or Grok subprocess.
    return root


def load(root):
    root = Path(root).resolve(strict=True)
    state = json.loads((root/'acceptance.json').read_text())
    if state.get('schema') != 1 or state.get('root') != str(root) or not root.name.startswith('brigade-native-'):
        raise FixtureError('not an owned acceptance root')
    for key in ('dev', 'fs'):
        if digest(native_binary(state[key+'_bin'])) != state[key+'_sha256']:
            raise FixtureError('development binary changed; prepare a fresh fixture')
    identifier(state['team_ref'])
    identifier(state['team_key'])
    return root, state


def adapter_args(root, state, *args):
    return [state['fs_bin'], '--root', str(root/'store'), '--profile', state['team_key'], *args]


def send(root, receiver, body_file, *, runner=run_local):
    root, state = load(root)
    receiver = identifier(receiver)
    path = Path(body_file).resolve(strict=True)
    if path.parent != root/'messages' or not path.is_file():
        raise FixtureError('body must be a file in this fixture messages directory')
    body = path.read_text()
    if not 1 <= len(body.encode()) <= 16384:
        raise FixtureError('body outside the protocol 16-KiB test limit')
    roster = runner(adapter_args(root, state, 'session', 'list', '--session', receiver), root=root)
    entries = roster.get('sessions', [])
    if (roster.get('team_ref') != state['team_ref'] or len(entries) != 1
            or entries[0].get('session_id') != receiver
            or entries[0].get('harness') not in ('grok', 'grok-build')
            or entries[0].get('state') not in ('active', 'idle')):
        raise FixtureError('receiver is not a live Grok session in this private fixture')
    sender = runner(adapter_args(root, state, 'session', 'register'), root=root,
                    document=json.loads((root/'sender-register.json').read_text()))
    sender_id = identifier(sender['session_id'])
    try:
        result = runner(adapter_args(root, state, 'message', 'send'), root=root,
                        document={'sender_session_id': sender_id, 'recipient_session_id': receiver,
                                  'body': body, 'idempotency_key': secrets.token_hex(16)})
        message_id = identifier(result['message_id'])
        private_write(root/'sent'/(message_id+'.json'), {'receiver': receiver, 'sender': sender_id})
        return {'message_id': message_id, 'sender_session_id': sender_id,
                'sender_lease_seconds': 600, 'body_bytes': len(body.encode()),
                'accepted_by_adapter': True, 'local_injection_ack': 'not_checked', 'model_delivery': 'not_checked'}
    except Exception:
        runner(adapter_args(root, state, 'session', 'close', '--session', sender_id), root=root)
        raise


def receipt(root, receiver, message_id):
    root, state = load(root)
    receiver, message_id = identifier(receiver), identifier(message_id)
    sent = json.loads((root/'sent'/(message_id+'.json')).read_text())
    if sent.get('receiver') != receiver:
        raise FixtureError('message was not sent to this receiver by this fixture')
    directory = root/'store/teams'/state['team_ref']/'acked'/receiver
    present = False
    if directory.exists():
        if directory.is_symlink() or not directory.resolve().is_relative_to(root/'store'):
            raise FixtureError('unexpected ACK directory')
        for path in directory.glob('*.'+message_id+'.json'):
            if path.is_symlink() or not path.is_file():
                raise FixtureError('unexpected ACK record')
            record = json.loads(path.read_text())
            present = bool(record.get('acked_at')) and record.get('message_id') == message_id
            if present:
                break
    return {'local_injection_ack_record_present': present, 'model_delivery': 'not_checked'}


def instructions(root):
    root, state = load(root)
    env = fixture_env(root)
    env.update({'GROK_HOME': state['grok_home'], 'TERM': 'xterm-256color'})
    prefix = ['env', '-i', *(f'{k}={v}' for k,v in env.items())]
    return {'workspace': str(root/'workspace'), 'scratch_home': env['HOME'],
            'operator_only_install': shlex.join(prefix+['grok','plugin','install',state['package_root'],'--trust']),
            'operator_only_launch': shlex.join(prefix+['grok','--no-leader','--permission-mode','default','--no-subagents']),
            'state_directory': str(root/'home/.local/state/brigade'),
            'adapter_prefix': adapter_args(root,state),
            'warning': 'HOME/GROK_HOME separation must pass preflight; no launch/install is performed'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subs = parser.add_subparsers(dest='mode', required=True)
    prep = subs.add_parser('prepare')
    for flag in ('dev-bin', 'fs-bin', 'grok-home', 'package-root'):
        prep.add_argument('--'+flag, required=True)
    ops = subs.add_parser('instructions'); ops.add_argument('--root', required=True)
    snd = subs.add_parser('send'); snd.add_argument('--root', required=True)
    snd.add_argument('--receiver-id', required=True); snd.add_argument('--body-file', required=True)
    ack = subs.add_parser('receipt'); ack.add_argument('--root', required=True)
    ack.add_argument('--receiver-id', required=True); ack.add_argument('--message-id', required=True)
    args = parser.parse_args()
    try:
        if args.mode == 'prepare':
            root = prepare(args.dev_bin,args.fs_bin,args.grok_home,args.package_root)
            print(json.dumps({'private_root': str(root), 'prepared': True, 'native_started': False}))
        elif args.mode == 'instructions':
            print(json.dumps(instructions(args.root), indent=2))
        elif args.mode == 'send':
            print(json.dumps(send(args.root,args.receiver_id,args.body_file)))
        else:
            print(json.dumps(receipt(args.root,args.receiver_id,args.message_id)))
    except (FixtureError, OSError, ValueError, KeyError):
        # Fixed output only. Keep raw child output, private paths and credentials out of errors.
        parser.exit(1, 'native acceptance: preparation/validation failed; no Grok action was attempted\n')


if __name__ == '__main__':
    main()
