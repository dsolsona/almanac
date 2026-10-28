#!/usr/bin/env python3
"""Validate fixed integration-package Go test discovery output."""

import re
import sys
from pathlib import Path


def select(source: str, package: str) -> list[str]:
    if package not in ("command", "storage"):
        raise ValueError("unsupported integration package")
    names = []
    for line in source.splitlines():
        if line.startswith("TestIntegration"):
            if not re.fullmatch(r"TestIntegration[A-Za-z0-9_]*", line) or line in names:
                raise ValueError("invalid or duplicate integration test")
            names.append(line)
        elif not re.fullmatch(r"(?:ok|\?)\s+\S+(?:\s+.*)?", line):
            raise ValueError("unexpected integration discovery output")
    if not names or (package == "command" and "TestIntegrationExecutableBrowserSLICollectionJourney" not in names):
        raise ValueError("integration discovery is empty or missing the SLI journey")
    priority = "TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict"
    if package == "storage" and priority in names:
        names.remove(priority)
        names.insert(0, priority)
    return names


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ValueError("expected package and discovery file")
        print("\n".join(select(Path(sys.argv[2]).read_text(encoding="utf-8"), sys.argv[1])))
    except (IndexError, OSError, ValueError) as error:
        print(f"integration discovery failed: {error}", file=sys.stderr)
        raise SystemExit(1)
