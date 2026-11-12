#!/usr/bin/env python3
"""Focused native Tern worker checks; external programs use private fixtures."""
import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import sys

root = Path(__file__).resolve().parent.parent
launcher = root / 'scripts/firecracker-setup.sh'

# Exercise the normal run dispatcher: provisioning must precede launch/connection.
with tempfile.TemporaryDirectory() as directory:
    state = Path(directory)
    (state / 'base-image').mkdir()
    result = subprocess.run(['bash', '-c', '''source "$1"
check() { :; }
check_legacy_agent() { :; }
reserve_network() { :; }
check_local_tern() { echo preflight; }
setup() { echo setup; }
start() { echo start; }
prepare_tern() { echo prepare-tern; }
launch_agent() { echo launch; }
connect_tern() { echo connect; }
main run worker --prompt 'a task'
''', 'test', str(launcher)], env=dict(os.environ, FIRECRACKER_STATE_DIR=directory),
        text=True, capture_output=True, timeout=10)
    assert result.returncode == 0, result.stderr
    calls = [x for x in result.stdout.splitlines() if x in
             ('preflight', 'setup', 'start', 'prepare-tern', 'launch', 'connect')]
    assert calls == ['preflight', 'setup', 'start', 'prepare-tern', 'launch', 'connect'], calls
    # A missing local prerequisite must fail before reserving any worker state.
    result = subprocess.run(['bash', '-c', '''source "$1"
check() { :; }
check_local_tern() { return 1; }
main run missing --prompt task
''', 'test', str(launcher)], env=dict(os.environ, FIRECRACKER_STATE_DIR=directory),
        text=True, capture_output=True, timeout=10)
    assert result.returncode != 0 and not (state / 'instances/missing').exists()
    # A UI failure after launch must not discard the prepared worker or task.
    result = subprocess.run(['bash', '-c', '''source "$1"
check() { :; }
check_local_tern() { :; }
check_legacy_agent() { :; }
reserve_network() { :; }
setup() { touch "$state/ready"; }
start() { :; }
prepare_tern() { :; }
launch_agent() { printf '%s' "$prompt" > "$state/started-task"; }
connect_tern() { return 1; }
main run ui-failure --prompt 'preserved task'
''', 'test', str(launcher)], env=dict(os.environ, FIRECRACKER_STATE_DIR=directory),
        text=True, capture_output=True, timeout=10)
    assert result.returncode != 0
    assert (state / 'instances/ui-failure/ready').exists()
    assert (state / 'instances/ui-failure/started-task').read_text() == 'preserved task'
    worker = state / 'instances/reconnect'
    worker.mkdir()
    (worker / 'ready').touch()
    (worker / 'tern-address').write_text('dev@192.168.240.18\n')
    result = subprocess.run(['bash', '-c', '''source "$1"
check() { :; }
check_local_tern() { :; }
start() { :; }
prepare_tern() { echo prepare; }
restore_tern_agent() { echo restore; }
launch_agent() { echo launch; }
connect_tern() { echo connected; }
ssh_guest() { echo 'unexpected legacy attachment' >&2; return 99; }
main codex reconnect
''', 'test', str(launcher)], env=dict(os.environ, FIRECRACKER_STATE_DIR=directory),
        text=True, capture_output=True, timeout=10)
    assert result.returncode == 0 and result.stdout.splitlines() == ['prepare', 'restore', 'launch', 'connected'], result
    result = subprocess.run(['bash', '-c', '''source "$1"
check() { :; }
check_tern_runtime() { return 1; }
reserve_network() { :; }
prepare_base() { :; }
main base missing-runtime
''', 'test', str(launcher)], env=dict(os.environ, FIRECRACKER_STATE_DIR=directory),
        text=True, capture_output=True, timeout=10)
    assert result.returncode != 0 and not (state / 'instances/missing-runtime').exists(), 'base skipped Tern runtime preflight'

def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, root / 'scripts' / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

host = load('firecracker_tern', 'firecracker-tern.py')
guest = load('firecracker_tern_guest', 'firecracker-tern-guest.py')

for address in ('dev@192.168.240.14', 'dev@192.168.241.2:8376'):
    host.validate_address(address)
for address in ('root@192.168.240.14', 'dev@127.0.0.1', 'dev@8.8.8.8',
                'dev@192.168.240.14;touch /tmp/injected', 'dev@192.168.240.14:22'):
    try:
        host.validate_address(address)
    except ValueError:
        pass
    else:
        raise AssertionError(f'unsafe worker address accepted: {address}')

def listing(live=True, exited=None, name='codex'):
    return {'sessions': [{'name': name, 'tabs': [{'blocks': [{'live': live, 'exited': exited}]}]}]}

assert guest.active(listing(), 'codex')
assert guest.active(listing(name='other'))
assert not guest.active(listing(name='other'), 'codex')
assert not guest.active(listing(False, 0), 'codex')
for malformed in ({}, {'sessions': None}, {'sessions': [{'name': 'codex'}]}):
    try:
        guest.active(malformed, 'codex')
    except (ValueError, KeyError, TypeError):
        pass
    else:
        raise AssertionError('unknown terminal state was treated as idle')

with tempfile.TemporaryDirectory(prefix='almanac-tern-guest-') as directory:
    folder = Path(directory)
    (folder / 'session.sock').touch()
    (folder / 'sessions.json').write_text(json.dumps(listing(False, 0)))
    native = folder / 'tern'
    native.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
folder = Path(__file__).parent
args = sys.argv[1:]
if args == ['ls', '--json']:
 print((folder / 'sessions.json').read_text())
elif args[:1] == ['process']:
 print((folder / 'process.json').read_text())
else:
 with open(folder / 'native-calls', 'a') as log:
  log.write(json.dumps(args) + '\\n')
 if args[:2] == ['kill', 'session']:
  (folder / 'sessions.json').write_text(json.dumps({'sessions': [{'name': 'codex', 'tabs': []}]}))
 elif args[:2] == ['new', 'session']:
  if any(s['name'] == 'codex' for s in json.loads((folder / 'sessions.json').read_text())['sessions']):
   sys.exit('a session is already called codex')
 elif args[:2] == ['new', 'tab']:
  child = None if os.environ.get('TERN_TEST_START_FAIL') else {'pid': 123, 'name': 'codex'}
  (folder / 'process.json').write_text(json.dumps({'child': child}))
  print('3')
''')
    native.chmod(0o700)
    guest.RUNTIME = guest.STATE = str(folder)
    guest.PROMPT_USED = folder / 'prompt.used'
    previous = sys.argv
    try:
        sys.argv = ['session.py', 'launch']
        assert guest.main() == 0
        calls = [json.loads(line) for line in (folder / 'native-calls').read_text().splitlines()]
        assert calls[0] == ['kill', 'session', 'codex']
        assert calls[1] == ['new', 'tab', 'codex', '--cwd', '/home/dev/almanac/.worktrees/codex',
                            '--keep-open', '--', '/home/dev/.codex/almanac-run']
        before = (folder / 'native-calls').read_text()
        for state in (listing(), {}):
            (folder / 'sessions.json').write_text(json.dumps(state))
            try:
                guest.main()
            except (RuntimeError, KeyError):
                pass
            else:
                raise AssertionError('active or unknown state allowed another task')
            assert (folder / 'native-calls').read_text() == before
        restored = listing()
        restored['sessions'][0]['tabs'][0]['blocks'][0].update(
            id=3, program='/bin/bash', args=['-l'], command='/home/dev/.codex/almanac-run')
        (folder / 'sessions.json').write_text(json.dumps(restored))
        shell = {'pid': os.getpid(), 'name': 'bash'}
        (folder / 'process.json').write_text(json.dumps({'child': shell, 'foreground': {'pid': os.getpid() + 1}}))
        sys.argv = ['session.py', 'restore']
        assert guest.main() == 0
        assert (folder / 'native-calls').read_text() == before, 'foreground work was replaced'
        (folder / 'process.json').write_text(json.dumps({'child': shell, 'foreground': shell}))
        background = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(10)'])
        try:
            warning = io.StringIO()
            with contextlib.redirect_stderr(warning):
                assert guest.main() == 0, 'protected work blocked reconnection'
            assert 'background work' in warning.getvalue()
            assert (folder / 'native-calls').read_text() == before
            assert not guest.PROMPT_USED.exists(), 'protected restore consumed the task'
        finally:
            background.terminate()
            background.wait(timeout=5)
        multiple = copy.deepcopy(restored)
        multiple['sessions'][0]['tabs'][0]['blocks'].append({'live': True, 'exited': None})
        (folder / 'sessions.json').write_text(json.dumps(multiple))
        warning = io.StringIO()
        with contextlib.redirect_stderr(warning):
            assert guest.main() == 0, 'extra panes blocked reconnection'
        assert 'other panes' in warning.getvalue()
        assert (folder / 'native-calls').read_text() == before
        assert not guest.PROMPT_USED.exists()
        (folder / 'sessions.json').write_text(json.dumps(restored))
        (folder / 'process.json').write_text('{}')
        try:
            guest.main()
        except KeyError:
            pass
        else:
            raise AssertionError('unknown restored process allowed replacement')
        assert (folder / 'native-calls').read_text() == before
        (folder / 'process.json').write_text(json.dumps({'child': shell, 'foreground': shell}))
        assert guest.main() == 0
        calls = [json.loads(line) for line in (folder / 'native-calls').read_text().splitlines()]
        assert calls[-2] == ['kill', 'session', 'codex'], 'restored login shell was mistaken for Codex'
        assert calls[-1][0:3] == ['new', 'tab', 'codex']
        assert guest.PROMPT_USED.exists(), 'restoring a shell could replay the initial task'
        (folder / 'sessions.json').write_text(json.dumps(listing(False, 0)))
        sys.argv = ['session.py', 'launch']
        os.environ['TERN_TEST_START_FAIL'] = '1'
        try:
            guest.main()
        except RuntimeError as error:
            assert 'exited before startup' in str(error)
        else:
            raise AssertionError('a dead Codex pane was reported as started')
        finally:
            del os.environ['TERN_TEST_START_FAIL']
    finally:
        sys.argv = previous

with tempfile.TemporaryDirectory(prefix='almanac-tern-') as directory:
    folder = Path(directory)
    fake = folder / 'tern'
    fake.write_text('''#!/usr/bin/env python3
import json, os, re, sys
from pathlib import Path
args = sys.argv[1:]
with open(os.environ['TERN_TEST_LOG'], 'a') as log:
 log.write(json.dumps(args) + '\\n')
if args == ['--help']:
 print('Tern — a multiplexing terminal')
elif args == ['--version']:
 print('tern 0.9.16 (fixture)')
elif args == ['inspect', '--json']:
 print(json.dumps({'clients': [{'kind': 'window', 'window': ''}]}))
elif args[:2] == ['remote', 'trust']:
 pass
elif args[:2] == ['remote', 'forget']:
 pass
elif args[:3] == ['ctl', 'carly', 'lua']:
 assert '\\n' not in args[3], 'control transport is one command per line'
 script = json.loads(args[3])
 path = json.loads(re.search(r'local result_path = ("(?:[^"\\\\]|\\\\.)*")', script)[1])
 Path(path).write_text(json.dumps({'ok': not bool(os.environ.get('TERN_TEST_FAIL')), 'error': 'fixture connection refused'}))
 print('{"ok":true}')
elif args[:2] == ['new', 'session']:
 import subprocess
 sys.exit(subprocess.run(args[args.index('--') + 1:]).returncode)
else:
 sys.exit(91)
''')
    fake.chmod(0o700)
    log = folder / 'commands.jsonl'
    original = os.environ.copy()
    os.environ.update(PATH=str(folder) + os.pathsep + os.environ['PATH'],
                      TERN_TEST_LOG=str(log), TERN_PANE='5', TERN_PANE_SOCKET='fixture',
                      TERN_CONFIG_DIR=str(folder / 'config'))
    try:
        host.connect('dev@192.168.240.14', 'SHA256:' + 'A' * 43)
        commands = [json.loads(line) for line in log.read_text().splitlines()]
        assert ['remote', 'trust', 'dev@192.168.240.14', 'SHA256:' + 'A' * 43] in commands
        lua = json.loads(next(c[3] for c in commands if c[:3] == ['ctl', 'carly', 'lua']))
        assert 'cx.hosts:add(address)' in lua and 'cx.hosts:switch(address)' in lua
        os.environ['TERN_TEST_FAIL'] = '1'
        try:
            host.connect('dev@192.168.240.14', 'SHA256:' + 'A' * 43)
        except RuntimeError as error:
            assert 'fixture connection refused' in str(error)
        else:
            raise AssertionError('connect command acceptance was mistaken for a successful connection')
        del os.environ['TERN_TEST_FAIL']
        del os.environ['TERN_PANE']
        del os.environ['TERN_PANE_SOCKET']
        host.connect('dev@192.168.240.18', 'SHA256:' + 'B' * 43)
        commands = [json.loads(line) for line in log.read_text().splitlines()]
        assert any(c[:2] == ['new', 'session'] and '--end-on-success' in c for c in commands)
        host.forget('dev@192.168.240.18', 'SHA256:' + 'B' * 43)
        commands = [json.loads(line) for line in log.read_text().splitlines()]
        forgotten = commands.index(['remote', 'forget', 'dev@192.168.240.18'])
        disconnected = next(i for i, c in enumerate(commands)
                            if c[:3] == ['ctl', 'carly', 'lua'] and 'cx.hosts:disconnect(address)' in json.loads(c[3]))
        assert forgotten < disconnected, 'forget trust before disconnecting the live host'
        pins = folder / 'config/known_hosts'
        pins.parent.mkdir()
        other = '192.168.240.14:8376 SHA256:' + 'C' * 43 + '\n'
        own = '192.168.240.18:8376 SHA256:' + 'B' * 43 + '\n'
        pins.write_text(other + own)
        host.forget('dev@192.168.240.18', 'SHA256:' + 'B' * 43)
        assert pins.read_text() == other, 'deletion left its pin or removed another host pin'
        assert pins.stat().st_mode & 0o777 == 0o600
        pins.write_text(other + own)
        before = log.read_text()
        try:
            host.forget('dev@192.168.240.18', 'SHA256:' + 'D' * 43)
        except RuntimeError:
            pass
        else:
            raise AssertionError('deletion removed a changed identity')
        assert pins.read_text() == other + own and log.read_text() == before
    finally:
        os.environ.clear()
        os.environ.update(original)

print('Firecracker Tern checks passed')
