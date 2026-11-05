import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import threading
import unittest
from types import SimpleNamespace
from unittest.mock import patch

MODULE_PATH = Path(__file__).resolve().parents[1] / "verify.py"
REPO_ROOT = MODULE_PATH.parents[1]
SPEC = importlib.util.spec_from_file_location("verify", MODULE_PATH)
vc = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(vc)
GO_SOURCES = ("go.mod", "cmd/almanac/main.go", "internal/backendhttp/server.go", "internal/command/agent_management_runtime_integration_test.go")


class VerifyTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.base = Path(self.temp.name).resolve()  # macOS temp paths are symlinks; receipts compare resolved paths
        self.root = self.base / "checkout"
        self.receipts = self.base / "shared-receipts"
        self.receipts.mkdir(parents=True)
        self.root.mkdir()
        self.old_root, self.old_receipts = vc.ROOT, vc.RECEIPTS
        vc.ROOT, vc.RECEIPTS = self.root, self.receipts

    def tearDown(self):
        vc.ROOT, vc.RECEIPTS = self.old_root, self.old_receipts
        self.temp.cleanup()

    def put(self, rel, contents="x"):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(contents)
        return path

    def go_receipt(self, name, **extra):
        """Write a passing full-scope go receipt into the active receipt directory."""
        vc.RECEIPTS.mkdir(parents=True, exist_ok=True)
        artifact = vc.RECEIPTS / f"{name}.log"
        artifact.write_text("actual run output")
        digest = vc.mode_fingerprint("go")
        record = {"mode": "go", "origin": str(vc.ROOT), "fingerprint": digest, "fingerprint_at_end": digest, "result": "passed", "exit_code": 0,
                  "full_scope": True, "selectors": {"go_package": "./..."}, "artifacts": [artifact.name], **extra}
        vc.atomic_json(vc.RECEIPTS / f"{name}.json", record)
        return record

    # --- mode table and selectors

    def test_mode_table_names_only_container_modes_and_fingerprints_this_script(self):
        self.assertEqual(set(vc.MODES), set(vc.MODE_INPUTS))
        for retired in ("linux", "linux-sli", "go-portability", "slo-unit", "slo-integration", "slo-journey", "agent-lifecycle", "verification-tools"):
            self.assertNotIn(retired, vc.MODES)
        for mode, inputs in vc.MODE_INPUTS.items():
            self.assertTrue("scripts" in inputs or "scripts/verify.py" in inputs, mode)
        makefile = (REPO_ROOT / "Makefile").read_text()
        for name in ("VERIFY_RERUN_REASON", "VERIFY_RERUN_BOUNDARY", "VERIFY_FOCUSED_RECEIPT", "VERIFICATION_PLAN", "CMD"):
            self.assertNotIn(f"$({name})", makefile)
        self.assertNotIn("verification-controls", makefile)
        self.assertNotIn("setup-database", makefile)
        ordinary = makefile.split("_verify_all:\n", 1)[1].split("\n_verify_web:", 1)[0]
        self.assertNotIn("performance", ordinary)

    def test_fixture_identity_is_fixed_and_never_read_from_arguments(self):
        self.assertEqual(vc.FIXTURE_URL, "postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable")
        compose = (REPO_ROOT / "compose.dev.yaml").read_text()
        self.assertIn("- ALMANAC_TEST_DATABASE_URL=" + vc.FIXTURE_URL, compose)
        self.assertIn("- ALMANAC_IN_DEV_CONTAINER=1", compose)
        self.assertIn('PGPORT: "15487"', compose)
        with patch.dict("os.environ", {"ALMANAC_TEST_DATABASE_URL": "postgres://other"}):
            with self.assertRaises(ValueError):
                vc.fixture_check()

    def test_exact_or_planned_integration_cannot_be_a_full_gate_receipt(self):
        self.assertFalse(vc.receipt_is_full_scope("integration", {"go_package": "all", "integration_test": "TestIntegrationA"}))
        self.assertFalse(vc.receipt_is_full_scope("integration", {"go_package": "all", "plan_only": True}))
        self.assertTrue(vc.receipt_is_full_scope("integration", {"go_package": "all", "integration_selection": "affected"}))
        self.assertFalse(vc.receipt_is_full_scope("unit", {"test_file": "tests/one.test.tsx"}))
        self.assertFalse(vc.receipt_is_full_scope("journey", {"browser_scenario": "unknown"}))

    def test_selectors_are_validated_before_any_command_runs(self):
        with patch.dict("os.environ", {"TEST_FILE": "../escape.spec.ts"}):
            with self.assertRaises(ValueError):
                vc.gate_commands("browser")
        with patch.dict("os.environ", {"BROWSER_SCENARIO": "not-a-journey"}):
            with self.assertRaises(ValueError):
                vc.gate_commands("journey")
        with patch.dict("os.environ", {"GO_PACKAGE": "./internal/secretfile"}):
            with self.assertRaises(ValueError):
                vc.gate_commands("integration")
        with patch.object(vc, "docker_available", return_value=False):
            with self.assertRaises(ValueError):
                vc.gate_commands("compose")
        with self.assertRaises(ValueError):
            vc.run_gate("linux")

    def test_integration_reuse_report_requires_complete_honest_coverage(self):
        cases = [{"package": "storage", "test": "TestIntegrationA", "result": "reused", "evidence": {"result": "passed"}},
                 {"package": "command", "test": "TestIntegrationB", "result": "passed", "evidence": {"result": "passed"}}]
        report = {"cases": cases, "started_unix": 101, "ended_unix": 102, "full_scope": True,
                  "discovered": 2, "selected": 2, "executed": 1, "reused": 1}
        vc.validate_integration_report(report, True, 100)
        for changes in ({"cases": []}, {"started_unix": 99}, {"discovered": 3}, {"executed": 2},
                        {"cases": [cases[0], cases[0]]}, {"cases": [cases[0], {**cases[1], "result": "failed"}]}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                vc.validate_integration_report({**report, **changes}, True, 100)

    def test_storage_discovery_checks_retained_regressions_early_without_losing_cases(self):
        names = ["TestIntegrationOther", "TestIntegrationRetainedDenseHistoryAndPersistedRestart",
                 "TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict", "TestIntegrationRetainedMinuteHistoryAndPersistedRestart"]
        got = vc.select_cases("\n".join(names), "storage")
        self.assertEqual(got, [names[2], names[3], names[1], names[0]])
        self.assertCountEqual(got, names)
        with self.assertRaises(ValueError):
            vc.select_cases("TestIntegrationOther\nTestIntegrationOther", "storage")
        with self.assertRaises(ValueError):
            vc.select_cases("TestIntegrationOnly", "command")
        with self.assertRaises(ValueError):
            vc.select_cases("ok  \tgithub.com/dsolsona/almanac/internal/storage\t0.004s", "storage")
        self.assertEqual(vc.select_cases("ok  \tgithub.com/dsolsona/almanac/internal/agentcmd\t0.004s", "agentcmd"), [])

    # --- integration case inputs and reuse

    def test_go_dependency_change_invalidates_consumers_but_not_unrelated_packages(self):
        graph = {}
        for package, imports in (("a", ["shared"]), ("b", ["shared"]), ("other", []), ("shared", [])):
            self.put(f"internal/{package}/main.go")
            graph[vc.GO_PREFIX + "internal/" + package] = {
                "Dir": str(self.root / "internal" / package), "GoFiles": ["main.go"],
                "Imports": [vc.GO_PREFIX + "internal/" + item for item in imports]}

        def inputs(package):
            return vc.fingerprints(self.root, vc.go_inputs(self.root, graph, package), "fixture")
        before = {p: inputs(p) for p in ("a", "b", "other")}
        self.put("internal/shared/main.go", "changed")
        self.assertNotEqual(before["a"], inputs("a"))
        self.assertNotEqual(before["b"], inputs("b"))
        self.assertEqual(before["other"], inputs("other"))

    def test_browser_imports_include_transitive_and_missing_inputs(self):
        self.put("web/scripts/a.mjs", "import './b.mjs';")
        self.put("web/scripts/b.mjs", "export { x } from './missing.mjs';")
        paths = vc.script_inputs(self.root, ["web/scripts/a.mjs"])
        self.assertEqual(paths, {"web/scripts/a.mjs", "web/scripts/b.mjs", "web/scripts/missing.mjs"})
        before = vc.fingerprints(self.root, paths, "fixture")
        self.put("web/scripts/missing.mjs", "export const x = 1;")
        self.assertNotEqual(before, vc.fingerprints(self.root, paths, "fixture"))

    def test_failed_running_skipped_wrong_case_or_changed_artifact_cannot_be_reused(self):
        test = "TestIntegrationExample"
        artifact = self.put("evidence.jsonl", json.dumps({"Package": vc.GO_PREFIX + "internal/storage", "Test": test, "Action": "pass"}) + "\n")
        inputs = {"a.go": "hash", "@environment": "fixture"}
        record = {"result": "passed", "inputs": inputs, "artifact": "evidence.jsonl", "artifact_sha256": vc.digest(artifact.read_bytes())}
        self.assertIsNone(vc.reuse_reason(record, inputs, "storage", test, self.root))
        for state in ("failed", "running", "skipped"):
            self.assertIsNotNone(vc.reuse_reason({**record, "result": state}, inputs, "storage", test, self.root))
        self.assertIsNotNone(vc.reuse_reason(record, inputs, "storage", "TestIntegrationOther", self.root))
        self.assertIsNotNone(vc.reuse_reason(record, {**inputs, "a.go": "changed"}, "storage", test, self.root))
        self.assertIsNotNone(vc.reuse_reason(record, {**inputs, "@environment": "new fixture"}, "storage", test, self.root))
        artifact.write_text("changed evidence")
        self.assertIsNotNone(vc.reuse_reason(record, inputs, "storage", test, self.root))

    def test_nested_failure_and_duplicate_passes_do_not_count_as_pass(self):
        base = {"Package": vc.GO_PREFIX + "internal/storage", "Test": "TestIntegrationExample", "Action": "pass"}
        self.assertTrue(vc.passed([base], "storage", base["Test"]))
        self.assertFalse(vc.passed([base, base], "storage", base["Test"]))
        self.assertFalse(vc.passed([base, {**base, "Test": base["Test"] + "/nested", "Action": "fail"}], "storage", base["Test"]))

    # --- evidence counting

    def test_forward_and_backward_clock_steps_do_not_change_test_duration(self):
        for wall_end in (488, -288):
            evidence = vc.elapsed_evidence(100, wall_end, 20, 38.929)
            self.assertAlmostEqual(evidence["elapsed_seconds"], 18.929)
            self.assertEqual(evidence["wall_elapsed_seconds"], wall_end - 100)
            self.assertAlmostEqual(evidence["clock_adjustment_seconds"], wall_end - 100 - 18.929)
            self.assertEqual(evidence["elapsed_clock"], "monotonic")

    def test_go_receipt_requires_expected_test_to_run_and_pass_without_skip(self):
        events = self.receipts / "go.jsonl"
        events.write_text('\n'.join(json.dumps({"Action": action, "Test": "TestLifecycle"}) for action in ("run", "pass")))
        self.assertEqual(vc.counted_go_events(events, {"TestLifecycle"}), 1)
        events.write_text(json.dumps({"Action": "run", "Test": "TestLifecycle"}) + "\n" + json.dumps({"Action": "skip", "Test": "TestLifecycle"}))
        with self.assertRaises(ValueError):
            vc.counted_go_events(events, {"TestLifecycle"})

    def test_performance_requires_ten_slo_scope_and_excludes_deferred_fleets(self):
        name = "TestIntegrationControlledBackendPerformance"
        names = [name, f"{name}/independent-10"]
        events = self.put(".local/performance-go-test.jsonl", "\n".join(json.dumps({"Action": action, "Test": case}) for case in names for action in ("run", "pass")))
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(vc.performance_evidence(), 0)
        for action in ("skip", "fail"):
            events.write_text("\n".join(json.dumps({"Action": outcome, "Test": case}) for case in names for outcome in ("run", action if case == names[-1] else "pass")))
            with self.assertRaises(ValueError):
                vc.performance_evidence()
        events.write_text("\n".join(json.dumps({"Action": action, "Test": case}) for case in names[:-1] for action in ("run", "pass")))
        with self.assertRaises(ValueError):
            vc.performance_evidence()
        events.write_text("\n".join(json.dumps({"Action": action, "Test": case}) for case in names for action in ("run", "pass")) + "\n" + json.dumps({"Action": "run", "Test": f"{name}/independent-1000"}))
        with self.assertRaises(ValueError):
            vc.performance_evidence()

    def test_asset_preflight_failure_is_returned_and_saved_to_receipt_artifact(self):
        artifact = self.receipts / "preflight.log"
        result = SimpleNamespace(returncode=7, stdout=b"asset build failed\n")
        with patch.object(vc.subprocess, "run", return_value=result) as run, contextlib.redirect_stdout(io.StringIO()):
            status = vc.ensure_web_assets(artifact, {"PATH": "/bin"})
        self.assertEqual(status, 7)
        self.assertIn("asset build failed", artifact.read_text())
        self.assertEqual(run.call_args.args[0], ["make", "_ensure_web_assets"])

    def test_platform_record_names_os_arch_image_and_revision(self):
        with patch.dict("os.environ", {"ALMANAC_DEV_IMAGE_ID": "sha256:abc"}):
            record = vc.platform_record()
        self.assertEqual(set(record), {"os", "arch", "python", "image_id", "git_revision", "git_dirty"})
        self.assertEqual(record["image_id"], "sha256:abc")
        self.assertTrue(record["os"] and record["arch"])
        self.assertEqual(record["git_revision"], "unknown")  # the temporary root is not a repository

    # --- timing ledger

    def test_timing_ledger_deduplicates_runs_marks_unknown_and_unions_overlap(self):
        records = [
            {"id": "a", "mode": "unit", "result": "passed", "started_unix": 100, "ended_unix": 110, "elapsed_seconds": 10, "selected_count": 12, "executed_count": 12},
            {"id": "a", "mode": "unit", "result": "running", "started_unix": 100},
            {"id": "b", "mode": "unit", "result": "failed", "started_unix": 105, "ended_unix": 125, "elapsed_seconds": 20},
            {"id": "c", "mode": "integration", "result": "running", "started_unix": 130},
            {"id": "d", "mode": "integration", "result": "failed", "started_unix": 80, "ended_unix": 90},
            {"id": "e", "mode": "unit", "result": "passed", "started_unix": 100, "ended_unix": 200, "elapsed_seconds": 100, "parent_id": "b"},
        ]
        summary = vc.timing_ledger(records, 140)
        totals = summary["totals"]
        self.assertEqual(totals["runs"], 5)
        self.assertEqual((totals["passed"], totals["failed"], totals["ongoing"]), (2, 2, 1))
        self.assertEqual(totals["unknown_duration_runs"], 1)
        self.assertEqual(totals["cumulative_invocation_seconds"], 30)
        self.assertEqual(totals["top_level_wall_interval_union_seconds"], 45)
        self.assertEqual(totals["child_elapsed_seconds_excluded"], 100)
        self.assertEqual(summary["modes"]["integration"]["ongoing"], 1)
        self.assertEqual(summary["ledger"][0]["id"], "d")

    def test_timing_ledger_reads_fixed_manual_checks_with_unknown_duration(self):
        manual = self.root / ".local/verification/manual-runs.jsonl"
        manual.parent.mkdir(parents=True)
        manual.write_text(json.dumps({"id": "docs-check", "command": "python3 scripts/check-docs.py", "result": "passed", "selected_count": 134, "executed_count": 937, "elapsed_seconds": None}) + "\n")
        records, unreadable = vc.timing_receipt_records()
        summary = vc.timing_ledger(records, 140, unreadable)
        self.assertEqual(summary["totals"]["passed"], 1)
        self.assertEqual(summary["totals"]["unknown_duration_runs"], 1)
        self.assertEqual(summary["ledger"][0]["command"], "python3 scripts/check-docs.py")

    def test_timing_keeps_pre_task_receipts_and_deduplicates_their_annotations(self):
        record = {"id": "before-task", "mode": "go", "result": "failed", "started_unix": 100, "ended_unix": 103, "elapsed_seconds": 3}
        base = self.root / ".local/verification"
        base.mkdir(parents=True)
        (base / "before-task.json").write_text(json.dumps(record))
        (self.receipts / "same-receipt.json").write_text(json.dumps(record))
        (base / "manual-runs.jsonl").write_text(json.dumps({**record, "command": "make verify MODE=go"}) + "\n")
        records, unreadable = vc.timing_receipt_records()
        summary = vc.timing_ledger(records, 140, unreadable)
        self.assertIn(base, vc.timing_receipt_directories())
        self.assertEqual(summary["totals"]["runs"], 1)
        self.assertEqual(summary["totals"]["failed"], 1)
        self.assertEqual(summary["totals"]["cumulative_invocation_seconds"], 3)
        self.assertEqual(summary["totals"]["top_level_wall_interval_union_seconds"], 3)

    # --- receipts

    def test_compose_fingerprint_includes_production_go(self):
        self.put("internal/backendhttp/server.go", "compose v1")
        first = vc.mode_fingerprint("compose")
        self.put("internal/backendhttp/server.go", "compose v2")
        self.assertNotEqual(first, vc.mode_fingerprint("compose"))

    def test_receipt_failure_staleness_and_traversal_are_rejected(self):
        for rel in GO_SOURCES:
            self.put(rel)
        state = {"required_receipts": ["go"]}
        record = self.go_receipt("run")
        self.assertTrue(vc.receipt_valid("run.json", state, "go"))
        self.assertFalse(vc.receipt_valid("../run.json", state, "go"))
        self.assertFalse(vc.receipt_valid("run.json", state, "browser"))
        self.put("internal/backendhttp/server.go", "changed")
        self.assertFalse(vc.receipt_valid("run.json", state, "go"))
        record.update(result="failed", exit_code=1, fingerprint=vc.mode_fingerprint("go"), fingerprint_at_end=vc.mode_fingerprint("go"))
        vc.atomic_json(self.receipts / "failed.json", record)
        self.assertFalse(vc.receipt_valid("failed.json", state, "go"))

    def test_narrow_browser_receipt_cannot_satisfy_full_gate(self):
        state = {"required_receipts": ["browser"]}
        artifact = self.receipts / "browser.log"
        artifact.write_text("one project only")
        digest = vc.mode_fingerprint("browser")
        record = {"mode": "browser", "fingerprint": digest, "fingerprint_at_end": digest, "result": "passed", "exit_code": 0, "full_scope": False, "selectors": {"test_file": "browser/one.spec.ts", "project": "mobile-light"}, "artifacts": [artifact.name]}
        vc.atomic_json(self.receipts / "narrow.json", record)
        self.assertFalse(vc.receipt_valid("narrow.json", state, "browser"))
        record["full_scope"] = True
        record["selectors"] = {"test_file": "", "project": ""}
        vc.atomic_json(self.receipts / "full.json", record)
        self.assertTrue(vc.receipt_valid("full.json", state, "browser"))

    def test_shared_receipt_is_valid_in_another_checkout_only_for_matching_source(self):
        worker = self.base / "worker"
        for root in (self.root, worker):
            for rel in GO_SOURCES:
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("same source")
        self.go_receipt("shared")
        state = {"required_receipts": ["go"]}
        old_root = vc.ROOT
        vc.ROOT = worker
        try:
            self.assertTrue(vc.receipt_valid("shared.json", state, "go"))
            (worker / "internal/backendhttp/server.go").write_text("changed worker source")
            self.assertFalse(vc.receipt_valid("shared.json", state, "go"))
        finally:
            vc.ROOT = old_root

    def test_default_task_symlinks_share_receipts_across_checkout_roots(self):
        coordinator = self.base / "coordinator"
        worker = self.base / "worker"
        central_state = self.receipts / "task.json"
        central_state.write_text("{}")
        for root in (coordinator, worker):
            link = root / ".local/verification/task.json"
            link.parent.mkdir(parents=True)
            link.symlink_to(central_state)
            for rel in GO_SOURCES:
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("identical checkout input")
        shared_a = vc.receipt_directory(coordinator)
        shared_b = vc.receipt_directory(worker)
        self.assertEqual(shared_a, shared_b)
        shared_a.mkdir(parents=True, exist_ok=True)
        old_root, old_receipts = vc.ROOT, vc.RECEIPTS
        vc.ROOT, vc.RECEIPTS = coordinator, shared_a
        try:
            self.go_receipt("run", origin=str(coordinator))
            vc.ROOT, vc.RECEIPTS = worker, shared_b
            self.assertTrue(vc.receipt_valid("run.json", {"required_receipts": ["go"]}, "go"))
            (worker / "internal/backendhttp/server.go").write_text("different worker input")
            self.assertFalse(vc.receipt_valid("run.json", {"required_receipts": ["go"]}, "go"))
        finally:
            vc.ROOT, vc.RECEIPTS = old_root, old_receipts

    # --- task budgets

    def test_handoff_requires_receipts_and_counts_failed_attempt(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["go"], "broad_gates": {}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        with patch.object(vc, "task_state_path", return_value=state_file), patch.dict("os.environ", {"VERIFY_REPLAN": "", "VERIFY_REPLAN_REASON": ""}), contextlib.redirect_stderr(io.StringIO()):
            for expected in (1, 2, 3):
                with self.assertRaises(ValueError):
                    vc.guard("handoff")
                self.assertEqual(json.loads(state_file.read_text())["handoffs"], expected)
            with patch.dict("os.environ", {"VERIFY_REPLAN": "1", "VERIFY_REPLAN_REASON": "replan after failed evidence"}):
                with self.assertRaises(ValueError):
                    vc.guard("handoff")
        self.assertEqual(json.loads(state_file.read_text())["handoffs"], 4)

    def test_same_source_later_pass_does_not_erase_an_unexplained_failure(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["go"], "broad_gates": {}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        for rel in GO_SOURCES:
            self.put(rel)
        self.go_receipt("failed", started_unix=1, ended_unix=2, result="failed", exit_code=1)
        self.go_receipt("passed", started_unix=3, ended_unix=4)
        with patch.object(vc, "task_state_path", return_value=state_file), contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(ValueError):
                vc.report()

    def test_failed_and_incomplete_broad_runs_have_receipts_and_block_handoff(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["ordinary"], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": []}}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        with patch.object(vc, "task_state_path", return_value=state_file), contextlib.redirect_stderr(io.StringIO()):
            run_id = vc.broad_start("ordinary")
            with self.assertRaises(ValueError):
                vc.report()
            vc.broad_complete(run_id, 1)
            receipt = json.loads((self.receipts / f"{run_id}.json").read_text())
            self.assertEqual(receipt["result"], "failed")
            self.assertEqual(set(receipt["platform"]), {"os", "arch", "python", "image_id", "git_revision", "git_dirty"})
            with self.assertRaises(ValueError):
                vc.report()

    def test_broad_source_change_turns_success_into_failed_receipt(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["ordinary"], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": []}}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        with patch.object(vc, "task_state_path", return_value=state_file):
            run_id = vc.broad_start("ordinary")
            self.put("internal/backendhttp/server.go", "changed during check")
            self.assertEqual(vc.broad_complete(run_id, 0), 1)
            result = json.loads((self.receipts / f"{run_id}.json").read_text())
            self.assertEqual(result["result"], "failed")

    def test_boundary_requires_same_mode_and_successful_current_receipt(self):
        for rel in GO_SOURCES:
            self.put(rel)
        state_file = self.receipts / "task.json"
        state = {"task": "T", "required_receipts": ["go"], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": ["go"]}}, "broad_attempts": {"ordinary": 1}, "handoffs": 0, "replans": []}
        state_file.write_text(json.dumps(state))
        self.go_receipt("focus")
        with patch.object(vc, "task_state_path", return_value=state_file), patch.dict("os.environ", {"VERIFY_RERUN_REASON": "bounded fixture correction", "VERIFY_RERUN_BOUNDARY": "journey", "VERIFY_FOCUSED_RECEIPT": "focus.json"}):
            with self.assertRaises(ValueError):
                vc.broad_start("ordinary")
        with patch.object(vc, "task_state_path", return_value=state_file), patch.dict("os.environ", {"VERIFY_RERUN_REASON": "bounded fixture correction", "VERIFY_RERUN_BOUNDARY": "go", "VERIFY_FOCUSED_RECEIPT": "focus.json"}):
            self.assertTrue(vc.broad_start("ordinary"))

    def test_task_state_symlinks_share_budget_across_checkouts(self):
        central = self.receipts / "task.json"
        central.write_text(json.dumps({"task": "T", "required_receipts": [], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": []}}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        worktrees = []
        for name in ("coordinator", "worker"):
            checkout = self.root / name
            link = checkout / ".local/verification/task.json"
            link.parent.mkdir(parents=True, exist_ok=True)
            link.symlink_to(central)
            worktrees.append(checkout)
        with patch.dict("os.environ", {"ALMANAC_VERIFICATION_TASK_STATE": ""}):
            old_root = vc.ROOT
            vc.ROOT = worktrees[0]
            self.assertEqual(vc.task_state_path(), central.resolve())
            vc.broad_start("ordinary")
            vc.ROOT = worktrees[1]
            with self.assertRaises(ValueError):
                vc.broad_start("ordinary")
            vc.ROOT = old_root
        self.assertEqual(json.loads(central.read_text())["broad_attempts"]["ordinary"], 1)

    def test_plan_seeds_prior_handoffs_without_reset_and_bounds_gate_modes(self):
        plan_path = self.receipts / "plan.json"
        state_path = self.receipts / "task.json"
        plan_path.write_text(json.dumps({"task": "T", "required_receipts": ["go"], "broad_gates": {"ordinary": {"allowance": 0, "boundaries": ["go"]}}, "prior_handoffs": 2, "prior_handoffs_note": "minimum recorded prior reviews"}))
        with patch.object(vc, "task_state_path", return_value=state_path), patch.dict("os.environ", {"VERIFICATION_PLAN": str(plan_path)}):
            vc.guard("init")
        state = json.loads(state_path.read_text())
        self.assertEqual(state["handoffs"], 2)
        self.assertEqual(state["prior_handoffs"], 2)
        self.assertEqual(state["prior_handoffs_note"], "minimum recorded prior reviews")
        for invalid in (
            {"task": "T", "broad_gates": {"ordinary": {"allowance": 3, "boundaries": []}}},
            {"task": "T", "broad_gates": {"unbounded": {"allowance": 1, "boundaries": []}}},
            {"task": "T", "broad_gates": {"ordinary": {"allowance": 1, "boundaries": ["slo-unit"]}}},
            {"task": "T", "broad_gates": {"ordinary": {"allowance": 1, "boundaries": ["ordinary"]}}},
            {"task": "T", "required_receipts": ["linux"]},
            {"task": "T", "prior_handoffs": -1},
        ):
            with self.subTest(plan=invalid), tempfile.TemporaryDirectory(dir=self.receipts) as td:
                invalid_plan = Path(td) / "plan.json"
                invalid_state = Path(td) / "task.json"
                invalid_plan.write_text(json.dumps(invalid))
                with patch.object(vc, "task_state_path", return_value=invalid_state), patch.dict("os.environ", {"VERIFICATION_PLAN": str(invalid_plan)}):
                    with self.assertRaises(ValueError):
                        vc.guard("init")
                self.assertFalse(invalid_state.exists())

    def test_atomic_state_replacement_never_exposes_partial_json(self):
        path = self.receipts / "state.json"
        vc.write_state(path, {"revision": 0, "payload": "x" * 1000})
        errors = []

        def reader():
            for _ in range(500):
                try:
                    value = json.loads(path.read_text())
                    self.assertEqual(len(value["payload"]), 1000)
                except Exception as exc:
                    errors.append(exc)

        thread = threading.Thread(target=reader)
        thread.start()
        for revision in range(1, 50):
            vc.write_state(path, {"revision": revision, "payload": "y" * 1000})
        thread.join()
        self.assertFalse(errors)


if __name__ == "__main__":
    unittest.main()
