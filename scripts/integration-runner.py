#!/usr/bin/env python3
"""Run fixed integration cases; reuse only passes with matching dependency inputs."""

import fcntl
from datetime import datetime
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
PACKAGES = ("storage", "authentication", "agentmanagement", "agenthttp", "authhttp", "agentcmd", "command")
PREFIX = "github.com/dsolsona/almanac/"
FIXTURE = "postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable"
GO = "go"  # from the dev image (ADR-0029)
FLAGS = ("-json", "-race", "-tags=integration", "-p=1", "-count=1", "-timeout=2m")
EXCLUDED = {".git", ".local", "node_modules", "__pycache__", "dist"}
SCRIPT_ROOTS = {
    **{name: () for name in (
        "TestIntegrationExecutableBackendTLSOwnerLifecycleAndRotation",
        "TestIntegrationExecutableCombinedBackendUITLSOwnerLifecycleAndRotation",
        "TestIntegrationExecutableMissingInitialCredentialsStaysUnavailable",
        "TestIntegrationExecutableWeakAndUnsafeCredentialsFailClosed",
        "TestIntegrationExecutableSetupCLICompletesPersistsAndRefusesReplay",
        "TestIntegrationExecutableCombinedSetupCLICompletesPersistsAndRefusesReplay")},
    "TestIntegrationExecutableAgentManagementRouteAndSessionLifecycle": ("check-agent-status-browser.mjs", "check-datasource-browser.mjs"),
    "TestIntegrationExecutableBrowserAuthentication": ("check-auth-browser.mjs",),
    "TestIntegrationExecutableBrowserAgentRegistration": ("check-auth-browser.mjs",),
    "TestIntegrationExecutableBrowserAgentLifecycle": ("check-auth-browser.mjs",),
    "TestIntegrationSafeBrowserFailureMarkerPreservesBoundedCancellationEvidence": ("check-datasource-browser.mjs",),
    "TestIntegrationExecutableBrowserSLICollectionJourney": ("check-sli-browser.mjs",),
    "TestIntegrationExecutableFirstObjectiveJourney": ("check-sli-browser.mjs", "check-slo-browser.mjs"),
}


def load_controls():
    spec = importlib.util.spec_from_file_location("verification_controls", ROOT / "scripts/verification-controls.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def digest(data):
    return hashlib.sha256(data).hexdigest()


def files_under(root, relative):
    path = root / relative
    return {p.relative_to(root).as_posix() for p in (path.rglob("*") if path.is_dir() else [path])
            if p.is_file() and not EXCLUDED.intersection(p.relative_to(root).parts)}


def script_inputs(root, roots):
    pending, found = list(roots), set()
    while pending:
        name = pending.pop()
        if name in found:
            continue
        found.add(name)
        path = root / name
        if not path.is_file():
            continue
        if path.suffix not in {".mjs", ".js", ".ts", ".tsx"}:
            continue
        imports = re.findall(r'''(?:from\s*|import\s*(?:\(\s*)?)['"](\.[^'"]+)['"]''', path.read_text())
        for imported in imports:
            dependency = (path.parent / imported).resolve()
            if not dependency.is_relative_to(root):
                raise ValueError("browser fixture import leaves checkout")
            pending.append(dependency.relative_to(root).as_posix())
    return found


def go_graph():
    output = subprocess.check_output([str(GO), "list", "-deps", "-test", "-json", "-tags=integration",
                                      *[f"./internal/{p}" for p in PACKAGES]], cwd=ROOT, text=True)
    decoder, graph = json.JSONDecoder(), {}
    while output.strip():
        item, end = decoder.raw_decode(output.lstrip())
        output = output.lstrip()[end:]
        directory = Path(item.get("Dir", "/"))
        replacement = item.get("Module", {}).get("Replace", {})
        if replacement.get("Dir") and not replacement.get("Version") and not directory.is_relative_to(ROOT):
            graph["@external_local_replacement"] = True
        if directory.is_relative_to(ROOT) and not item.get("ForTest") and not item["ImportPath"].endswith(".test"):
            graph[item["ImportPath"]] = item
    return graph


def go_inputs(root, graph, package):
    target = PREFIX + "internal/" + package
    pending = [target, *graph[target].get("TestImports", []), *graph[target].get("XTestImports", [])]
    found, paths = set(), set()
    while pending:
        name = pending.pop().split(" [", 1)[0]
        if name in found or name not in graph:
            continue
        found.add(name)
        item = graph[name]
        directory = Path(item["Dir"])
        if EXCLUDED.intersection(directory.relative_to(root).parts):
            continue
        pending.extend(item.get("Imports", []))
        fields = ["GoFiles", "CgoFiles", "CFiles", "HFiles", "SFiles", "EmbedFiles"]
        if name == target:
            fields += ["TestGoFiles", "XTestGoFiles", "TestEmbedFiles", "XTestEmbedFiles"]
        for field in fields:
            for filename in item.get(field, []):
                path = (directory / filename).relative_to(root)
                if not EXCLUDED.intersection(path.parts):
                    paths.add(path.as_posix())
    return paths


def case_inputs(root, graph, package, test):
    paths = go_inputs(root, graph, package)
    for name in ("go.mod", "go.sum", "api", "sqlc.yaml", "Makefile", "scripts/setup-database.sh"):
        paths.update(files_under(root, name))
    if package == "command":
        for name in ("cmd", "web/assets.go", "web/src", "web/public", "web/package.json", "web/package-lock.json", "web/tsconfig.json", "web/vite.config.ts", "deploy/local", "deploy/app", "deploy/agent", "docs/design", "compose.yaml"):
            paths.update(files_under(root, name))
        roots = SCRIPT_ROOTS.get(test)
        if roots is None:
            paths.update(files_under(root, "web/scripts"))
        else:
            paths.update(script_inputs(root, ["web/scripts/" + name for name in roots]))
    return paths


def fingerprints(root, paths, environment, archived=None):
    inputs = {"@environment": environment}
    for name in sorted(paths):
        data = archived.get(name) if archived is not None else ((root / name).read_bytes() if (root / name).is_file() else None)
        if data is not None and name == "Makefile":
            # Orchestration changes do not alter the fixed Go execution contract.
            data = re.sub(rb"(?m)^_verify_integration[^\n]*\n(?:\t[^\n]*\n)*", b"", data)
        inputs[name] = digest(data) if data is not None else "missing"
    return inputs


def read_events(path):
    try:
        return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    except (OSError, ValueError):
        return []


def passed(events, package, test):
    matching = [e for e in events if e.get("Package") == PREFIX + "internal/" + package and
                (e.get("Test") == test or str(e.get("Test", "")).startswith(test + "/"))]
    return ([e["Action"] for e in matching if e.get("Test") == test and e.get("Action") in {"pass", "fail", "skip"}] == ["pass"]
            and not any(e.get("Action") == "fail" for e in matching))


def reuse_reason(record, inputs, package, test, root=ROOT, evidence_cache=None):
    if not record or record.get("result") != "passed":
        return "failed, interrupted or missing result"
    if record.get("inputs") != inputs:
        previous = record.get("inputs", {})
        changed = sorted(k for k in set(previous) | set(inputs) if previous.get(k) != inputs.get(k))
        return "changed: " + ", ".join(changed[:4])
    artifact = root / record["artifact"]
    if not artifact.is_file():
        return "missing or changed evidence"
    evidence_cache = {} if evidence_cache is None else evidence_cache
    if artifact not in evidence_cache:
        evidence_cache[artifact] = (digest(artifact.read_bytes()), read_events(artifact))
    actual_hash, events = evidence_cache[artifact]
    if actual_hash != record.get("artifact_sha256"):
        return "missing or changed evidence"
    if not passed(events, package, test):
        return "evidence does not contain a passing selected case"
    return None


def report_evidence(record):
    return {**{key: value for key, value in record.items() if key != "inputs"},
            "inputs_sha256": digest(json.dumps(record["inputs"], sort_keys=True).encode())}


def archive_head():
    raw = subprocess.check_output(["git", "archive", "HEAD"], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
        return {item.name: archive.extractfile(item).read() for item in archive if item.isfile()}


def archived_fingerprint(entries, paths):
    h = hashlib.sha256()
    for relative in paths:
        for name in sorted((n for n in entries if n == relative or n.startswith(relative + "/")), key=Path):
            if {".git", "node_modules", "__pycache__", "dist"}.intersection(Path(name).parts) or name.endswith(".pyc"):
                continue
            h.update(name.encode() + b"\0" + entries[name] + b"\0")
    return h.hexdigest()


def import_legacy(controls, graph, cases, environment, cache, destination, fixture_id):
    """Migrate actual backend passes from the matching pre-cache full run."""
    source = ROOT / ".local/integration-go-test.jsonl"
    events = read_events(source)
    candidates = []
    for path in controls.RECEIPTS.glob("*.json"):
        try:
            record = json.loads(path.read_text())
            if record.get("mode") == "integration" and record.get("origin") == str(ROOT) and record.get("ended_unix"):
                candidates.append(record)
        except (OSError, ValueError):
            continue
    if not candidates or not events or not fixture_id:
        print("Legacy reuse unavailable: no completed source run/fixture", flush=True)
        return
    entries = archive_head()
    expected = archived_fingerprint(entries, ("cmd", "internal", "go.mod", "go.sum", "api", "sqlc.yaml", "scripts", "Makefile"))
    candidates = [r for r in candidates if r.get("fingerprint") == expected and r.get("fingerprint_at_end") == expected and r.get("full_scope")]
    if not candidates:
        print("Legacy reuse unavailable: HEAD archive does not match a completed full source receipt", flush=True)
        return
    record = max(candidates, key=lambda item: item["ended_unix"])
    old_make = entries.get("Makefile", b"")
    if b"go test -json -race -tags=integration -p=1 -run" not in old_make or b"-count=1 -timeout=2m" not in old_make:
        print("Legacy reuse unavailable: execution contract differs", flush=True)
        return
    log = controls.RECEIPTS / (record["id"] + ".log")
    if not log.is_file() or fixture_id[:12] not in log.read_text():
        print("Legacy reuse unavailable: fixture identity is not recorded", flush=True)
        return
    if datetime.fromisoformat(fixture_id.split()[1]).timestamp() > record["started_unix"]:
        print("Legacy reuse unavailable: fixture restarted after source run", flush=True)
        return
    # The legacy integration fingerprint omitted UI assets, so import backend cases only.
    artifact = destination / ("legacy-" + record["id"] + ".jsonl")
    shutil.copyfile(source, artifact)
    artifact_hash = digest(artifact.read_bytes())
    for package, test in cases:
        if package + "/" + test in cache or package == "command" or not passed(events, package, test):
            continue
        times = [e.get("Time", "") for e in events if e.get("Package") == PREFIX + "internal/" + package and e.get("Test") == test and e.get("Action") == "pass"]
        if not times or not record["started_unix"] <= datetime.fromisoformat(times[-1]).timestamp() <= record["ended_unix"]:
            continue
        paths = case_inputs(ROOT, graph, package, test)
        cache[package + "/" + test] = {"result": "passed", "inputs": fingerprints(ROOT, paths, environment, entries),
            "artifact": artifact.relative_to(ROOT).as_posix(), "artifact_sha256": artifact_hash,
            "origin_receipt": record["id"], "origin_time": times[-1]}


def discover():
    cases = []
    spec = importlib.util.spec_from_file_location("integration_selection", ROOT / "scripts/select-integration-tests.py")
    selection = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(selection)
    for package in PACKAGES:
        output = subprocess.check_output([str(GO), "test", "-tags=integration", "-list", "^TestIntegration", "./internal/" + package], cwd=ROOT, text=True)
        names = [line for line in output.splitlines() if line.startswith("TestIntegration")]
        if len(names) != len(set(names)) or any(not re.fullmatch(r"TestIntegration[A-Za-z0-9_]+", name) for name in names):
            raise ValueError("invalid integration discovery")
        if package in {"storage", "command"}:
            names = selection.select(output, package)
        cases.extend((package, name) for name in names)
    if not cases:
        raise ValueError("integration discovery is empty")
    return cases


def main():
    os.umask(0o077)
    if os.environ.get("ALMANAC_TEST_DATABASE_URL") != FIXTURE:
        raise ValueError("integration reuse requires the existing isolated fixture on port 15487")
    mode = os.environ.get("INTEGRATION_SELECTION", "affected")
    selected_test = os.environ.get("INTEGRATION_TEST", "")
    selected_package = os.environ.get("GO_PACKAGE", "all")
    if mode not in {"affected", "all"} or (selected_test and not re.fullmatch(r"TestIntegration[A-Za-z0-9_]+", selected_test)):
        raise ValueError("select affected/all, or one exact discovered integration test")
    controls = load_controls()
    base = ROOT / ".local/integration-runs"
    base.mkdir(parents=True, exist_ok=True)
    with (base / "lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        cases, graph = discover(), go_graph()
        if graph.pop("@external_local_replacement", False):
            mode = "all"
        chosen = [(p, t) for p, t in cases if (selected_package == "all" or selected_package == "./internal/" + p) and (not selected_test or selected_test == t)]
        if not chosen or (selected_test and len(chosen) != 1):
            raise ValueError("integration selector matches zero or ambiguous cases")
        # The fixture identity is its server start time and version: a restarted tmpfs fixture never reuses passes.
        fixture_id = subprocess.check_output(["psql", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align",
                                              "--command=SELECT pg_postmaster_start_time()::text || ' ' || version()", FIXTURE], text=True).strip()
        tool = subprocess.check_output([str(GO), "env", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED"], cwd=ROOT, text=True)
        environment = digest(json.dumps([fixture_id, tool, FLAGS, os.environ.get("GOFLAGS", "")]).encode())
        path = ROOT / ".local/integration-results.json"
        cache = json.loads(path.read_text()) if path.is_file() else {}
        if not isinstance(cache, dict):
            raise ValueError("integration result cache must be an object")
        run = base / (str(int(time.time())) + "-" + uuid.uuid4().hex)
        run.mkdir()
        if len(cache) < len(cases) and mode != "all":
            import_legacy(controls, graph, cases, environment, cache, run, fixture_id)
        inputs = {(p, t): fingerprints(ROOT, case_inputs(ROOT, graph, p, t), environment) for p, t in chosen}
        report = {"cases": [], "started_unix": time.time(), "full_scope": len(chosen) == len(cases), "discovered": len(cases), "selected": len(chosen), "executed": 0, "reused": 0}
        evidence_cache = {}
        for package, test in chosen:
            key = package + "/" + test
            previous = cache.get(key)
            reason = "explicit test selection" if selected_test else "forced full execution" if mode == "all" else reuse_reason(previous, inputs[(package, test)], package, test, evidence_cache=evidence_cache)
            report["cases"].append({"package": package, "test": test, "reason": reason, "result": "pending" if reason else "reused", "evidence": report_evidence(previous) if not reason else None})
        planned = sum(item["reason"] is not None for item in report["cases"])
        print(f"Integration plan: {len(chosen)} covered cases; {planned} to execute, {len(chosen)-planned} reusable passes", flush=True)
        if os.environ.get("INTEGRATION_PLAN", "0") == "1":
            report.update({"plan_only": True, "full_scope": False, "would_execute": planned, "would_reuse": len(chosen)-planned})
            controls.atomic_json(ROOT / ".local/integration-plan.json", report)
            for item in report["cases"]:
                if item["reason"]:
                    print(f"WOULD RUN {item['package']}/{item['test']}: {item['reason']}", flush=True)
            return 0
        for item in report["cases"]:
            package, test = item["package"], item["test"]
            if item["reason"] is None:
                report["reused"] += 1
                continue
            print(f"RUN {package}/{test}: {item['reason']}", flush=True)
            key = package + "/" + test
            cache[key] = {"result": "running", "inputs": inputs[(package, test)]}
            controls.atomic_json(path, cache)
            artifact = run / (package + "-" + test + ".jsonl")
            started = time.monotonic()
            with artifact.open("wb") as output:
                code = subprocess.run([str(GO), "test", *FLAGS, "-run", "^" + test + "$", "./internal/" + package], cwd=ROOT, stdout=output, stderr=subprocess.STDOUT).returncode
            unchanged = fingerprints(ROOT, inputs[(package, test)].keys() - {"@environment"}, environment) == inputs[(package, test)]
            ok = code == 0 and unchanged and passed(read_events(artifact), package, test)
            cache[key] = {"result": "passed" if ok else "failed", "inputs": inputs[(package, test)],
                "artifact": artifact.relative_to(ROOT).as_posix(), "artifact_sha256": digest(artifact.read_bytes()),
                "ended_unix": time.time(), "elapsed_seconds": time.monotonic() - started}
            item.update({"result": cache[key]["result"], "evidence": report_evidence(cache[key])})
            report["executed"] += 1
            controls.atomic_json(path, cache)
            controls.atomic_json(ROOT / ".local/integration-report.json", report)
            print(f"{cache[key]['result'].upper()} {package}/{test}: {cache[key]['elapsed_seconds']:.3f}s", flush=True)
        report["ended_unix"] = time.time()
        report["failed"] = sum(item["result"] == "failed" for item in report["cases"])
        controls.atomic_json(ROOT / ".local/integration-report.json", report)
        controls.atomic_json(run / "report.json", report)
        print(f"Integration: {report['executed']} executed, {report['reused']} reused, {report['failed']} failed; report: .local/integration-report.json", flush=True)
        return int(report["failed"] != 0)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"integration selection failed: {error}")
        raise SystemExit(2)
