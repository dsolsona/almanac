#!/usr/bin/env python3
"""Inspect the worker-owned Tern daemon; unknown state must never look idle."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

RUNTIME = '/usr/local/lib/almanac-tern'
STATE = '/home/dev/.local/state/almanac-tern'
PROMPT_USED = Path('/home/dev/.codex/almanac-prompt.used')


def active(data, session=None):
    if not isinstance(data['sessions'], list):
        raise ValueError('invalid Tern sessions')
    live = False
    for current in data['sessions']:
        for tab in current['tabs']:
            for block in tab['blocks']:
                if type(block['live']) is not bool or 'exited' not in block:
                    raise ValueError('unknown Tern block state')
                if session is None or current['name'] == session:
                    live |= block['live'] and block['exited'] is None
    return live


def launch(data, env):
    if any(s['name'] == 'codex' for s in data['sessions']):
        subprocess.run([RUNTIME + '/tern', 'kill', 'session', 'codex'], env=env, check=True, timeout=10)
        data = json.loads(subprocess.check_output([RUNTIME + '/tern', 'ls', '--json'], env=env, timeout=10))
    kind = 'tab' if any(s['name'] == 'codex' for s in data['sessions']) else 'session'
    pane = subprocess.check_output([RUNTIME + '/tern', 'new', kind, 'codex', '--cwd',
                                   '/home/dev/almanac/.worktrees/codex', '--keep-open', '--',
                                   '/home/dev/.codex/almanac-run'], env=env, timeout=10).decode().strip()
    if not pane.isdecimal():
        raise RuntimeError('Tern did not identify the new Codex pane')
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        process = json.loads(subprocess.check_output(
            [RUNTIME + '/tern', 'process', pane, '--json'], env=env, timeout=10))
        if process['child'] is None:
            raise RuntimeError('Codex exited before startup; terminal and task file preserved')
        if process['child']['name'] == 'codex':
            return
        time.sleep(.05)
    raise RuntimeError('Codex did not start within five seconds; terminal and task file preserved')


def restore(data, env):
    for session in data['sessions']:
        if session['name'] != 'codex':
            continue
        blocks = [block for tab in session['tabs'] for block in tab['blocks']]
        restored = [b for b in blocks if b.get('args') == ['-l'] and
                    b.get('command') == '/home/dev/.codex/almanac-run' and b['live'] and b['exited'] is None]
        if not restored:
            return
        if len(blocks) != 1:
            print('restored Codex session has other panes; reconnecting without replacing it', file=sys.stderr)
            return
        process = json.loads(subprocess.check_output(
            [RUNTIME + '/tern', 'process', str(restored[0]['id']), '--json'], env=env, timeout=10))
        child, foreground = process['child'], process['foreground']
        if child['name'] != 'bash' or foreground['pid'] != child['pid']:
            return  # A user has already started a program in the restored pane.
        if Path(f"/proc/{int(child['pid'])}/task/{int(child['pid'])}/children").read_text().strip():
            print('restored shell has background work; reconnecting without replacing it', file=sys.stderr)
            return
        # Tern restores login shells. Recreate only the owned idle pane, retaining prompt consumption.
        PROMPT_USED.touch(mode=0o600)
        launch(data, env)
        return


def main():
    env = dict(os.environ, TERN_CONFIG_DIR=STATE + '/config',
               TERN_DAEMON_SOCKET=STATE + '/session.sock', STENCIL_LOG_DIR=STATE + '/logs')
    if not Path(STATE).exists():
        return 1
    if not Path(STATE + '/session.sock').exists():
        status = subprocess.run(['systemctl', 'is-active', '--quiet', 'almanac-tern-session'],
                                capture_output=True, timeout=5)
        if status.returncode == 3:
            return 1
        raise RuntimeError('cannot inspect Tern session socket')
    output = subprocess.check_output([RUNTIME + '/tern', 'ls', '--json'], env=env, timeout=10)
    data = json.loads(output)
    if sys.argv[1] == 'status':
        return 0 if active(data, sys.argv[2] if len(sys.argv) > 2 else None) else 1
    if sys.argv[1] == 'restore':
        active(data)  # Validate the entire reply before considering a replacement.
        restore(data, env)
        return 0
    if sys.argv[1] != 'launch' or active(data, 'codex'):
        raise RuntimeError('Codex session already exists; task preserved')
    launch(data, env)
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, KeyError, TypeError, RuntimeError, OSError, subprocess.SubprocessError) as error:
        print(f'cannot inspect/launch Tern: {error}', file=sys.stderr)
        sys.exit(2)
