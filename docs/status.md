# Delivery status

## Current state

The first visual foundation task is complete. Shared controls, theme tokens,
the local heading font, catalogue and web verification harness are implemented.
The catalogue also includes a date picker, switches, shared loading feedback,
aligned timeline charts and exact-value tables.
No Go module, application binary, database schema, CLI or provider is implemented.
The scope of the design pass is the product described in SPEC-0001; further product
requirements may legitimately create additional specifications and ADRs.

| Records | Status | Evidence |
| --- | --- | --- |
| SPEC-0001, ADR-0001 | Accepted | Completed maintainer reviews recorded in document headers |
| SPEC-0002, ADR-0002 | Accepted | Completed maintainer reviews recorded in document headers |
| SPEC-0003, ADR-0003 | Accepted | Crit review `b7b16f696fed` approved after all comments resolved |
| SPEC-0004–0015 | Accepted | Crit review `7fd22977b6c0` approved with all comments resolved, 2026-10-24 |
| ADR-0004–0013 | Accepted | Same completed review; deferred encryption-library choice remains explicit |
| SPEC-0016–0017, ADR-0014–0015 | Accepted | Crit review `0de0c0e42db6` approved with no changes requested, 2026-10-24 |
| Product implementation | F1-T01–03 done; F1 incomplete | Catalogue, controls and charts verified; product journeys remain |

## Coverage

| Requirement | Primary specs |
| --- | --- |
| Single tenant, Go, preserved Almanac design | 0001, 0012, 0013 |
| Agent-owned collection, direct API ingestion | 0002, 0003, 0009, 0015 |
| Datasource host/auth/frequency, assignments and capability differences | 0005, 0009, 0015 |
| Prompt remote tests, versioned config and heartbeats | 0002, 0009 |
| Bounded live ring, isolated historical work and best-effort healing | 0003, 0008, 0009 |
| Periodic finite retries, visible gaps, manual repair | 0003, 0008, 0010, 0012 |
| PostgreSQL and measurement history | 0006, 0007 |
| Local/Google login, first-admin onboarding and scoped tokens | 0004, 0010, 0013 |
| Automation CLI with UI parity | 0010, 0012 |
| OpenSLO import and explicit execution compatibility | 0005, 0006 |
| SLOs, budgets, burn, reports and alerts | 0006, 0011 |
| Combined/separate runtime components and operational recovery | 0013 |
| Delivery sequence and meaningful verification | 0014 |
| Maintenance exclusions and SLO graph annotations | 0016, 0017 |

## Documentation checks

Checks cover local link targets, index/status consistency, unique numbered criteria,
review-note coverage, whitespace and publication constraints. Cross-domain review
reconciled permissions, snapshot precision, initial historical validity, lease timing,
restore credentials and live/historical resource limits. Numerical examples received
independent arithmetic checks where stated. These checks validate the design package,
not product correctness or scalability.

See [review guide](review-guide.md) for the highest-impact decisions, reading order,
and decision register. The completed review clarified mandatory initial-password
rotation, local users before Google SSO, calculation examples, composite sequencing,
React/API packaging and measurable UI performance. The encryption library remains a
candidate selection before encryption implementation. Web foundation checks now run;
no Go/API/database or full product-journey tests are claimed.

## Next work and session handoff

- The initial accepted design is committed as `09cfdfd`.
- The maintainer selected a primary coordinator and `gpt-6-luna` implementers.
  Follow AGENTS.md and [ways of working](ways-of-working.md) on every new session.
- [Testing guidance](testing.md) requires focused iteration, independent coordinator
  review, a real-stack smoke gate and full boundary/release evidence. Web foundation
  timings are measured below; real-stack and later boundary gates remain future work.
- The [visual foundation plan](plans/0001-visual-foundation.md) and
  [F1-T01](tasks/F1-T01.md) are complete and integrated into `main` through `21505bd`.
  Prepare the next bounded chart/exact-table task. The implementation worktree and
  ignored screenshots/review reports remain available for inspection.
- [F1-T02](tasks/F1-T02.md) and its [controls plan](plans/0002-catalogue-controls.md)
  are complete, integrated through `4a1f15b` with independent review and fresh checks.
- [F1-T03](tasks/F1-T03.md) and its [plan](plans/0003-charts-and-tables.md)
  are complete, integrated through `5ade04d` plus the reviewed test URL correction.
  Continue with the independently runnable Go UI executable foundation.
- F1-T04 is actively being implemented on local branch `feat/f1-runtime` in
  `.worktrees/f1-runtime`. Its task brief and plan are on that branch; Go/runtime
  changes are not accepted or integrated yet. Resume from its task report and
  plan ledger before dispatching another implementer. Preserve the main catalogue
  preview on port 5198; the runtime task does not require browser checks.
  Work is checkpointed for a session restart to pick up Docker group membership;
  the implementer is paused and its uncommitted files are preserved. Resume the
  existing task after reading its report and restart checkpoint. Final lifecycle
  regressions, full verification, review and integration remain outstanding.
- Resolve explicitly deferred choices before their affected task, including encryption
  library/key format; verify tool/dependency versions when scaffolding.
- Documentation validation uses `python3 scripts/check-docs.py`. Web build, unit and
  catalogue browser entry points exist; API/database/agent integration remains F1 work.

## Visual foundation verification

The coordinator independently ran `make verify`: component tests, typecheck,
production build, emitted-bundle exclusion and production routing smoke passed.
The initial component suite had 8 tests and took 0.97 seconds; the reviewed correction
adds a ninth regression test (focused suite: 1.16 seconds). The browser matrix
passed 36 checks in 15.3 seconds with two Chromium workers, at 1440/1024/360 px in
both themes. Production JavaScript measured 68.92 kB gzip. These are foundation
measurements, not the SLO workload benchmark or real-stack smoke acceptance.

Desktop and mobile screenshots in both themes received visual inspection. A missing
spacing-token mapping was corrected and guarded by computed layout assertions.
Review found a duplicate dialog title ID; negative verification found that an
unmatched test-file selector incorrectly ran all tests. Both were corrected in
`fd7a12a`, with a failing-then-passing dialog regression and a nonzero exit for an
unmatched selector. Manual screen-reader testing and Firefox/Safari coverage remain
outstanding; browser semantics, keyboard and computed contrast checks are partial
accessibility evidence only.

Fresh verification on integrated `main` passed on 2026-10-25: 9 component tests,
typecheck, build and production routing/exclusion checks; full `make verify` elapsed
2.75 seconds, with component tests taking 0.95 seconds. The browser matrix passed
36/36 in 15.5 seconds. Setup in this checkout used the committed lockfile and cached
Chromium, installing dependencies in about 2 seconds. Environment: AMD Ryzen 5 7640U
(6 cores/12 threads), Linux x86_64, Node 26.8.1, npm 12.0.2, Playwright 1.64.1 with
bundled Chromium 156.0.8078.4. Playwright uses its Ubuntu fallback on this host;
Node emitted a color-environment warning during browser checks. Neither warning
was suppressed or treated as test evidence.

Independent task review, scoped correction review and final branch review approved
this bounded delivery. Two nonblocking follow-ups remain before broader reuse:
replace heading-derived Panel IDs with unique per-instance IDs, and exercise both
forward and reverse dialog focus wrapping in browser tests. The supplied OFL file
retains its upstream line endings/trailing whitespace; whole-range whitespace
checking therefore reports that license asset, an intentional preservation exception.

## Workflow preparation evidence

The coordinator reviewed the workflow and a fresh `gpt-6-luna` session performed an
advisory read-only review. Its CI wording finding was corrected: CI gates are required
future work, not running infrastructure. The documentation checker passed and rejected
isolated deliberate broken-link, duplicate-criterion and index-status fixtures, then
passed again after repair. These checks establish documentation tooling behavior only.

## Calendar, switches and loading verification

The coordinator reran `make verify` on integrated main: 20 unit tests, typecheck,
build and production exclusion/routing checks passed. Full browser verification
passed 54/54 in 22.2s across both themes at 1440/1024/360px. Independent focused
mobile-dark controls verification selected 3 tests and passed in 2.5s. Invalid test
files and unknown browser projects fail before browser startup. No dependencies
were added; production JavaScript remains 68.92kB gzip.

Visual review covered open calendars and switches on desktop/mobile in both themes.
Tests cover leap dates, early years, month clamping, date navigation/commit/cancel,
switch keyboard/disabled/form-reset semantics, explicit demo completion, continuous
transform animation and static reduced-motion presentation. Initial failures exposed
an outdated timed-completion assertion and an ambiguous switch locator; both were
corrected. A failing native-reset regression drove checked-state styling from the
actual checkbox. Task and whole-branch reviews found no remaining issues. Manual
screen-reader and Firefox/Safari evidence is still outstanding.

## Timeline chart verification

Integrated main passed `make verify`: 28 component tests in 2.05s, typecheck,
production build and routing/exclusion smoke. Browser verification passed 90/90
in 57.9s across six viewport/theme projects. Production JavaScript remains
68.92kB gzip; ECharts and catalogue modules remain development-only. Desktop and
mobile chart screenshots in both themes were inspected. This is catalogue
coverage, not the full SLO workload benchmark, real-stack journeys or manual
screen-reader/Firefox/Safari acceptance.

The reported blank catalogue was reproduced by failing the lazy chart import.
A scoped boundary preserves other controls and offers reload recovery. Preview
5198 now remains separate from browser5200 and production-smoke5199. Independent
review also drove unique internal chart IDs, exact page-row validation, CSS-token
consumption, accessible chart-label contrast, bounded gap expansion with a
subprocess-protected hang regression, and unique table heading IDs.

First failures were retained: initial chart layout/hover selectors and catalogue
readiness needed corrections; the bounded regression harness required a Node
builtin access adjustment; two simultaneous browser invocations conflicted and
were subsequently sequenced. The first integrated matrix passed 89/90: a custom
touch context still used preview5198 during its restart. Its URL now inherits the
test configuration; independent review approved the fix before the passing gate.
The original trace is retained locally. Expected React console errors in the
deliberately failed-import cases are caught by the boundary; normal navigation
tests assert no uncaught page errors or failed requests.
