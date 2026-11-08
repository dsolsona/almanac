# ADR-0033: A simple test suite: fast tests, one parallel integration run, Playwright journeys and four page timings

- Status: accepted
- Created: 2026-11-05
- Accepted: 2026-11-05
- Approval: the maintainer asked for "the simplest thing possible that gives us good coverage" and gave "free reign to improve the situation" on 2026-11-05. The goals are good unit coverage, fast unit tests, end-to-end tests of the critical user journeys, a performance test of the SLO list, SLO detail, SLI list and SLI detail, and easy, fast local development. The maintainer agreed to add CI on every pull request, no hidden retries, measured coverage and keeping upgrade and permission coverage.
- Related specs: [SPEC-0014](../specs/0014-delivery-and-verification.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md)
- Supersedes / superseded by: supersedes decisions 4, 6 and 7 of [ADR-0029](0029-platform-agnostic-development-and-verification.md) (`make verify MODE=…`, the Compose verification mode and `scripts/verify.py` with its receipts, selection and timing ledger) and decision 2 of [ADR-0030](0030-browser-tracing-overhead-is-not-an-acceptance-check.md) (the Compose performance stage); implements the acceptance measurement of [ADR-0025](0025-fast-operational-journeys-and-tracing.md) as an automated test. ADR-0029's Docker-only runtime, images and fixtures stand.

## Context

Verification selection was clarified by the maintainer on 2026-11-08:
[testing](../testing.md) exempts documentation, skills and CI-only changes from
application suites. The commands and application-suite coverage below remain unchanged.

On 2026-11-05 the suite was measured. Coverage was good and the unit layers were fast, but:

- **The integration suite was slow.** It took about 14 minutes for 173 cases. Each case ran in its own `go test` process, one after another, with the race detector on. Even a trivial database test took about 2 seconds.
- **The end-to-end layers were slow and fragile.**
  - The same journeys were driven three ways: Go tests launching custom Node browser scripts, a Compose script and the performance harness.
  - The Compose run failed in 13 of its last 15 runs.
  - The browser scripts were 5,300 lines of bespoke code, outside Playwright's test runner.
  - They checked pixel alignment and date formats inside deployment tests, so markup changes broke them.
  - The SLI journey ran three times.
- **The performance harness was oversized for its job.** It was 6,400 lines with an 8.5-hour budget, and it never measured SLI detail.
- **The tooling existed mainly to avoid re-running slow suites.** A 1,150-line runner with its own tests handled receipts, input fingerprints, pass reuse and a timing ledger.
- **No CI ran on pull requests.**

## Decision

1. **Five commands, all in the dev container.**
   - `make test` runs formatting, vet, generated-code and docs checks, the agent launcher checks, and the Go and web unit tests.
   - `make test-integration` runs the Go tests against PostgreSQL and the real binaries.
   - `make test-browser` runs the UI catalogue matrix and the production build check.
   - `make test-e2e` runs the end-to-end journeys.
   - `make test-perf` runs the page timings.

   `make check`, also spelled `make verify`, runs what a pull request needs. `make coverage` writes a Go coverage report. `RACE=1` adds the race detector. Nothing records receipts, reuses passes or keeps a ledger: the suites are fast enough to run.
2. **Integration runs as one `go test` with parallelism.** Packages run in parallel and every integration test calls `t.Parallel()`. Each test owns its database in the dev container's PostgreSQL. CI runs it with the race detector.
3. **End-to-end means Playwright against a fresh Compose stack of the real images.** `scripts/e2e.sh` starts the published `app` and `agent` images, PostgreSQL, VictoriaMetrics and VictoriaTraces. The stack runs under its own project name and ports, so the operator's `make run` stack and data are untouched, and it is removed afterwards.

   The journeys use Playwright's own runner:
   - **Onboarding** through the UI, which saves the session for the others.
   - **The core journey:** register an agent and run it, connect a datasource through it, test the connection and preview a real series, define an SLI, see its first collected interval, set an SLO and read its evaluation against an independently computed value, then rename both.
   - **Agent lifecycle:** rotate, revoke, disable and delete.
   - **Restart:** data and collection survive an application restart.

   The journey under test always goes through the UI. Other data may be set up through the API. Journeys assert user outcomes. Layout, themes and sizes belong to the catalogue matrix and component tests.
4. **No retries.** Playwright retries are 0 and Go tests run once. A flaky test is a bug: fix it, or delete it with a recorded reason. Never retry it into green.
5. **Performance measures the four main pages in the browser.**
   - **Fixture:** `make test-perf` seeds 20 SLIs and 20 SLOs, one full list page, with `PERF_DAYS` of one-minute history (35 by default). A real agent collects every SLI first, the history behind that interval is written in SQL with the agent's own provenance, and the SLOs are created with a historical start, so the backend's own preparation computes every value.
   - **Correctness:** the list must show the exact expected budgets.
   - **Timing:** each page is timed cold and through in-app navigation. The time is the UI's own journey measure, from navigation start until the content, and the charts on SLO detail, has painted. The same measure appears in the trace.
   - **Budget:** the 95th percentile must be under 1,000 ms (SPEC-0019).
   - **Schedule:** it runs nightly and on demand, not on every pull request.

   Traces stay in the stack's VictoriaTraces for finding where time goes. Trace completeness is not a gating check.
6. **CI runs the same commands.** `.github/workflows/ci.yaml` builds the dev image from the Actions cache. It runs `make test`, `make test-integration RACE=1`, `make test-browser` and `make test-e2e` as parallel jobs on every pull request and on `main`. `make test-perf` runs nightly and on manual dispatch, and its results are kept as an artifact.

## Alternatives considered

- **Keep the per-case runner and add parallel workers.** Rejected: one `go test` already parallelizes, and the reuse machinery only existed because of the per-process cost.
- **Keep the Go-driven browser scripts and fix their flakiness.** Rejected: they duplicate Playwright's runner, waiting, tracing and reporting in custom code.
- **A template database cloned per test.** Measured and rejected: migrations are a small part of each test's time.
- **Run the performance suite on every pull request.** Rejected: seeding takes several minutes and timings on shared CI runners vary. Nightly runs show trends without blocking work.

## Consequences

- **Deleted:**
  - `scripts/verify.py` and its tests.
  - The `make verify MODE=…` modes, `verification-init`, `verification-report` and `verification-handoff`.
  - The custom browser scripts and their unit tests.
  - The Go tests that only launched them.
  - The controlled performance harness.
  - The UI smoke script, which the end-to-end journeys cover.
- **Kept but no longer driven from a browser:** the Go journeys that also check the database, the CLI, the API and HTTP boundaries.
- **Dropped end-to-end coverage:**
  - The lost-response retry scenarios. The API's idempotency and replay remain covered by integration tests.
  - Datasource Basic and Bearer connection tests through a real agent, with authentication headers still covered by unit tests.
  - Agent reassignment, the delete-blocked case and replacement collectors. These remain covered by storage integration tests.
  - The `make run` Makefile behaviours: build cache reuse, a failed build keeping the old app, port refusal and bootstrap repetition.
  - The 100- and 1,000-SLO capacity loads, which were already deferred.
- **Known gaps worth adding next:** a journey for permission failures once multiple users exist, and SLI or SLO deletion once the UI offers it.
- **Updated:** the command and testing guides, `AGENTS.md`, the ways-of-working guide and SPEC-0014's gate list. Historical task records keep the commands they ran.

## Revisit conditions

- Revisit the integration layout if `make test-integration` grows past about three minutes.
- Revisit the end-to-end layout if `make test-e2e` grows past about ten minutes, or if a journey becomes flaky twice.
- Revisit the performance budget if nightly results drift towards it.
