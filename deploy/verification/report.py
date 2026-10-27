#!/usr/bin/env python3
"""Render the Linux verifier report without interpreting its contents."""

from __future__ import annotations

import sys
from pathlib import Path


def render_report(
	started: str,
	finished: str,
	state: str,
	detail: str,
	elapsed: str,
	setup_fingerprint: str,
	candidate_hash: str,
	artifact_ref: str,
	database: str,
	username: str,
	port: str,
	status_details: str,
) -> str:
	result_details = status_details.strip() or "No verification gates started."
	return (
		"# Linux verification report\n\n"
		f"Started: {started}\n"
		f"Finished: {finished}\n"
		f"Status: {state} ({detail})\n"
		f"Elapsed: {elapsed}s\n"
		f"Setup fingerprint: {setup_fingerprint}\n"
		f"Candidate source SHA256: {candidate_hash}\n"
		f"Artifacts: {artifact_ref}\n\n"
		"## Fixed entry points\n\n"
		"- Setup: `make setup SETUP_MODE=verification`\n"
		"- Verification: `make verify VERIFY_MODE=linux`\n"
		"- Focused SLI journey: `make verify VERIFY_MODE=linux-sli` runs only the real `TestIntegrationExecutableBrowserSLICollectionJourney`; it does not run the full Linux gate.\n"
		"- Full inner sequence: ordinary `make verify`, `make verify VERIFY_MODE=go-race`, `make verify VERIFY_MODE=integration`, and `make verify VERIFY_MODE=browser`; fail fast at the first unsuccessful gate.\n\n"
		"## Fixture and isolation\n\n"
		f"The integration fixture is the existing F1-T05 identity: database `{database}`, role `{username}`, PostgreSQL 18.6, loopback `127.0.0.1:{port}`. PostgreSQL and the verifier share only this invocation's private network namespace. The fixture has no host port, named volume, Docker socket, or host data mount. The candidate snapshot is read-only; only sanitized artifacts are exported.\n\n"
		"## Result details\n\n"
		f"{result_details}\n"
	)


def main() -> int:
	if len(sys.argv) != 14:
		print(
			"usage: report.py OUTPUT STARTED FINISHED STATE DETAIL ELAPSED SETUP_SHA256 CANDIDATE_SHA256 ARTIFACTS DATABASE USER PORT STATUS_FILE",
			file=sys.stderr,
		)
		return 2

	(
		_, output, started, finished, state, detail, elapsed, setup_fingerprint,
		candidate_hash, artifact_ref, database, username, port, status_file,
	) = sys.argv
	status_path = Path(status_file)
	status_details = status_path.read_text(encoding="utf-8") if status_path.is_file() else ""
	Path(output).write_text(
		render_report(
			started,
			finished,
			state,
			detail,
			elapsed,
			setup_fingerprint,
			candidate_hash,
			artifact_ref,
			database,
			username,
			port,
			status_details,
		),
		encoding="utf-8",
	)
	return 0


if __name__ == "__main__":
	raise SystemExit(main())
