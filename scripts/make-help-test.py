#!/usr/bin/env python3
"""Keep every public Make target discoverable without starting development services."""
import os
from pathlib import Path
import re
import subprocess
import tempfile

root = Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix="almanac-help-") as directory:
    private = Path(directory)
    tools = private / "tools"
    tools.mkdir()
    marker = private / "service-started"
    for name in ("docker", "sudo"):
        program = tools / name
        program.write_text('#!/bin/sh\ntouch "$HELP_SERVICE_MARKER"\nexit 99\n')
        program.chmod(0o700)
    result = subprocess.run(
        ["make", "--no-print-directory", "help"], cwd=root, text=True,
        capture_output=True, timeout=10,
        env=dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                 HELP_SERVICE_MARKER=str(marker), FIRECRACKER_STATE_DIR=str(private / "vms")),
    )
    assert result.returncode == 0, result.stderr
    assert not marker.exists(), "help started a service or requested privileges"
    assert not (private / "vms").exists(), "help created VM state"

    source = (root / "Makefile").read_text()
    source = re.sub(r"(?ms)^define [^\n]*\n.*?^endef[^\n]*\n", "", source)
    public = set()
    for line in source.splitlines():
        match = re.match(r"^([A-Za-z][\w -]*):(?!=)", line)
        if match:
            public.update(match[1].split())
    documented = re.findall(r"^  ([a-z][\w-]*) {2,}\S.*$", result.stdout, re.M)
    assert public == set(documented), {
        "missing": sorted(public - set(documented)), "unknown": sorted(set(documented) - public),
    }
    assert len(documented) == len(set(documented)), "help lists a target more than once"
    for option in ("VM_PROMPT", "VM_REPO", "GENERATE_MODE", "RACE", "ARGS", "IMAGE_REGISTRY"):
        assert option in result.stdout, f"missing useful option: {option}"
    assert "Ctrl-D" in result.stdout, "help does not explain interactive VM task input"

print("Make help checks passed")
