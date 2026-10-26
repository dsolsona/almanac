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
| PostgreSQL integration | Real constraints, transactions, concurrent writes, persistence, leases, retention and upgrades | Focused affected cases during edits; full suite in gate |
| Agent/protocol integration | Actual agent/backend communication, ingestion, reconnects, capabilities and isolation | On agent/API changes; critical path in gate |
| Browser smoke | Small set of real authenticated user journeys across UI/API/DB | UI or integration changes and integration gate |
| Browser broad | Wider workflows, permission failures, themes/mobile and supported browsers | Affected flows before acceptance; full suite before milestone/release |
| Performance/recovery | UI budgets, sustained isolation, process failure, upgrade/restore and deployment | Affected boundary changes and milestone/release, independently reported |

Use Go's standard test facilities. Choose/pin compatible UI/component and browser
runners during F1; the intended division is component tests plus real-browser tests,
not a mandate to introduce a second application runtime. Do not add dependencies or
assert specific tool versions merely because a reference setup uses them.

## Gate selection

- **Edit loop:** formatting/type checks and focused affected tests. It must support
  selecting one Go package/test, UI test or browser journey. It never implicitly
  installs dependencies or runs every browser/project.
- **Task review:** focused checks plus the boundaries the task changes. API/storage
  changes need real integration evidence; UI flows need the real stack, and agent
  changes need real agent/backend execution. The coordinator records why a check is
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
named deeper lane only when its mandatory trigger remains explicit. The coordinator
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

Every worker handoff identifies the base/candidate revision or working-tree diff,
commands, selected tests/counts, results, timing, fixture/artifact identity and known
gaps. The coordinator inspects the diff, reviews test strength and runs checks on the
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
| `make build` | Typecheck, production assets and embedded Go executable; size report |
| `make generate` | Operational API generation; GENERATE_MODE=format uses pinned gofmt; GENERATE_MODE=sqlc regenerates storage queries |
| `make verify` | Web and Go suites, vet/format, build, generated drift, routing/exclusion and binary smoke |
| `make verify VERIFY_MODE=go` | Go checks; optional GO_PACKAGE selects a tested package without whole-binary smoke |
| `make verify VERIFY_MODE=go-race` | HTTP/lifecycle, command, setup client, storage, identity, authentication and secret-file unit race checks |
| `make verify VERIFY_MODE=auth-bench` | Fixed password Hash/Verify benchmarks, ten iterations each, one run and a two-minute timeout |
| `make verify VERIFY_MODE=integration ALMANAC_TEST_DATABASE_URL=...` | Explicit isolated PostgreSQL fixture; storage, authentication, authhttp and executable command suites run serially; GO_PACKAGE=all (default) or one of those package paths |
| `make verify VERIFY_MODE=smoke` | Existing binary, real HTTP and SIGTERM smoke with bounded waits |
| `make verify VERIFY_MODE=unit` | Component suite only; optional `TEST_FILE=tests/ui.test.tsx` |
| `make verify VERIFY_MODE=browser` | Catalogue Chromium matrix; no build or install |
| `make preview` | Local catalogue on port 5198 |

The browser matrix owns port 5200 and refuses an existing server. Keep the
maintainer's preview on port 5198 running during checks; stopping it breaks lazy
imports in already-open tabs. The production smoke uses port 5199. Checks require completed
setup and never install dependencies. Unknown modes and empty test selections fail.
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
Name integration-tagged tests with the `TestIntegration` prefix: that is the lane's
execution selector. Pure unit cases belong in ordinary test files so the fast lane
runs them. Discovery alone does not prove a test was executed.
The authentication service and real TLS authentication handlers are covered here,
including bounded upload rejection and cookie/security boundaries. Executable backend
lifecycle and noninteractive setup run through actual TLS and PostgreSQL. Browser
authentication, ingestion and complete product journeys remain future work.

For browser iteration, select a file and a single viewport/theme, for example
`make verify VERIFY_MODE=browser TEST_FILE=browser/controls.spec.ts BROWSER_PROJECT=mobile-dark`.
Both selectors are validated; absent files and unknown projects fail. Omit them
for the complete matrix. This focused controls check currently runs three tests
in about 2.5 seconds with dependencies ready; it does not replace the full
matrix before shared-control acceptance.
