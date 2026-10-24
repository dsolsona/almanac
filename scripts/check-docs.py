#!/usr/bin/env python3
"""Check repository Markdown structure without third-party dependencies."""

import argparse
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit


def check(root: Path) -> tuple[list[str], int, int]:
    errors: list[str] = []
    files = [root / "AGENTS.md", root / "README.md"]
    files += sorted((root / "docs").rglob("*.md"))
    links = 0
    for path in files:
        name = path.relative_to(root)
        if not path.is_file():
            errors.append(f"{name}: required document missing")
            continue
        text = path.read_text(encoding="utf-8")
        for number, line in enumerate(text.splitlines(), 1):
            if line.rstrip() != line:
                errors.append(f"{name}:{number}: trailing whitespace")
        # Code examples are not navigable Markdown links.
        prose = re.sub(r"^```[^\n]*\n.*?^```[^\n]*$", "", text, flags=re.M | re.S)
        for target in re.findall(r"\]\(([^)]+)\)", prose):
            target = target.strip().removeprefix("<").removesuffix(">")
            parsed = urlsplit(target)
            if parsed.scheme or parsed.netloc or not parsed.path:
                continue
            links += 1
            destination = (path.parent / unquote(parsed.path)).resolve()
            if not destination.is_relative_to(root):
                errors.append(f"{name}: local link escapes repository: {target}")
            elif not destination.exists():
                errors.append(f"{name}: missing local link target: {target}")

    for directory, prefix, states in (
        ("specs", "SPEC", {"draft", "accepted", "superseded"}),
        ("adr", "ADR", {"proposed", "accepted", "rejected", "superseded"}),
    ):
        folder = root / "docs" / directory
        index_path = folder / "README.md"
        if not index_path.is_file():
            errors.append(f"docs/{directory}: index missing")
            continue
        index = index_path.read_text(encoding="utf-8")
        records = sorted(folder.glob("[0-9]*.md"))
        numbers: set[int] = set()
        if not records:
            errors.append(f"docs/{directory}: no numbered records")
        for path in records:
            name = path.relative_to(root)
            match = re.fullmatch(r"(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md", path.name)
            if not match:
                errors.append(f"{name}: invalid record filename")
                continue
            number = int(match[1])
            if number == 0 or number in numbers:
                errors.append(f"{name}: invalid or duplicate record number")
            numbers.add(number)
            text = path.read_text(encoding="utf-8")
            identifier = f"{prefix}-{number:04d}"
            if not text.startswith(f"# {identifier}: "):
                errors.append(f"{name}: title must begin with {identifier}")
            statuses = re.findall(r"^- Status: (\S+)\s*$", text, re.M)
            if len(statuses) != 1 or statuses[0] not in states:
                errors.append(f"{name}: missing or invalid status")
                continue
            status = statuses[0]
            rows = [line for line in index.splitlines() if f"]({path.name})" in line]
            if len(rows) != 1 or f"[{identifier}]" not in rows[0] or f"| {status} |" not in rows[0]:
                errors.append(f"{name}: index identifier/status missing or inconsistent")
            if status == "accepted":
                approval = re.search(r"^- Approval: (.+)$", text, re.M)
                if not approval or approval[1].strip().lower() == "pending":
                    errors.append(f"{name}: accepted record lacks approval provenance")
            if directory == "specs":
                criteria = re.findall(
                    r"^\s*-\s+(?:\*\*)?(AC-\d+)(?:\*\*)?(?: \([^\n]+?\))?:",
                    text,
                    re.M,
                )
                if not criteria or len(criteria) != len(set(criteria)):
                    errors.append(f"{name}: missing or duplicate acceptance criterion IDs")
                if not re.search(r"^- Implementation: (not started|in progress|verified)$", text, re.M):
                    errors.append(f"{name}: missing or invalid implementation status")
    return errors, len(files), links


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    errors, files, links = check(args.root.resolve())
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    print(f"PASS: {files} Markdown files, {links} local links; record/index/status/criterion checks.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
