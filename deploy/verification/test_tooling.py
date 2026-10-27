from __future__ import annotations

import importlib.util
import contextlib
import io
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from report import render_report

EXPORTER_SPEC = importlib.util.spec_from_file_location(
	"export_artifacts", HERE / "export-artifacts.py"
)
assert EXPORTER_SPEC is not None and EXPORTER_SPEC.loader is not None
export_artifacts = importlib.util.module_from_spec(EXPORTER_SPEC)
EXPORTER_SPEC.loader.exec_module(export_artifacts)
SNAPSHOT_SPEC = importlib.util.spec_from_file_location("snapshot", HERE / "snapshot.py")
assert SNAPSHOT_SPEC is not None and SNAPSHOT_SPEC.loader is not None
snapshot = importlib.util.module_from_spec(SNAPSHOT_SPEC)
SNAPSHOT_SPEC.loader.exec_module(snapshot)
SUMMARY_SPEC = importlib.util.spec_from_file_location(
	"integration_summary", HERE.parents[1] / "scripts/summarize-integration-tests.py"
)
assert SUMMARY_SPEC is not None and SUMMARY_SPEC.loader is not None
integration_summary = importlib.util.module_from_spec(SUMMARY_SPEC)
SUMMARY_SPEC.loader.exec_module(integration_summary)
SELECTION_SPEC = importlib.util.spec_from_file_location(
	"integration_selection", HERE.parents[1] / "scripts/select-integration-tests.py"
)
assert SELECTION_SPEC is not None and SELECTION_SPEC.loader is not None
integration_selection = importlib.util.module_from_spec(SELECTION_SPEC)
SELECTION_SPEC.loader.exec_module(integration_selection)


class VerificationToolingTests(unittest.TestCase):
	def test_command_discovery_preserves_each_name_in_order(self) -> None:
		names = ["TestIntegrationFirst", "TestIntegrationExecutableBrowserSLICollectionJourney", "TestIntegrationLast"]
		self.assertEqual(integration_selection.select("\n".join(names) + "\nok  package 0.01s\n", "command"), names)
		self.assertEqual(integration_selection.select("TestIntegrationFirst\nTestIntegrationLast\nok  package 0.01s\n", "storage"), [names[0], names[2]])

	def test_storage_discovery_prioritizes_sli_without_changing_other_names(self) -> None:
		priority = "TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict"
		names = ["TestIntegrationFirst", "TestIntegrationSecond", priority, "TestIntegrationLast", "TestIntegrationExecutableBrowserSLICollectionJourney"]
		self.assertEqual(integration_selection.select("\n".join(names) + "\n", "storage"), [priority, names[0], names[1], names[3], names[4]])
		self.assertEqual(integration_selection.select("\n".join(names) + "\n", "command"), names)

	def test_command_discovery_rejects_empty_duplicate_invalid_and_missing_sli(self) -> None:
		sli = "TestIntegrationExecutableBrowserSLICollectionJourney"
		for source in ["", "TestIntegrationFirst\n", f"{sli}\n{sli}\n", f"{sli}\nTestIntegrationBad/child\n", f"{sli}\nunexpected output\n"]:
			with self.subTest(source=source), self.assertRaises(ValueError):
				integration_selection.select(source, "command")
		for source in ["", "TestIntegrationFirst\nTestIntegrationFirst\n", "TestIntegrationBad/child\n"]:
			with self.subTest(source=source), self.assertRaises(ValueError):
				integration_selection.select(source, "storage")
		with self.assertRaises(ValueError):
			integration_selection.select(f"{sli}\n", "other")

	def test_integration_summary_preserves_split_failures_and_sums_elapsed(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary)
			source = root / "events.jsonl"
			exit_status = root / "status"
			destination = root / "packages"
			package = "github.com/dsolsona/almanac/internal/command"
			events = [
				{"Package": package, "Action": "fail", "Test": "TestIntegrationFirst", "Elapsed": 3},
				{"Package": package, "Action": "fail", "Elapsed": 4},
				{"Package": package, "Action": "pass", "Test": "TestIntegrationSecond", "Elapsed": 6},
				{"Package": package, "Action": "pass", "Elapsed": 7},
			]
			source.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
			exit_status.write_text("1\n", encoding="utf-8")
			output = io.StringIO()
			with patch.multiple(integration_summary, SOURCE=source, EXIT_STATUS=exit_status, DESTINATION=destination), contextlib.redirect_stdout(output):
				self.assertEqual(integration_summary.main(), 0)
			self.assertIn(f"{package}: fail in 11.00s; 2 test events", output.getvalue())
			self.assertIn("TestIntegrationFirst", output.getvalue())
			self.assertEqual(len((destination / "command.jsonl").read_text(encoding="utf-8").splitlines()), 4)

	def test_integration_summary_requires_each_selected_test_to_pass_once(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary)
			source = root / "events.jsonl"
			exit_status = root / "status"
			destination = root / "packages"
			destination.mkdir()
			(destination / "selected-packages.txt").write_text("storage\ncommand\n", encoding="utf-8")
			for name in ["storage", "command"]:
				(destination / f"{name}-selected.txt").write_text("TestIntegrationFirst\nTestIntegrationSecond\n", encoding="utf-8")
			exit_status.write_text("0\n", encoding="utf-8")
			with patch.multiple(integration_summary, SOURCE=source, EXIT_STATUS=exit_status, DESTINATION=destination), contextlib.redirect_stdout(io.StringIO()):
				for target in ["storage", "command"]:
					for outcomes, expected in [(["pass", "pass"], 0), (["pass", "skip"], 1), (["pass"], 1), (["pass", "pass", "pass"], 1)]:
						events = []
						for name in ["storage", "command"]:
							actual = outcomes if name == target else ["pass", "pass"]
							for test, action in zip(["TestIntegrationFirst", "TestIntegrationSecond", "TestIntegrationSecond"], actual):
								events.append({"Package": f"github.com/dsolsona/almanac/internal/{name}", "Test": test, "Action": action})
						source.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
						self.assertEqual(integration_summary.main(), expected)
				events = [{"Package": f"github.com/dsolsona/almanac/internal/{name}", "Test": test, "Action": "pass"} for name in ["storage", "command"] for test in ["TestIntegrationFirst", "TestIntegrationSecond"]]
				source.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
				for name in ["storage", "command"]:
					manifest = destination / f"{name}-selected.txt"
					manifest.write_text("", encoding="utf-8")
					self.assertEqual(integration_summary.main(), 1)
					manifest.write_text("TestIntegrationFirst\nTestIntegrationFirst\n", encoding="utf-8")
					self.assertEqual(integration_summary.main(), 1)
					manifest.unlink()
					self.assertEqual(integration_summary.main(), 1)
					manifest.write_text("TestIntegrationFirst\nTestIntegrationSecond\n", encoding="utf-8")
				(destination / "selected-packages.txt").write_text("", encoding="utf-8")
				self.assertEqual(integration_summary.main(), 1)
				(destination / "selected-packages.txt").unlink()
				self.assertEqual(integration_summary.main(), 1)
	def test_snapshot_keeps_all_migrations_and_excludes_credential_files(self) -> None:
		migrations = HERE.parents[1] / "internal/storage/migrations"
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary) / "source"
			destination = Path(temporary) / "snapshot"
			names = []
			for migration in sorted(migrations.glob("*.sql")):
				name = f"internal/storage/migrations/{migration.name}"
				path = root / name
				path.parent.mkdir(parents=True, exist_ok=True)
				path.write_bytes(migration.read_bytes())
				names.append(name)
			self.assertIn("internal/storage/migrations/0007_agent_credential_retirement.sql", names)
			sources = {"docs/credential-policy.md": "# Credential policy\n",
				"api/agent/credential.schema.json": '{"type":"object"}\n',
				"internal/storage/query/credential.sql": "SELECT 1;\n"}
			secrets = {"api/agent/prod-credentials.json": '{"secret":"private"}\n',
				"docs/credentials.yaml": "secret: private\n", "internal/storage/query/credential.key": "private",
				".env": "PASSWORD=private\n"}
			for name, content in (sources | secrets).items():
				path = root / name
				path.parent.mkdir(parents=True, exist_ok=True)
				path.write_text(content, encoding="utf-8")
			with patch.object(snapshot, "git_paths", side_effect=lambda _root, *args: list(names) + list(sources) + list(secrets) if args == ("--cached",) else []):
				excluded = snapshot.create_snapshot(root, destination)
			self.assertEqual(excluded, len(secrets))
			self.assertEqual({path.name for path in (destination / "internal/storage/migrations").glob("*.sql")},
				{path.name for path in migrations.glob("*.sql")})
			for name in sources:
				self.assertTrue((destination / name).is_file(), name)
			for name in secrets:
				self.assertFalse((destination / name).exists(), name)

	def test_snapshot_rejects_credential_material_in_included_sources(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary)
			path = root / "internal/storage/migrations/0007_agent_credential_retirement.sql"
			path.parent.mkdir(parents=True)
			path.write_text("-----BEGIN " + "PRIVATE KEY-----\n", encoding="utf-8")
			with patch.object(snapshot, "git_paths", side_effect=lambda _root, *args: [path.relative_to(root).as_posix()] if args == ("--cached",) else []):
				with self.assertRaisesRegex(ValueError, "candidate contains credential material"):
					snapshot.source_files(root)

	def test_snapshot_rejects_known_secret_in_untracked_schema(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary)
			path = root / "api/agent/credential.schema.json"
			path.parent.mkdir(parents=True)
			path.write_text('"' + 'AKIA' + 'A' * 16 + '"', encoding="utf-8")
			with patch.object(snapshot, "git_paths", side_effect=lambda _root, *args: [path.relative_to(root).as_posix()] if args == ("--others", "--exclude-standard") else []):
				with self.assertRaisesRegex(ValueError, "candidate contains credential material"):
					snapshot.source_files(root)

	def test_snapshot_includes_untracked_browser_mjs_and_rejects_secrets(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary) / "source"
			destination = Path(temporary) / "snapshot"
			names = ["web/scripts/check-sli-browser.mjs", "web/scripts/check-credential-browser.mjs"]
			for name in names:
				path = root / name
				path.parent.mkdir(parents=True, exist_ok=True)
				path.write_text("console.log('browser check');\n", encoding="utf-8")
			with patch.object(snapshot, "git_paths", side_effect=lambda _root, *args: names if args == ("--others", "--exclude-standard") else []):
				self.assertEqual(snapshot.create_snapshot(root, destination), 0)
				for name in names:
					self.assertEqual((destination / name).read_text(encoding="utf-8"), "console.log('browser check');\n")
				(root / names[0]).write_text('"' + 'AKIA' + 'A' * 16 + '"', encoding="utf-8")
				with self.assertRaisesRegex(ValueError, "candidate contains credential material"):
					snapshot.source_files(root)

	def test_report_keeps_shell_syntax_as_literal_text(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			marker = Path(temporary) / "must-not-exist"
			injected = f"literal `touch {marker}` and $(touch {marker})"
			report = render_report(
				"start",
				"finish",
				"failed",
				injected,
				"3",
				"setup",
				"candidate",
				"artifacts",
				"database",
				"role",
				"15487",
				"step_full_exit=1",
			)
			self.assertIn(injected, report)
			self.assertIn("`make verify VERIFY_MODE=linux`", report)
			self.assertIn("`make verify VERIFY_MODE=linux-sli`", report)
			self.assertIn("does not run the full Linux gate", report)
			self.assertFalse(marker.exists())

	def test_collect_exports_explicit_browser_evidence_and_redacts_text(self) -> None:
		with tempfile.TemporaryDirectory() as temporary:
			root = Path(temporary)
			candidate = root / "candidate"
			artifacts = root / "artifacts"
			prefixes = (
				"browser-auth-",
				"browser-agents-",
				"browser-agent-status-",
				"browser-datasources-",
				"browser-sli-",
			)
			for prefix in prefixes:
				directory = candidate / ".local" / f"{prefix}fixture"
				directory.mkdir(parents=True)
				(directory / "stage.log").write_text("token=fixture-secret\n", encoding="utf-8")
				(directory / "screen.png").write_bytes(b"fixture-image")
			unknown = candidate / ".local" / "browser-unrelated-fixture"
			unknown.mkdir(parents=True)
			(unknown / "stage.log").write_text("unrelated\n", encoding="utf-8")

			count = export_artifacts.collect(candidate, artifacts)
			self.assertEqual(count, len(prefixes) * 2)
			for prefix in prefixes:
				stage = artifacts / "acceptance" / f"{prefix}fixture" / "stage.log"
				screenshot = artifacts / "screenshots" / "integration" / f"{prefix}fixture" / "screen.png"
				self.assertEqual(stage.read_text(encoding="utf-8"), "token=[REDACTED]\n")
				self.assertEqual(screenshot.read_bytes(), b"fixture-image")
			self.assertFalse((artifacts / "acceptance" / "browser-unrelated-fixture").exists())

	def test_setup_fingerprint_layer_follows_expensive_dependency_layers(self) -> None:
		dockerfile = (HERE / "Dockerfile").read_text(encoding="utf-8")
		fingerprint_copy = dockerfile.index("COPY setup-fingerprint")
		victoria_metrics_copy = dockerfile.index(
			"COPY --from=victoria-metrics /victoria-metrics-prod /usr/local/bin/victoria-metrics-prod"
		)
		self.assertIn(
			"FROM victoriametrics/victoria-metrics:v1.155.0@sha256:f60dc268b54ecf87f6e09d740c2b732838aa9193090cc741417c9f6386a130df AS victoria-metrics",
			dockerfile,
		)
		self.assertLess(dockerfile.index("RUN npm ci"), fingerprint_copy)
		self.assertLess(dockerfile.index("RUN go mod download"), fingerprint_copy)
		self.assertLess(dockerfile.index("go install github.com/sqlc-dev/sqlc"), fingerprint_copy)
		self.assertLess(victoria_metrics_copy, fingerprint_copy)
		self.assertLess(fingerprint_copy, dockerfile.index("LABEL org.almanac.verification.setup-fingerprint"))

	def test_sli_journey_uses_a_fixed_integration_test_selector(self) -> None:
		makefile = (HERE.parents[1] / "Makefile").read_text(encoding="utf-8")
		self.assertIn(
			"slis) expected='TestIntegrationExecutableBrowserSLICollectionJourney'; journey='^TestIntegrationExecutableBrowserSLICollectionJourney$$' ;;",
			makefile,
		)

	def test_linux_sli_mode_dispatches_to_a_fixed_focused_entrypoint(self) -> None:
		makefile = (HERE.parents[1] / "Makefile").read_text(encoding="utf-8")
		self.assertTrue('"$${VERIFY_MODE:-}" = "linux-sli"' in makefile)
		self.assertTrue("sh scripts/verify-linux.sh sli" in makefile)
		self.assertTrue("linux-sli" in makefile.split("unknown VERIFY_MODE", 1)[1])

		entrypoint = HERE / "verify-sli-container.sh"
		self.assertTrue(entrypoint.is_file(), "fixed focused SLI container entrypoint is required")
		source = entrypoint.read_text(encoding="utf-8")
		self.assertIn("ALMANAC_LINUX_VERIFICATION_CONTAINER=1", source)
		self.assertIn('ALMANAC_TEST_DATABASE_URL="$fixed_database_url"', source)
		self.assertIn("VERIFY_MODE=journey BROWSER_SCENARIO=slis", source)
		self.assertIn("focused_sli_journey", source)
		self.assertNotIn('"$@"', source)
		self.assertIn("selected SLI browser journey did not execute and pass", source)
		result = subprocess.run(
			["sh", str(entrypoint), "unapproved"], capture_output=True, text=True, check=False
		)
		self.assertEqual(result.returncode, 2)
		self.assertIn("does not accept arguments", result.stderr)

	def test_focused_container_entrypoint_is_fingerprinted(self) -> None:
		fingerprint = (HERE / "fingerprint.py").read_text(encoding="utf-8")
		self.assertIn('"deploy/verification/verify-sli-container.sh"', fingerprint)
		outer = (HERE.parents[1] / "scripts" / "verify-linux.sh").read_text(encoding="utf-8")
		self.assertIn("setup|verify|sli)", outer)
		self.assertIn("/candidate/deploy/verification/verify-sli-container.sh", outer)

	def test_fixed_verifier_vm_stderr_is_retained_with_a_size_bound(self) -> None:
		helper = (HERE.parents[1] / "internal/command/agent_management_runtime_integration_test.go").read_text(encoding="utf-8")
		self.assertTrue(
			"maxVictoriaMetricsStderrBytes" in helper
			and '"browser-sli-victoriametrics-"' in helper
			and "boundedStderrWriter" in helper,
			"fixed verifier VM stderr must be retained in a bounded browser-SLI artifact",
		)

	def test_cleanup_reports_failed_invocation_owned_container_removal(self) -> None:
		source = (HERE.parents[1] / "scripts" / "verify-linux.sh").read_text(encoding="utf-8")
		cleanup = source.split("cleanup() {", 1)[1].split("\n}", 1)[0]
		self.assertIn("failed to remove invocation-owned verifier container", cleanup)
		self.assertIn("failed to remove invocation-owned PostgreSQL container", cleanup)
		self.assertIn("cleanup_error=1", cleanup)
		self.assertIn("cleanup_failed", cleanup)
		self.assertNotIn("docker rm --force \"$verifier_id\" >/dev/null 2>&1 || true", cleanup)


if __name__ == "__main__":
	unittest.main()
