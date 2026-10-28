#!/usr/bin/env python3
"""Exercise the launcher prompt with a real pseudo-terminal and fake Docker."""
import os
import pty
import select
import signal
import tempfile
import termios
import time
from pathlib import Path

root = Path(__file__).resolve().parent.parent

with tempfile.TemporaryDirectory(prefix="almanac-agent-tty-") as directory:
    docker = Path(directory, "docker")
    log = Path(directory, "docker.log")
    docker.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$FAKE_LOG"
case "$1 $2" in
  "compose -f") case "$*" in *" ps "*) echo abc123;; *" exec "*) echo public-certificate;; esac ;;
  "inspect --format") case "$3" in *Health*) echo healthy;; *) echo sha256:abc;; esac ;;
  "create -i") echo owned-id ;;
  "start --attach") exec sleep 30 ;;
  "rm -f") : ;;
  *) exit 1 ;;
esac
''')
    docker.chmod(0o700)
    env = dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"], TMPDIR=directory, FAKE_LOG=str(log))
    env.pop("AGENT_TOKEN_FILE", None)
    for submit in (False, True):
        log.write_text("")
        read_flags, write_flags = os.pipe()
        pid, master = pty.fork()
        if pid == 0:
            os.close(read_flags)
            os.write(write_flags, str(termios.tcgetattr(0)[3]).encode())
            os.close(write_flags)
            signal.signal(signal.SIGINT, signal.SIG_DFL)
            os.chdir(root)
            os.execvpe("make", ["make", "agent"], env)
        os.close(write_flags)
        original = int(os.read(read_flags, 32))
        os.close(read_flags)
        output = bytearray()
        status = None

        def collect_until(needle, timeout):
            deadline = time.monotonic() + timeout
            while needle not in output and time.monotonic() < deadline:
                if select.select([master], [], [], 0.1)[0]:
                    try:
                        chunk = os.read(master, 4096)
                    except OSError:
                        break
                    if not chunk:
                        break
                    output.extend(chunk)
            safe = bytes(output).replace(b"private-test-credential", b"[REDACTED]")
            assert needle in output, f"missing {needle!r}; output={safe!r}"

        try:
            collect_until(b"Paste agent credential:", 10)
            assert not termios.tcgetattr(master)[3] & termios.ECHO, "prompt echoed input"
            if submit:
                os.write(master, b"private-test-credential\n")
                collect_until(b"Agent starting.", 10)
                assert b"private-test-credential" not in output, "credential appeared on terminal"
            os.write(master, b"\x03")
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                waited, candidate = os.waitpid(pid, os.WNOHANG)
                if waited:
                    status = candidate
                    break
                if select.select([master], [], [], 0.05)[0]:
                    try:
                        output.extend(os.read(master, 4096))
                    except OSError:
                        pass
            if status is None:
                safe = bytes(output).replace(b"private-test-credential", b"[REDACTED]")
                raise AssertionError(f"PTY child {pid} was not reaped after Ctrl-C; output={safe!r}")
            assert not os.WIFEXITED(status) or os.WEXITSTATUS(status) != 0, "cancellation exited successfully"
            assert termios.tcgetattr(master)[3] & termios.ECHO == original & termios.ECHO, "terminal echo was not restored"
            if submit:
                assert "rm -f owned-id" in log.read_text(), "owned container was not cleaned up"
            assert b"private-test-credential" not in output, "credential appeared on terminal"
        finally:
            if status is None:
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
                try:
                    os.waitpid(pid, os.WNOHANG)
                except ChildProcessError:
                    pass
            os.close(master)
print("Local agent prompt: private input, cancellation and cleanup passed")
