# Testing

How Almanac is tested ([ADR-0033](adr/0033-simple-test-suite.md)). Everything runs in the dev
container ([ADR-0029](adr/0029-platform-agnostic-development-and-verification.md)); the commands
are in [commands](commands.md).

## Layers

| Command | What it proves | Speed | When |
| --- | --- | --- | --- |
| `make test` | Formatting, vet, generated code, docs links, the agent launcher, Go and web unit tests | about a minute | while editing, every PR |
| `make test-integration` | Go code against real PostgreSQL and the real binaries: constraints, transactions, persistence, auth, agents, CLI | a few minutes | every PR |
| `make test-browser` | The UI catalogue in light, dark, intermediate and mobile setups; the production build | about two minutes | UI changes, every PR |
| `make test-e2e` | The critical user journeys on a fresh Compose stack of the real images | a few minutes | every PR |
| `make test-perf` | Page speed and exact values of the SLO list, SLO detail, SLI list and SLI detail on a seeded fleet | about ten minutes | nightly, performance work |

`make check` runs the first three; CI runs all four PR suites in parallel on every pull request.
In one checkout, run `make check` and `make test-e2e` sequentially: the catalogue
runner clears `web/test-results`, which contains the E2E runner's output directory.
CI jobs have separate workspaces and do not share these artifacts.

## Where a test belongs

- **Unit** (Go `_test.go`, web `web/tests/*.test.tsx`): calculations, validation, state transitions,
  formatting, components, keyboard and error states. Most tests live here. No network or database.
- **Integration** (Go files with `//go:build integration`, functions named `TestIntegration…`):
  anything whose correctness depends on PostgreSQL, the HTTP stack, TLS, the agent protocol or the
  real executables. Every test calls `t.Parallel()` and gets its own database from
  `testpostgres.Database(t)`; never share a database or a fixed port between tests.
- **Catalogue** (`web/browser/*.spec.ts`): how shared controls and charts look and behave in every
  theme and size, against the design-system page. Visual and layout checks go here, not in journeys.
- **End-to-end** (`web/e2e/*.spec.ts`): a user journey through the real UI on the real stack. The
  journey under test uses the UI; set up anything else through the API helper (`e2e/support/api.ts`).
  Assert outcomes a user sees, not markup details.
- **Performance** (`web/perf/`): page timings and exact values on the seeded fleet. Add a page here
  only when it is a main area under [SPEC-0019](specs/0019-backend-performance-and-derived-data.md).

## Rules

- **No retries.** A flaky test is a bug: fix the cause or delete the test with a recorded reason.
  Never rerun until green, and never raise a timeout to hide slowness.
- **Deterministic time and data.** Pass explicit times into calculations. End-to-end tests wait for
  a condition (`expect.poll`, `toPass`), never a fixed sleep.
- **Independent expected values.** Derive expectations from the inputs (for example, 99 good of
  100 against a 99.9% target leaves −900% of the budget), not by copying the implementation.
- **Missing is never success.** Test that missing, stale, partial and zero-traffic data are shown
  as such.
- **Credentials stay out of output.** Fixtures use disposable secrets; never print real ones.
- **Reproduce a reported bug first**, then fix it with a test that fails without the fix.

## End-to-end and performance stacks

`scripts/e2e.sh` starts a fresh stack under the project name `almanac-e2e` on ports 18188, 18528
and 19528, runs Playwright, and removes the stack. It never touches the `make run` stack or its
data. `E2E_KEEP=1` keeps the stack for inspection; `sh scripts/e2e.sh down` (inside
`make shell SOCKET=1`) removes it. The HTML report, with screenshots and traces of failures, is
`.local/e2e/report/index.html`; raw results stay in the dev container's test-results volume, because
trace recording on the macOS bind mount fails.

The performance suite uses the same stack with `playwright.perf.config.ts`. It seeds 20 SLIs and
20 SLOs with `PERF_DAYS` of history (35 by default), checks the exact budgets, then times each page
`PERF_RUNS` times cold and through in-app navigation. The budget is < 1,000 ms at the 95th
percentile. Results are in `.local/perf/results.json`. Traces of every load are in the stack's
VictoriaTraces while it runs (`E2E_KEEP=1`), for finding where time goes.

## Coverage

`make coverage` runs the unit and integration tests with coverage of `internal/...` and writes
`.local/coverage/go.html`. Use it to find untested code, not as a target to game.
