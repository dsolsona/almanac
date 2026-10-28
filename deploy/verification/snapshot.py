#!/usr/bin/env python3
"""Build and verify a source-only Linux candidate snapshot."""

from __future__ import annotations

import hashlib
import os
import re
import shutil
import stat
import subprocess
import sys
from pathlib import Path


EXCLUDED_PARTS = {
	".aws",
	".codex",
	".git",
	".local",
	".ssh",
	".worktrees",
	"node_modules",
}
SENSITIVE_BASENAMES = {
	".env",
	".envrc",
	".netrc",
	".npmrc",
	".pypirc",
	"credentials",
	"credentials.json",
	"credentials.yaml",
	"credentials.yml",
	"id_rsa",
	"id_ed25519",
	"service-account.json",
	"secrets.yaml",
	"secrets.yml",
}
SENSITIVE_SUFFIXES = {".key", ".pem", ".p12", ".pfx", ".p7b", ".p7c"}
PRIVATE_KEY_RE = re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----")
UNTRACKED_SECRET_RE = re.compile(
	r"(?i)(?:gh[pousr]_[A-Za-z0-9]{30,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)"
)
UNTRACKED_PREFIXES = (
	"api/",
	"cmd/",
	"deploy/",
	"docs/",
	"internal/",
	"scripts/",
	"testdata/",
	"tests/",
	"tools/",
	"web/",
)
UNTRACKED_SUFFIXES = {
	".css",
	".go",
	".html",
	".js",
	".json",
	".jsx",
	".md",
	".mjs",
	".py",
	".sh",
	".sql",
	".svg",
	".ts",
	".tsx",
	".txt",
	".yaml",
	".yml",
}


def git_paths(root: Path, *args: str) -> list[str]:
	result = subprocess.run(
		["git", "ls-files", "-z", *args],
		cwd=root,
		check=True,
		stdout=subprocess.PIPE,
	)
	return [os.fsdecode(item) for item in result.stdout.split(b"\0") if item]


def should_exclude(relative: str) -> bool:
	path = Path(relative)
	parts = path.parts
	name = path.name.lower()
	if any(part in EXCLUDED_PARTS for part in parts):
		return True
	if name in SENSITIVE_BASENAMES or name.startswith(".env."):
		return True
	if path.suffix.lower() in SENSITIVE_SUFFIXES:
		return True
	if name.startswith(("id_rsa", "id_ed25519")) or "service-account" in name:
		return True
	if "credential" in name:
		source_code = path.suffix.lower() in {".go", ".js", ".jsx", ".mjs", ".py", ".ts", ".tsx"}
		storage_sql = relative.startswith(("internal/storage/migrations/", "internal/storage/query/")) and path.suffix.lower() == ".sql"
		documentation = relative.startswith("docs/") and path.suffix.lower() == ".md"
		api_schema = relative.startswith("api/") and name.endswith(".schema.json")
		if not (source_code or storage_sql or documentation or api_schema):
			return True
	return False


def content_hash(paths: list[Path], root: Path) -> str:
	digest = hashlib.sha256()
	for path in sorted(paths, key=lambda item: item.relative_to(root).as_posix()):
		relative = path.relative_to(root).as_posix()
		metadata = path.lstat()
		digest.update(relative.encode("utf-8") + b"\0")
		digest.update(f"{stat.S_IMODE(metadata.st_mode):o}".encode("ascii") + b"\0")
		if stat.S_ISLNK(metadata.st_mode):
			target = os.readlink(path).encode("utf-8")
			digest.update(b"symlink\0" + len(target).to_bytes(8, "big") + target)
		elif stat.S_ISREG(metadata.st_mode):
			data = path.read_bytes()
			digest.update(b"file\0" + len(data).to_bytes(8, "big") + data)
		else:
			raise ValueError(f"unsupported source file type: {relative}")
	return digest.hexdigest()


def source_files(root: Path) -> tuple[list[Path], int]:
	tracked = set(git_paths(root, "--cached"))
	untracked = set(git_paths(root, "--others", "--exclude-standard"))
	selected: list[Path] = []
	excluded = 0
	for relative in sorted(tracked | untracked):
		if should_exclude(relative):
			excluded += 1
			continue
		if relative in untracked and relative not in tracked:
			path_name = Path(relative).name
			if not relative.startswith(UNTRACKED_PREFIXES) or (
				Path(relative).suffix.lower() not in UNTRACKED_SUFFIXES and path_name != "Dockerfile"
			):
				excluded += 1
				continue
		path = root / relative
		if not path.exists() and not path.is_symlink():
			continue
		if not path.is_symlink() and not path.is_file():
			excluded += 1
			continue
		if path.is_symlink():
			resolved = path.resolve(strict=False)
			try:
				resolved.relative_to(root)
			except ValueError:
				raise ValueError(f"source symlink escapes the repository: {relative}") from None
		if path.is_file():
			data = path.read_bytes()
			if PRIVATE_KEY_RE.search(data) or (
				relative in untracked and relative not in tracked and UNTRACKED_SECRET_RE.search(data.decode("utf-8", errors="ignore"))
			):
				raise ValueError(f"candidate contains credential material in: {relative}")
		selected.append(path)

	selected_names = {path.relative_to(root).as_posix() for path in selected}
	for path in selected:
		if path.is_symlink():
			target = (path.parent / os.readlink(path)).resolve(strict=False)
			try:
				target_name = target.relative_to(root).as_posix()
			except ValueError:
				raise ValueError(f"source symlink escapes the repository: {path.relative_to(root)}") from None
			if target_name not in selected_names:
				raise ValueError(f"source symlink target is excluded from snapshot: {path.relative_to(root)}")
	return selected, excluded


def create_snapshot(root: Path, destination: Path) -> int:
	if destination.exists() and any(destination.iterdir()):
		raise ValueError("snapshot destination must be an empty directory")
	destination.mkdir(parents=True, exist_ok=True)
	files, excluded = source_files(root)
	for source in files:
		relative = source.relative_to(root)
		target = destination / relative
		target.parent.mkdir(parents=True, exist_ok=True)
		if source.is_symlink():
			os.symlink(os.readlink(source), target)
		else:
			shutil.copy2(source, target)
	return excluded


def snapshot_paths(root: Path) -> list[Path]:
	paths: list[Path] = []
	for current, directories, files in os.walk(root, followlinks=False):
		current_path = Path(current)
		directories[:] = sorted(
			name for name in directories if not (current_path / name).is_symlink()
		)
		for name in sorted(files):
			path = current_path / name
			if path.is_symlink() or path.is_file():
				paths.append(path)
		for name in sorted(os.listdir(current_path)):
			path = current_path / name
			if path.is_symlink() and path not in paths:
				paths.append(path)
	return paths


def main() -> int:
	if len(sys.argv) not in (3, 4):
		print("usage: snapshot.py create REPOSITORY_ROOT SNAPSHOT_DIR | verify SNAPSHOT_DIR SHA256", file=sys.stderr)
		return 2

	try:
		operation = sys.argv[1]
		if operation == "create" and len(sys.argv) == 4:
			root = Path(sys.argv[2]).resolve()
			destination = Path(sys.argv[3]).resolve()
			excluded = create_snapshot(root, destination)
			source_hash = content_hash(snapshot_paths(destination), destination)
			print(f"source_sha256={source_hash}")
			print(f"source_files={len(snapshot_paths(destination))}")
			print(f"excluded_local_or_sensitive_files={excluded}")
			return 0
		if operation == "verify" and len(sys.argv) == 4:
			directory = Path(sys.argv[2]).resolve()
			expected = sys.argv[3]
			if not re.fullmatch(r"[0-9a-f]{64}", expected):
				print("expected source SHA256 must be 64 lowercase hexadecimal characters", file=sys.stderr)
				return 2
			actual = content_hash(snapshot_paths(directory), directory)
			if actual != expected:
				print("read-only candidate source snapshot hash mismatch", file=sys.stderr)
				return 1
			print(f"verified_source_sha256={actual}")
			return 0
		print("invalid snapshot operation", file=sys.stderr)
		return 2
	except (OSError, subprocess.CalledProcessError, ValueError) as error:
		print(f"snapshot failed: {error}", file=sys.stderr)
		return 2


if __name__ == "__main__":
	raise SystemExit(main())
