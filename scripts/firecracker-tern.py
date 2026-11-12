#!/usr/bin/env python3
"""Connect the laptop's Stencil Tern window to one authenticated worker."""
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time


def run(*args, timeout=15):
    return subprocess.run(['tern', *args], text=True, capture_output=True,
                          check=True, timeout=timeout).stdout


def validate_address(address):
    match = re.fullmatch(r'dev@([0-9.]+)(?::8376)?', address)
    if not match or ipaddress.IPv4Address(match[1]) not in ipaddress.IPv4Network('192.168.240.0/20'):
        raise ValueError('expected a private Firecracker worker address for dev on UDP 8376')


def prepare_identity():
    directory = Path.home() / '.ssh/almanac-firecracker'
    key = directory / 'id_ed25519'
    config = directory.parent / 'config'
    include = directory / 'config'
    backup = directory / 'ssh-config.before'
    for path in (directory, key, key.with_suffix('.pub'), config, include, backup, directory / 'lock'):
        if path.is_symlink():
            raise RuntimeError(f'refusing to replace symlinked SSH state: {path}')
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    directory.chmod(0o700)
    with open(directory / 'lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if not key.exists():
            if key.with_suffix('.pub').exists():
                raise RuntimeError('dedicated SSH private key is missing; existing public key preserved')
            subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-C',
                            'almanac-firecracker', '-f', str(key)],
                           capture_output=True, check=True, timeout=10)
        key.chmod(0o600)
        public = subprocess.check_output(['ssh-keygen', '-y', '-P', '', '-f', str(key)],
                                         text=True, stderr=subprocess.PIPE, timeout=5)
        original = config.read_text() if config.exists() else ''
        directive = 'Include ' + json.dumps(str(include), ensure_ascii=False)
        configured = original
        if not original.startswith(directive + '\n'):
            configured = directive + '\n' + ''.join(
                line for line in original.splitlines(keepends=True) if line.rstrip('\r\n') != directive)
        contents = {
            key.with_suffix('.pub'): public,
            include: '# Managed by Almanac for its reserved Firecracker VM addresses.\n'
                     'Host ' + ' '.join(f'192.168.{octet}.*' for octet in range(240, 256)) + '\n'
                     f'    IdentityFile {json.dumps(str(key), ensure_ascii=False)}\n'
                     '    IdentityAgent none\n    IdentitiesOnly yes\nHost *\n',
        }
        if configured != original:
            if config.exists() and not backup.exists():
                contents[backup] = original
            contents[config] = configured
        for path, content in contents.items():
            if path.exists() and path.read_text() == content:
                continue
            with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as output:
                temporary = Path(output.name)
                try:
                    output.write(content)
                    output.flush()
                    os.replace(temporary, path)
                finally:
                    temporary.unlink(missing_ok=True)
        return public


def check_runtime():
    if not shutil.which('tern') or 'a multiplexing terminal' not in run('--help'):
        raise RuntimeError('install Stencil Tern on the laptop and put its executable on PATH')


def check():
    check_runtime()
    prepare_identity()


def inspect():
    try:
        return json.loads(run('inspect', '--json'))
    except subprocess.CalledProcessError as error:
        if 'no Tern is running' not in error.stderr:
            raise
        return {'clients': []}


def window_command(address, result_path, action):
    # Each launch owns its result; simultaneous Lua calls must not share last_tool.
    operation = '''local host = await(cx.hosts:add(address))
    cx.hosts:switch(address)
    return host.state == "connected"''' if action == 'connect' else '''for _, host in ipairs(cx.hosts:list()) do
        if host.address == address then cx.hosts:disconnect(address) end
    end
    return true'''
    lua = f'''local result_path = {json.dumps(str(result_path))}
local address = {json.dumps(address)}
local ok, result = pcall(function()
    {operation}
end)
tern.fs.write(result_path, tern.json.encode({{ok = ok and result == true, error = tostring(result)}}))'''
    try:
        reply = json.loads(run('ctl', 'carly', 'lua', json.dumps(lua), timeout=25))
        if not reply.get('ok'):
            raise RuntimeError(reply.get('error', 'Tern rejected the connection command'))
    except Exception as error:
        result_path.write_text(json.dumps({'ok': False, 'error': str(error)}))


def wait_result(path):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if path.exists():
            try:
                result = json.loads(path.read_text())
            except json.JSONDecodeError:
                pass  # Tern's small result write is not atomic.
            else:
                if result.get('ok') is not True:
                    raise RuntimeError('Tern connection failed: ' + str(result.get('error', 'unknown result')))
                return
        time.sleep(.05)
    raise RuntimeError('Tern did not confirm its connection within 30 seconds')


def control(address, action):
    with tempfile.TemporaryDirectory(prefix='almanac-tern-connect-') as directory:
        result_path = Path(directory) / 'result.json'
        if os.environ.get('TERN_PANE') and os.environ.get('TERN_PANE_SOCKET'):
            window_command(address, result_path, action)
        else:
            run('new', 'session', 'almanac-connect-' + Path(directory).name.rsplit('-', 1)[-1],
                '--end-on-success', '--', sys.executable, str(Path(__file__).resolve()),
                '_window', address, str(result_path), action)
        wait_result(result_path)


def connect(address, fingerprint):
    validate_address(address)
    if not re.fullmatch(r'SHA256:[A-Za-z0-9+/]{43}', fingerprint):
        raise ValueError('invalid remote host fingerprint')
    run('remote', 'trust', address, fingerprint)
    data = inspect()
    if not any(client.get('kind') == 'window' for client in data.get('clients', [])):
        # Opening the installed app keeps its existing desktop authentication.
        with open(os.devnull, 'w') as sink:
            subprocess.Popen(['tern'], stdout=sink, stderr=sink, start_new_session=True)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            data = inspect()
            if any(c.get('kind') == 'window' for c in data.get('clients', [])):
                break
            time.sleep(.1)
        else:
            raise RuntimeError('local Tern window did not open; start Tern and use vm-codex to reconnect')
    control(address, 'connect')


def remove_trust(address, fingerprint):
    config = Path(os.environ.get('TERN_CONFIG_DIR') or
                  str(Path(os.environ.get('XDG_CONFIG_HOME') or str(Path.home() / '.config')) / 'tern'))
    pins = config / 'known_hosts'
    if not pins.exists():
        return
    if pins.is_symlink():
        raise RuntimeError('refusing to replace a symlinked Tern trust file')
    # Tern 0.9.16 forgets registration but retains pins; use its sibling file lock.
    with open(config / '.known_hosts.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        lines = pins.read_bytes().splitlines(keepends=True)
        endpoint = address.removeprefix('dev@').removesuffix(':8376') + ':8376'
        keep = []
        for line in lines:
            fields = line.split()
            if fields and fields[0] == endpoint.encode():
                if len(fields) != 2 or fields[1] != fingerprint.encode():
                    raise RuntimeError('Tern identity changed; local trust and worker disk preserved')
            else:
                keep.append(line)
        with tempfile.NamedTemporaryFile(dir=config, prefix='.known_hosts.', delete=False) as output:
            temporary = Path(output.name)
            try:
                output.write(b''.join(keep))
                output.flush()
                os.replace(temporary, pins)
            finally:
                temporary.unlink(missing_ok=True)


def forget(address, fingerprint):
    validate_address(address)
    if not re.fullmatch(r'SHA256:[A-Za-z0-9+/]{43}', fingerprint):
        raise ValueError('invalid worker fingerprint')
    remove_trust(address, fingerprint)
    try:
        run('remote', 'forget', address)
    except subprocess.CalledProcessError as error:
        if error.returncode != 1 or error.stderr.strip() != f'tern remote forget: {address} is not a saved host':
            raise
    if any(c.get('kind') == 'window' for c in inspect().get('clients', [])):
        control(address, 'forget')


def main():
    action = sys.argv[1]
    if action == 'check':
        check()
    elif action == 'check-runtime':
        check_runtime()
    elif action == 'keys':
        print(prepare_identity(), end='')
    elif action == 'connect':
        connect(*sys.argv[2:])
    elif action == '_window':
        window_command(sys.argv[2], Path(sys.argv[3]), sys.argv[4])
    elif action == 'forget':
        forget(*sys.argv[2:])
    else:
        raise ValueError('unknown Tern controller action')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError) as error:
        print(f'firecracker: {error}', file=sys.stderr)
        sys.exit(1)
