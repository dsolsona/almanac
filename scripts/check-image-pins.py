#!/usr/bin/env python3
"""Keep every Dockerfile and Compose file on one digest per image (ADR-0029).

Images must be pinned by a multi-architecture index digest. The same image name
may appear in several files; this check fails when two files pin it differently
or when any reference lacks a digest.
"""
from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
FILES = (
    "deploy/dev/Dockerfile",
    "deploy/app/Dockerfile",
    "deploy/agent/Dockerfile",
    "compose.yaml",
    "compose.dev.yaml",
)
REFERENCE = re.compile(r"(?P<name>[a-z0-9][a-z0-9._/-]*):(?P<tag>[A-Za-z0-9._-]+)(?:@sha256:(?P<digest>[0-9a-f]{64}))?")
LINE = re.compile(r"^\s*(?:FROM\s+(?:--platform=\S+\s+)?|image:\s*|(?:postgres_)?image=['\"]?)(?P<ref>[^'\"\s]+)")


def main() -> int:
    pins: dict[str, dict[str, set[str]]] = {}
    problems: list[str] = []
    for relative in FILES:
        path = ROOT / relative
        if not path.is_file():
            problems.append(f"{relative}: missing")
            continue
        for number, line in enumerate(path.read_text().splitlines(), 1):
            match = LINE.match(line)
            if not match:
                continue
            ref = match.group("ref").strip()
            if ref.startswith("${"):
                continue  # locally built images are named by the Compose project
            parsed = REFERENCE.fullmatch(ref)
            if not parsed or not parsed.group("digest"):
                problems.append(f"{relative}:{number}: {ref} is not pinned by a digest")
                continue
            pins.setdefault(parsed.group("name"), {}).setdefault(parsed.group("digest"), set()).add(f"{relative}:{number}")
    for name, digests in sorted(pins.items()):
        if len(digests) > 1:
            where = "; ".join(f"{digest[:12]} in {', '.join(sorted(places))}" for digest, places in sorted(digests.items()))
            problems.append(f"{name} is pinned to different digests: {where}")
    if problems:
        print("image pin check failed:", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1
    print(f"Image pins consistent across {len(FILES)} files: {', '.join(sorted(pins))}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
