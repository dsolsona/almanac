#!/usr/bin/env python3
"""Hash the fixed inputs that determine the Linux verifier tool cache."""

from __future__ import annotations

import hashlib
import os
import sys
from pathlib import Path


INPUTS = (
	"Makefile",
	"go.mod",
	"go.sum",
	"web/package.json",
	"web/package-lock.json",
	"tools/openapi/package.json",
	"tools/openapi/package-lock.json",
	"deploy/verification/Dockerfile",
	"deploy/verification/fingerprint.py",
	"deploy/verification/snapshot.py",
	"deploy/verification/verify-container.sh",
	"deploy/verification/verify-sli-container.sh",
	"deploy/verification/export-artifacts.py",
	"deploy/verification/report.py",
	"deploy/verification/test_tooling.py",
	"scripts/verify-linux.sh",
	"scripts/setup-database.sh",
	"scripts/check-docs.py",
	"scripts/select-integration-tests.py",
	"scripts/summarize-integration-tests.py",
	"scripts/smoke-ui.sh",
)


def main() -> int:
	if len(sys.argv) != 2:
		print("usage: fingerprint.py REPOSITORY_ROOT", file=sys.stderr)
		return 2

	root = Path(sys.argv[1]).resolve()
	digest = hashlib.sha256()
	for relative in INPUTS:
		path = root / relative
		if not path.is_file():
			print(f"missing verifier setup input: {relative}", file=sys.stderr)
			return 2
		data = path.read_bytes()
		mode = os.stat(path).st_mode & 0o777
		digest.update(relative.encode("utf-8") + b"\0")
		digest.update(f"{mode:o}".encode("ascii") + b"\0")
		digest.update(len(data).to_bytes(8, "big"))
		digest.update(data)

	print(digest.hexdigest())
	return 0


if __name__ == "__main__":
	raise SystemExit(main())
