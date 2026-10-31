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

MODULE_PATH = Path(__file__).resolve().parents[1] / "verification-controls.py"
REPO_ROOT = MODULE_PATH.parents[1]
SPEC = importlib.util.spec_from_file_location("verification_controls", MODULE_PATH)
vc = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(vc)


class VerificationControlsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name) / "checkout"
        self.receipts = Path(self.temp.name) / "shared-receipts"
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

    def test_fixed_selectors_name_existing_files_and_exact_db_cases(self):
        self.assertEqual(set(vc.MODES), {"slo-unit", "slo-integration", "slo-journey", "agent-lifecycle", "verification-tools"})
        for relative in vc.WEB_TESTS:
            self.assertTrue((REPO_ROOT / "web" / relative).is_file(), relative)
        self.assertEqual(vc.GO_PACKAGES, ("./internal/slo", "./internal/sloclient", "./internal/slohttp", "./internal/sli", "./internal/datasourceclient", "./internal/storage", "./internal/command"))
        self.assertEqual(vc.DB_TESTS["slo-integration"], ("TestIntegrationOperationalOverviewGroupsAndIndependentBurnWindows", "TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict"))
        self.assertEqual(vc.DB_TESTS["slo-journey"], ("TestIntegrationExecutableFirstObjectiveJourney",))
        self.assertEqual(vc.DB_TESTS["agent-lifecycle"], ("TestIntegrationExecutableAgentManagementRouteAndSessionLifecycle",))
        for relative, test_name in (
            ("internal/storage/slo_operational_integration_test.go", vc.DB_TESTS["slo-integration"][0]),
            ("internal/storage/sli_ingestion_integration_test.go", vc.DB_TESTS["slo-integration"][1]),
            ("internal/command/objectives_journey_integration_test.go", vc.DB_TESTS["slo-journey"][0]),
            ("internal/command/agent_management_runtime_integration_test.go", vc.DB_TESTS["agent-lifecycle"][0]),
        ):
            self.assertIn(test_name, (REPO_ROOT / relative).read_text())
        self.assertEqual(vc.db_test_pattern("slo-integration"), "^(TestIntegrationOperationalOverviewGroupsAndIndependentBurnWindows|TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict)$")
        self.assertNotIn("slo-chart-boundary.test.tsx", vc.WEB_TESTS)

    def test_make_does_not_expand_user_reason_or_plan_as_make_code(self):
        makefile = (REPO_ROOT / "Makefile").read_text()
        for name in ("VERIFY_RERUN_REASON", "VERIFY_RERUN_BOUNDARY", "VERIFY_FOCUSED_RECEIPT", "VERIFICATION_PLAN"):
            self.assertNotIn(f"$({name})", makefile)

    def test_vitest_refuses_missing_files_zero_cases_and_skips(self):
        with self.assertRaises(ValueError):
            vc.count_vitest({"testResults": []})
        results = [{"name": f"/repo/web/{file}", "status": "passed", "assertionResults": [{"status": "passed"}]} for file in vc.WEB_TESTS]
        self.assertEqual(vc.count_vitest({"testResults": results}), len(vc.WEB_TESTS))
        results[0]["assertionResults"] = []
        with self.assertRaises(ValueError):
            vc.count_vitest({"testResults": results})
        results[0]["assertionResults"] = [{"status": "pending"}]
        with self.assertRaises(ValueError):
            vc.count_vitest({"testResults": results})

    def test_go_receipt_requires_expected_test_to_run_and_pass_without_skip(self):
        events = self.receipts / "go.jsonl"
        events.write_text('\n'.join(json.dumps({"Action": action, "Test": "TestLifecycle"}) for action in ("run", "pass")))
        self.assertEqual(vc.counted_go_events(events, {"TestLifecycle"}), 1)
        events.write_text(json.dumps({"Action": "run", "Test": "TestLifecycle"}) + "\n" + json.dumps({"Action": "skip", "Test": "TestLifecycle"}))
        with self.assertRaises(ValueError):
            vc.counted_go_events(events, {"TestLifecycle"})

    def test_integration_receipt_requires_both_fixed_slo_and_sli_cases(self):
        events = self.receipts / "two-go.jsonl"
        names = set(vc.DB_TESTS["slo-integration"])
        events.write_text("\n".join(json.dumps({"Action": action, "Test": name}) for name in sorted(names) for action in ("run", "pass")))
        self.assertEqual(vc.counted_go_events(events, names), 2)
        events.write_text(json.dumps({"Action": "run", "Test": sorted(names)[0]}))
        with self.assertRaises(ValueError):
            vc.counted_go_events(events, names)

    def test_timing_ledger_deduplicates_runs_marks_unknown_and_unions_overlap(self):
        records = [
            {"id": "a", "mode": "slo-unit", "result": "passed", "started_unix": 100, "ended_unix": 110, "elapsed_seconds": 10, "selected_count": 12, "executed_count": 12},
            {"id": "a", "mode": "slo-unit", "result": "running", "started_unix": 100},
            {"id": "b", "mode": "slo-unit", "result": "failed", "started_unix": 105, "ended_unix": 125, "elapsed_seconds": 20},
            {"id": "c", "mode": "slo-integration", "result": "running", "started_unix": 130},
            {"id": "d", "mode": "slo-integration", "result": "failed", "started_unix": 80, "ended_unix": 90},
            {"id": "e", "mode": "slo-unit", "result": "passed", "started_unix": 100, "ended_unix": 200, "elapsed_seconds": 100, "parent_id": "b"},
        ]
        summary = vc.timing_ledger(records, 140)
        totals = summary["totals"]
        self.assertEqual(totals["runs"], 5)
        self.assertEqual((totals["passed"], totals["failed"], totals["ongoing"]), (2, 2, 1))
        self.assertEqual(totals["unknown_duration_runs"], 1)
        self.assertEqual(totals["cumulative_invocation_seconds"], 30)
        self.assertEqual(totals["top_level_wall_interval_union_seconds"], 45)
        self.assertEqual(totals["child_elapsed_seconds_excluded"], 100)
        self.assertEqual(summary["modes"]["slo-integration"]["ongoing"], 1)
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
        (base / "manual-runs.jsonl").write_text(json.dumps({**record, "command": "make verify VERIFY_MODE=go"}) + "\n")
        records, unreadable = vc.timing_receipt_records()
        summary = vc.timing_ledger(records, 140, unreadable)
        self.assertIn(base, vc.timing_receipt_directories())
        self.assertEqual(summary["totals"]["runs"], 1)
        self.assertEqual(summary["totals"]["failed"], 1)
        self.assertEqual(summary["totals"]["cumulative_invocation_seconds"], 3)
        self.assertEqual(summary["totals"]["top_level_wall_interval_union_seconds"], 3)

    def test_asset_preflight_failure_is_returned_and_saved_to_receipt_artifact(self):
        artifact = self.receipts / "preflight.log"
        result = SimpleNamespace(returncode=7, stdout=b"asset build failed\n")
        with patch.object(vc.subprocess, "run", return_value=result) as run, contextlib.redirect_stdout(io.StringIO()):
            status = vc.ensure_web_assets(artifact, {"PATH": "/bin"})
        self.assertEqual(status, 7)
        self.assertIn("asset build failed", artifact.read_text())
        self.assertEqual(run.call_args.args[0], ["make", "_ensure_web_assets"])

    def test_fingerprint_boundaries_match_mode_dependencies(self):
        self.put("go.mod", "module x")
        self.put("internal/backendhttp/server.go", "v1")
        self.put("internal/command/objectives_journey_integration_test.go", "journey")
        self.put("internal/command/agent_management_runtime_integration_test.go", "helper")
        self.put("internal/storage/storage_integration_test.go", "db fixture v1")
        self.put("web/src/slos/slos.css", "css v1")
        db = vc.test_inputs_fingerprint("slo-integration")
        journey = vc.test_inputs_fingerprint("slo-journey")
        unit = vc.test_inputs_fingerprint("slo-unit")
        self.put("web/src/slos/slos.css", "css v2")
        self.assertEqual(db, vc.test_inputs_fingerprint("slo-integration"))
        self.assertNotEqual(journey, vc.test_inputs_fingerprint("slo-journey"))
        self.assertNotEqual(unit, vc.test_inputs_fingerprint("slo-unit"))
        journey = vc.test_inputs_fingerprint("slo-journey")
        db = vc.test_inputs_fingerprint("slo-integration")
        self.put("internal/storage/storage_integration_test.go", "db fixture v2")
        self.assertNotEqual(db, vc.test_inputs_fingerprint("slo-integration"))
        self.assertEqual(journey, vc.test_inputs_fingerprint("slo-journey"))
        self.put("internal/backendhttp/server.go", "v2")
        self.assertNotEqual(journey, vc.test_inputs_fingerprint("slo-journey"))
        self.assertNotEqual(db, vc.test_inputs_fingerprint("slo-integration"))

    def test_new_sli_and_shared_ui_dependencies_stale_unit_receipt(self):
        self.put("internal/datasourceclient/client.go", "v1")
        self.put("internal/datasourceclient/client_test.go", "test v1")
        self.put("internal/slihttp/handler.go", "v1")
        self.put("internal/sli/definition_test.go", "test v1")
        self.put("web/src/controls/Control.tsx", "v1")
        self.put("web/tests/controls.test.tsx", "test v1")
        self.put("web/tests/slis-client.test.tsx", "test v1")
        self.put("web/tests/slis-page.test.tsx", "test v1")
        self.put("internal/storage/storage_integration_test.go", "fixture v1")
        initial = vc.test_inputs_fingerprint("slo-unit")

        self.put("internal/datasourceclient/client_test.go", "test v2")
        self.assertNotEqual(initial, vc.test_inputs_fingerprint("slo-unit"))
        initial = vc.test_inputs_fingerprint("slo-unit")
        self.put("web/tests/slis-page.test.tsx", "test v2")
        self.assertNotEqual(initial, vc.test_inputs_fingerprint("slo-unit"))
        initial = vc.test_inputs_fingerprint("slo-unit")
        self.put("internal/storage/storage_integration_test.go", "fixture v2")
        self.assertEqual(initial, vc.test_inputs_fingerprint("slo-unit"))

    def test_receipt_failure_staleness_and_traversal_are_rejected(self):
        self.put("go.mod", "module x")
        self.put("cmd/almanac/main.go")
        self.put("internal/backendhttp/server.go")
        self.put("internal/command/agent_management_runtime_integration_test.go")
        state = {"required_receipts": ["agent-lifecycle"]}
        artifact = self.receipts / "run.log"
        artifact.write_text("passed")
        mode_hash = vc.test_inputs_fingerprint("agent-lifecycle")
        record = {"mode": "agent-lifecycle", "fingerprint": mode_hash, "fingerprint_at_end": mode_hash, "result": "passed", "exit_code": 0, "artifacts": [artifact.name]}
        vc.atomic_json(self.receipts / "run.json", record)
        self.assertTrue(vc.receipt_valid("run.json", state, "agent-lifecycle"))
        self.assertFalse(vc.receipt_valid("../run.json", state, "agent-lifecycle"))
        self.put("internal/backendhttp/server.go", "changed")
        self.assertFalse(vc.receipt_valid("run.json", state, "agent-lifecycle"))
        record["result"] = "failed"
        record["exit_code"] = 1
        record["fingerprint"] = vc.test_inputs_fingerprint("agent-lifecycle")
        record["fingerprint_at_end"] = record["fingerprint"]
        vc.atomic_json(self.receipts / "run.json", record)
        self.assertFalse(vc.receipt_valid("run.json", state, "agent-lifecycle"))

    def test_focused_receipts_fingerprint_control_definitions(self):
        self.put("Makefile", "verify target v1")
        self.put("scripts/verification-controls.py", "selector v1")
        first = vc.test_inputs_fingerprint("agent-lifecycle")
        self.put("Makefile", "verify target v2")
        self.assertNotEqual(first, vc.test_inputs_fingerprint("agent-lifecycle"))

    def test_compose_fingerprint_includes_production_go(self):
        self.put("internal/backendhttp/server.go", "compose v1")
        first = vc.broad_fingerprint("compose")
        self.put("internal/backendhttp/server.go", "compose v2")
        self.assertNotEqual(first, vc.broad_fingerprint("compose"))

    def test_narrow_browser_receipt_cannot_satisfy_full_gate(self):
        state = {"required_receipts": ["browser"]}
        artifact = self.receipts / "browser.log"
        artifact.write_text("one project only")
        digest = vc.broad_fingerprint("browser")
        record = {"mode": "browser", "fingerprint": digest, "fingerprint_at_end": digest, "result": "passed", "exit_code": 0, "full_scope": False, "selectors": {"test_file": "browser/one.spec.ts", "project": "mobile-light"}, "artifacts": [artifact.name]}
        vc.atomic_json(self.receipts / "narrow.json", record)
        self.assertFalse(vc.receipt_valid("narrow.json", state, "browser"))
        record["full_scope"] = True
        record["selectors"] = {"test_file": "", "project": ""}
        vc.atomic_json(self.receipts / "full.json", record)
        self.assertTrue(vc.receipt_valid("full.json", state, "browser"))

    def test_shared_receipt_is_valid_in_another_checkout_only_for_matching_source(self):
        worker = Path(self.temp.name) / "worker"
        for root in (self.root, worker):
            for rel in ("go.mod", "cmd/almanac/main.go", "internal/backendhttp/server.go", "internal/command/agent_management_runtime_integration_test.go"):
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("same source")
        digest = vc.test_inputs_fingerprint("agent-lifecycle")
        artifact = self.receipts / "shared.log"
        artifact.write_text("actual run output")
        vc.atomic_json(self.receipts / "shared.json", {"mode": "agent-lifecycle", "origin": str(self.root), "fingerprint": digest, "fingerprint_at_end": digest, "result": "passed", "exit_code": 0, "artifacts": [artifact.name]})
        state = {"required_receipts": ["agent-lifecycle"]}
        old_root = vc.ROOT
        vc.ROOT = worker
        try:
            self.assertTrue(vc.receipt_valid("shared.json", state, "agent-lifecycle"))
            (worker / "internal/backendhttp/server.go").write_text("changed worker source")
            self.assertFalse(vc.receipt_valid("shared.json", state, "agent-lifecycle"))
        finally:
            vc.ROOT = old_root

    def test_default_task_symlinks_share_receipts_across_checkout_roots(self):
        coordinator = Path(self.temp.name) / "coordinator"
        worker = Path(self.temp.name) / "worker"
        central_state = self.receipts / "task.json"
        central_state.write_text("{}")
        for root in (coordinator, worker):
            link = root / ".local/verification/task.json"
            link.parent.mkdir(parents=True)
            link.symlink_to(central_state)
            for rel in ("go.mod", "cmd/almanac/main.go", "internal/backendhttp/server.go", "internal/command/agent_management_runtime_integration_test.go"):
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("identical checkout input")
        shared_a = vc.receipt_directory(coordinator)
        shared_b = vc.receipt_directory(worker)
        self.assertEqual(shared_a, shared_b)
        shared_a.mkdir(parents=True, exist_ok=True)
        artifact = shared_a / "run.log"
        artifact.write_text("actual result")
        old_root, old_receipts = vc.ROOT, vc.RECEIPTS
        vc.ROOT, vc.RECEIPTS = coordinator, shared_a
        try:
            digest = vc.test_inputs_fingerprint("agent-lifecycle")
            vc.atomic_json(shared_a / "run.json", {"mode": "agent-lifecycle", "origin": str(coordinator), "fingerprint": digest, "fingerprint_at_end": digest, "result": "passed", "exit_code": 0, "artifacts": [artifact.name]})
            vc.ROOT, vc.RECEIPTS = worker, shared_b
            self.assertTrue(vc.receipt_valid("run.json", {"required_receipts": ["agent-lifecycle"]}, "agent-lifecycle"))
            (worker / "internal/backendhttp/server.go").write_text("different worker input")
            self.assertFalse(vc.receipt_valid("run.json", {"required_receipts": ["agent-lifecycle"]}, "agent-lifecycle"))
        finally:
            vc.ROOT, vc.RECEIPTS = old_root, old_receipts

    def test_handoff_requires_receipts_and_counts_failed_attempt(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["agent-lifecycle"], "broad_gates": {}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        with patch.object(vc, "task_state_path", return_value=state_file), patch.dict("os.environ", {"VERIFY_REPLAN": "", "VERIFY_REPLAN_REASON": ""}):
            for expected in (1, 2, 3):
                with self.assertRaises(ValueError):
                    vc.guard("handoff", SimpleNamespace())
                self.assertEqual(json.loads(state_file.read_text())["handoffs"], expected)
            with patch.dict("os.environ", {"VERIFY_REPLAN": "1", "VERIFY_REPLAN_REASON": "replan after failed evidence"}):
                with self.assertRaises(ValueError):
                    vc.guard("handoff", SimpleNamespace())
        self.assertEqual(json.loads(state_file.read_text())["handoffs"], 4)

    def test_same_source_later_pass_does_not_erase_an_unexplained_failure(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["agent-lifecycle"], "broad_gates": {}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        for rel in ("go.mod", "cmd/almanac/main.go", "internal/backendhttp/server.go", "internal/command/agent_management_runtime_integration_test.go"):
            self.put(rel)
        artifact = self.receipts / "evidence.log"
        artifact.write_text("log")
        digest = vc.test_inputs_fingerprint("agent-lifecycle")
        common = {"mode": "agent-lifecycle", "fingerprint": digest, "fingerprint_at_end": digest, "artifacts": [artifact.name]}
        vc.atomic_json(self.receipts / "failed.json", {**common, "started_unix": 1, "ended_unix": 2, "result": "failed", "exit_code": 1})
        vc.atomic_json(self.receipts / "passed.json", {**common, "started_unix": 3, "ended_unix": 4, "result": "passed", "exit_code": 0})
        with patch.object(vc, "task_state_path", return_value=state_file):
            with self.assertRaises(ValueError):
                vc.report()

    def test_failed_and_incomplete_broad_runs_have_receipts_and_block_handoff(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["ordinary"], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": []}}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        with patch.object(vc, "task_state_path", return_value=state_file):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                vc.guard("broad-start", SimpleNamespace(gate="ordinary"))
            run_id = output.getvalue().strip()
            with self.assertRaises(ValueError):
                vc.report()
            vc.guard("broad-complete", SimpleNamespace(run_id=run_id, exit_code=1))
            self.assertTrue((self.receipts / f"{run_id}.json").is_file())
            with self.assertRaises(ValueError):
                vc.report()

    def test_broad_source_change_turns_success_into_failed_receipt(self):
        state_file = self.receipts / "task.json"
        state_file.write_text(json.dumps({"task": "T", "required_receipts": ["ordinary"], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": []}}, "broad_attempts": {}, "handoffs": 0, "replans": []}))
        with patch.object(vc, "task_state_path", return_value=state_file):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                vc.guard("broad-start", SimpleNamespace(gate="ordinary"))
            run_id = output.getvalue().strip()
            self.put("internal/backendhttp/server.go", "changed during check")
            self.assertEqual(vc.guard("broad-complete", SimpleNamespace(run_id=run_id, exit_code=0)), 1)
            result = json.loads((self.receipts / f"{run_id}.json").read_text())
            self.assertEqual(result["result"], "failed")

    def test_boundary_requires_same_mode_and_successful_current_receipt(self):
        self.put("go.mod", "module x")
        self.put("cmd/almanac/main.go")
        self.put("internal/backendhttp/server.go")
        self.put("internal/command/agent_management_runtime_integration_test.go")
        state_file = self.receipts / "task.json"
        state = {"task": "T", "required_receipts": ["agent-lifecycle"], "broad_gates": {"ordinary": {"allowance": 1, "boundaries": ["agent-lifecycle"]}}, "broad_attempts": {"ordinary": 1}, "handoffs": 0, "replans": []}
        state_file.write_text(json.dumps(state))
        log = self.receipts / "focus.log"
        log.write_text("done")
        digest = vc.test_inputs_fingerprint("agent-lifecycle")
        record = {"mode": "agent-lifecycle", "fingerprint": digest, "fingerprint_at_end": digest, "result": "passed", "exit_code": 0, "artifacts": [log.name]}
        vc.atomic_json(self.receipts / "focus.json", record)
        with patch.object(vc, "task_state_path", return_value=state_file), patch.dict("os.environ", {"VERIFY_RERUN_REASON": "bounded fixture correction", "VERIFY_RERUN_BOUNDARY": "slo-journey", "VERIFY_FOCUSED_RECEIPT": "focus.json"}):
            with self.assertRaises(ValueError):
                vc.guard("broad-start", SimpleNamespace(gate="ordinary"))
        with patch.object(vc, "task_state_path", return_value=state_file), patch.dict("os.environ", {"VERIFY_RERUN_REASON": "bounded fixture correction", "VERIFY_RERUN_BOUNDARY": "agent-lifecycle", "VERIFY_FOCUSED_RECEIPT": "focus.json"}):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(vc.guard("broad-start", SimpleNamespace(gate="ordinary")), 0)

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
        with patch.dict("os.environ", {}, clear=False):
            with patch.dict("os.environ", {"ALMANAC_VERIFICATION_TASK_STATE": ""}):
                old_root = vc.ROOT
                vc.ROOT = worktrees[0]
                self.assertEqual(vc.task_state_path(), central.resolve())
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    vc.guard("broad-start", SimpleNamespace(gate="ordinary"))
                vc.ROOT = worktrees[1]
                with self.assertRaises(ValueError):
                    vc.guard("broad-start", SimpleNamespace(gate="ordinary"))
                vc.ROOT = old_root
        self.assertEqual(json.loads(central.read_text())["broad_attempts"]["ordinary"], 1)

    def test_plan_seeds_prior_handoffs_without_reset_and_bounds_gate_modes(self):
        plan_path = self.receipts / "plan.json"
        state_path = self.receipts / "task.json"
        plan_path.write_text(json.dumps({"task": "T", "required_receipts": ["agent-lifecycle"], "broad_gates": {"ordinary": {"allowance": 0, "boundaries": ["agent-lifecycle"]}}, "prior_handoffs": 2, "prior_handoffs_note": "minimum recorded prior reviews"}))
        with patch.object(vc, "task_state_path", return_value=state_path), patch.dict("os.environ", {"VERIFICATION_PLAN": str(plan_path)}):
            vc.guard("init", SimpleNamespace())
        state = json.loads(state_path.read_text())
        self.assertEqual(state["handoffs"], 2)
        self.assertEqual(state["prior_handoffs"], 2)
        self.assertEqual(state["prior_handoffs_note"], "minimum recorded prior reviews")
        for invalid in (
            {"task": "T", "broad_gates": {"ordinary": {"allowance": 3, "boundaries": []}}},
            {"task": "T", "broad_gates": {"unbounded": {"allowance": 1, "boundaries": []}}},
            {"task": "T", "broad_gates": {"ordinary": {"allowance": 1, "boundaries": ["not-a-mode"]}}},
            {"task": "T", "prior_handoffs": -1},
        ):
            with self.subTest(plan=invalid), tempfile.TemporaryDirectory(dir=self.receipts) as td:
                invalid_plan = Path(td) / "plan.json"
                invalid_state = Path(td) / "task.json"
                invalid_plan.write_text(json.dumps(invalid))
                with patch.object(vc, "task_state_path", return_value=invalid_state), patch.dict("os.environ", {"VERIFICATION_PLAN": str(invalid_plan)}):
                    with self.assertRaises(ValueError):
                        vc.guard("init", SimpleNamespace())
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
