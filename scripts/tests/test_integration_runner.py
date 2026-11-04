import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("integration_runner", Path(__file__).resolve().parents[1] / "integration-runner.py")
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class IntegrationRunnerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def put(self, name, value="source"):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(value)
        return path

    def test_go_dependency_change_invalidates_consumers_but_not_unrelated_packages(self):
        graph = {}
        for package, imports in (("a", ["shared"]), ("b", ["shared"]), ("other", []), ("shared", [])):
            self.put(f"internal/{package}/main.go")
            graph[runner.PREFIX + "internal/" + package] = {
                "Dir": str(self.root / "internal" / package), "GoFiles": ["main.go"],
                "Imports": [runner.PREFIX + "internal/" + item for item in imports]}
        def inputs(package):
            return runner.fingerprints(self.root, runner.go_inputs(self.root, graph, package), "fixture")
        before = {p: inputs(p) for p in ("a", "b", "other")}
        self.put("internal/shared/main.go", "changed")
        self.assertNotEqual(before["a"], inputs("a"))
        self.assertNotEqual(before["b"], inputs("b"))
        self.assertEqual(before["other"], inputs("other"))

    def test_browser_imports_include_transitive_and_missing_inputs(self):
        self.put("web/scripts/a.mjs", "import './b.mjs';")
        self.put("web/scripts/b.mjs", "export { x } from './missing.mjs';")
        paths = runner.script_inputs(self.root, ["web/scripts/a.mjs"])
        self.assertEqual(paths, {"web/scripts/a.mjs", "web/scripts/b.mjs", "web/scripts/missing.mjs"})
        before = runner.fingerprints(self.root, paths, "fixture")
        self.put("web/scripts/missing.mjs", "export const x = 1;")
        self.assertNotEqual(before, runner.fingerprints(self.root, paths, "fixture"))

    def test_only_integration_dispatch_is_excluded_from_make_inputs(self):
        before = self.put("Makefile", "build:\n\tcompile\n\n_verify_integration:\n\told dispatcher\n\nnext:\n\tcheck\n")
        original = runner.fingerprints(self.root, {"Makefile"}, "fixture")
        before.write_text(before.read_text().replace("old dispatcher", "new dispatcher"))
        self.assertEqual(original, runner.fingerprints(self.root, {"Makefile"}, "fixture"))
        before.write_text(before.read_text().replace("compile", "different compiler flags"))
        self.assertNotEqual(original, runner.fingerprints(self.root, {"Makefile"}, "fixture"))

    def test_failed_running_skipped_wrong_case_or_changed_artifact_cannot_be_reused(self):
        test = "TestIntegrationExample"
        artifact = self.put("evidence.jsonl", json.dumps({"Package": runner.PREFIX + "internal/storage", "Test": test, "Action": "pass"}) + "\n")
        inputs = {"a.go": "hash", "@environment": "fixture"}
        record = {"result": "passed", "inputs": inputs, "artifact": "evidence.jsonl", "artifact_sha256": runner.digest(artifact.read_bytes())}
        self.assertIsNone(runner.reuse_reason(record, inputs, "storage", test, self.root))
        for state in ("failed", "running", "skipped"):
            self.assertIsNotNone(runner.reuse_reason({**record, "result": state}, inputs, "storage", test, self.root))
        self.assertIsNotNone(runner.reuse_reason(record, inputs, "storage", "TestIntegrationOther", self.root))
        self.assertIsNotNone(runner.reuse_reason(record, {**inputs, "a.go": "changed"}, "storage", test, self.root))
        self.assertIsNotNone(runner.reuse_reason(record, {**inputs, "@environment": "new fixture"}, "storage", test, self.root))
        artifact.write_text("changed evidence")
        self.assertIsNotNone(runner.reuse_reason(record, inputs, "storage", test, self.root))

    def test_nested_failure_and_duplicate_passes_do_not_count_as_pass(self):
        base = {"Package": runner.PREFIX + "internal/storage", "Test": "TestIntegrationExample", "Action": "pass"}
        self.assertTrue(runner.passed([base], "storage", base["Test"]))
        self.assertFalse(runner.passed([base, base], "storage", base["Test"]))
        self.assertFalse(runner.passed([base, {**base, "Test": base["Test"] + "/nested", "Action": "fail"}], "storage", base["Test"]))

    def test_legacy_archive_fingerprint_matches_original_path_component_order(self):
        entries = {"internal/migrations.go": b"source", "internal/migrations/001.sql": b"schema",
                   "scripts/__pycache__/ignored.pyc": b"ignored", "Makefile": b"build"}
        for name, content in entries.items():
            self.put(name, content.decode())
        paths = ("internal", "scripts", "Makefile")
        self.assertEqual(runner.archived_fingerprint(entries, paths),
                         runner.load_controls().fingerprint_for_root(self.root, paths))


if __name__ == "__main__":
    unittest.main()
