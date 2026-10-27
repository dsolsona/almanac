#!/usr/bin/env python3
"""Split the fixed integration Go JSON artifact by package and summarize it."""

from __future__ import annotations

import json
import re
from collections import defaultdict
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / ".local" / "integration-go-test.jsonl"
EXIT_STATUS = ROOT / ".local" / "integration-go-test.exit"
DESTINATION = ROOT / ".local" / "integration-test-json"


def main() -> int:
    if not SOURCE.is_file():
        print(f"integration test JSON artifact is missing: {SOURCE}")
        return 1

    package_lines: dict[str, list[str]] = defaultdict(list)
    tests: list[tuple[float, str, str]] = []
    failures: list[dict[str, object]] = []
    packages: dict[str, dict[str, object]] = {}
    malformed = 0

    with SOURCE.open(encoding="utf-8") as artifact:
        for line in artifact:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                malformed += 1
                continue
            package = str(event.get("Package") or "unknown")
            package_lines[package].append(line)
            action = event.get("Action")
            test_name = event.get("Test")
            details = packages.setdefault(package, {"result": "running", "elapsed": 0.0, "tests": 0})
            if action in ("pass", "fail", "skip") and test_name:
                details["tests"] = int(details["tests"]) + 1
                elapsed = float(event.get("Elapsed") or 0.0)
                tests.append((elapsed, package, str(test_name)))
                if action == "fail":
                    failures.append(event)
            elif action in ("pass", "fail"):
                details["result"] = str(action)
                details["elapsed"] = float(event.get("Elapsed") or 0.0)
                if action == "fail":
                    failures.append(event)

    DESTINATION.mkdir(parents=True, exist_ok=True)
    for package, lines in package_lines.items():
        suffix = package.rsplit("/", 1)[-1]
        safe_name = re.sub(r"[^A-Za-z0-9._-]", "_", suffix)
        (DESTINATION / f"{safe_name}.jsonl").write_text("".join(lines), encoding="utf-8")

    print(f"Integration Go JSON: {SOURCE}")
    try:
        exit_status = EXIT_STATUS.read_text(encoding="utf-8").strip()
    except OSError:
        exit_status = "unavailable"
    if exit_status.isdecimal():
        print(f"Go test exit code: {exit_status}")
    else:
        print(f"Go test exit code: {exit_status} (the test process did not report completion)")
    print(f"Per-package event files: {DESTINATION}")
    for package in sorted(packages):
        details = packages[package]
        print(f"{package}: {details['result']} in {details['elapsed']:.2f}s; {details['tests']} test events")
    if failures:
        print("Failed tests/packages:")
        for event in failures:
            package = event.get("Package", "unknown")
            test_name = event.get("Test", "<package>")
            print(f"  {package} {test_name}")
    if malformed:
        print(f"Malformed Go test JSON lines: {malformed}")
        return 1
    print("Slowest integration tests:")
    for elapsed, package, test_name in sorted(tests, reverse=True)[:10]:
        print(f"  {elapsed:.2f}s {package} {test_name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
