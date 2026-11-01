# Testing and verification

Status: engineering guidance requested by the maintainer on 2026-10-24. The
foundation has component, Chromium, Go, operational HTTP and process checks.
The storage foundation includes real PostgreSQL upgrade checks; full product journeys
remain unimplemented. The initial time budgets below are planning targets;
measured evidence is recorded in [delivery status](status.md).

## Fast feedback with credible acceptance

Prove each behavior at the lowest layer that can detect its failure, then cover the
important boundaries. Every product acceptance criterion maps to evidence; not every
criterion needs a browser test. A mocked UI test proves UI behavior against its mock,
not that the API, database, authentication or collector works.

| Layer | What it proves | When it runs |
| --- | --- | --- |
| Go unit/calculation | Arithmetic, validation, state transitions and boundary cases without network/DB | Focused during edits; complete unit suite in integration gate |
| UI unit/component | Formatting, controls, forms, loading/error states and keyboard behavior | Focused during edits; complete suite in gate |
| API/contract | Request/response compatibility, auth decisions, CLI/API parity and generated contract consistency | When relevant contracts/handlers/clients change; full contract check in gate |
| PostgreSQL integration | Real constraints, transactions, concurrent writes, persistence, leases, retention and upgrades | Completed integrated task candidate and PR verification |
| Agent/protocol integration | Actual agent/backend communication, ingestion, reconnects, capabilities and isolation | Completed task/PR verification for agent/API changes |
| Browser smoke | Small set of real authenticated user journeys across UI/API/DB | Completed task candidate and integration gate |
| Browser broad | Wider workflows, permission failures, themes/mobile and supported browsers | Catalogue matrix at task completion/PR verification; full supported matrix before milestone/release |
| Compose end-to-end | Normal container deployment, onboarding, collection, user journeys, restarts and cleanup | Completed integrated task candidate and PR verification |
| Performance/recovery | UI budgets, sustained isolation, process failure, upgrade/restore and deployment | Affected boundary changes and milestone/release, independently reported |

Use Go's standard test facilities. Choose/pin compatible UI/component and browser
runners during F1; the intended division is component tests plus real-browser tests,
not a mandate to introduce a second application runtime. Do not add dependencies or
assert specific tool versions merely because a reference setup uses them.

## Gate selection

Inspect a running real page or a single user journey early. This inspection must
not launch the Compose end-to-end suite, catalogue browser matrix or database/API
integration suite. Those broad suites run once implementation is complete and the
task is integrated for completion/PR verification. A partial worker handoff, small
edit or request for status does not trigger them. Reuse passing completion evidence
for the PR when its relevant inputs match; publishing the same candidate does not
require another run.

If a completion gate fails, preserve the failure, diagnose it and use focused
unit/component checks during correction. Run the affected gate when the corrected
candidate is ready, retaining passing evidence for unaffected gates. This sequencing
does not waive a failed required gate or replace real-stack acceptance with mocks.
Command availability is governed by [command discipline](commands.md); do not
request new approvals or improvise invocation variations to obtain a focused run.

- **Edit loop:** formatting/type checks and focused unit/component tests. Inspect
  the existing running application as needed; reserve the broad suites above for
  completion/PR verification. Never implicitly install dependencies or run every
  browser/project.
- **Task review:** once implementation is complete, verify the boundaries the task changes. API/storage
  changes need real integration evidence; UI flows need the real stack, and agent
  changes need real agent/backend execution. The primary agent records why a check is
  inapplicable. An uncertain dependency scope expands verification, not shrinks it.
- **Integrated candidate:** full Go/UI unit suites, static/contract/generated checks,
  PostgreSQL integration, required race checks and a compact real-stack smoke set.
  Run once against the actual combined revision after its last relevant change.
- **Milestone/release:** full browser coverage, supported browser/theme/viewport checks,
  workload budgets, shutdown/failure/restore and deployment checks from SPEC-0014.
  Shared auth/router/schema/calculation/chart changes may require these earlier.

When CI is introduced, it must run the bounded integrated-candidate gate for
application changes and document validation for documentation-only changes. Deeper suites may run separately/on a schedule,
but required release checks must pass on the release candidate; a stale nightly
result cannot substitute. No red required gate is waived because a focused check passed.

## Protect iteration speed

1. Build each artifact once per source/configuration fingerprint and share it across
   checks for that revision. Avoid repeating whole-stack setup for every test or
   wrapping every focused browser invocation in lint/unit/build/all-browser stages.
2. Locally, allow a healthy isolated test database/service to be reused only after
   checking revision, schema, configuration and test fixture identity. Rebuild/restart
   when those change. CI starts a known stack from the candidate's artifacts. Never
   accidentally test an older preview server or operator data.
3. Start PostgreSQL once per suite/session where safe. Give each parallel worker its
   own isolated database and bounded connection pool; reset deterministically between
   tests. Transaction rollback is suitable only when all exercised work participates
   in it. Background workers and separate processes require explicit isolation/reset.
4. Use API/database fixture setup for tests unrelated to onboarding. Keep genuine
   browser coverage of login, bootstrap and password rotation. Reuse authenticated
   browser state only within isolated fixtures where authentication is not the subject;
   do not disable auth to make an authenticated acceptance test faster.
5. Use deterministic clocks for windows, retry/backoff and lease logic. Protocol tests
   use controllable clocks or short test settings without changing production semantics.
   Keep a few real-process checks. Never wait minutes for a production poll tick when
   the behavior can be exercised deterministically.
6. Wait on observable readiness/conditions with bounded deadlines, not fixed sleeps.
   A timeout must show the missing condition and relevant logs. Do not increase global
   timeouts or retries to hide hangs, slow fixtures or contention.
7. Use one browser for the compact smoke gate. Select mobile/theme/cross-browser cases
   by actual risk, and exercise the full promised support matrix before release.
   Do not multiply every backend edge case across every browser and viewport.
8. Bound concurrency by measured DB/CPU/memory capacity. More workers can slow a suite
   and make it flaky. Tests cannot rely on execution order or mutate shared globals.

## Initial timing targets

Measure warm iteration separately from cold provisioning/downloads; record both.
On documented developer/CI hardware, aim for:

| Check | Initial elapsed-time target |
| --- | --- |
| One focused unit/component case or small related group | 10 seconds |
| Complete Go/UI unit suites | 30 seconds each |
| Fast checks including lint/type/contracts | 60 seconds total |
| Focused database/protocol case | 30 seconds with infrastructure ready |
| Complete database/protocol integration lane | 2 minutes |
| Real-stack browser smoke | 90 seconds, with artifacts/infrastructure ready |
| Integrated-candidate gate | 5 minutes, with dependencies cached |

These are starting budgets, not justification to omit cases. F1 records repeated
runs, fixture/build/startup/test/cleanup timings, hardware, versions and cache state.
Later gates publish total duration, slowest cases, test counts and first-attempt
failures. Investigate an exceeded budget; move genuinely expensive coverage into a
named deeper lane only when its mandatory trigger remains explicit. The primary agent
cannot silently raise targets, skip tests or claim the suite is fast without evidence.

## What the first real-stack smoke set must demonstrate

Add journeys as their feature slice lands; missing future features are recorded as
not implemented, never as passing placeholders:

- Provision the initial admin, enforce restricted first login, rotate the password,
  then sign in and read an authenticated page; an unauthorized API request fails.
- Register a real agent against a deterministic local provider fixture, apply config,
  collect/ingest an observation and show the correct SLO/budget value in the UI.
- Submit a direct-producer observation through the actual API and verify persistence
  and calculation without an agent; exercise relevant scoped denial separately.
- Exercise maintenance/annotation behavior and one real webhook trigger/recovery
  path when those features land. Capture the outbound request in a local receiver.

Use browser interaction only for the user journey under test; setup unrelated records
through supported APIs/fixtures. Do not mock the API or DB in these smoke cases. A
provider fixture is acceptable for deterministic full-stack plumbing; real provider
compatibility fixtures/tests still govern each supported query dialect separately.

## Correctness and failure evidence

Tests added to cover already-correct behavior may pass on their first run. Do not
change correct production behavior merely to manufacture a red result. Label
deliberate mutation checks separately from reproduced defects, and preserve any
interim implementation or fixture mistake accurately in the handoff.

For calculations, use independently derived expected values, boundary/property tests,
zero traffic, missingness, revisions and maintenance unions. Tests that just reproduce
the implementation formula are insufficient. For storage/auth/leases, test concurrent
requests and crash/retry boundaries against real persistence. Run race detection on
changed concurrent Go behavior during task review and the relevant/full Go suite in
the integrated gate; keep ordinary focused edits fast without removing that gate.

For UI, inspect production builds, keyboard/focus, both themes/mobile, exact chart
values and empty/error states. Preserve labelled missing data. Measure the existing
SPEC-0012 performance fixture separately from general browser test duration.

Regression fixes include a check that fails for the defect and passes with the fix.
Tests must exercise the asserted failure, not merely fail because a dependency is
missing. Use reproducible seeds and bounded fuzz/property cases. Longer randomized
runs belong in a separately visible lane.

## Failures and agent evidence

Do not accept a rerun-to-green as if the initial failure did not happen. Preserve
first-attempt diagnostics; diagnostic retries do not erase gate failures. Quarantine
requires a named issue/owner, scope, expiry and explicit maintainer approval. Critical
acceptance coverage needs an alternative passing check before it can be quarantined.
Do not delete assertions, change expected values or weaken fixtures just to pass.

Every task's verification evidence (including any subagent handoff) identifies the
base/candidate revision or working-tree diff,
commands, selected tests/counts, results, timing, fixture/artifact identity and known
gaps. The primary agent inspects the diff, reviews test strength and verifies the
integrated candidate. Attach bounded redacted logs, failure traces/screenshots and
provider/DB diagnostics when relevant; keep generated artifacts out of git.

A successful process that selected zero expected tests is a failure. Required checks
cannot be silently skipped for missing tools, database access or environment variables.
A cached result is reusable only for matching inputs and is reported as cached;
fresh final verification must execute the relevant acceptance checks on the candidate.

## Commands available now

`python3 scripts/check-docs.py` (Python 3.9+) validates inline local Markdown links, numbered specs/ADRs,
statuses, index coverage and acceptance-criterion IDs. It does not validate product
behavior, external URLs, Markdown anchors, security or performance.

The foundation provides these fixed entry points:

| Command | Scope |
| --- | --- |
| `make setup` | Pinned Go/generators, locked web dependencies and Chromium; explicit network-capable setup; optional SETUP_MODE=go/web |
| `make setup SETUP_MODE=database` | Isolated PostgreSQL18.6 fixture bound to loopback; validates existing fixture before reuse |
| `make setup SETUP_MODE=verification` | Network-capable build of the pinned Linux verifier image and PostgreSQL18.6 image; cache fingerprint binds Go, web and OpenAPI lockfiles plus verifier tooling |
| `make build` | Typecheck, production assets and embedded Go executable; size report |
| `make generate` | Operational API generation; GENERATE_MODE=format uses pinned gofmt; GENERATE_MODE=sqlc regenerates storage queries |
| `make verify` | Web and Go suites, vet/format, build, generated drift, routing/exclusion and binary smoke |
| `make verify VERIFY_MODE=linux` | Offline Linux candidate gate: snapshots tracked and safe untracked source changes, attests a SHA256, then runs the full gate, race gate, real PostgreSQL/browser integration suite and catalogue Chromium matrix in order; exports sanitized logs and acceptance screenshots under `.local/linux-verification.*` |
| `make verify VERIFY_MODE=linux-sli` | Offline focused Linux journey: runs only the real SLI browser journey in the same isolated PostgreSQL fixture and exports the same sanitized evidence; does not replace the full Linux gate |
| `make verify VERIFY_MODE=go` | Go checks; optional GO_PACKAGE selects a tested package without whole-binary smoke |
| `make verify VERIFY_MODE=go-portability` | Compile-only `internal/command` test binaries for fixed `darwin/amd64`, with ordinary and integration tags, CGO disabled and the pinned offline Go toolchain; ignored outputs and a distinct build cache live under `.local/` |
| `make verify VERIFY_MODE=go-race` | HTTP/lifecycle, command, agent and setup clients, protected profile, storage, identity, authentication, source-secret, Prometheus datasource and diagnostic unit race checks |
| `make verify VERIFY_MODE=auth-bench` | Fixed password Hash/Verify benchmarks, ten iterations each, one run and a two-minute timeout |
| `make verify VERIFY_MODE=integration ALMANAC_TEST_DATABASE_URL=...` | Explicit isolated PostgreSQL fixture; storage, authentication, authhttp and executable command suites run serially, including real Chromium onboarding and agent CLI acceptance; GO_PACKAGE=all (default) or one of those package paths. Preserves per-package Go JSON events under `.local/integration-test-json/`, the combined event stream at `.local/integration-go-test.jsonl`, the numeric Go test status at `.local/integration-go-test.exit`, and a slowest-test summary. |
| `make verify VERIFY_MODE=smoke` | Existing binary, real HTTP and SIGTERM smoke with bounded waits |
| Focused browser journey invocation in [command discipline](commands.md) | Real HTTPS/PostgreSQL journey with the current built candidate; fails if the selected named journey does not execute and pass |
| `make verify VERIFY_MODE=unit` | Component suite only; optional `TEST_FILE=tests/ui.test.tsx` |
| `make verify VERIFY_MODE=browser` | Catalogue Chromium matrix; no build or install |
| `make preview` | Local catalogue on port 5198 |
| `make verify VERIFY_MODE=compose` | Isolated Compose build/start, initializer checks, actual browser onboarding, repeated bootstrap and restart persistence; owns and removes only its temporary project volumes |

The browser matrix owns port 5200 and refuses an existing server. Keep the
maintainer's preview on port 5198 running during checks; stopping it breaks lazy
imports in already-open tabs. The production smoke uses port 5199. Checks require completed
setup and never install dependencies. Unknown modes and empty test selections fail.
The Linux verifier setup is separate from offline verification: verification rejects
a missing or stale image fingerprint and never pulls images or installs dependencies.
It uses a local Docker context, a read-only source snapshot, and a private network
namespace shared only with its invocation-owned PostgreSQL fixture. The database check
queries the live server and requires the fixed database, role, PostgreSQL 18.6,
127.0.0.1 address and port 15487 before integration tests run. It does not mount the
Docker socket, publish a host port, reuse the host fixture, or mount operator data.
The existing Compose journey remains a separate host-side check.
The fixed SLI journey selector is `BROWSER_SCENARIO=slis`; it requires the integrated
candidate containing `TestIntegrationExecutableBrowserSLICollectionJourney`. The
Linux lane runs that test as part of the full database integration suite.
Go tests exercise real loopback upstreams and embedded assets.
Go and race checks build missing embedded assets before
test discovery; focused config/storage unit checks do not need a web build. Existing
assets are reused for a fast Go-only loop. After web source changes, use `make build`
or the full `make verify` gate to refresh them. Integration checks build the binary
and its assets after the selected integration packages' test discovery.
The process smoke owns an ephemeral port. Generation/build/tests use local pinned tools and offline
Go module settings. PostgreSQL checks run separately from the ordinary unit loop.
They require the matching local fixture, use a fresh disposable database per test,
and fail for unavailable infrastructure or an empty integration-test selection.
The integration gate runs storage first and command/browser acceptance last, and stops at the first
failed package. It records Go JSON events in `.local/integration-go-test.jsonl`,
per-package files under `.local/integration-test-json/`, and the numeric Go exit in
`.local/integration-go-test.exit`; `running` means completion has not been recorded.
The storage and command packages run each discovered top-level integration test
serially with the existing two-minute Go timeout per invocation. Shared invocations
hit that bound as cumulative test time grew. The gate checks that discovery is valid
and that each selected test passed exactly once; individual results and failures
remain in the combined and per-test artifacts.
The terminal summary reports package results and slow tests. A successful full gate
requires every selected package; partial artifacts are not a passing gate.
Name integration-tagged tests with the `TestIntegration` prefix: that is the lane's
execution selector. Pure unit cases belong in ordinary test files so the fast lane
runs them. Discovery alone does not prove a test was executed.
The Go portability mode only compiles test binaries; it does not execute Darwin
tests or prove runtime behavior on macOS. Its target platform, architecture,
CGO setting, cache, and output paths are fixed in the Makefile.
The authentication service and real TLS authentication handlers are covered here,
including bounded upload rejection and cookie/security boundaries. Executable backend
lifecycle, noninteractive setup and browser authentication run through actual TLS
and PostgreSQL. The command integration suite launches a disposable combined runtime
for initial setup, lost-login and lost-password-response journeys. It requires the
installed Chromium from `make setup`, uses ephemeral ECDSA certificates and scoped
browser certificate trust, and never disables application authentication. Lost
responses follow actual committed backend operations, with subsequent deliberate
sign-in and database assertions. An unreachable session created by a lost rotation
response retains normal expiry; logout revokes the browser's own session only.
Traces/video and secret-bearing error output are disabled for these flows. Sanitized
stage diagnostics and screenshots of empty or masked fields live under ignored
`.local/browser-auth-*` directories. Unit checks do not start this stack. Ingestion
and the remaining product journeys are still future work.

For browser iteration, select a file and a single viewport/theme, for example
`make verify VERIFY_MODE=browser TEST_FILE=browser/controls.spec.ts BROWSER_PROJECT=mobile-dark`.
Both selectors are validated; absent files and unknown projects fail. Omit them
for the complete matrix. This focused controls check currently runs three tests
in about 2.5 seconds with dependencies ready; it does not replace the full
matrix before shared-control acceptance.
