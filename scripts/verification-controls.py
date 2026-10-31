#!/usr/bin/env python3
"""Fixed Almanac verification selectors, receipts, and local task budgets."""
from __future__ import annotations

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent


def receipt_directory(root: Path, receipts_override: str | None = None, state_override: str | None = None) -> Path:
    if receipts_override:
        return Path(receipts_override).expanduser().resolve()
    default_state_link = root / ".local/verification/task.json"
    shared_state = Path(state_override).expanduser().resolve() if state_override else (default_state_link.resolve() if default_state_link.exists() or default_state_link.is_symlink() else None)
    return (shared_state.parent / "receipts" if shared_state else root / ".local/verification").resolve()


RECEIPTS = receipt_directory(ROOT, os.environ.get("ALMANAC_VERIFICATION_RECEIPTS"), os.environ.get("ALMANAC_VERIFICATION_TASK_STATE"))
GO_PRODUCTION = ("cmd", "internal", "go.mod", "go.sum", "api")
WEB_PRODUCTION = ("web/src", "web/scripts", "web/package.json", "web/package-lock.json", "web/tsconfig.json", "web/vite.config.ts")
CONTROL_INPUTS = ("Makefile", "scripts/verification-controls.py")
MODE_PATHS = {
    "slo-unit": GO_PRODUCTION + ("web/src", "web/tests", "web/package.json", "web/package-lock.json", "web/tsconfig.json", "web/vite.config.ts") + CONTROL_INPUTS,
    "slo-integration": GO_PRODUCTION + ("internal/storage", "sqlc.yaml") + CONTROL_INPUTS,
    "slo-journey": GO_PRODUCTION + WEB_PRODUCTION + ("internal/command/objectives_journey_integration_test.go",) + CONTROL_INPUTS,
    "agent-lifecycle": GO_PRODUCTION + ("internal/command", "internal/agentmanagement", "internal/agenthttp", "internal/authentication", "internal/authhttp", "internal/backendhttp", "internal/sli", "internal/storage") + CONTROL_INPUTS,
    "verification-tools": ("Makefile", "scripts/verification-controls.py", "scripts/tests/test_verification_controls.py"),
}
MODES = {"slo-unit": ("web", MODE_PATHS["slo-unit"]), "slo-integration": ("db", MODE_PATHS["slo-integration"]), "slo-journey": ("journey", MODE_PATHS["slo-journey"]), "agent-lifecycle": ("db", MODE_PATHS["agent-lifecycle"]), "verification-tools": ("tools", MODE_PATHS["verification-tools"])}
WEB_TESTS = ("tests/slos-client.test.tsx", "tests/slos-page.test.tsx", "tests/slo-operational.test.tsx", "tests/charts.test.tsx", "tests/controls.test.tsx", "tests/slis-client.test.tsx", "tests/slis-page.test.tsx")
GO_PACKAGES = ("./internal/slo", "./internal/sloclient", "./internal/slohttp", "./internal/sli", "./internal/datasourceclient", "./internal/storage", "./internal/command")
DB_TESTS = {
    "slo-integration": ("TestIntegrationOperationalOverviewGroupsAndIndependentBurnWindows", "TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict"),
    "slo-journey": ("TestIntegrationExecutableFirstObjectiveJourney",),
    "agent-lifecycle": ("TestIntegrationExecutableAgentManagementRouteAndSessionLifecycle",),
}
BROAD_MODES = {"ordinary", "linux", "linux-sli", "go", "go-portability", "go-race", "auth-bench", "integration", "journey", "compose", "providers", "smoke", "unit", "browser"}
BROAD_INPUTS = {
    "ordinary": GO_PRODUCTION + WEB_PRODUCTION + ("web/tests", "sqlc.yaml", "scripts", "Makefile"),
    "linux": GO_PRODUCTION + WEB_PRODUCTION + ("web/tests", "sqlc.yaml", "scripts", "Makefile"),
    "linux-sli": GO_PRODUCTION + WEB_PRODUCTION + ("web/tests", "sqlc.yaml", "scripts", "Makefile"),
    "go": GO_PRODUCTION + ("scripts", "Makefile"),
    "go-portability": GO_PRODUCTION + ("web/src", "scripts", "Makefile"),
    "go-race": GO_PRODUCTION + ("scripts", "Makefile"),
    "auth-bench": ("internal/identity", "go.mod", "go.sum", "Makefile", "scripts"),
    "integration": GO_PRODUCTION + ("sqlc.yaml", "scripts", "Makefile"),
    "journey": GO_PRODUCTION + WEB_PRODUCTION + ("web/tests", "sqlc.yaml", "scripts", "Makefile"),
    "compose": GO_PRODUCTION + ("compose.yaml", "Dockerfile", ".dockerignore", "deploy") + WEB_PRODUCTION + ("web/tests", "web/browser", "scripts", "Makefile"),
    "unit": ("web/src", "web/tests", "web/package.json", "web/package-lock.json", "web/vitest.config.ts", "Makefile", "scripts/verification-controls.py"),
    "browser": WEB_PRODUCTION + ("web/tests", "web/browser", "web/playwright.config.ts", "Makefile", "scripts/verification-controls.py"),
    "providers": ("web/src", "web/scripts", "web/package.json", "web/package-lock.json", "Makefile", "scripts/verification-controls.py"),
    "smoke": GO_PRODUCTION + ("web/dist", "Makefile", "scripts"),
}


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


def test_inputs_fingerprint(mode: str) -> str:
    """Fingerprint mode inputs, retaining only that mode's executable fixtures."""
    paths = MODE_PATHS[mode]
    files = []
    for rel in paths:
        p = ROOT / rel
        files.extend(sorted(p.rglob("*")) if p.is_dir() else [p])
    selected = []
    for item in files:
        if not item.is_file() or any(part in {".git", "node_modules", "__pycache__", "dist"} for part in item.parts) or item.suffix == ".pyc":
            continue
        if item.name.endswith("_test.go"):
            integration_only = "_integration_test.go" in item.name or "//go:build integration" in item.read_text(errors="replace")[:512]
            if mode == "slo-unit" and (item.parent.name not in {"slo", "sloclient", "slohttp", "sli", "datasourceclient", "storage", "command"} or integration_only):
                continue
            if mode == "slo-journey" and item.parent.name != "command":
                continue
            if mode == "slo-integration" and item.parent.name != "storage":
                continue
            if mode == "agent-lifecycle" and item.parent.name not in {"command", "agentmanagement", "agenthttp", "authentication", "authhttp", "backendhttp"}:
                continue
        selected.append(item)
    h = hashlib.sha256()
    for item in sorted(set(selected)):
        h.update(item.relative_to(ROOT).as_posix().encode() + b"\0")
        h.update(item.read_bytes() + b"\0")
    return h.hexdigest()


def broad_fingerprint(gate: str) -> str:
    return fingerprint_for_root(ROOT, BROAD_INPUTS[gate], include_dist=gate == "smoke")


def effective_scope(gate: str) -> tuple[dict, bool]:
    if gate == "browser":
        selectors = {"test_file": os.environ.get("TEST_FILE", ""), "project": os.environ.get("BROWSER_PROJECT", "")}
        return selectors, not any(selectors.values())
    if gate == "go":
        package = os.environ.get("GO_PACKAGE", "")
        return {"go_package": package or "./..."}, not package or package == "./..."
    if gate == "integration":
        package = os.environ.get("GO_PACKAGE", "")
        return {"go_package": package or "all"}, not package or package == "all"
    if gate == "journey":
        scenario = os.environ.get("BROWSER_SCENARIO", "onboarding")
        return {"browser_scenario": scenario}, True
    return {"selection": "fixed-full-gate"}, True


def receipt_is_full_scope(mode: str, selectors: dict) -> bool:
    if mode == "browser":
        return not selectors.get("test_file") and not selectors.get("project")
    if mode == "go":
        return selectors.get("go_package") in {"", "./..."}
    if mode == "integration":
        return selectors.get("go_package") in {"", "all"}
    if mode == "journey":
        return selectors.get("browser_scenario") in {"onboarding", "lost-login", "lost-password", "agents", "agents-close", "agents-lost", "agents-unavailable", "agents-lifecycle", "slis"}
    return selectors == {"selection": "fixed-full-gate"}


def run_broad(gate: str) -> int:
    commands = {
        "ordinary": [["make", "_verify_all"]],
        "linux": [["sh", "scripts/verify-linux.sh", "verify"]],
        "linux-sli": [["sh", "scripts/verify-linux.sh", "sli"]],
        "go": [["make", "_verify_go"]],
        "go-portability": [["make", "_verify_go_portability"]],
        "go-race": [["make", "_verify_go_race"]],
        "auth-bench": [["make", "_verify_auth_bench"]],
        "integration": [["make", "_verify_integration"]],
        "journey": [["make", "_verify_journey"]],
        "compose": [["python3", "scripts/local-agent-tty-test.py"], ["node", "scripts/check-local-agent.mjs"], ["node", "scripts/check-local-compose.mjs"], ["node", "scripts/check-local-compose-cancellation.mjs"]],
        "providers": [["node", "scripts/check-local-providers.mjs"]],
        "smoke": [["make", "_verify_smoke"]],
        "unit": [["./node_modules/.bin/vitest", "run", "--config", "vitest.config.ts"]],
    }
    if gate == "browser":
        command = ["./node_modules/.bin/playwright", "test", "--config", "playwright.config.ts"]
        test_file = os.environ.get("TEST_FILE", "")
        if test_file:
            if not test_file.startswith("browser/") or not test_file.endswith(".spec.ts") or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-" for c in test_file) or ".." in test_file or not (ROOT / "web" / test_file).is_file():
                raise ValueError("TEST_FILE must name an existing web/browser/*.spec.ts file")
            command.append(test_file)
        project = os.environ.get("BROWSER_PROJECT", "")
        if project:
            if project not in {"desktop-light", "desktop-dark", "intermediate-light", "intermediate-dark", "mobile-light", "mobile-dark"}:
                raise ValueError("Unknown BROWSER_PROJECT")
            command.extend(["--project", project])
        commands["browser"] = [command]
    token_proc = subprocess.run([sys.executable, str(Path(__file__)), "broad-start", gate], cwd=ROOT, text=True, capture_output=True)
    if token_proc.returncode:
        sys.stderr.write(token_proc.stderr)
        return token_proc.returncode
    run_id = token_proc.stdout.strip()
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
            for command in commands[gate]:
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
        completed = subprocess.run([sys.executable, str(Path(__file__)), "broad-complete", run_id, str(status)], cwd=ROOT, check=False)
        if status == 0 and completed.returncode:
            status = completed.returncode
    return status


def counted_go_events(path: Path, expected: set[str]) -> int:
    run, passed, failed, skipped = set(), set(), set(), set()
    for line in path.read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        name, action = event.get("Test"), event.get("Action")
        if name in expected:
            {"run": run, "pass": passed, "fail": failed, "skip": skipped}.get(action, set()).add(name)
    if run != expected or passed != expected or failed or skipped:
        raise ValueError(f"Go selected tests did not all run and pass (expected {len(expected)}, ran {len(run)}, passed {len(passed)}, failed {len(failed)}, skipped {len(skipped)})")
    return len(expected)


def go_run_count(path: Path, expected: set[str]) -> int:
    found = set()
    for line in path.read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Action") == "run" and event.get("Test") in expected:
            found.add(event["Test"])
    return len(found)


def db_test_pattern(mode: str) -> str:
    """Build an anchored Go regexp solely from the fixed catalog for a mode."""
    names = DB_TESTS[mode]
    return "^(" + "|".join(re.escape(name) for name in names) + ")$"


def ensure_web_assets(artifact: Path, env: dict[str, str]) -> int:
    result = subprocess.run(["make", "_ensure_web_assets"], cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
    output = result.stdout or b""
    with artifact.open("ab") as log:
        log.write(output)
    if output:
        sys.stdout.write(output.decode(errors="replace"))
        sys.stdout.flush()
    return result.returncode


def count_vitest(report: dict) -> int:
    results = report.get("testResults", [])
    expected = {f"web/{item}" for item in WEB_TESTS}
    normalized = {Path(result.get("name", "")).as_posix().replace("\\", "/") for result in results}
    if not all(any(path.endswith(item) for path in normalized) for item in expected):
        raise ValueError("Vitest did not report every fixed SLO test file")
    count = 0
    for result in results:
        path = Path(result.get("name", "")).as_posix()
        if not any(path.endswith(item) for item in WEB_TESTS):
            continue
        assertions = result.get("assertionResults", [])
        if not assertions or result.get("status") != "passed" or any(a.get("status") != "passed" for a in assertions):
            raise ValueError(f"Vitest file did not run all cases successfully: {path}")
        count += len(assertions)
    return count


def vitest_reported_count(report: dict) -> int:
    results = report.get("testResults", [])
    return sum(len(result.get("assertionResults", [])) for result in results if any(Path(result.get("name", "")).as_posix().replace("\\", "/").endswith(item) for item in WEB_TESTS))


def execute(mode: str) -> int:
    if mode not in MODES:
        raise ValueError("unknown fixed verification mode")
    kind, sources = MODES[mode]
    started = time.time()
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime(started))
    run_id = f"{stamp}-{uuid.uuid4().hex}"
    RECEIPTS.mkdir(parents=True, exist_ok=True)
    artifact = RECEIPTS / f"{run_id}.log"
    receipt = RECEIPTS / f"{run_id}.json"
    before = test_inputs_fingerprint(mode)
    selected = 0
    executed = 0
    artifact_paths = [artifact]
    status = 1
    RECEIPTS.mkdir(parents=True, exist_ok=True)
    pending_path = RECEIPTS / f"{run_id}.pending.json"
    atomic_json(pending_path, {"id": run_id, "mode": mode, "origin": str(ROOT), "fingerprint": before, "started_unix": started, "result": "running", "artifacts": [artifact.name]})
    try:
        if kind == "tools":
            command = [sys.executable, "-m", "unittest", "discover", "-s", "scripts/tests", "-p", "test_verification_controls.py", "-v"]
            with artifact.open("wb") as log:
                proc = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, check=False)
            status = proc.returncode
            output_text = artifact.read_text(errors="replace")
            marker = re.search(r"^Ran ([0-9]+) tests?\b", output_text, re.MULTILINE)
            try:
                selected = int(marker.group(1)) if marker else 0
            except ValueError:
                selected = 0
                status = status or 1
            if selected == 0:
                status = status or 1
            executed = selected if status == 0 else 0
        if kind == "web":
            missing = [f for f in WEB_TESTS if not (ROOT / "web" / f).is_file()]
            if missing:
                raise ValueError("fixed Vitest files missing: " + ", ".join(missing))
        if kind == "web":
            report_path = RECEIPTS / f"{run_id}-vitest.json"
            commands = [["npm", "run", "typecheck"], ["./node_modules/.bin/vitest", "run", "--config", "vitest.config.ts", "--reporter=json", f"--outputFile={report_path}", *WEB_TESTS]]
            with artifact.open("wb") as log:
                for command in commands:
                    proc = subprocess.run(command, cwd=ROOT / "web", stdout=log, stderr=subprocess.STDOUT, check=False)
                    if proc.returncode:
                        status = proc.returncode
                        break
                else:
                    try:
                        report_data = json.loads(report_path.read_text())
                        selected = count_vitest(report_data)
                        executed += selected
                        artifact_paths.append(report_path)
                        status = 0
                    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as exc:
                        with artifact.open("ab") as log:
                            log.write((f"unable to confirm Vitest selected test count: {exc}\n").encode())
                        status = 1
            if report_path.is_file():
                try:
                    selected = max(selected, vitest_reported_count(json.loads(report_path.read_text())))
                except (OSError, ValueError, TypeError, json.JSONDecodeError):
                    pass
            if status == 0:
                go = str(ROOT / ".local/bin/go")
                env = os.environ.copy()
                env.update(GOTOOLCHAIN="local", GOPROXY="off", GOMODCACHE=str(ROOT / ".local/go/mod"), GOCACHE=str(ROOT / ".local/go/build"))
                status = ensure_web_assets(artifact, env)
                if status:
                    with artifact.open("ab") as log:
                        log.write(f"web asset preflight failed with exit {status}\n".encode())
                for index, package in enumerate(GO_PACKAGES if status == 0 else ()):
                    listing = subprocess.run([go, "test", "-list", ".", package], cwd=ROOT, env=env, text=True, capture_output=True)
                    expected = {line.strip() for line in listing.stdout.splitlines() if line.strip().startswith("Test")}
                    if listing.returncode or not expected:
                        with artifact.open("ab") as log:
                            log.write((listing.stdout + listing.stderr + f"\nexpected tests in {package}\n").encode())
                        status = listing.returncode or 1
                        break
                    selected += len(expected)
                    result_path = RECEIPTS / f"{run_id}-go-{index}.jsonl"
                    artifact_paths.append(result_path)
                    result = subprocess.run([go, "test", "-json", package], cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
                    result_path.write_bytes(result.stdout)
                    executed += go_run_count(result_path, expected)
                    with artifact.open("ab") as log:
                        log.write(result.stdout)
                    if result.returncode:
                        status = result.returncode
                        break
                    try:
                        counted_go_events(result_path, expected)
                    except ValueError as exc:
                        with artifact.open("ab") as log:
                            log.write((str(exc) + "\n").encode())
                        status = 1
                        break
        elif kind != "tools":
            if not os.environ.get("ALMANAC_TEST_DATABASE_URL"):
                raise ValueError("ALMANAC_TEST_DATABASE_URL must select the isolated local fixture")
            if not (ROOT / ".local/bin/go").is_file() or not os.access(ROOT / ".local/bin/go", os.X_OK):
                raise ValueError("pinned Go is missing; use make setup SETUP_MODE=go")
            test_names = DB_TESTS[mode]
            expected = set(test_names)
            test_pattern = db_test_pattern(mode)
            package = "./internal/storage" if mode == "slo-integration" else "./internal/command"
            env = os.environ.copy()
            env.update(GOTOOLCHAIN="local", GOPROXY="off", GOMODCACHE=str(ROOT / ".local/go/mod"), GOCACHE=str(ROOT / ".local/go/build"))
            discovery = subprocess.run([str(ROOT / ".local/bin/go"), "test", "-tags=integration", "-list", test_pattern, package], cwd=ROOT, env=env, text=True, capture_output=True)
            found = {line.strip() for line in discovery.stdout.splitlines() if line.strip().startswith("Test")}
            if discovery.returncode or found != expected:
                artifact.write_text(discovery.stdout + discovery.stderr)
                missing, extra = sorted(expected - found), sorted(found - expected)
                raise ValueError(f"expected exactly fixed tests {sorted(expected)}; discovery failed, missing={missing}, extra={extra}")
            selected = len(expected)
            preflight = subprocess.run(["make", "_verify_sqlc_generated"], cwd=ROOT, env=env, capture_output=True)
            if preflight.returncode:
                with artifact.open("wb") as log:
                    log.write(preflight.stdout + preflight.stderr)
                status = preflight.returncode
                raise RuntimeError("generated SQL check failed")
            fixture = subprocess.run(["sh", "scripts/setup-database.sh", "--check"], cwd=ROOT, env=env, capture_output=True)
            if fixture.returncode:
                with artifact.open("wb") as log:
                    log.write(fixture.stdout + fixture.stderr)
                status = fixture.returncode
                raise RuntimeError("database fixture preflight failed")
            build = subprocess.run(["make", "build"], cwd=ROOT, env=env, capture_output=True)
            if build.returncode:
                with artifact.open("wb") as log:
                    log.write(build.stdout + build.stderr)
                status = build.returncode
                raise RuntimeError("candidate build failed")
            with artifact.open("wb") as log:
                proc = subprocess.run([str(ROOT / ".local/bin/go"), "test", "-json", "-race", "-tags=integration", "-p=1", "-run", test_pattern, "-count=1", "-timeout=2m", package], cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
                output = proc.stdout
                dsn = env.get("ALMANAC_TEST_DATABASE_URL", "").encode()
                if dsn:
                    output = output.replace(dsn, b"<redacted database URL>")
                log.write(output)
            result_json = RECEIPTS / f"{run_id}-go.jsonl"
            result_json.write_bytes(output)
            artifact_paths.append(result_json)
            status = proc.returncode
            executed = go_run_count(result_json, expected)
            if status == 0:
                selected = counted_go_events(result_json, expected)
    except (OSError, ValueError, RuntimeError) as exc:
        with artifact.open("ab") as log:
            log.write((str(exc) + "\n").encode())
        if status in (0, 1):
            status = 2
    ended = time.time()
    after = test_inputs_fingerprint(mode)
    if after != before:
        status = status or 1
        with artifact.open("ab") as log:
            log.write(b"relevant source changed during verification; result is not accepted\n")
    artifacts = [p.name for p in artifact_paths if p.is_file()]
    selected_items = list(WEB_TESTS) + list(GO_PACKAGES) if kind == "web" else (["scripts/tests/test_verification_controls.py"] if kind == "tools" else list(DB_TESTS[mode]))
    record = {"id": run_id, "mode": mode, "origin": str(ROOT), "fingerprint": before, "fingerprint_at_end": after, "started_unix": started, "ended_unix": ended, "elapsed_seconds": ended-started, "exit_code": status, "result": "passed" if status == 0 else "failed", "selected_count": selected, "executed_count": executed, "selected_count_kind": "test-cases", "selected": selected_items, "artifacts": artifacts, "receipt": receipt.name, "limitations": "Local filesystem evidence; not tamper-proof."}
    # Immutable receipt creation; UUID collision is practically impossible and
    # O_EXCL makes the immutability guarantee explicit.
    atomic_json(receipt, record, immutable=True)
    pending_path.unlink()
    refresh_timing_ledger_safely()
    print(json.dumps(record, sort_keys=True))
    return status


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


def guard(action: str, args: argparse.Namespace | None = None) -> int:
    path = task_state_path()
    if action == "init" and path is None:
        path = (ROOT / ".local/verification/task.json").resolve()
    if path is None and action not in {"broad-start", "broad-complete"}:
        if action == "handoff":
            raise ValueError("handoff requires an active verification task")
        return 0
    if action == "broad-complete":
        if not args.run_id or any(c not in "0123456789abcdef-" for c in args.run_id):
            raise ValueError("invalid broad run id")
        pending_file = RECEIPTS / f"{args.run_id}.pending.json"
        pending = json.loads(pending_file.read_text())
        end_fingerprint = broad_fingerprint(pending["mode"])
        status = args.exit_code
        if pending["fingerprint"] != end_fingerprint and status == 0:
            status = 1
        ended = time.time()
        pending.update({"ended_unix": ended, "elapsed_seconds": ended - pending["started_unix"], "fingerprint_at_end": end_fingerprint, "exit_code": status, "result": "passed" if status == 0 else "failed"})
        atomic_json(RECEIPTS / f"{args.run_id}.json", pending, immutable=True)
        pending_file.unlink()
        refresh_timing_ledger_safely()
        if path is not None:
            with locked_state(path) as state:
                if state is not None and args.run_id in state.get("broad_runs", {}):
                    state["broad_runs"].pop(args.run_id)
                    write_state(path, state)
        return status
    if action == "broad-start":
        gate = args.gate
        if gate not in BROAD_MODES:
            raise ValueError("unknown fixed broad verification gate")
        if path is not None:
            with locked_state(path) as state:
                if state is None:
                    raise ValueError("active task state path is configured but state file is missing")
                entry = state["broad_gates"].get(gate)
                if entry is None:
                    raise ValueError(f"gate {gate!r} is not in this task's plan")
                budget = entry["allowance"] if isinstance(entry, dict) else int(entry)
                count = state["broad_attempts"].get(gate, 0)
                if count >= budget:
                    reason = os.environ.get("VERIFY_RERUN_REASON", "").strip()
                    boundary = os.environ.get("VERIFY_RERUN_BOUNDARY", "")
                    receipt = os.environ.get("VERIFY_FOCUSED_RECEIPT", "")
                    allowed = entry.get("boundaries", []) if isinstance(entry, dict) else []
                    if not reason or boundary not in allowed or not receipt or not receipt_valid(receipt, state, expected_mode=boundary):
                        raise ValueError("broad rerun denied: a recorded reason, allowed boundary, and matching fresh receipt are required")
                state["broad_attempts"][gate] = count + 1
                state.setdefault("broad_reasons", []).append({"gate": gate, "reason": os.environ.get("VERIFY_RERUN_REASON", ""), "boundary": os.environ.get("VERIFY_RERUN_BOUNDARY", ""), "receipt": os.environ.get("VERIFY_FOCUSED_RECEIPT", "")})
                run_id = f"{int(time.time())}-{uuid.uuid4().hex}"
                state.setdefault("broad_runs", {})[run_id] = {"mode": gate, "result": "running"}
                write_state(path, state)
        else:
            run_id = f"{int(time.time())}-{uuid.uuid4().hex}"
        selectors, full_scope = effective_scope(gate)
        record = {"id": run_id, "mode": gate, "origin": str(ROOT), "fingerprint": broad_fingerprint(gate), "started_unix": time.time(), "exit_code": None, "result": "running", "selectors": selectors, "full_scope": full_scope, "artifacts": [f"{run_id}.log"]}
        RECEIPTS.mkdir(parents=True, exist_ok=True)
        atomic_json(RECEIPTS / f"{run_id}.pending.json", record)
        print(run_id)
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
        elif action == "handoff":
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
    if not isinstance(required, list) or any(not isinstance(mode, str) or (mode not in MODES and mode not in BROAD_MODES) for mode in required):
        raise ValueError("required_receipts contains an unknown fixed verification mode")
    gates = plan.get("broad_gates", {})
    if not isinstance(gates, dict):
        raise ValueError("broad_gates must be an object")
    for gate, config in gates.items():
        if gate not in BROAD_MODES or not isinstance(config, dict) or set(config) - {"allowance", "boundaries"}:
            raise ValueError(f"invalid broad gate plan for {gate!r}")
        allowance = config.get("allowance")
        if not isinstance(allowance, int) or isinstance(allowance, bool) or not 0 <= allowance <= 2:
            raise ValueError(f"{gate} allowance must be an integer from 0 through 2")
        boundaries = config.get("boundaries", [])
        if not isinstance(boundaries, list) or any(not isinstance(mode, str) or mode not in MODES or mode in BROAD_MODES for mode in boundaries):
            raise ValueError(f"{gate} boundaries must list fixed focused modes")


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
        if mode not in MODES and mode not in BROAD_MODES:
            return False
        if mode not in state.get("required_receipts", [mode]):
            return False
        if expected_mode is not None and mode != expected_mode:
            return False
        if mode in BROAD_MODES and (record.get("full_scope") is not True or not receipt_is_full_scope(mode, record.get("selectors", {}))):
            return False
        artifacts = []
        for item in record["artifacts"]:
            if Path(item).name != item:
                return False
            artifact = (RECEIPTS / item).resolve()
            if artifact.parent != RECEIPTS:
                return False
            artifacts.append(artifact)
        current_fingerprint = test_inputs_fingerprint(mode) if mode in MODES else broad_fingerprint(mode)
        return all(item.is_file() for item in artifacts) and current_fingerprint == record["fingerprint"] and record.get("fingerprint_at_end") == record["fingerprint"]
    except (OSError, ValueError, KeyError, TypeError):
        return False


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
                observed = None
        else:
            observed = None
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
            "observed_ongoing_seconds": observed if result == "running" else None,
            "selected_count": record.get("selected_count"), "executed_count": record.get("executed_count"),
            "selected": record.get("selected"), "selectors": record.get("selectors"),
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
        "timing_note": "Recorded elapsed is top-level verification invocation wall time, including preflight, startup/build, and cleanup. Nested commands are included in their parent invocation and are not added separately. Wall interval union counts overlapping top-level runs once; it is not test-only CPU time. Checks without receipts cannot be backfilled, and missing durations are unknown.",
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
        "Recorded elapsed is top-level invocation wall time, including preflight/startup/build/cleanup. Nested commands count only in their parent. The wall interval union counts overlap once; it is not test-only CPU time. Checks without receipts and missing durations remain unknown.", "",
        f"Totals: {summary['totals']['runs']} runs; {summary['totals']['passed']} passed; {summary['totals']['failed']} failed; {summary['totals']['ongoing']} ongoing; {summary['totals']['unknown_duration_runs']} unknown duration.",
        f"Cumulative invocation elapsed: {summary['totals']['cumulative_invocation_seconds']:.2f}s; ongoing observed: {summary['totals']['ongoing_observed_seconds']:.2f}s; top-level wall interval union: {summary['totals']['top_level_wall_interval_union_seconds']:.2f}s.", "",
        "| Mode | Runs | Passed | Failed | Ongoing | Unknown duration | Invocation elapsed |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for mode, stats in summary["modes"].items():
        lines.append(f"| {mode} | {stats['runs']} | {stats['passed']} | {stats['failed']} | {stats['ongoing']} | {stats['unknown_duration_runs']} | {stats['cumulative_invocation_seconds']:.2f}s |")
    lines.extend(["", "## Runs (chronological)", "", "| Started UTC | Mode | Result | Duration | Cases selected/run | Command or selection | ID |", "| --- | --- | --- | ---: | ---: | --- | --- |"])
    for run in summary["ledger"]:
        start = datetime.fromtimestamp(run["started_unix"], timezone.utc).isoformat() if isinstance(run["started_unix"], (int, float)) else "unknown"
        duration = f"{run['elapsed_seconds']:.2f}s" if run["elapsed_seconds"] is not None else (f"ongoing ({run['observed_ongoing_seconds']:.2f}s observed)" if run["observed_ongoing_seconds"] is not None else "unknown")
        counts = f"{run['selected_count'] if run['selected_count'] is not None else '?'} / {run['executed_count'] if run['executed_count'] is not None else '?'}"
        selection = run.get("command") or (", ".join(run.get("selected") or []) or run["mode"])
        selection = str(selection).replace("|", "\\|").replace("\n", " ")
        lines.append(f"| {start} | {run['mode']} | {run['result']} | {duration} | {counts} | {selection} | `{run['id']}` |")
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
        latest_path, latest = runs[-1]
        unresolved_same_source_failure = latest.get("result") == "passed" and any(
            old.get("result") == "failed" and old.get("fingerprint") == latest.get("fingerprint")
            for _, old in runs[:-1]
        )
        if unresolved_same_source_failure or not receipt_valid(latest_path.name, state):
            missing.append(mode)
    if missing:
        raise ValueError("missing, failed, or stale required receipts: " + ", ".join(missing))
    print(json.dumps({"task": state["task"], "required_receipts": state["required_receipts"], "result": "ready", "limitation": "Local receipts and task state can be edited by local users."}, sort_keys=True))
    return 0


def main_for_test(argv=None) -> int:
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run"); run.add_argument("mode", choices=MODES)
    sub.add_parser("init")
    start = sub.add_parser("broad-start"); start.add_argument("gate", choices=sorted(BROAD_MODES))
    complete = sub.add_parser("broad-complete"); complete.add_argument("run_id"); complete.add_argument("exit_code", type=int)
    gate = sub.add_parser("run-gate"); gate.add_argument("gate", choices=sorted(BROAD_MODES))
    sub.add_parser("handoff")
    sub.add_parser("report")
    args = p.parse_args(argv)
    if args.command == "run": return execute(args.mode)
    if args.command == "run-gate": return run_broad(args.gate)
    if args.command == "report": return report()
    return guard(args.command, args)


def main() -> int:
    try:
        return main_for_test()
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(str(exc), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
