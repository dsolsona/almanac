#!/usr/bin/env python3
"""Redact verifier logs and export only reviewed acceptance artifacts."""

from __future__ import annotations

import json
import re
import shutil
import sys
from pathlib import Path
from typing import Any


DATABASE_URL = re.compile(r"(?i)(postgres(?:ql)?://[^:/\s]+:)[^@/\s]+(@)")
SECRET_ASSIGNMENT = re.compile(
	r"(?i)(\b(?:password|secret|token|api[_-]?key|authorization|cookie)\b\s*[:=]\s*)(?:\"[^\"]*\"|'[^']*'|[^\s,;}\]]+)"
)
SECRET_KEY = re.compile(r"(?i)(password|secret|token|credential|authorization|cookie|private[_-]?key)")
IMAGE_SUFFIXES = {".png", ".jpg", ".jpeg"}
TEXT_SUFFIXES = {".json", ".jsonl", ".log", ".txt", ".exit"}
BROWSER_ARTIFACT_PREFIXES = (
	"browser-auth-",
	"browser-agents-",
	"browser-agent-status-",
	"browser-datasources-",
	"browser-sli-",
)


def redact_text(text: str) -> str:
	text = DATABASE_URL.sub(r"\1[REDACTED]\2", text)
	return SECRET_ASSIGNMENT.sub(r"\1[REDACTED]", text)


def redact_value(value: Any) -> Any:
	if isinstance(value, dict):
		result = {}
		for key, nested in value.items():
			if SECRET_KEY.search(str(key)):
				result[key] = "[REDACTED]"
			else:
				result[key] = redact_value(nested)
		return result
	if isinstance(value, list):
		return [redact_value(nested) for nested in value]
	if isinstance(value, str):
		return redact_text(value)
	return value


def sanitize_bytes(data: bytes, suffix: str) -> bytes:
	text = data.decode("utf-8", errors="replace")
	if suffix.lower() in {".json", ".jsonl"}:
		lines = []
		for line in text.splitlines():
			try:
				value = json.loads(line)
			except json.JSONDecodeError:
				lines.append(redact_text(line))
			else:
				lines.append(json.dumps(redact_value(value), separators=(",", ":"), ensure_ascii=False))
		return ("\n".join(lines) + ("\n" if text.endswith("\n") else "")).encode("utf-8")
	return redact_text(text).encode("utf-8")


def sanitize_log(source: Path, destination: Path) -> None:
	destination.parent.mkdir(parents=True, exist_ok=True)
	destination.write_bytes(sanitize_bytes(source.read_bytes(), source.suffix))


def copy_text(source: Path, destination: Path) -> None:
	destination.parent.mkdir(parents=True, exist_ok=True)
	destination.write_bytes(sanitize_bytes(source.read_bytes(), source.suffix))


def copy_images(source: Path, destination: Path, base: Path) -> int:
	count = 0
	if not source.exists():
		return count
	for path in sorted(source.rglob("*")):
		if not path.is_symlink() and path.is_file() and path.suffix.lower() in IMAGE_SUFFIXES:
			target = destination / base / path.relative_to(source)
			target.parent.mkdir(parents=True, exist_ok=True)
			shutil.copy2(path, target)
			count += 1
	return count


def copy_text_tree(source: Path, destination: Path, base: Path) -> int:
	count = 0
	if not source.exists():
		return count
	for path in sorted(source.rglob("*")):
		if not path.is_symlink() and path.is_file() and path.suffix.lower() in TEXT_SUFFIXES:
			copy_text(path, destination / base / path.relative_to(source))
			count += 1
	return count


def collect(candidate: Path, destination: Path) -> int:
	count = 0
	local = candidate / ".local"
	for relative in (
		Path("integration-go-test.jsonl"),
		Path("integration-go-test.exit"),
	):
		source = local / relative
		if source.is_file():
			copy_text(source, destination / "integration" / relative)
			count += 1

	per_package = local / "integration-test-json"
	if per_package.is_dir():
		count += copy_text_tree(per_package, destination / "integration" / "packages", Path())

	for prefix in BROWSER_ARTIFACT_PREFIXES:
		for path in sorted(local.glob(f"{prefix}*")):
			if path.is_dir():
				count += copy_text_tree(path, destination / "acceptance" / path.name, Path())
				count += copy_images(path, destination / "screenshots" / "integration", Path(path.name))

	web = candidate / "web"
	for relative in (Path("test-results"), Path("playwright-report")):
		count += copy_images(web / relative, destination / "screenshots" / "catalogue", relative)

	manifest = destination / "artifact-index.txt"
	manifest.parent.mkdir(parents=True, exist_ok=True)
	manifest.write_text(f"sanitized acceptance artifacts exported: {count}\n", encoding="utf-8")
	return count


def main() -> int:
	if len(sys.argv) == 4 and sys.argv[1] == "sanitize-log":
		sanitize_log(Path(sys.argv[2]), Path(sys.argv[3]))
		return 0
	if len(sys.argv) == 4 and sys.argv[1] == "collect":
		count = collect(Path(sys.argv[2]), Path(sys.argv[3]))
		print(f"Exported sanitized acceptance artifacts: {count}")
		return 0
	print("usage: export-artifacts.py sanitize-log INPUT OUTPUT | collect CANDIDATE OUTPUT", file=sys.stderr)
	return 2


if __name__ == "__main__":
	raise SystemExit(main())
