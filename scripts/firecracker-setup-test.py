#!/usr/bin/env python3
"""Check the Firecracker launcher's offline safety boundaries without privileges."""
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import shutil
import subprocess
import tempfile
import tarfile
import termios
import time
import tomllib


root = Path(__file__).resolve().parent.parent
relative_script = Path('scripts/firecracker-setup.sh')
script = root / relative_script


def run(*args, env=None):
    return subprocess.run(
        ["bash", str(script), *args], text=True, capture_output=True, env=env,
        timeout=10,
    )


assert script.is_file(), "scripts/firecracker-setup.sh is missing"
with tempfile.TemporaryDirectory(prefix='almanac-shared-vms-') as directory:
    primary = Path(directory) / 'primary checkout'
    primary.mkdir()
    (primary / 'scripts').mkdir()
    shutil.copy(script, primary / relative_script)
    def fixture_git(*args):
        return subprocess.run(['git', '-C', str(primary), *args], check=True,
                              text=True, capture_output=True).stdout
    fixture_git('init', '-q')
    fixture_git('config', 'user.name', 'Launcher fixture')
    fixture_git('config', 'user.email', 'fixture@example.invalid')
    fixture_git('add', str(relative_script))
    fixture_git('commit', '-qm', 'launcher fixture')
    worktrees = [primary]
    for index in range(2):
        linked = primary / '.worktrees' / f'worker {index}'
        fixture_git('worktree', 'add', '-qb', f'worker-{index}', str(linked))
        worktrees.append(linked)
    default_env = os.environ.copy()
    default_env.pop('FIRECRACKER_STATE_DIR', None)
    for checkout in worktrees:
        result = subprocess.run(['bash', '-c', 'source "$1"; printf "%s\\n" "$base" "$repo" "$launcher_repo"',
                                 'test', str(checkout / relative_script)], env=default_env,
                                text=True, capture_output=True, timeout=10)
        assert result.returncode == 0, result.stderr
        assert result.stdout.splitlines() == [str(primary / '.local/firecracker'), str(checkout), str(checkout)], \
            'launcher relocation changed shared state, source root or helper lookup'
        assert not (checkout / '.local').exists(), 'resolving state created files'
    shared = primary / '.local/firecracker/instances/shared-worker'
    shared.mkdir(parents=True)
    (shared / 'ready').touch()
    (shared / 'network-slot').write_text('1\n')
    for checkout in worktrees:
        result = subprocess.run(['bash', str(checkout / relative_script), 'list'],
                                env=default_env, text=True, capture_output=True, timeout=10)
        assert result.returncode == 0 and 'shared-worker' in result.stdout, result
        assert '192.168.240.6' in result.stdout
    override = Path(directory) / 'custom state'
    result = subprocess.run(['bash', '-c', 'source "$1"; printf "%s" "$base"',
                             'test', str(worktrees[-1] / relative_script)],
                            env=dict(default_env, FIRECRACKER_STATE_DIR=str(override)),
                            text=True, capture_output=True, timeout=10)
    assert result.returncode == 0 and result.stdout == str(override), result
    assert not override.exists()

for guest_program in re.findall(r"<<'GUEST'\n(.*?)\nGUEST", script.read_text(), re.S):
    result = subprocess.run(["bash", "-n"], input=guest_program, text=True, capture_output=True)
    assert result.returncode == 0, result.stderr
with tempfile.TemporaryDirectory(prefix="almanac-firecracker-") as directory:
    state = Path(directory) / "private state"
    env = dict(os.environ, FIRECRACKER_STATE_DIR=str(state))
    for action in ("--help", "status", "stop", "list"):
        result = run(action, env=env)
        assert result.returncode == 0, (action, result.stderr)
        assert not state.exists(), f"{action} created state"
    result = run("destroy", env=env)
    assert result.returncode != 0 and not state.exists()
    result = run("delete", env=env)
    assert result.returncode != 0 and not state.exists(), "delete requires an explicit name"
    for name in ("api", "ui"):
        result = run("status", name, env=env)
        assert result.returncode == 0 and "stopped" in result.stdout, result.stderr
        assert not state.exists(), "named status created state"
    for name in ("../escape", "bad;command", "-option", "a" * 33):
        result = run("status", name, env=env)
        assert result.returncode != 0 and not state.exists(), result
    result = run("run", "api", env=env)
    assert result.returncode != 0 and "prompt" in result.stderr.lower(), result.stderr
    result = run("run", "api", "--prompt", "", env=env)
    assert result.returncode != 0 and not state.exists(), result.stderr

    # Missing bases must fail before allocating names, addresses or instance files.
    preflight = '''source "$1"
shift
check() { :; }
check_local_tern() { :; }
check_tern_runtime() { :; }
check_legacy_agent() { :; }
prepare_tern() { :; }
connect_tern() { :; }
start() { :; }
launch_agent() { :; }
prepare_base() { printf 'seed preparation\\n'; }
main "$@"
'''
    missing_base = Path(directory) / "missing-base"
    preflight_env = dict(env, FIRECRACKER_STATE_DIR=str(missing_base))
    for args in (("setup",), ("setup", "api"), ("setup", "almanac"),
                 ("run", "--prompt", "task"), ("run", "api", "--prompt", "task")):
        result = subprocess.run(
            ["bash", "-c", preflight, "test", str(script), *args],
            env=preflight_env, text=True, capture_output=True, timeout=10,
        )
        assert result.returncode != 0 and "prepare shared dependencies first" in result.stderr, result
        assert not missing_base.exists(), "missing base consumed a name or network slot"
    ready_guest = missing_base / "instances/existing"
    ready_guest.mkdir(parents=True)
    (ready_guest / "ready").touch()
    for args in (("setup", "existing"), ("run", "existing", "--prompt", "task"), ("base", "seed")):
        result = subprocess.run(
            ["bash", "-c", preflight, "test", str(script), *args],
            env=preflight_env, text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr

    # Real terminal editing and EOF/cancellation must finish before any VM work.
    def own_terminal():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)

    terminal_parser = '''source "$1"
check() { printf '%s' "$prompt" > "$CAPTURE_TYPED_TASK"; exit 0; }
main run --prompt -
'''
    for cancel in (False, True):
        terminal, terminal_input = pty.openpty()
        original_terminal = termios.tcgetattr(terminal_input)
        capture = Path(directory) / f"typed-task-{cancel}"
        interactive = subprocess.Popen(
            ["bash", "-c", terminal_parser, "test", str(script)],
            env=dict(env, TERM="xterm-256color", INPUTRC="/dev/null", CAPTURE_TYPED_TASK=str(capture)),
            stdin=terminal_input, stdout=terminal_input, stderr=terminal_input,
            preexec_fn=own_terminal,
        )
        try:
            readable, _, _ = select.select([terminal], [], [], 1)
            assert readable, "interactive run silently waits for stdin"
            message = os.read(terminal, 4096).decode()
            assert "task" in message.lower() and "Ctrl-D" in message, message
            assert not state.exists(), "waiting for a task created VM state"
            deadline = time.monotonic() + 1
            while termios.tcgetattr(terminal_input)[3] & termios.ICANON:
                assert time.monotonic() < deadline, "line editor did not start"
                time.sleep(0.005)
            if cancel:
                os.write(terminal, b"unfinished\x03")
            else:
                os.write(terminal, b"Sessions expire quicklx\x7fy.\nMake them laster\x7f\x7f longerx\x08.\ncodx\x1b[De\n\x04")
            try:
                interactive.wait(timeout=3)
            except subprocess.TimeoutExpired:
                raise AssertionError("terminal input did not finish after Ctrl-C/Ctrl-D") from None
            if cancel:
                assert interactive.returncode != 0 and not capture.exists()
            else:
                tail = os.read(terminal, 4096) if select.select([terminal], [], [], 0)[0] else b""
                assert interactive.returncode == 0, tail
                assert capture.read_text() == "Sessions expire quickly.\nMake them last longer.\ncodex\n"
            assert termios.tcgetattr(terminal_input) == original_terminal, "terminal settings were not restored"
            assert not state.exists(), "terminal parser started VM setup"
        finally:
            if interactive.poll() is None:
                interactive.kill()
                interactive.wait(timeout=5)
            os.close(terminal)
            os.close(terminal_input)

    # Sealing a dependency image must remove seed identity/work without deleting caches.
    seed = Path(directory) / "seed-filesystem"
    for folder in ("etc/ssh", "var/lib/dbus", "var/lib/docker", "var/lib/containerd", "usr/local/lib/almanac-tern",
                   "home/dev/.codex", "root/.ssh", "tmp", "var/tmp", "var/log"):
        (seed / folder).mkdir(parents=True, exist_ok=True)
    for relative in ("etc/ssh/ssh_host_ed25519_key", "etc/ssh/ssh_host_ed25519_key.pub",
                     "var/lib/dbus/machine-id", "var/lib/docker/engine-id", "home/dev/.codex/auth.json",
                     "root/.ssh/authorized_keys", "tmp/token", "var/tmp/private", "var/log/history"):
        (seed / relative).write_text("private seed data")
    (seed / "etc/machine-id").write_text("old machine identity")
    (seed / "var/lib/docker/cache-fixture").write_text("warmed dependencies")
    (seed / "var/lib/containerd/image-fixture").write_text("cached images")
    (seed / 'usr/local/lib/almanac-tern/tern').write_text('Tern runtime')
    (seed / 'usr/local/lib/almanac-tern/session.py').write_text('Tern session helper')
    for service in ('almanac-tern-session.service', 'almanac-tern-remote.service'):
        units = seed / 'etc/systemd/system'
        (units / 'multi-user.target.wants').mkdir(parents=True, exist_ok=True)
        (units / service).write_text('seed terminal service')
        (units / 'multi-user.target.wants' / service).symlink_to('../' + service)
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; sudo() { "$@"; }; sanitize_image "$2"',
         "test", str(script), str(seed)], text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    for relative in ("home/dev", "root/.ssh", "etc/ssh/ssh_host_ed25519_key",
                     "var/lib/dbus/machine-id", "var/lib/docker/engine-id", "tmp/token",
                     "var/tmp/private", "var/log/history"):
        assert not (seed / relative).exists(), f"seed data leaked into base: {relative}"
    assert (seed / "etc/machine-id").read_text() == ""
    assert (seed / "var/lib/docker/cache-fixture").read_text() == "warmed dependencies"
    assert (seed / "var/lib/containerd/image-fixture").read_text() == "cached images"
    assert (seed / 'usr/local/lib/almanac-tern/tern').read_text() == 'Tern runtime'
    assert (seed / 'usr/local/lib/almanac-tern/session.py').read_text() == 'Tern session helper'
    for service in ('almanac-tern-session.service', 'almanac-tern-remote.service'):
        assert not (seed / 'etc/systemd/system' / service).exists()
        assert not (seed / 'etc/systemd/system/multi-user.target.wants' / service).is_symlink()

    # A guest filesystem must never redirect privileged edits outside the mounted image.
    outside = Path(directory) / "outside-image"
    outside.mkdir()
    (outside / "keep").write_text("host data")
    shutil.rmtree(seed / "home")
    (seed / "home").symlink_to(outside, target_is_directory=True)
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; sudo() { "$@"; }; sanitize_image "$2"',
         "test", str(script), str(seed)], text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0, "accepted a guest symlink into the host"
    assert (outside / "keep").read_text() == "host data"

    # Clones must not provision again, touch the base, or reuse SSH identity.
    # Use the checkout filesystem so a Btrfs/XFS host exercises real reflinks.
    (root / ".local").mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="firecracker-clone-test-", dir=root / ".local") as clone_directory:
        clone_base = Path(clone_directory)
        image = clone_base / "base-images/image.fixture"
        image.mkdir(parents=True)
        (clone_base / "base-image").symlink_to(image)
        (image / "rootfs.ext4").write_bytes(b"dependency-cache\n" * 4096)
        (image / "vmlinux").write_text("kernel fixture")
        for binary in ("firecracker", "jailer"):
            (image / binary).write_text("executable fixture")
        fingerprint = subprocess.run(
            ["bash", "-c", 'source "$1"; dependency_fingerprint host', "test", str(script)],
            capture_output=True, text=True, check=True,
        ).stdout
        (image / "dependencies.sha256").write_text(fingerprint)
        root_template = clone_base / "root-template"
        for folder in ("etc/ssh", "etc/systemd/system/multi-user.target.wants", "home", "root"):
            (root_template / folder).mkdir(parents=True, exist_ok=True)
        (root_template / "etc/passwd").write_text(f"dev:x:{os.getuid()}:{os.getgid()}:Developer:/home/dev:/bin/bash\n")
        clone_env = dict(env, FIRECRACKER_STATE_DIR=str(clone_base), ROOT_TEMPLATE=str(root_template))
        clone_program = '''source "$1"
sudo() { "$@"; }
edit_image() {
    cp -a "$ROOT_TEMPLATE" "$state/filesystem"
    "$2" "$state/filesystem"
}
umask 077
select_instance "$2"
mkdir -p "$state"
reserve_network
VM_CPUS=2 VM_MEMORY_MIB=4096
clone_image
'''
        first = subprocess.run(
            ["bash", "-c", clone_program, "test", str(script), "first"],
            env=clone_env, text=True, capture_output=True, timeout=10,
        )
        if first.returncode != 0 and "CoW clone failed" in first.stderr:
            assert not (clone_base / "instances/first/id_ed25519").exists()
            print("Reflinks unavailable on checkout filesystem; verified explicit clone refusal")
        else:
            assert first.returncode == 0, first.stderr
            second = subprocess.run(
                ["bash", "-c", clone_program, "test", str(script), "second"],
                env=clone_env, text=True, capture_output=True, timeout=10,
            )
            assert second.returncode == 0, second.stderr
            one, two = (clone_base / f"instances/{name}" for name in ("first", "second"))
            assert (one / "id_ed25519.pub").read_bytes() != (two / "id_ed25519.pub").read_bytes()
            assert (one / "guest-host-key.pub").read_bytes() != (two / "guest-host-key.pub").read_bytes()
            for instance in (one, two):
                config = json.loads((instance / f"jailer/firecracker/{instance.name}/root/vm.json").read_text())
                assert config["machine-config"] == {"vcpu_count": 2, "mem_size_mib": 4096}
                assert instance.name in (instance / "filesystem/etc/hostname").read_text()
                assert not (instance / "filesystem/home/dev/.codex/auth.json").exists()
                assert (instance / "filesystem/home/dev/.ssh/authorized_keys").read_bytes() == (instance / "id_ed25519.pub").read_bytes()
            disk_one = one / "jailer/firecracker/first/root/rootfs.ext4"
            disk_two = two / "jailer/firecracker/second/root/rootfs.ext4"
            with disk_one.open("r+b") as file:
                file.write(b"worker edit")
            assert disk_two.read_bytes() == (image / "rootfs.ext4").read_bytes()
            assert disk_one.read_bytes() != disk_two.read_bytes()
            repeat = subprocess.run(
                ["bash", "-c", clone_program, "test", str(script), "first"],
                env=clone_env, text=True, capture_output=True, timeout=10,
            )
            assert repeat.returncode != 0 and "nothing overwritten" in repeat.stderr

            # Publish and rebuild atomically, preserving old generations and seed bytes.
            seed_state = clone_base / "instances/seed"
            seed_jail = seed_state / "jailer/firecracker/seed/root"
            seed_jail.mkdir(parents=True)
            (seed_state / "bin").mkdir()
            (seed_state / "ready").touch()
            (seed_jail / "rootfs.ext4").write_bytes(b"seed generation one")
            (seed_jail / "vmlinux").write_bytes(b"kernel")
            for binary in ("firecracker", "jailer"):
                (seed_state / f"bin/{binary}").write_text("executable fixture")
            publish = '''source "$1"
select_instance seed
start() { :; }
stop() { test -f "$state/tern-runtime-installed" || { echo 'base did not install Tern before sealing' >&2; exit 1; }; }
ssh_guest() { cat >/dev/null; }
ensure_github_cli() { :; }
install_tern_runtime() { touch "$state/tern-runtime-installed"; }
dependency_fingerprint() { printf 'fixture-fingerprint\\n'; }
edit_image() { [[ -z ${FAIL_BASE_EDIT:-} ]]; }
prepare_base
'''
            built = subprocess.run(
                ["bash", "-c", publish, "test", str(script)], env=clone_env,
                text=True, capture_output=True, timeout=10,
            )
            assert built.returncode == 0, built.stderr
            generation_one = (clone_base / "base-image").resolve()
            assert (generation_one / "rootfs.ext4").read_bytes() == b"seed generation one"
            assert (generation_one / "rootfs.ext4").stat().st_mode & 0o222 == 0
            (seed_jail / "rootfs.ext4").write_bytes(b"seed generation two")
            failed = subprocess.run(
                ["bash", "-c", publish, "test", str(script)], env=dict(clone_env, FAIL_BASE_EDIT="1"),
                text=True, capture_output=True, timeout=10,
            )
            assert failed.returncode != 0
            assert (clone_base / "base-image").resolve() == generation_one
            rebuilt = subprocess.run(
                ["bash", "-c", publish, "test", str(script)], env=clone_env,
                text=True, capture_output=True, timeout=10,
            )
            assert rebuilt.returncode == 0, rebuilt.stderr
            generation_two = (clone_base / "base-image").resolve()
            assert generation_two != generation_one
            assert (generation_two / "rootfs.ext4").read_bytes() == b"seed generation two"
            assert (generation_one / "rootfs.ext4").read_bytes() == b"seed generation one"
            assert disk_two.read_bytes() == (image / "rootfs.ext4").read_bytes()

        # Reject stale dependencies before creating a disk or a key.
        ((clone_base / "base-image").resolve() / "dependencies.sha256").chmod(0o600)
        ((clone_base / "base-image").resolve() / "dependencies.sha256").write_text("outdated\n")
        mismatch = subprocess.run(
            ["bash", "-c", clone_program, "test", str(script), "outdated"],
            env=clone_env, text=True, capture_output=True, timeout=10,
        )
        assert mismatch.returncode != 0 and "dependencies differ" in mismatch.stderr
        assert not (clone_base / "instances/outdated/id_ed25519").exists()

    # Deleting one stopped worker must preserve the other workers and shared assets.
    victim = state / "instances/delete-me"
    victim.mkdir(parents=True)
    (victim / "private-key").write_text("private fixture")
    (victim / "ready").touch()
    (victim / "network-slot").write_text("100\n")
    (state / "downloads").mkdir()
    (state / "downloads/keep").write_text("shared cache")
    deletion = '''source "$1"
sudo() { [[ -z ${FAIL_DELETE:-} ]] || return 1; "$@"; }
main delete delete-me
'''
    result = subprocess.run(
        ["bash", "-c", deletion, "test", str(script)], env=dict(env, FAIL_DELETE="1"),
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0, "failed privileged deletion reported success"
    assert (victim / "private-key").read_text() == "private fixture", "failed deletion hid credentials"
    assert (victim / "network-slot").read_text() == "100\n", "failed deletion released its address"
    assert not (victim / "ready").exists(), "partly deleted worker still appears ready"
    result = run("list", env=env)
    assert result.returncode == 0 and re.search(r"delete-me\s+partial", result.stdout), result
    result = subprocess.run(
        ["bash", "-c", deletion, "test", str(script)], env=env,
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    assert not victim.exists()
    assert (state / "downloads/keep").read_text() == "shared cache"

    # Concurrent setups must reserve distinct, stable addresses and network resources.
    allocation = '''source "$1"
umask 077
select_instance "$2"
mkdir -p "$state"
reserve_network
printf '%s\\n' "$guest" "$gateway" "$subnet" "$tap" "$chain" "$guest_mac" "$state"
'''
    workers = [subprocess.Popen(
        ["bash", "-c", allocation, "test", str(script), f"worker-{i}"],
        env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    ) for i in range(6)]
    assigned = []
    for worker in workers:
        stdout, stderr = worker.communicate(timeout=10)
        assert worker.returncode == 0, stderr
        assigned.append(stdout.splitlines())
    for column in (0, 1, 2, 3, 4, 5, 6):
        assert len({row[column] for row in assigned}) == 6, assigned
    for row in assigned:
        assert ipaddress.ip_address(row[0]) in ipaddress.ip_network(row[2])
        assert ipaddress.ip_address(row[1]) in ipaddress.ip_network(row[2])
    extended = subprocess.run(
        ["bash", "-c", 'source "$1"; use_slot 1023; printf "%s\\n" "$guest" "$subnet" "$guest_mac"',
         "test", str(script)], text=True, capture_output=True, timeout=10,
    )
    assert extended.returncode == 0, extended.stderr
    assert extended.stdout.splitlines() == ["192.168.255.254", "192.168.255.252/30", "06:03:c0:a8:f0:ff"]
    repeat = subprocess.run(
        ["bash", "-c", allocation, "test", str(script), "worker-0"],
        env=env, text=True, capture_output=True, timeout=10,
    )
    assert repeat.returncode == 0 and repeat.stdout.splitlines() == assigned[0], repeat.stderr
    result = run("list", env=env)
    assert result.returncode == 0, result.stderr
    for i in range(6):
        assert f"worker-{i}" in result.stdout, "list omitted an existing worker"

    payload = f'''Keep "quotes", 'apostrophes', `backticks`, $(touch {directory}/injected),
newlines, --options and Unicode: café.\n\n'''
    # Parser-only fixtures stand in for an already prepared dependency base.
    (state / "base-image").mkdir()
    parse_only = '''source "$1"
shift
check() { :; }
check_local_tern() { :; }
check_tern_runtime() { :; }
check_legacy_agent() { :; }
prepare_tern() { :; }
connect_tern() { :; }
setup() { :; }
start() { :; }
launch_agent() {
    printf '%s' "$prompt" > "$state/received-prompt"
    printf '%s' "$repo" > "$state/received-source"
}
main "$@"
'''
    for mode in ("inline", "stdin"):
        result = subprocess.run(
            ["bash", "-c", parse_only, "test", str(script), "run", f"prompt-{mode}",
             "--prompt", "-" if mode == "stdin" else payload],
            input=payload if mode == "stdin" else None, env=env,
            text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        assert (state / f"instances/prompt-{mode}/received-prompt").read_text() == payload
    assert not (Path(directory) / "injected").exists()

    # Unnamed creations reserve different readable names, even when launched concurrently.
    automatic = [subprocess.Popen(
        ["bash", "-c", parse_only, "test", str(script), "run", "--prompt", payload],
        env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    ) for _ in range(8)]
    generated = []
    for worker in automatic:
        stdout, stderr = worker.communicate(timeout=10)
        assert worker.returncode == 0, stderr
        match = re.search(r'^VM: ([a-z]+-[a-z]+)$', stdout, re.M)
        assert match, f"generated name missing: {stdout}"
        generated.append(match[1])
        assert (state / f"instances/{match[1]}/received-prompt").read_text() == payload
    assert len(set(generated)) == 8, "unnamed workers shared a name"

    # A repeated random sequence must skip an existing VM, preserving its files.
    reserve_name = '''source "$1"
umask 077
RANDOM=42
reserve_random_instance
'''
    first = subprocess.run(
        ["bash", "-c", reserve_name, "test", str(script)], env=env,
        text=True, capture_output=True, timeout=10,
    )
    assert first.returncode == 0, first.stderr
    first_name = first.stdout.strip().removeprefix("VM: ")
    guard = state / f"instances/{first_name}/keep"
    guard.write_text("existing worker data")
    second = subprocess.run(
        ["bash", "-c", reserve_name, "test", str(script)], env=env,
        text=True, capture_output=True, timeout=10,
    )
    assert second.returncode == 0 and second.stdout != first.stdout, second.stderr
    assert guard.read_text() == "existing worker data"
    # Make must pass prompt text literally, including Make function syntax.
    make_fixture = Path(directory) / "make-entrypoint"
    make_fixture.mkdir()
    (make_fixture / 'scripts').mkdir()
    (make_fixture / relative_script).write_text('''#!/usr/bin/env bash
python3 - "$@" <<'PY'
import json, os, sys
from pathlib import Path
Path(os.environ['CAPTURE_MAKE_ARGS']).write_text(json.dumps(sys.argv[1:]))
PY
''')
    make_payload = f'Read literal $(shell touch {directory}/make-injected), $$ and "quotes".'
    make_env = dict(env, CAPTURE_MAKE_ARGS=str(make_fixture / "args.json"),
                    VM="parent-vm", VM_REPO="/parent-checkout", VM_PROMPT="parent task",
                    MAKEFLAGS="--just-print", MAKELEVEL="3", MFLAGS="-n")
    # Fixture invocations must ignore the parent Make's options and VM settings.
    for variable in ("VM", "VM_REPO", "VM_PROMPT", "MAKEFLAGS", "MAKELEVEL", "MFLAGS"):
        make_env.pop(variable, None)
    result = subprocess.run(
        ["make", "-f", str(root / "Makefile"), "vm-run", "VM=api", f"VM_PROMPT={make_payload}"],
        cwd=make_fixture, env=make_env,
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    args = json.loads((make_fixture / "args.json").read_text())
    assert args == [
        "run", "api", "--repo", str(make_fixture), "--prompt", make_payload,
    ], f"Make changed launcher arguments: {args}"
    assert not (Path(directory) / "make-injected").exists()
    unnamed_env = dict(make_env)
    unnamed_env.pop("VM", None)
    for target, action in (("vm-run", "run"), ("vm-setup", "setup"), ("vm-base", "base")):
        result = subprocess.run(
            ["make", "-f", str(root / "Makefile"), target], cwd=make_fixture,
            env=unnamed_env, text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        args = json.loads((make_fixture / "args.json").read_text())
        assert args[:2] == [action, "--repo"], f"Make supplied a default name: {args}"
    for name, value in (("VM_CPUS", "0"), ("VM_MEMORY_MIB", "abc"), ("VM_DISK_GIB", "1")):
        invalid_state = Path(directory) / "invalid"
        result = run("setup", env=dict(env, FIRECRACKER_STATE_DIR=str(invalid_state), **{name: value}))
        assert result.returncode != 0 and name in result.stderr, result.stderr
        assert not invalid_state.exists(), "invalid configuration mutated state"

    # A reused PID must never be mistaken for this VM or signalled by stop.
    jail = state / "jailer/firecracker/almanac/root"
    jail.mkdir(parents=True)
    shutil.copy2("/bin/sleep", jail / "firecracker")
    (jail / "firecracker.pid").write_text(str(os.getpid()))
    result = run("status", env=env)
    assert result.returncode == 0 and "stopped" in result.stdout, result
    result = run("stop", env=env)
    assert result.returncode == 0, result.stderr

    # Jailer writes a root-only PID file before dropping privileges.
    # Simulate the privileged read, but use a real live process and file permissions.
    pid_tools = Path(directory) / "pid-tools"
    pid_tools.mkdir()
    (pid_tools / "sudo").write_text('''#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == cat && "$2" == -- && "$3" == "$TEST_PID_FILE" ]] || exit 99
[[ -z ${DENY_PID_READ:-} ]] || exit 1
chmod 600 "$3"
cat -- "$3"
chmod 000 "$3"
''')
    (pid_tools / "sudo").chmod(0o700)
    (pid_tools / "readlink").write_text("#!/bin/sh\nprintf '/firecracker\\n'\n")
    (pid_tools / "readlink").chmod(0o700)
    pid_file = jail / "firecracker.pid"
    live_vm = subprocess.Popen([str(jail / "firecracker"), "60"])
    try:
        pid_file.write_text(str(live_vm.pid))
        pid_file.chmod(0)
        pid_env = dict(env, PATH=str(pid_tools) + os.pathsep + os.environ["PATH"],
                       TEST_PID_FILE=str(pid_file))
        result = run("status", env=pid_env)
        assert result.returncode == 0 and "running" in result.stdout, result.stderr
        assert "unbound variable" not in result.stderr
        result = run("status", env=dict(pid_env, DENY_PID_READ="1"))
        assert result.returncode != 0 and "cannot read" in result.stderr, result
        assert "stopped" not in result.stdout
        assert live_vm.poll() is None, "permission failure affected the live VM"
    finally:
        pid_file.chmod(0o600)
        live_vm.terminate()
        live_vm.wait(timeout=5)
    pid_file.write_text("invalid PID")
    result = run("status", env=env)
    assert result.returncode != 0 and "invalid PID" in result.stderr, result
    pid_file.unlink()

    # The guest network service and ordinary users must read generated network/DNS config.
    rootfs = Path(directory) / "network-rootfs"
    (rootfs / "etc/systemd/system/multi-user.target.wants").mkdir(parents=True)
    (rootfs / "etc/systemd/system/fcnet.service").symlink_to(outside, target_is_directory=True)
    (rootfs / "etc/systemd/system/multi-user.target.wants/systemd-networkd.service").symlink_to(outside, target_is_directory=True)
    result = subprocess.run(
        ["bash", "-c", '''source "$1"
sudo() { "$@"; }
umask 077
prepare_network "$2"
''', "test", str(script), str(rootfs)],
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    assert not (outside / "null").is_symlink(), "followed a guest service symlink into the host"
    assert not (outside / "systemd-networkd.service").is_symlink()
    for file in (rootfs / "etc/resolv.conf", rootfs / "etc/systemd/network/10-almanac.network"):
        assert file.stat().st_mode & 0o777 == 0o644, f"guest network config unreadable: {file}"

    # Host IPv6 rejection must precede TAP activation and clean up its own rules.
    network_tools = Path(directory) / "network-tools"
    network_tools.mkdir()
    network_log = Path(directory) / "network-commands.jsonl"
    for tool in ("ip", "iptables", "ip6tables"):
        program = network_tools / tool
        program.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
tool = Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['TEST_NETWORK_LOG'], 'a') as log:
    log.write(json.dumps([tool, *args]) + '\\n')
if tool == 'ip' and args[:2] == ['link', 'show']:
    sys.exit(1)
if tool == 'ip' and args[:4] == ['-j', '-4', 'route', 'show']:
    print('[]')
if tool == 'ip6tables' and os.environ.get('FAIL_IPV6'):
    sys.exit(1)
''')
        program.chmod(0o700)
    firewall_program = '''source "$1"
tap=lo
sudo() { if [[ $1 == -v ]]; then return; fi; "$@"; }
read() { [[ $* == '-r alias' ]] || return 99; alias="almanac-firecracker:$state"; }
network_up
network_down
'''
    firewall_env = dict(env, TEST_NETWORK_LOG=str(network_log),
                        PATH=str(network_tools) + os.pathsep + os.environ['PATH'])
    result = subprocess.run(
        ["bash", "-c", firewall_program, "test", str(script)], env=firewall_env,
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    network_commands = [json.loads(line) for line in network_log.read_text().splitlines()]
    for chain in ("INPUT", "FORWARD"):
        reject = ["ip6tables", "-w", "-I", chain, "1", "-i", "lo", "-j", "DROP"]
        assert reject in network_commands, f"guest IPv6 can bypass the host {chain} policy"
        assert network_commands.index(reject) < network_commands.index(["ip", "link", "set", "dev", "lo", "up"])
        assert ["ip6tables", "-w", "-t", "filter", "-D", chain, "-i", "lo", "-j", "DROP"] in network_commands
    network_log.unlink()
    result = subprocess.run(
        ["bash", "-c", firewall_program, "test", str(script)], env=dict(firewall_env, FAIL_IPV6="1"),
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0, "IPv6 firewall failure was ignored"
    network_commands = [json.loads(line) for line in network_log.read_text().splitlines()]
    assert ["ip", "link", "set", "dev", "lo", "up"] not in network_commands

    disk = jail / "rootfs.ext4"
    disk.write_text("existing guest data")
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; state=$2; jail=$3; prepare_image',
         "test", str(script), str(state), str(jail)],
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0 and "nothing overwritten" in result.stderr, result.stderr
    assert disk.read_text() == "existing guest data"

    # A second boot reuses the disk, but jailer recreates its device nodes exclusively.
    nodes = ["dev/net/tun", "dev/kvm", "dev/urandom", "dev/userfaultfd"]
    preserved = {"rootfs.ext4": b"existing guest data", "vmlinux": b"kernel",
                 "vm.json": b"{}", "firecracker.log": b"previous boot", "dev/keep": b"operator file"}
    for relative, contents in preserved.items():
        path = jail / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(contents)
    for relative in [*nodes, "api.socket", "firecracker.pid"]:
        path = jail / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("stale runtime file")
    (state / "id_ed25519").write_text("private fixture key")
    (state / "bin").mkdir()
    (state / "bin/jailer").write_text('''#!/usr/bin/env python3
import os
from pathlib import Path
jail = Path(os.environ['TEST_JAIL'])
for relative in ('dev/net/tun', 'dev/kvm', 'dev/urandom', 'dev/userfaultfd'):
    with (jail / relative).open('x'):
        pass
(jail / 'firecracker.pid').write_text(str(os.getpid()))
''')
    (state / "bin/jailer").chmod(0o700)
    restart_program = '''source "$1"
state=$2
jail=$3
running() { return 1; }
sudo() { if [[ "$1" == -v ]]; then return; fi; "$@"; }
network_down() { :; }
network_up() { :; }
ssh_guest() { :; }
start
'''
    for _ in range(2):
        result = subprocess.run(
            ["bash", "-c", restart_program, "test", str(script), str(state), str(jail)],
            env=dict(env, TEST_JAIL=str(jail)), text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        for relative, contents in preserved.items():
            assert (jail / relative).read_bytes() == contents, f"restart changed {relative}"
        assert (state / "id_ed25519").read_text() == "private fixture key"

    # Minimal guests have PID 1's private socket but no general D-Bus daemon.
    # Queue a normal reboot without logind fallback warnings or a forced shutdown.
    live = Path(directory) / "live-vm"
    live.touch()
    tools_for_stop = Path(directory) / "stop-tools"
    tools_for_stop.mkdir()
    (tools_for_stop / "systemctl").write_text('''#!/usr/bin/env python3
import os, sys
from pathlib import Path
args = sys.argv[1:]
if args == ['reboot']:
    print('Failed to connect to bus: No such file or directory', file=sys.stderr)
else:
    assert args == ['--system', '--no-block', '--job-mode=replace-irreversibly', 'start', 'reboot.target'], args
Path(os.environ['TEST_LIVE_VM']).unlink()
''')
    (tools_for_stop / "systemctl").chmod(0o700)
    result = subprocess.run(
        ["bash", "-c", '''source "$1"
state=$2
live=$3
running() { test -f "$live"; }
ssh_guest() { bash -c "${*: -1}"; }
network_down() { :; }
stop
''', "test", str(script), str(state), str(live)],
        env=dict(env, TEST_LIVE_VM=str(live), PATH=str(tools_for_stop) + os.pathsep + os.environ['PATH']),
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0 and not live.exists(), result.stderr
    assert not result.stderr, f"successful stop still reports a bus failure: {result.stderr}"

    # Corrupt cached artifacts fail closed; no network or replacement is attempted.
    cached = Path(directory) / "artifact"
    cached.write_text("corrupt")
    expected = hashlib.sha256(b"expected").hexdigest()
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; download https://invalid.example "$2" "$3"',
         "test", str(script), expected, str(cached)],
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0 and "checksum" in result.stderr.lower(), result.stderr
    assert cached.read_text() == "corrupt"

    # Exercise a real Git bundle and dirty linked-worktree copy; only SSH transport is fake.
    original = Path(directory) / "original"
    checkout = Path(directory) / "linked checkout"
    guest_home = Path(directory) / "guest"
    tools = Path(directory) / "tools"
    for folder in (original, guest_home, tools):
        folder.mkdir()

    def git(*args):
        return subprocess.run(
            ["git", "-C", str(original), *args], check=True,
            text=True, capture_output=True,
        ).stdout.strip()

    git("init", "-b", "main")
    git("config", "user.name", "Launcher test")
    git("config", "user.email", "test@example.invalid")
    git("remote", "add", "origin", "https://fixture-token@github.com/example/almanac.git")
    (original / ".gitignore").write_text(".local/\n.worktrees/\n.env\n")
    (original / "tracked").write_text("original")
    (original / "deleted").write_text("delete me")
    git("add", ".")
    git("commit", "-m", "fixture")
    git("worktree", "add", str(checkout), "-b", "codex/almanac")
    subprocess.run(
        ["git", "-C", str(checkout), "commit", "--allow-empty", "-m", "task base"],
        check=True, capture_output=True,
    )
    (checkout / "tracked").write_text("dirty work")
    (checkout / "deleted").unlink()
    (checkout / "new file").write_text("untracked work")
    (checkout / ".env").write_text("private-fixture")
    result = subprocess.run(
        ["bash", "-c", parse_only, "test", str(script), "run", "custom-source", "--repo",
         str(checkout), "--prompt", payload],
        env=env, text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    assert (state / "instances/custom-source/received-source").read_text() == str(checkout)
    (tools / "scp").write_text('''#!/usr/bin/env python3
import os, shutil, sys
for path in sys.argv[3:-1]:
    if path.startswith('dev:'):
        shutil.copy(path[4:].replace('/home/dev', os.environ['FAKE_GUEST_HOME']), sys.argv[-1])
    else:
        shutil.copy(path, os.environ['FAKE_GUEST_HOME'])
''')
    (tools / "ssh").write_text('''#!/usr/bin/env python3
import os, subprocess, sys
args = sys.argv[3:]
if args[0] == '-t':
    args = args[1:]
assert args[0] == 'dev', args
if os.environ.get('FAKE_SSH_FAIL'):
    sys.exit(255)
if args[1:] == ['bash', '-s']:
    program = sys.stdin.read().replace('/home/dev', os.environ['FAKE_GUEST_HOME'])
    result = subprocess.run(['bash', '-s'], input=program, text=True)
else:
    if len(args) == 1:
        command = 'exec "$SHELL" -l'
    else:
        assert len(args) == 2, args
        command = args[1].replace('/home/dev', os.environ['FAKE_GUEST_HOME'])
    result = subprocess.run(['bash', '-c', command], stdin=sys.stdin, cwd=os.environ['FAKE_GUEST_HOME'])
sys.exit(result.returncode)
''')
    for tool in tools.iterdir():
        tool.chmod(0o700)
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; repo=$2; state=$3; copy_checkout',
         "test", str(script), str(checkout), str(state)],
        env=dict(env, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                 FAKE_GUEST_HOME=str(guest_home)),
        text=True, capture_output=True, timeout=15,
    )
    assert result.returncode == 0, result.stderr
    guest_checkout = guest_home / "almanac/.worktrees/codex"
    assert "fixture-token" not in (state / "git-config.json").read_text()
    assert subprocess.check_output(
        ["git", "-C", str(guest_checkout), "remote", "get-url", "origin"], text=True,
    ).strip() == "https://github.com/example/almanac.git"
    assert subprocess.check_output(
        ["git", "-C", str(guest_checkout), "config", "user.email"], text=True,
    ).strip() == "test@example.invalid"
    # GH credentials travel through encrypted stdin, never command arguments/output.
    (tools / "gh").write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
if args == ['auth', 'token', '--hostname', 'github.com']:
    print('fixture-secret-token')
elif args == ['auth', 'login', '--hostname', 'github.com', '--git-protocol', 'https', '--with-token']:
    path = Path(os.environ['FAKE_GUEST_HOME']) / '.config/gh/received.json'
    path.write_text(json.dumps({'args': args, 'stdin': sys.stdin.read()}))
elif args != ['auth', 'setup-git', '--hostname', 'github.com']:
    sys.exit(99)
''')
    (tools / "gh").chmod(0o700)
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; state=$2; ensure_github_cli() { :; }; copy_github_auth',
         "test", str(script), str(state)],
        env=dict(env, PATH=str(tools) + os.pathsep + os.environ["PATH"], FAKE_GUEST_HOME=str(guest_home)),
        text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    received = guest_home / ".config/gh/received.json"
    login = json.loads(received.read_text())
    assert login["stdin"] == "fixture-secret-token\n"
    assert all("fixture-secret-token" not in arg for arg in login["args"])
    assert "fixture-secret-token" not in result.stdout + result.stderr
    assert received.stat().st_mode & 0o777 == 0o600
    assert (guest_home / ".config/gh").stat().st_mode & 0o777 == 0o700
    assert (guest_checkout / "tracked").read_text() == "dirty work"
    assert (guest_checkout / "new file").read_text() == "untracked work"
    assert not (guest_checkout / "deleted").exists()
    assert not (guest_checkout / ".env").exists()
    assert not (guest_checkout / ".local").exists()
    assert (checkout / "tracked").read_text() == "dirty work"

    assert git("status", "--porcelain") == ""

    (guest_home / ".codex").mkdir()
    (tools / "tmux").write_text('''#!/usr/bin/env python3
import json, os, subprocess, sys
if sys.argv[1] == 'has-session':
    sys.exit(0 if os.environ.get('FAKE_SESSION') else 1)
if 'attach-session' in sys.argv:
    with open(os.environ['FAKE_TMUX_ATTACH'], 'w') as output:
        json.dump(sys.argv[1:], output)
    sys.exit(0)
assert sys.argv[1:6] == ['new-session', '-d', '-s', 'codex', '-c'], sys.argv
sys.exit(subprocess.run([sys.argv[-1]], cwd=sys.argv[-2]).returncode)
''')
    (tools / "codex").write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['FAKE_ARGV'], 'w') as f:
    json.dump(sys.argv[1:], f)
''')
    for tool in tools.iterdir():
        tool.chmod(0o700)
    transport_env = dict(env, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                         FAKE_GUEST_HOME=str(guest_home), FAKE_ARGV=str(state / "argv.json"))
    # A manual Codex launch from vm-ssh needs the checkout cwd and saved trust.
    login_shell = tools / "guest-login-shell"
    login_shell.write_text("#!/bin/sh\npwd -P\n")
    login_shell.chmod(0o700)
    (state / "ready").touch()
    guest_config = guest_home / ".codex/config.toml"
    shell_program = '''source "$1"
check() { :; }
check_local_tern() { :; }
check_tern_runtime() { :; }
check_legacy_agent() { :; }
prepare_tern() { :; }
connect_tern() { :; }
start() { :; }
main ssh
'''
    for previous in (None, 'cli_auth_credentials_store = "file"\n', 'model = "fixture-model"\n'):
        if previous is None:
            guest_config.unlink(missing_ok=True)
        else:
            guest_config.write_text(previous)
        result = subprocess.run(
            ["bash", "-c", shell_program, "test", str(script)],
            env=dict(transport_env, SHELL=str(login_shell)), text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        assert result.stdout.strip() == str(guest_checkout), "guest shell opened outside the task worktree"
        if previous and previous.startswith("model"):
            assert guest_config.read_text() == previous, "guest preferences were overwritten"
        else:
            config = tomllib.loads(guest_config.read_text())
            assert config["projects"] == {str(guest_home / "almanac"): {"trust_level": "trusted"}}
            assert config["cli_auth_credentials_store"] == "file"
            assert guest_config.stat().st_mode & 0o777 == 0o600
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; state=$2; prompt=$3; launch_agent',
         "test", str(script), str(state), payload],
        env=transport_env, text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    assert json.loads((state / "argv.json").read_text()) == [
        "--sandbox", "danger-full-access", "--ask-for-approval", "never", "-c",
        f'projects."{guest_home / "almanac"}".trust_level="trusted"', "--", payload,
    ]
    assert not (Path(directory) / "injected").exists()
    # A restored Tern pane may execute the runner again; never replay its task.
    result = subprocess.run([str(guest_home / '.codex/almanac-run')],
                            env=transport_env, text=True, capture_output=True, timeout=10)
    assert result.returncode == 0, result.stderr
    assert '--' not in json.loads((state / 'argv.json').read_text()), 'initial task was replayed'
    result = subprocess.run(
        ['bash', '-c', 'source "$1"; state=$2; prompt=; launch_agent',
         'test', str(script), str(state)], env=transport_env,
        text=True, capture_output=True, timeout=10)
    assert result.returncode == 0, result.stderr
    assert (guest_home / '.codex/almanac-prompt.txt').read_text() == payload, 'reconnect discarded the original task'
    assert '--' not in json.loads((state / 'argv.json').read_text()), 'reconnect submitted the original task again'
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; state=$2; prompt=new; launch_agent',
         "test", str(script), str(state)],
        env=dict(transport_env, FAKE_SESSION="1"), text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0 and "already exists" in result.stderr, result.stderr
    assert (guest_home / ".codex/almanac-prompt.txt").read_text() == payload
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; state=$2; prompt=new; launch_agent',
         "test", str(script), str(state)],
        env=dict(transport_env, FAKE_SSH_FAIL="1"), text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0 and "inspect Codex" in result.stderr, result.stderr
    assert (guest_home / ".codex/almanac-prompt.txt").read_text() == payload

    # New and existing Codex sessions must attach with distinct Shift+Enter input.
    attachment = state / "tmux-attach.json"
    for existing in (False, True):
        result = subprocess.run(
            ["bash", "-c", '''source "$1"
check() { :; }
check_local_tern() { :; }
check_tern_runtime() { :; }
check_legacy_agent() { :; }
prepare_tern() { :; }
connect_tern() { :; }
start() { :; }
main codex
''', "test", str(script)],
            env=dict(transport_env, FAKE_SESSION="1" if existing else "",
                     FAKE_TMUX_ATTACH=str(attachment)),
            text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        assert json.loads(attachment.read_text()) == [
            "-T", "extkeys", "set-option", "-s", "extended-keys", "on", ";",
            "bind-key", "-n", "S-Enter", "if-shell", "-F",
            "#{==:#{session_name},codex}", "send-keys C-j", "send-keys S-Enter", ";",
            "attach-session", "-t", "codex",
        ], "Codex attachment omitted extended keys or its scoped newline binding"

    # Committed and uncommitted work must both be recoverable, without committing host files.
    def guest_git(*args):
        return subprocess.run(["git", "-C", str(guest_checkout), *args],
                              check=True, text=True, capture_output=True).stdout.strip()

    guest_git("config", "user.name", "Launcher test")
    guest_git("config", "user.email", "test@example.invalid")
    (guest_checkout / "committed").write_text("committed guest work")
    guest_git("add", "committed")
    guest_git("commit", "-m", "guest fixture")

    # Native live/unknown session states must refuse collection before exporting.
    python_shim = tools / 'python3'
    python_shim.write_text('''#!/bin/sh
if test "$1" = /usr/local/lib/almanac-tern/session.py; then
    exit "$FAKE_NATIVE_STATUS"
fi
exec ''' + shlex.quote(shutil.which('python3')) + ''' "$@"
''')
    python_shim.chmod(0o700)
    native_marker = guest_home / '.codex/almanac-terminal-tern'
    native_marker.touch()
    for native_status in ('0', '2'):
        result = subprocess.run(
            ['bash', '-c', 'source "$1"; state=$2; collect', 'test', str(script), str(state)],
            env=dict(transport_env, FAKE_SESSION='', FAKE_NATIVE_STATUS=native_status),
            text=True, capture_output=True, timeout=10,
        )
        assert result.returncode != 0 and 'Quit Codex in Tern' in result.stderr, result.stderr
        assert not list(state.glob('results.*')), 'live or unknown Tern state exported files'
    native_marker.unlink()
    python_shim.unlink()

    for active in (True, False):
        result = subprocess.run(
            ["bash", "-c", 'source "$1"; state=$2; collect', "test", str(script), str(state)],
            env=dict(transport_env, FAKE_SESSION="1" if active else ""),
            text=True, capture_output=True, timeout=15,
        )
        if active:
            assert result.returncode != 0 and "Quit Codex" in result.stderr, result.stderr
            assert not list(state.glob("results.*"))
        else:
            assert result.returncode == 0, result.stderr
    results, = state.glob("results.*")
    assert "dirty work" in (results / "changes.patch").read_text()
    with tarfile.open(results / "untracked.tar.gz") as archive:
        assert archive.getnames() == ["new file"]
    assert (results / "repo.bundle").stat().st_size > 0
    assert (results / "base.txt").read_text() != (results / "head.txt").read_text()
    heads = guest_git("bundle", "list-heads", str(results / "repo.bundle"))
    assert f'{guest_git("rev-parse", "HEAD")} refs/heads/codex/almanac-1' in heads
    assert git("rev-parse", "codex/almanac") != guest_git("rev-parse", "HEAD")
    assert (checkout / "tracked").read_text() == "dirty work"

    # A failed orderly shutdown must never fall through to deleting a live disk.
    refusal = state / "instances/refuse-delete"
    refusal.mkdir()
    (refusal / "rootfs.ext4").write_text("live guest data")
    result = subprocess.run(
        ["bash", "-c", '''source "$1"
running() { return 0; }
ssh_guest() { return 1; }
main delete refuse-delete
''', "test", str(script)], env=env, text=True, capture_output=True, timeout=10,
    )
    assert result.returncode != 0 and "shutdown request failed" in result.stderr
    assert (refusal / "rootfs.ext4").read_text() == "live guest data"

    # The legacy guest shares the state root: deleting it must preserve bases/workers/results.
    shared_base = state / "base-images/preserved"
    shared_base.mkdir(parents=True)
    (shared_base / "rootfs.ext4").write_text("shared dependency image")
    (state / "base-image").rmdir()
    (state / "base-image").symlink_to(shared_base)
    result = subprocess.run(
        ["bash", "-c", 'source "$1"; sudo() { "$@"; }; main delete almanac',
         "test", str(script)], env=env, text=True, capture_output=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    assert not jail.exists()
    assert not (state / "id_ed25519").exists()
    assert (state / "base-image/rootfs.ext4").read_text() == "shared dependency image"
    assert (state / "instances/worker-0/network-slot").is_file()
    assert (results / "repo.bundle").is_file()

print("Firecracker offline safety checks passed")
