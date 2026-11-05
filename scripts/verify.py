#!/usr/bin/env python3
"""Almanac verification: the mode table, fixture identity, integration case selection,
source-bound receipts, local task budgets and the timing ledger (ADR-0029).

Runs inside the development container (`make verify MODE=…` enters it); the Makefile's
`_verify_*` targets hold the individual check recipes, this script owns everything
around them.
"""
from __future__ import annotations

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
GO = "go"  # from the dev image

# The PostgreSQL fixture of compose.dev.yaml: one identity, never passed on the command line.
FIXTURE_DATABASE, FIXTURE_USER, FIXTURE_PASSWORD, FIXTURE_PORT = "almanac_f1_storage", "almanac", "almanac_f1_t05_local_only", 15487
FIXTURE_URL = f"postgres://{FIXTURE_USER}:{FIXTURE_PASSWORD}@127.0.0.1:{FIXTURE_PORT}/{FIXTURE_DATABASE}?sslmode=disable"
FIXTURE_SERVER_VERSION = "180006"  # PostgreSQL 18.6


def receipt_directory(root: Path, receipts_override: str | None = None, state_override: str | None = None) -> Path:
    if receipts_override:
        return Path(receipts_override).expanduser().resolve()
    default_state_link = root / ".local/verification/task.json"
    shared_state = Path(state_override).expanduser().resolve() if state_override else (default_state_link.resolve() if default_state_link.exists() or default_state_link.is_symlink() else None)
    return (shared_state.parent / "receipts" if shared_state else root / ".local/verification").resolve()


RECEIPTS = receipt_directory(ROOT, os.environ.get("ALMANAC_VERIFICATION_RECEIPTS"), os.environ.get("ALMANAC_VERIFICATION_TASK_STATE"))
GO_PRODUCTION = ("cmd", "internal", "go.mod", "go.sum", "api")
WEB_PRODUCTION = ("web/src", "web/scripts", "web/package.json", "web/package-lock.json", "web/tsconfig.json", "web/vite.config.ts")
CONTROL = ("Makefile", "scripts/verify.py")
MODES = ("ordinary", "unit", "browser", "go", "go-race", "auth-bench", "integration", "performance", "journey", "compose", "providers", "smoke")
MODE_INPUTS = {
    "ordinary": GO_PRODUCTION + WEB_PRODUCTION + ("web/tests", "sqlc.yaml", "scripts", "Makefile"),
    "go": GO_PRODUCTION + ("scripts", "Makefile"),
    "go-race": GO_PRODUCTION + ("scripts", "Makefile"),
    "auth-bench": ("internal/identity", "go.mod", "go.sum", "Makefile", "scripts"),
    "integration": GO_PRODUCTION + WEB_PRODUCTION + ("web/public", "web/assets.go", "deploy/local", "sqlc.yaml", "scripts", "Makefile"),
    "performance": GO_PRODUCTION + WEB_PRODUCTION + ("compose.yaml", ".dockerignore", "deploy", "sqlc.yaml", "scripts", "Makefile"),
    "journey": GO_PRODUCTION + WEB_PRODUCTION + ("web/tests", "sqlc.yaml", "scripts", "Makefile"),
    "compose": GO_PRODUCTION + ("compose.yaml", ".dockerignore", "deploy") + WEB_PRODUCTION + ("web/tests", "web/browser", "scripts", "Makefile"),
    "unit": ("web/src", "web/tests", "web/package.json", "web/package-lock.json", "web/vitest.config.ts") + CONTROL,
    "browser": WEB_PRODUCTION + ("web/tests", "web/browser", "web/playwright.config.ts") + CONTROL,
    "providers": ("web/src", "web/scripts", "web/package.json", "web/package-lock.json") + CONTROL,
    "smoke": GO_PRODUCTION + ("web/dist", "Makefile", "scripts"),
}
JOURNEYS = ("onboarding", "lost-login", "lost-password", "agents", "agents-close", "agents-lost", "agents-unavailable", "agents-lifecycle", "slis")
BROWSER_PROJECTS = ("desktop-light", "desktop-dark", "intermediate-light", "intermediate-dark", "mobile-light", "mobile-dark")
INTEGRATION_PACKAGES = ("storage", "authentication", "agentmanagement", "agenthttp", "authhttp", "agentcmd", "command")
GO_PREFIX = "github.com/dsolsona/almanac/"
INTEGRATION_FLAGS = ("-json", "-race", "-tags=integration", "-p=1", "-count=1", "-timeout=2m")
EXCLUDED_PARTS = {".git", ".local", "node_modules", "__pycache__", "dist"}
# Browser fixtures each executable journey loads; cases without an entry depend on every web script.
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
# Storage cases whose regressions are the most expensive to discover late run first.
STORAGE_PRIORITIES = ("TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict",
                      "TestIntegrationRetainedMinuteHistoryAndPersistedRestart",
                      "TestIntegrationRetainedDenseHistoryAndPersistedRestart")


# ----------------------------------------------------------------------------- environment

def platform_record() -> dict:
    """What produced a receipt: OS, architecture, dev image and Git revision."""
    record = {"os": platform.system().lower(), "arch": platform.machine(), "python": platform.python_version(),
              "image_id": os.environ.get("ALMANAC_DEV_IMAGE_ID") or "unknown", "git_revision": "unknown", "git_dirty": None}
    try:
        record["git_revision"] = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True, capture_output=True, check=True, timeout=30).stdout.strip()
        status = subprocess.run(["git", "status", "--porcelain", "--untracked-files=no"], cwd=ROOT, text=True, capture_output=True, check=True, timeout=60)
        record["git_dirty"] = bool(status.stdout.strip())
    except (OSError, subprocess.SubprocessError):
        pass
    return record


def fixture_check() -> str:
    """Query the live fixture for the fixed database, role, server version, loopback address and port."""
    if os.environ.get("ALMANAC_TEST_DATABASE_URL") != FIXTURE_URL:
        raise ValueError("ALMANAC_TEST_DATABASE_URL must select the fixed fixture of compose.dev.yaml (the dev container presets it)")
    if shutil.which("psql") is None:
        raise ValueError("psql is missing; run inside the dev container (make shell)")
    query = ("SELECT current_database() || '|' || current_user || '|' || current_setting('server_version_num') || '|' || "
             "host(inet_server_addr()) || '|' || inet_server_port()::text || '|' || pg_postmaster_start_time()::text")
    result = subprocess.run(["psql", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command=" + query, FIXTURE_URL],
                            text=True, capture_output=True, timeout=30)
    if result.returncode:
        raise ValueError(f"PostgreSQL fixture is unreachable on loopback port {FIXTURE_PORT}; start it with make shell or make setup")
    fields = result.stdout.strip().split("|")
    expected = [FIXTURE_DATABASE, FIXTURE_USER, FIXTURE_SERVER_VERSION, "127.0.0.1", str(FIXTURE_PORT)]
    if len(fields) != 6 or fields[:5] != expected:
        observed = re.sub(r"[^a-zA-Z0-9_.:|/-]", "", result.stdout.strip())[:256]
        raise ValueError(f"fixture identity mismatch; expected database|user|server_version_num|address|port = {'|'.join(expected)}, observed {observed}")
    return fields[5]


def docker_available() -> bool:
    try:
        return subprocess.run(["docker", "version", "--format", "{{.Server.Version}}"], capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


# ----------------------------------------------------------------------------- fingerprints

def fingerprint_for_root(root: Path, paths: tuple[str, ...], include_dist: bool = False) -> str:
    h = hashlib.sha256()
    for rel in paths:
        p = root / rel
        candidates = sorted(p.rglob("*") if p.is_dir() else [p])
        for item in candidates:
            excluded = {".git", "node_modules", "__pycache__"} | (set() if include_dist else {"dist"})
            if not item.is_file() or any(part in excluded for part in item.parts) or item.suffix == ".pyc":
                continue
            h.update(item.relative_to(root).as_posix().encode() + b"\0")
            h.update(item.read_bytes() + b"\0")
    return h.hexdigest()


def mode_fingerprint(mode: str) -> str:
    return fingerprint_for_root(ROOT, MODE_INPUTS[mode], include_dist=mode == "smoke")


def effective_scope(mode: str) -> tuple[dict, bool]:
    if mode == "browser":
        selectors = {"test_file": os.environ.get("TEST_FILE", ""), "project": os.environ.get("BROWSER_PROJECT", "")}
        return selectors, not any(selectors.values())
    if mode == "unit":
        selectors = {"test_file": os.environ.get("TEST_FILE", "")}
        return selectors, not selectors["test_file"]
    if mode == "go":
        package = os.environ.get("GO_PACKAGE", "")
        return {"go_package": package or "./..."}, not package or package == "./..."
    if mode == "integration":
        package = os.environ.get("GO_PACKAGE", "")
        test = os.environ.get("INTEGRATION_TEST", "")
        selection = os.environ.get("INTEGRATION_SELECTION", "affected")
        plan_only = os.environ.get("INTEGRATION_PLAN", "0") == "1"
        return {"go_package": package or "all", "integration_test": test, "integration_selection": selection, "plan_only": plan_only}, (not package or package == "all") and not test and not plan_only
    if mode == "journey":
        scenario = os.environ.get("BROWSER_SCENARIO", "onboarding")
        return {"browser_scenario": scenario}, True
    return {"selection": "fixed-full-gate"}, True


def receipt_is_full_scope(mode: str, selectors: dict) -> bool:
    if mode == "browser":
        return not selectors.get("test_file") and not selectors.get("project")
    if mode == "unit":
        return not selectors.get("test_file")
    if mode == "go":
        return selectors.get("go_package") in {"", "./..."}
    if mode == "integration":
        return selectors.get("go_package") in {"", "all"} and not selectors.get("integration_test") and not selectors.get("plan_only")
    if mode == "journey":
        return selectors.get("browser_scenario") in JOURNEYS
    return selectors == {"selection": "fixed-full-gate"}


def validate_integration_report(report: dict, full_scope: bool, started: float) -> None:
    cases = report.get("cases", [])
    if (not cases or report.get("started_unix", 0) < started or not report.get("ended_unix") or
        report.get("full_scope") is not full_scope or report.get("selected") != len(cases) or
        (full_scope and report.get("discovered") != len(cases)) or
        len({(item.get("package"), item.get("test")) for item in cases}) != len(cases) or
        any(item.get("result") not in {"passed", "reused"} or item.get("evidence", {}).get("result") != "passed" for item in cases) or
        report.get("executed") != sum(item.get("result") == "passed" for item in cases) or
        report.get("reused") != sum(item.get("result") == "reused" for item in cases)):
        raise ValueError("integration coverage is missing, incomplete, failed or from another run")


# ----------------------------------------------------------------------------- gates

def gate_commands(mode: str) -> list[list[str]]:
    web_test = os.environ.get("TEST_FILE", "")
    if mode == "ordinary":
        return [["make", "_verify_all"], [sys.executable, "-m", "unittest", "discover", "-s", "scripts/tests", "-t", "scripts/tests"]]
    if mode == "unit":
        command = ["./node_modules/.bin/vitest", "run", "--config", "vitest.config.ts"]
        if web_test:
            if not web_test.startswith("tests/") or not web_test.endswith(".test.tsx") or not safe_relative(web_test) or not (ROOT / "web" / web_test).is_file():
                raise ValueError("TEST_FILE must name an existing web/tests/*.test.tsx file")
            command.append(web_test)
        return [command]
    if mode == "browser":
        command = ["./node_modules/.bin/playwright", "test", "--config", "playwright.config.ts"]
        if web_test:
            if not web_test.startswith("browser/") or not web_test.endswith(".spec.ts") or not safe_relative(web_test) or not (ROOT / "web" / web_test).is_file():
                raise ValueError("TEST_FILE must name an existing web/browser/*.spec.ts file")
            command.append(web_test)
        project = os.environ.get("BROWSER_PROJECT", "")
        if project:
            if project not in BROWSER_PROJECTS:
                raise ValueError("Unknown BROWSER_PROJECT")
            command.extend(["--project", project])
        return [command]
    if mode == "integration":
        test = os.environ.get("INTEGRATION_TEST", "")
        if os.environ.get("INTEGRATION_PLAN", "0") not in {"0", "1"} or os.environ.get("INTEGRATION_SELECTION", "affected") not in {"affected", "all"} or (test and not re.fullmatch(r"TestIntegration[A-Za-z0-9_]+", test)):
            raise ValueError("integration requires affected/all or one exact test name")
        if os.environ.get("GO_PACKAGE", "all") not in {"all", *("./internal/" + p for p in INTEGRATION_PACKAGES)}:
            raise ValueError("integration package is outside the fixed catalog")
        return [[sys.executable, str(Path(__file__)), "run-integration"]]
    if mode == "journey":
        if os.environ.get("BROWSER_SCENARIO", "onboarding") not in JOURNEYS:
            raise ValueError("BROWSER_SCENARIO must select a documented browser journey")
        return [["make", "_verify_journey"]]
    if mode in {"compose", "providers"}:
        if not docker_available():
            raise ValueError(f"{mode} drives the host Docker daemon; run it as make verify MODE={mode} from the host so the socket is mounted")
        if mode == "providers":
            return [["node", "scripts/check-local-providers.mjs"]]
        return [["python3", "scripts/local-agent-tty-test.py"], ["node", "scripts/check-local-agent.mjs"], ["node", "scripts/check-local-compose.mjs"], ["node", "scripts/check-local-compose-cancellation.mjs"]]
    return {"go": [["make", "_verify_go"]], "go-race": [["make", "_verify_go_race"]], "auth-bench": [["make", "_verify_auth_bench"]],
            "performance": [["make", "_verify_performance"]], "smoke": [["make", "_verify_smoke"]]}[mode]


def safe_relative(value: str) -> bool:
    return bool(value) and ".." not in value and all(c in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-" for c in value)


def run_gate(mode: str) -> int:
    if mode not in MODES:
        raise ValueError(f"unknown verification mode {mode!r}; use one of {', '.join(MODES)}")
    commands = gate_commands(mode)
    run_id = broad_start(mode)
    RECEIPTS.mkdir(parents=True, exist_ok=True)
    log = RECEIPTS / f"{run_id}.log"
    status = 0
    interrupted = {"signal": None}
    child = None
    old_handlers = {}
    secret_values = [value.encode() for key, value in os.environ.items() if value and len(value) >= 8 and ("DATABASE_URL" in key or any(word in key.upper() for word in ("PASSWORD", "TOKEN", "SECRET", "CREDENTIAL")))]

    def interrupt(signum, _frame):
        interrupted["signal"] = signum
        raise KeyboardInterrupt

    for sig in (signal.SIGINT, signal.SIGTERM):
        old_handlers[sig] = signal.signal(sig, interrupt)
    try:
        with log.open("wb") as artifact:
            for command in commands:
                cwd = ROOT / "web" if command[0] in {"node", "./node_modules/.bin/vitest", "./node_modules/.bin/playwright"} else ROOT
                child = subprocess.Popen(command, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True, close_fds=False)
                assert child.stdout is not None
                for line in iter(child.stdout.readline, b""):
                    for secret in secret_values:
                        line = line.replace(secret, b"<redacted>")
                    artifact.write(line); artifact.flush()
                    sys.stdout.buffer.write(line); sys.stdout.buffer.flush()
                code = child.wait()
                if code:
                    status = 128 + (-code) if code < 0 else code
                    break
    except KeyboardInterrupt:
        status = 128 + int(interrupted["signal"] or signal.SIGINT)
        if child is not None and child.poll() is None:
            try:
                os.killpg(child.pid, signal.SIGTERM)
                child.wait(timeout=10)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
    except OSError as exc:
        status = 2
        with log.open("ab") as artifact:
            artifact.write((str(exc) + "\n").encode())
    finally:
        for sig, handler in old_handlers.items():
            signal.signal(sig, handler)
        completed = broad_complete(run_id, status)
        if status == 0 and completed:
            status = completed
    return status


def counted_go_events(path: Path, expected: set[str]) -> int:
    run, passed_names, failed, skipped = set(), set(), set(), set()
    for line in path.read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        name, action = event.get("Test"), event.get("Action")
        if name in expected:
            {"run": run, "pass": passed_names, "fail": failed, "skip": skipped}.get(action, set()).add(name)
    if run != expected or passed_names != expected or failed or skipped:
        raise ValueError(f"Go selected tests did not all run and pass (expected {len(expected)}, ran {len(run)}, passed {len(passed_names)}, failed {len(failed)}, skipped {len(skipped)})")
    return len(expected)


def performance_evidence() -> int:
    name = "TestIntegrationControlledBackendPerformance"
    expected = {name, f"{name}/independent-10"}
    path = ROOT / ".local/performance-go-test.jsonl"
    count = counted_go_events(path, expected)
    for line in path.read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        test = event.get("Test", "")
        if event.get("Action") == "run" and isinstance(test, str) and test.startswith(name + "/independent-") and test not in expected:
            raise ValueError("deferred capacity fleet executed outside the ten-SLO phase")
    print(f"Controlled performance: {count} required cases ran and passed")
    return 0


def ensure_web_assets(artifact: Path, env: dict[str, str]) -> int:
    result = subprocess.run(["make", "_ensure_web_assets"], cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
    output = result.stdout or b""
    with artifact.open("ab") as log:
        log.write(output)
    if output:
        sys.stdout.write(output.decode(errors="replace"))
        sys.stdout.flush()
    return result.returncode


# ----------------------------------------------------------------------------- integration cases

def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def files_under(root: Path, relative: str) -> set[str]:
    path = root / relative
    return {p.relative_to(root).as_posix() for p in (path.rglob("*") if path.is_dir() else [path])
            if p.is_file() and not EXCLUDED_PARTS.intersection(p.relative_to(root).parts)}


def script_inputs(root: Path, roots: list[str]) -> set[str]:
    pending, found = list(roots), set()
    while pending:
        name = pending.pop()
        if name in found:
            continue
        found.add(name)
        path = root / name
        if not path.is_file() or path.suffix not in {".mjs", ".js", ".ts", ".tsx"}:
            continue
        imports = re.findall(r'''(?:from\s*|import\s*(?:\(\s*)?)['"](\.[^'"]+)['"]''', path.read_text())
        for imported in imports:
            dependency = (path.parent / imported).resolve()
            if not dependency.is_relative_to(root):
                raise ValueError("browser fixture import leaves checkout")
            pending.append(dependency.relative_to(root).as_posix())
    return found


def go_graph() -> dict:
    output = subprocess.check_output([GO, "list", "-deps", "-test", "-json", "-tags=integration",
                                      *[f"./internal/{p}" for p in INTEGRATION_PACKAGES]], cwd=ROOT, text=True)
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


def go_inputs(root: Path, graph: dict, package: str) -> set[str]:
    target = GO_PREFIX + "internal/" + package
    pending = [target, *graph[target].get("TestImports", []), *graph[target].get("XTestImports", [])]
    found, paths = set(), set()
    while pending:
        name = pending.pop().split(" [", 1)[0]
        if name in found or name not in graph:
            continue
        found.add(name)
        item = graph[name]
        directory = Path(item["Dir"])
        if EXCLUDED_PARTS.intersection(directory.relative_to(root).parts):
            continue
        pending.extend(item.get("Imports", []))
        fields = ["GoFiles", "CgoFiles", "CFiles", "HFiles", "SFiles", "EmbedFiles"]
        if name == target:
            fields += ["TestGoFiles", "XTestGoFiles", "TestEmbedFiles", "XTestEmbedFiles"]
        for field in fields:
            for filename in item.get(field, []):
                path = (directory / filename).relative_to(root)
                if not EXCLUDED_PARTS.intersection(path.parts):
                    paths.add(path.as_posix())
    return paths


def case_inputs(root: Path, graph: dict, package: str, test: str) -> set[str]:
    paths = go_inputs(root, graph, package)
    for name in ("go.mod", "go.sum", "api", "sqlc.yaml", "Makefile", "scripts/verify.py"):
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


def fingerprints(root: Path, paths, environment: str) -> dict:
    inputs = {"@environment": environment}
    for name in sorted(paths):
        path = root / name
        inputs[name] = digest(path.read_bytes()) if path.is_file() else "missing"
    return inputs


def read_events(path: Path) -> list[dict]:
    try:
        return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    except (OSError, ValueError):
        return []


def passed(events: list[dict], package: str, test: str) -> bool:
    matching = [e for e in events if e.get("Package") == GO_PREFIX + "internal/" + package and
                (e.get("Test") == test or str(e.get("Test", "")).startswith(test + "/"))]
    return ([e["Action"] for e in matching if e.get("Test") == test and e.get("Action") in {"pass", "fail", "skip"}] == ["pass"]
            and not any(e.get("Action") == "fail" for e in matching))


def reuse_reason(record, inputs: dict, package: str, test: str, root: Path = ROOT, evidence_cache=None):
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


def report_evidence(record: dict) -> dict:
    return {**{key: value for key, value in record.items() if key != "inputs"},
            "inputs_sha256": digest(json.dumps(record["inputs"], sort_keys=True).encode())}


def select_cases(source: str, package: str) -> list[str]:
    """Validate `go test -list` output for a package and order the storage priorities first."""
    names = []
    for line in source.splitlines():
        if line.startswith("TestIntegration"):
            if not re.fullmatch(r"TestIntegration[A-Za-z0-9_]*", line) or line in names:
                raise ValueError("invalid or duplicate integration test")
            names.append(line)
        elif not re.fullmatch(r"(?:ok|\?)\s+\S+(?:\s+.*)?", line):
            raise ValueError("unexpected integration discovery output")
    if package == "storage" and not names:
        raise ValueError("storage integration discovery is empty")
    if package == "command" and "TestIntegrationExecutableBrowserSLICollectionJourney" not in names:
        raise ValueError("command integration discovery is missing the SLI journey")
    if package == "storage":
        names = [name for name in STORAGE_PRIORITIES if name in names] + [name for name in names if name not in STORAGE_PRIORITIES]
    return names


def discover_cases() -> list[tuple[str, str]]:
    cases = []
    for package in INTEGRATION_PACKAGES:
        output = subprocess.check_output([GO, "test", "-tags=integration", "-list", "^TestIntegration", "./internal/" + package], cwd=ROOT, text=True)
        cases.extend((package, name) for name in select_cases(output, package))
    if not cases:
        raise ValueError("integration discovery is empty")
    return cases


def run_integration() -> int:
    """Run the selected integration cases serially; reuse passes whose inputs and fixture are unchanged."""
    os.umask(0o077)
    mode = os.environ.get("INTEGRATION_SELECTION", "affected")
    selected_test = os.environ.get("INTEGRATION_TEST", "")
    selected_package = os.environ.get("GO_PACKAGE", "all")
    if mode not in {"affected", "all"} or (selected_test and not re.fullmatch(r"TestIntegration[A-Za-z0-9_]+", selected_test)):
        raise ValueError("select affected/all, or one exact discovered integration test")
    env = {**os.environ, "GOTOOLCHAIN": "local", "GOPROXY": "off"}
    for preflight in (["make", "_ensure_web_assets"], ["make", "_verify_sqlc_generated"]):
        if subprocess.run(preflight, cwd=ROOT, env=env).returncode:
            raise ValueError(f"preflight failed: {' '.join(preflight)}")
    fixture_started = fixture_check()
    if subprocess.run(["make", "build"], cwd=ROOT, env=env).returncode:
        raise ValueError("candidate build failed")
    base = ROOT / ".local/integration-runs"
    base.mkdir(parents=True, exist_ok=True)
    with (base / "lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        cases, graph = discover_cases(), go_graph()
        if graph.pop("@external_local_replacement", False):
            mode = "all"
        chosen = [(p, t) for p, t in cases if (selected_package == "all" or selected_package == "./internal/" + p) and (not selected_test or selected_test == t)]
        if not chosen or (selected_test and len(chosen) != 1):
            raise ValueError("integration selector matches zero or ambiguous cases")
        # A restarted tmpfs fixture or a different toolchain never reuses passes.
        tool = subprocess.check_output([GO, "env", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED"], cwd=ROOT, text=True)
        environment = digest(json.dumps([fixture_started, tool, INTEGRATION_FLAGS, os.environ.get("GOFLAGS", "")]).encode())
        path = ROOT / ".local/integration-results.json"
        cache = json.loads(path.read_text()) if path.is_file() else {}
        if not isinstance(cache, dict):
            raise ValueError("integration result cache must be an object")
        run = base / (str(int(time.time())) + "-" + uuid.uuid4().hex)
        run.mkdir()
        inputs = {(p, t): fingerprints(ROOT, case_inputs(ROOT, graph, p, t), environment) for p, t in chosen}
        report = {"cases": [], "started_unix": time.time(), "full_scope": len(chosen) == len(cases), "discovered": len(cases), "selected": len(chosen), "executed": 0, "reused": 0, "platform": platform_record()}
        evidence_cache = {}
        for package, test in chosen:
            previous = cache.get(package + "/" + test)
            reason = "explicit test selection" if selected_test else "forced full execution" if mode == "all" else reuse_reason(previous, inputs[(package, test)], package, test, evidence_cache=evidence_cache)
            report["cases"].append({"package": package, "test": test, "reason": reason, "result": "pending" if reason else "reused", "evidence": report_evidence(previous) if not reason else None})
        planned = sum(item["reason"] is not None for item in report["cases"])
        print(f"Integration plan: {len(chosen)} covered cases; {planned} to execute, {len(chosen)-planned} reusable passes", flush=True)
        if os.environ.get("INTEGRATION_PLAN", "0") == "1":
            report.update({"plan_only": True, "full_scope": False, "would_execute": planned, "would_reuse": len(chosen)-planned})
            atomic_json(ROOT / ".local/integration-plan.json", report)
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
            atomic_json(path, cache)
            artifact = run / (package + "-" + test + ".jsonl")
            started = time.monotonic()
            with artifact.open("wb") as output:
                code = subprocess.run([GO, "test", *INTEGRATION_FLAGS, "-run", "^" + test + "$", "./internal/" + package], cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT).returncode
            unchanged = fingerprints(ROOT, inputs[(package, test)].keys() - {"@environment"}, environment) == inputs[(package, test)]
            ok = code == 0 and unchanged and passed(read_events(artifact), package, test)
            cache[key] = {"result": "passed" if ok else "failed", "inputs": inputs[(package, test)],
                          "artifact": artifact.relative_to(ROOT).as_posix(), "artifact_sha256": digest(artifact.read_bytes()),
                          "ended_unix": time.time(), "elapsed_seconds": time.monotonic() - started}
            item.update({"result": cache[key]["result"], "evidence": report_evidence(cache[key])})
            report["executed"] += 1
            atomic_json(path, cache)
            atomic_json(ROOT / ".local/integration-report.json", report)
            print(f"{cache[key]['result'].upper()} {package}/{test}: {cache[key]['elapsed_seconds']:.3f}s", flush=True)
        report["ended_unix"] = time.time()
        report["failed"] = sum(item["result"] == "failed" for item in report["cases"])
        atomic_json(ROOT / ".local/integration-report.json", report)
        atomic_json(run / "report.json", report)
        print(f"Integration: {report['executed']} executed, {report['reused']} reused, {report['failed']} failed; report: .local/integration-report.json", flush=True)
        return int(report["failed"] != 0)


# ----------------------------------------------------------------------------- receipts and task state

def elapsed_evidence(started: float, ended: float, started_monotonic: float, ended_monotonic: float) -> dict:
    """Keep UTC boundaries for correlation, monotonic time for actual duration."""
    elapsed = ended_monotonic - started_monotonic
    return {"started_monotonic": started_monotonic, "ended_monotonic": ended_monotonic,
            "elapsed_seconds": elapsed, "elapsed_clock": "monotonic",
            "wall_elapsed_seconds": ended - started,
            "clock_adjustment_seconds": ended - started - elapsed}


@contextmanager
def locked_state(path: Path):
    path = path.resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    lock = path.with_suffix(path.suffix + ".lock")
    with lock.open("a+") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        state = json.loads(path.read_text()) if path.exists() else None
        yield state


def write_state(path: Path, state: dict) -> None:
    path = path.resolve()
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(state, f, indent=2)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def task_state_path() -> Path | None:
    configured = os.environ.get("ALMANAC_VERIFICATION_TASK_STATE")
    path = Path(configured).expanduser() if configured else ROOT / ".local/verification/task.json"
    path = path.resolve()
    if not configured and not path.exists():
        return None
    return path


def broad_start(mode: str) -> str:
    """Charge the task budget (when a task is active) and open a pending receipt."""
    if mode not in MODES:
        raise ValueError("unknown verification mode")
    path = task_state_path()
    if path is not None:
        with locked_state(path) as state:
            if state is None:
                raise ValueError("active task state path is configured but state file is missing")
            entry = state["broad_gates"].get(mode)
            if entry is None:
                raise ValueError(f"gate {mode!r} is not in this task's plan")
            budget = entry["allowance"] if isinstance(entry, dict) else int(entry)
            count = state["broad_attempts"].get(mode, 0)
            if count >= budget:
                reason = os.environ.get("VERIFY_RERUN_REASON", "").strip()
                boundary = os.environ.get("VERIFY_RERUN_BOUNDARY", "")
                receipt = os.environ.get("VERIFY_FOCUSED_RECEIPT", "")
                allowed = entry.get("boundaries", []) if isinstance(entry, dict) else []
                if not reason or boundary not in allowed or not receipt or not receipt_valid(receipt, state, expected_mode=boundary):
                    raise ValueError("broad rerun denied: a recorded reason, allowed boundary, and matching fresh receipt are required")
            state["broad_attempts"][mode] = count + 1
            state.setdefault("broad_reasons", []).append({"gate": mode, "reason": os.environ.get("VERIFY_RERUN_REASON", ""), "boundary": os.environ.get("VERIFY_RERUN_BOUNDARY", ""), "receipt": os.environ.get("VERIFY_FOCUSED_RECEIPT", "")})
            run_id = f"{int(time.time())}-{uuid.uuid4().hex}"
            state.setdefault("broad_runs", {})[run_id] = {"mode": mode, "result": "running"}
            write_state(path, state)
    else:
        run_id = f"{int(time.time())}-{uuid.uuid4().hex}"
    selectors, full_scope = effective_scope(mode)
    record = {"id": run_id, "mode": mode, "origin": str(ROOT), "fingerprint": mode_fingerprint(mode), "started_unix": time.time(), "started_monotonic": time.monotonic(),
              "exit_code": None, "result": "running", "selectors": selectors, "full_scope": full_scope, "artifacts": [f"{run_id}.log"], "platform": platform_record()}
    RECEIPTS.mkdir(parents=True, exist_ok=True)
    atomic_json(RECEIPTS / f"{run_id}.pending.json", record)
    return run_id


def broad_complete(run_id: str, exit_code: int) -> int:
    """Close the pending receipt: a changed source or a failed integration report fails the run."""
    if not run_id or any(c not in "0123456789abcdef-" for c in run_id):
        raise ValueError("invalid broad run id")
    pending_file = RECEIPTS / f"{run_id}.pending.json"
    pending = json.loads(pending_file.read_text())
    end_fingerprint = mode_fingerprint(pending["mode"])
    status = exit_code
    if pending["fingerprint"] != end_fingerprint and status == 0:
        status = 1
    plan_only = pending.get("selectors", {}).get("plan_only", False)
    if pending["mode"] == "integration" and not plan_only:
        report_path = ROOT / ".local/integration-report.json"
        try:
            integration_report = json.loads(report_path.read_text())
            if status == 0:
                validate_integration_report(integration_report, pending["full_scope"], pending["started_unix"])
            if integration_report.get("started_unix", 0) >= pending["started_unix"]:
                name = run_id + "-integration.json"
                atomic_json(RECEIPTS / name, integration_report, immutable=True)
                pending["artifacts"].append(name)
                pending.update(selected_count=integration_report.get("selected"), executed_count=integration_report.get("executed"), reused_count=integration_report.get("reused"))
        except (OSError, ValueError, TypeError, AttributeError):
            if status == 0:
                status = 1
    ended = time.time()
    pending.update({"ended_unix": ended, "elapsed_seconds": ended - pending["started_unix"], "fingerprint_at_end": end_fingerprint, "exit_code": status, "result": "planned" if status == 0 and plan_only else "passed" if status == 0 else "failed"})
    if isinstance(pending.get("started_monotonic"), (int, float)):
        pending.update(elapsed_evidence(pending["started_unix"], ended, pending["started_monotonic"], time.monotonic()))
    else:
        pending["elapsed_clock"] = "wall-legacy"
    atomic_json(RECEIPTS / f"{run_id}.json", pending, immutable=True)
    pending_file.unlink()
    refresh_timing_ledger_safely()
    path = task_state_path()
    if path is not None:
        with locked_state(path) as state:
            if state is not None and run_id in state.get("broad_runs", {}):
                state["broad_runs"].pop(run_id)
                write_state(path, state)
    return status


def guard(action: str) -> int:
    path = task_state_path()
    if action == "init" and path is None:
        path = (ROOT / ".local/verification/task.json").resolve()
    if path is None:
        if action == "handoff":
            raise ValueError("handoff requires an active verification task")
        return 0
    with locked_state(path) as state:
        if action == "init":
            if state is not None:
                raise ValueError("task state already exists; refusing silent reset")
            plan_path = Path(os.environ.get("VERIFICATION_PLAN", ""))
            if not plan_path.is_file():
                raise ValueError("VERIFICATION_PLAN must name the task budget JSON")
            plan = json.loads(plan_path.read_text())
            validate_plan(plan)
            prior_handoffs = plan.get("prior_handoffs", 0)
            state = {"task": plan["task"], "required_receipts": plan.get("required_receipts", []), "broad_gates": plan.get("broad_gates", {}), "broad_attempts": {}, "handoffs": prior_handoffs, "prior_handoffs": prior_handoffs, "prior_handoffs_note": plan.get("prior_handoffs_note", ""), "replans": []}
            write_state(path, state)
            return 0
        if state is None:
            raise ValueError("active task state path is configured but state file is missing")
        if action == "handoff":
            state["handoffs"] += 1
            replan = os.environ.get("VERIFY_REPLAN") == "1"
            reason = os.environ.get("VERIFY_REPLAN_REASON", "").strip()
            if state["handoffs"] > 2 and not replan:
                write_state(path, state)
                raise ValueError("handoff denied after two attempts; record VERIFY_REPLAN=1 and VERIFY_REPLAN_REASON")
            if replan:
                if not reason:
                    write_state(path, state)
                    raise ValueError("VERIFY_REPLAN_REASON is required")
                state["replans"].append(reason)
        write_state(path, state)
    if action == "handoff":
        return report()
    return 0


def validate_plan(plan: dict) -> None:
    if not isinstance(plan, dict) or not isinstance(plan.get("task"), str) or not plan["task"].strip():
        raise ValueError("plan task must be a nonempty string")
    prior = plan.get("prior_handoffs", 0)
    if not isinstance(prior, int) or isinstance(prior, bool) or prior < 0:
        raise ValueError("prior_handoffs must be a nonnegative integer")
    note = plan.get("prior_handoffs_note", "")
    if not isinstance(note, str):
        raise ValueError("prior_handoffs_note must be text")
    required = plan.get("required_receipts", [])
    if not isinstance(required, list) or any(not isinstance(mode, str) or mode not in MODES for mode in required):
        raise ValueError("required_receipts contains an unknown verification mode")
    gates = plan.get("broad_gates", {})
    if not isinstance(gates, dict):
        raise ValueError("broad_gates must be an object")
    for gate, config in gates.items():
        if gate not in MODES or not isinstance(config, dict) or set(config) - {"allowance", "boundaries"}:
            raise ValueError(f"invalid broad gate plan for {gate!r}")
        allowance = config.get("allowance")
        if not isinstance(allowance, int) or isinstance(allowance, bool) or not 0 <= allowance <= 2:
            raise ValueError(f"{gate} allowance must be an integer from 0 through 2")
        boundaries = config.get("boundaries", [])
        if not isinstance(boundaries, list) or any(not isinstance(mode, str) or mode not in MODES or mode == gate for mode in boundaries):
            raise ValueError(f"{gate} boundaries must list other verification modes")


def atomic_json(path: Path, record: dict, immutable: bool = False) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(record, f, sort_keys=True)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        if immutable:
            # Hard-link creation is atomic and fails rather than replacing a receipt.
            os.link(tmp, path)
        else:
            os.replace(tmp, path)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def receipt_valid(name: str, state: dict, expected_mode: str | None = None) -> bool:
    try:
        if Path(name).name != name or name in {"", ".", ".."}:
            return False
        receipt_path = (RECEIPTS / name).resolve()
        if receipt_path.parent != RECEIPTS:
            return False
        record = json.loads(receipt_path.read_text())
        if record.get("result") != "passed" or record.get("exit_code") != 0:
            return False
        mode = record.get("mode")
        if mode not in MODES:
            return False
        if mode not in state.get("required_receipts", [mode]):
            return False
        if expected_mode is not None and mode != expected_mode:
            return False
        if record.get("full_scope") is not True or not receipt_is_full_scope(mode, record.get("selectors", {})):
            return False
        artifacts = []
        for item in record["artifacts"]:
            if Path(item).name != item:
                return False
            artifact = (RECEIPTS / item).resolve()
            if artifact.parent != RECEIPTS:
                return False
            artifacts.append(artifact)
        return all(item.is_file() for item in artifacts) and mode_fingerprint(mode) == record["fingerprint"] and record.get("fingerprint_at_end") == record["fingerprint"]
    except (OSError, ValueError, KeyError, TypeError):
        return False


# ----------------------------------------------------------------------------- timing ledger

def verification_project_root() -> Path:
    return ROOT.parent.parent if ROOT.parent.name == ".worktrees" else ROOT


def timing_receipt_directories() -> list[Path]:
    project = verification_project_root()
    bases = [project / ".local/verification"]
    worktrees = project / ".worktrees"
    if worktrees.is_dir():
        bases.extend(checkout / ".local/verification" for checkout in worktrees.iterdir() if checkout.is_dir() and not checkout.is_symlink())
    directories = {RECEIPTS.resolve()} if RECEIPTS.is_dir() else set()
    # Before a task is initialized, automatic receipts live directly in this
    # base. Keep collecting them after initialization selects a receipts/ child.
    directories.update(base.resolve() for base in bases if base.is_dir())
    for base in bases:
        if not base.is_dir():
            continue
        for current, children, _files in os.walk(base, followlinks=False):
            children[:] = [name for name in children if not (Path(current) / name).is_symlink()]
            if Path(current).name == "receipts":
                directories.add(Path(current).resolve())
                children.clear()
    return sorted(directories)


def timing_receipt_records() -> tuple[list[dict], int]:
    records, unreadable = [], 0
    for directory in timing_receipt_directories():
        for path in directory.glob("*.json"):
            try:
                value = json.loads(path.read_text())
                if isinstance(value, dict) and value.get("mode"):
                    value.setdefault("id", path.name.removesuffix(".pending.json").removesuffix(".json"))
                    records.append(value)
            except (OSError, ValueError, json.JSONDecodeError):
                unreadable += 1
    manual_runs = verification_project_root() / ".local/verification/manual-runs.jsonl"
    if manual_runs.is_file():
        try:
            for line in manual_runs.read_text().splitlines():
                try:
                    value = json.loads(line)
                    if isinstance(value, dict) and value.get("id") and value.get("command"):
                        value.setdefault("mode", "manual")
                        value.setdefault("result", "unknown")
                        records.append(value)
                    else:
                        unreadable += 1
                except json.JSONDecodeError:
                    unreadable += 1
        except OSError:
            unreadable += 1
    return records, unreadable


def timing_ledger(records: list[dict], generated_unix: float, unreadable_receipts: int = 0) -> dict:
    """Summarize immutable top-level receipts; child records never add wall time twice."""
    unique = {}
    for record in records:
        run_id = str(record.get("id", ""))
        if not run_id:
            continue
        current = unique.get(run_id)
        priority = (bool(record.get("ended_unix")), record.get("result") != "running", bool(record.get("elapsed_seconds") is not None))
        old_priority = (bool(current and current.get("ended_unix")), bool(current and current.get("result") != "running"), bool(current and current.get("elapsed_seconds") is not None))
        if current is None or priority > old_priority:
            unique[run_id] = dict(record)

    modes: dict[str, dict] = {}
    ledger = []
    intervals = []
    invocation_seconds = 0.0
    child_seconds = 0.0
    ongoing_seconds = 0.0
    unknown_duration_runs = 0
    for record in unique.values():
        mode = str(record.get("mode", "unknown"))
        result = str(record.get("result", "unknown"))
        child = bool(record.get("parent_id") or record.get("parent_run_id"))
        stats = modes.setdefault(mode, {"runs": 0, "passed": 0, "failed": 0, "ongoing": 0, "other": 0, "child_runs": 0, "unknown_duration_runs": 0, "cumulative_invocation_seconds": 0.0, "child_elapsed_seconds_excluded": 0.0})
        stats["runs"] += 1
        if result in {"passed", "failed", "running"}:
            stats["ongoing" if result == "running" else result] += 1
        else:
            stats["other"] += 1
        if child:
            stats["child_runs"] += 1
        elapsed = record.get("elapsed_seconds")
        duration_known = isinstance(elapsed, (int, float)) and not isinstance(elapsed, bool) and elapsed >= 0 and result != "running"
        observed = None
        if duration_known:
            if child:
                child_seconds += float(elapsed)
                stats["child_elapsed_seconds_excluded"] += float(elapsed)
            else:
                invocation_seconds += float(elapsed)
                stats["cumulative_invocation_seconds"] += float(elapsed)
        elif result == "running":
            start = record.get("started_unix")
            if isinstance(start, (int, float)) and not isinstance(start, bool):
                observed = max(0.0, generated_unix - float(start))
                ongoing_seconds += observed
        else:
            stats["unknown_duration_runs"] += 1
            unknown_duration_runs += 1
        start = record.get("started_unix")
        end = record.get("ended_unix")
        if isinstance(start, (int, float)) and not isinstance(start, bool):
            if isinstance(end, (int, float)) and not isinstance(end, bool):
                interval_end = float(end)
            elif result == "running":
                interval_end = generated_unix
            elif duration_known:
                interval_end = float(start) + float(elapsed)
            else:
                interval_end = None
            if interval_end is not None and interval_end >= float(start) and not child:
                intervals.append((float(start), interval_end))
        ledger.append({
            "id": record.get("id"), "mode": mode, "origin": record.get("origin"),
            "started_unix": start, "ended_unix": end, "result": result,
            "exit_code": record.get("exit_code"), "duration_status": "recorded" if duration_known else ("ongoing" if result == "running" else "unknown"),
            "elapsed_seconds": float(elapsed) if duration_known else None,
            "elapsed_clock": record.get("elapsed_clock", "wall-legacy"),
            "wall_elapsed_seconds": record.get("wall_elapsed_seconds"),
            "clock_adjustment_seconds": record.get("clock_adjustment_seconds"),
            "observed_ongoing_seconds": observed if result == "running" else None,
            "selected_count": record.get("selected_count"), "executed_count": record.get("executed_count"),
            "selected": record.get("selected"), "selectors": record.get("selectors"), "platform": record.get("platform"),
            "command": record.get("command"), "artifacts": record.get("artifacts", []), "top_level_invocation": not child,
        })
    ledger.sort(key=lambda item: (item["started_unix"] if isinstance(item["started_unix"], (int, float)) else float("inf"), str(item["id"])))
    wall_union = 0.0
    last_end = None
    for start, end in sorted(intervals):
        if last_end is None or start > last_end:
            wall_union += end - start
            last_end = end
        elif end > last_end:
            wall_union += end - last_end
            last_end = end
    return {
        "generated_unix": generated_unix,
        "generated_utc": datetime.fromtimestamp(generated_unix, timezone.utc).isoformat(),
        "totals": {
            "runs": len(unique), "passed": sum(x["passed"] for x in modes.values()),
            "failed": sum(x["failed"] for x in modes.values()), "ongoing": sum(x["ongoing"] for x in modes.values()),
            "other": sum(x["other"] for x in modes.values()), "unknown_duration_runs": unknown_duration_runs,
            "cumulative_invocation_seconds": invocation_seconds, "ongoing_observed_seconds": ongoing_seconds,
            "child_elapsed_seconds_excluded": child_seconds, "top_level_wall_interval_union_seconds": wall_union,
            "unreadable_receipts": unreadable_receipts,
        },
        "modes": {mode: {key: (round(value, 3) if key.endswith("seconds") else value) for key, value in sorted(stats.items())} for mode, stats in sorted(modes.items())},
        "ledger": ledger,
        "timing_note": "New receipts measure elapsed with a monotonic clock, including preflight, startup/build and cleanup; wall-legacy receipts retain their original UTC-based duration. UTC boundaries and clock adjustment are retained separately. Nested commands are included in their parent invocation and are not added separately. Wall interval union counts overlapping UTC intervals once, but clock steps can distort it; it is not reliable occupied time across a detected clock discontinuity, nor test-only CPU time. Checks without receipts cannot be backfilled, and missing durations are unknown.",
    }


def write_timing_ledger() -> tuple[dict, Path, Path]:
    records, unreadable = timing_receipt_records()
    summary = timing_ledger(records, time.time(), unreadable)
    destination = verification_project_root() / ".local/verification"
    json_path, markdown_path = destination / "timing-ledger.json", destination / "timing-ledger.md"
    atomic_json(json_path, summary)
    lines = [
        "# Verification timing ledger", "",
        f"Generated: {summary['generated_utc']}", "",
        summary["timing_note"], "",
        f"Totals: {summary['totals']['runs']} runs; {summary['totals']['passed']} passed; {summary['totals']['failed']} failed; {summary['totals']['ongoing']} ongoing; {summary['totals']['unknown_duration_runs']} unknown duration.",
        f"Cumulative invocation elapsed: {summary['totals']['cumulative_invocation_seconds']:.2f}s; ongoing observed: {summary['totals']['ongoing_observed_seconds']:.2f}s; top-level wall interval union: {summary['totals']['top_level_wall_interval_union_seconds']:.2f}s.", "",
        "| Mode | Runs | Passed | Failed | Ongoing | Unknown duration | Invocation elapsed |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for mode, stats in summary["modes"].items():
        lines.append(f"| {mode} | {stats['runs']} | {stats['passed']} | {stats['failed']} | {stats['ongoing']} | {stats['unknown_duration_runs']} | {stats['cumulative_invocation_seconds']:.2f}s |")
    lines.extend(["", "## Runs (chronological)", "", "| Started UTC | Mode | Result | Duration | Cases selected/run | Platform | Command or selection | ID |", "| --- | --- | --- | ---: | ---: | --- | --- | --- |"])
    for run in summary["ledger"]:
        start = datetime.fromtimestamp(run["started_unix"], timezone.utc).isoformat() if isinstance(run["started_unix"], (int, float)) else "unknown"
        duration = f"{run['elapsed_seconds']:.2f}s" if run["elapsed_seconds"] is not None else (f"ongoing ({run['observed_ongoing_seconds']:.2f}s observed)" if run["observed_ongoing_seconds"] is not None else "unknown")
        counts = f"{run['selected_count'] if run['selected_count'] is not None else '?'} / {run['executed_count'] if run['executed_count'] is not None else '?'}"
        selection = run.get("command") or (", ".join(run.get("selected") or []) or run["mode"])
        selection = str(selection).replace("|", "\\|").replace("\n", " ")
        where = run.get("platform") or {}
        platform_text = f"{where.get('os', '?')}/{where.get('arch', '?')} {str(where.get('git_revision', '?'))[:12]}{'+' if where.get('git_dirty') else ''}" if where else "unknown"
        lines.append(f"| {start} | {run['mode']} | {run['result']} | {duration} | {counts} | {platform_text} | {selection} | `{run['id']}` |")
    atomic_text(markdown_path, "\n".join(lines) + "\n")
    return summary, json_path, markdown_path


def atomic_text(path: Path, contents: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, path)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def refresh_timing_ledger_safely() -> None:
    try:
        write_timing_ledger()
    except OSError as exc:
        print(f"warning: could not refresh verification timing ledger: {exc}", file=sys.stderr)


def print_timing_summary(summary: dict, json_path: Path, markdown_path: Path) -> None:
    totals = summary["totals"]
    print("Verification timing (top-level invocation elapsed)", file=sys.stderr)
    print("mode | runs | pass | fail | ongoing | unknown duration | elapsed", file=sys.stderr)
    for mode, stats in summary["modes"].items():
        print(f"{mode} | {stats['runs']} | {stats['passed']} | {stats['failed']} | {stats['ongoing']} | {stats['unknown_duration_runs']} | {stats['cumulative_invocation_seconds']:.2f}s", file=sys.stderr)
    print(f"Total invocation elapsed {totals['cumulative_invocation_seconds']:.2f}s; ongoing observed {totals['ongoing_observed_seconds']:.2f}s; top-level interval union {totals['top_level_wall_interval_union_seconds']:.2f}s; {totals['unknown_duration_runs']} duration(s) unknown.", file=sys.stderr)
    print(f"Ledger: {json_path} and {markdown_path}", file=sys.stderr)


def report() -> int:
    timing, json_path, markdown_path = write_timing_ledger()
    print_timing_summary(timing, json_path, markdown_path)
    path = task_state_path()
    if path is None or not path.is_file():
        raise ValueError("active task state is required for handoff check")
    state = json.loads(path.read_text())
    if any(pending.get("result") == "running" for pending in state.get("broad_runs", {}).values()):
        raise ValueError("handoff blocked: verification has an uncompleted run")
    if any(RECEIPTS.glob("*.pending.json")):
        raise ValueError("handoff blocked: a verification receipt is incomplete")
    records = {}
    for receipt_path in RECEIPTS.glob("*.json"):
        try:
            record = json.loads(receipt_path.read_text())
            mode = record.get("mode")
            if mode in state.get("required_receipts", []):
                records.setdefault(mode, []).append((receipt_path, record))
        except (OSError, ValueError, json.JSONDecodeError):
            continue
    missing = []
    for mode in state.get("required_receipts", []):
        runs = sorted(records.get(mode, []), key=lambda pair: pair[1].get("ended_unix", pair[1].get("started_unix", 0)))
        if not runs:
            missing.append(mode)
            continue
        eligible = [(path, record) for path, record in runs if record.get("result") != "planned" and record.get("full_scope") is True]
        if not eligible:
            missing.append(mode)
            continue
        latest_path, latest = eligible[-1]
        unresolved_same_source_failure = latest.get("result") == "passed" and any(
            old.get("result") == "failed" and old.get("fingerprint") == latest.get("fingerprint")
            for old_path, old in runs if old_path != latest_path
        )
        if unresolved_same_source_failure or not receipt_valid(latest_path.name, state):
            missing.append(mode)
    if missing:
        raise ValueError("missing, failed, or stale required receipts: " + ", ".join(missing))
    print(json.dumps({"task": state["task"], "required_receipts": state["required_receipts"], "result": "ready", "limitation": "Local receipts and task state can be edited by local users."}, sort_keys=True))
    return 0


# ----------------------------------------------------------------------------- entry

def main_for_test(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    gate = sub.add_parser("gate", help="run one verification mode and write its receipt"); gate.add_argument("mode", nargs="?", default="ordinary", choices=MODES)
    sub.add_parser("run-integration", help="internal: the integration case runner")
    sub.add_parser("fixture-check", help="check the PostgreSQL fixture identity")
    sub.add_parser("performance-evidence", help="internal: validate the controlled performance events")
    sub.add_parser("init", help="start a task budget from VERIFICATION_PLAN")
    sub.add_parser("report", help="refresh the timing ledger and check handoff readiness")
    sub.add_parser("handoff", help="count a handoff attempt and check readiness")
    args = parser.parse_args(argv)
    if args.command == "gate":
        return run_gate(args.mode)
    if args.command == "run-integration":
        return run_integration()
    if args.command == "fixture-check":
        print(f"Verified PostgreSQL fixture: {FIXTURE_DATABASE}, user {FIXTURE_USER}, server 18.6, loopback port {FIXTURE_PORT}, started {fixture_check()}")
        return 0
    if args.command == "performance-evidence":
        return performance_evidence()
    if args.command == "report":
        return report()
    return guard(args.command)


def main() -> int:
    try:
        return main_for_test()
    except (OSError, ValueError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(str(exc), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
