# Delivery status

The latest combined candidate passes the real SLI/SLO browser journey and normal
Compose acceptance. Linux build/unit and race stages pass; its database stage fails
in `TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict`: the new
zero-traffic list assertion receives `storage: invalid session`. That failure and
the earlier shutdown investigation remain unresolved. The maintainer requested
committing the current work and opening a PR without further implementation.

## Current state

The maintainer confirmed on 2026-10-29 that the SLI/SLO screens must match the
retained [slo-tracker visual reference](design/slo-visual-reference.md), with current
left navigation retained. Urgency grouping, separate burn windows, budget views and
all four detail graphs are required F3 scope. The [F3 plan](plans/0021-first-objective.md)
now tracks [F3-T02](tasks/F3-T02.md) operational views, [F3-T03](tasks/F3-T03.md)
numerical compliance, [F3-T04](tasks/F3-T04.md) SLI presentation and
[F3-T06](tasks/F3-T06.md) contextual annotations. The maintainer
authorized implementation and accepted [ADR-0023](adr/0023-slo-operational-urgency.md)
in the subsequent working-conversation go-ahead. F3-T02 is in progress on
`feat/f3-operational-slos`; F3-T03 numerical compliance is now in progress in an
isolated checkout. F3-T04 SLI presentation is implemented in the working tree and
under integrated verification; F3-T06 annotations remain draft.
The operational API, urgency
overview, independent burn windows, budget history and three-lane detail are
implemented. The combined `make verify` passed 297 web tests, 410 Go tests,
tagged integration-test compilation, generated checks, production build and binary
smoke. The normal isolated Compose journey and cancellation cleanup passed, with
the SLO list, presets and detail inspected in both themes at 360/1024/1440px.
Real PostgreSQL storage checks passed, including urgency groups, independent burn
windows, snapshot replay/expiry/admission, late observations, historical input
bounds and revocation. Native and Linux full integration both remain unsuccessful:
the executable agent lifecycle case times out during backend shutdown before its
reconnect step. Its cause is under focused investigation; no full integration or
entire-F3 acceptance is claimed. Linux ordinary and race gates passed; the full
Linux browser stage was not reached. The focused catalogue matrix passed 42 checks.
The source is integrated into the main working tree without commit or deployment.
F3-T03 now also implements the fourth daily-compliance lane and exact table,
with actual rolling attainment, objective references and separate qualified day
counts. Its focused checks passed 172 unit cases, the real PostgreSQL operational
case and the executable API/CLI/browser journey. Desktop and mobile captures were
independently inspected. The combined gate passed 301 web cases, Go/generated/build
and smoke checks. Normal Compose first exercised the four-lane SLO journey, then
failed a replacement-collector assertion before that SLI's first permitted dispatch.
After correcting the harness to derive its deadline from the actual schedule, the
entire normal Compose journey and cancellation cleanup passed with matching source
fingerprints. The earlier general shutdown failure remains unresolved; full
integration acceptance is still outstanding.

The maintainer requested concrete delivery-efficiency improvements while app work
continues. [F3-T05](tasks/F3-T05.md) adds fixed focused verification, source-bound
receipts and local task-wide rerun/handoff controls. The tooling is integrated and
verified: 21 tooling checks passed; shared active-task state refused an unjustified
broad rerun and an over-budget handoff. Fixed SLO unit checks passed 166 cases in
7.32 seconds, its real database case passed in 6.52 seconds, and the isolated SLO
API/CLI/browser journey passed in 39.95 seconds. Prior failures remain retained.
The maintainer approved the closed command catalog; other approval boundaries and
required final checks remain in force.
F3-T01 is merged through `fb648c3`; its functional checks do not establish visual
acceptance of the simplified screens. No operator deployment change is recorded.

[F3-T01](tasks/F3-T01.md) was verified locally on `feat/f3-first-objective`,
based on merged `e174291`. The maintainer authorized starting F3 with current
Prometheus SLIs while external submission remains deferred and F2 incomplete.
The [first objective plan](plans/0021-first-objective.md) separates the initial
usable SLO journey from the remaining F3 reporting, import and annotation work.
Users can create services and single-indicator request-count SLOs, evaluate fixed
rolling windows, and inspect exact success, budget, burn and qualified coverage
through UI/API/CLI. The PR #46 review candidate at source `33b85ced` passed the full
Linux gate: 282 web, 379 Go, 371 race, 166 PostgreSQL integration and 108 browser
tests. Normal Compose and cancellation cleanup passed; recovered SLO results were
inspected in light desktop and dark mobile layouts. The three Greptile findings
are corrected: evaluation retries serialization conflicts with fresh authorization,
historical-create recovery preserves its body/key, and sign-out invalidates delayed
navigation. The task records the original regressions and test-helper corrections.
This is the first usable F3 increment. Target
revisions, calendar windows, reports, OpenSLO and annotations remain in the plan.
The increment was published in PR #46 and is now merged; it has not been deployed to the operator app.
F3-T01 remains in review because an earlier run again encountered the existing
intermittent browser response-read failure before the SLO journey. That investigation
remains open; the final passing gate is not presented as a fix for it.

[F2-T32](tasks/F2-T32.md) is verified on `fix/stack-review`, based on `bcd636b`.
It repairs thirteen review findings across the SLI and agent stack: empty-history
lists, bounded large responses, focus and retry, lease indexes, month-end partitions,
handoff configuration/cleanup, phased heartbeat timing and replacement compatibility.
Source `06f59ca3` passed 261 web, 365 Go, 357 race, 165 PostgreSQL integration and
108 browser tests. Normal Compose acceptance covered navigation/retry, exact 99/100
collection and clean/forced/backend restarts; desktop/mobile pages in both themes
were inspected. F2-T32 records first failures, corrections and compatibility limits.
The correction and its dependency stack merged into main at `e174291`; the operator app remains unchanged.

[F2-T31](tasks/F2-T31.md) is verified on `fix/agent-restart`, based on `c6b8674`.
[ADR-0022](adr/0022-restartable-agent-ownership.md) replaces the one-hour restart
wait with immediate authenticated process replacement and durable generation fencing.
Independent Compose acceptance proved clean restart (1.181s), hard-kill replacement
(1.066s) and backend restart plus replacement (7.638s), with new observations before
old lease expiry, preserved history and rejected former-process results. The Linux
gate passed 258 web, 360 Go, 352 race, 162 PostgreSQL integration and 108 browser
tests. F2-T31 records artifacts and the corrected fixture readiness failure.
The operator deployment has not been updated.

[F2-T28](tasks/F2-T28.md) is verified on `fix/sli-detail-usability`, based on
`258f968`. Maintainer testing exposed noninteractive breadcrumbs, a raw agent UUID,
crowded collection status and an uneven SLI detail layout. The repair covers linked
navigation, named related records, a result-first layout and an explanation of the
first result's timing. Collection-policy changes are not part of this UI repair.
The coordinator verified the actual 60/60 Compose case, navigation and both themes
at desktop/mobile widths. Combined source `a6929f88` passed 255 web, 351 Go, 343 race,
161 selected integration and 108 browser checks. The task retains the earlier
unexplained response-read failure; the diagnostic patch does not establish its cause.

[F2-T29](tasks/F2-T29.md) is verified in the same candidate. It
removes tick-throttled slot refill, rotates admission under saturation and retains
the current settled interval when an older pending interval expires. The maintainer
has now approved immediate saved initial data and distributed recurring queries
under [ADR-0021](adr/0021-immediate-initial-collection.md).
[F2-T30](tasks/F2-T30.md) is verified on source `4510d93c`. The normal Compose
journey passed, including
99/100 visible in 21.407 seconds for the reported 60/60 setup and inspection at
desktop/mobile widths in both themes. The full Linux gate passed 258 web, 358 Go,
350 race, 161 selected integration and 108 browser checks. The task records the
corrected CLI and migration-fixture failures and the verifier's file-watcher warnings.
This increment has not been deployed to the operator application.

[Agent management](plans/0020-agent-management.md) is verified on
`feat/agent-management`, stacked on `fix/workspace-usability`. The maintainer
approved credential-driven `make agent`, dependency-guarded deletion, readable
credential durations and session-authorized management. Tasks F2-T24 through
F2-T27 cover the implementation and safe active-datasource handoff. The integrated
Compose journey passed replacement collection, agent deletion, old-token rejection
and preserved history. The full Linux gate passed 243 web, 345 Go, 337 race,
161 selected integration and 108 browser checks on source snapshot `bd0bb25b`.
The coordinator inspected desktop/mobile pages in both themes and accepted this
candidate. Earlier unexplained browser failures remain recorded in
[F2-T24](tasks/F2-T24.md); later passes do not establish their causes.
This increment has not been deployed to the operator application.

[F2-T23](tasks/F2-T23.md) is verified on `fix/workspace-usability`, based on
the F2-T22 candidate. Product pages no longer show loading indicators; first-use
SLI setup preserves the datasource return path, agent rows and credential records
fit their layouts, and password guidance uses plain character limits. The full
Linux gate passed 225 web, 336 Go, 328 race, 151 integration and 108 browser checks.
The normal Compose journey and cancellation cleanup passed, with both themes and
mobile layouts inspected. The task records the verified source snapshot, diagnosed
test-locator failures and the host sleep/Colima interruptions. This is prepared
for PR review; the coordinator has not restarted the operator deployment.

[F2-T22](tasks/F2-T22.md) is verified locally on `feat/f2-observations`, based on
`06be8f0`. Operators can create native Prometheus interval-count SLIs, collect them
through the assigned remote agent, and read exact persisted observations through
UI/API/CLI. Linux checks passed 207 web, 336 Go, 328 race and all 151 integration
tests; the corrected host Chromium matrix passed all 102 checks. The normal Compose
journey and cancellation cleanup passed, including exact 99/100 observations and
the approved host-curl startup readiness fix. The task records first failures and
the Linux catalogue watcher limitation; the complete Linux wrapper is not claimed
as passing. This change is prepared for PR review. Direct-producer ingestion is still required
for the full F2 checkpoint.

PR #38 review corrections are verified at `a9f6d28`: cancellation retains safe
request metadata, workspace navigation focuses the Agents heading, and Compose
acceptance verifies the issued token never appears in service logs. The full gate
passed 190 web and 305 Go tests; race verification and the real Compose journey
(including desktop/mobile focus and cancellation cleanup) passed. See
[F2-T21](tasks/F2-T21.md) for regression and deployment evidence.

Workspace usability repairs under [F2-T21](tasks/F2-T21.md) are verified on product
checkpoint `a5acd61` with final test-only correction `744a4d3`. The normal isolated
Compose journey registers `Local`, shows the suggested identifier and one-time
token, verifies the credential record, history/reload and PostgreSQL persistence.
Internal navigation retains the document/session with zero reload/context calls;
literal endpoint text and real desktop/mobile light/dark pages were inspected.
The same display-name/identifier pattern applies to datasource creation, and saved
identifiers cannot be edited. Passing checks: 190 web tests, 304 Go tests, 296 race
tests, 42 catalogue browser checks, real Compose and documentation/build/smoke gates.
Guidance now requires exact user-case evidence, deployed journeys, reference
comparison, independent coordinator acceptance and frozen verification candidates.
Earlier results below establish only their tested cases. At that checkpoint no
new F2 feature work was underway; the maintainer deployment and its data remained
untouched.

PR #37 review corrections are verified on combined code checkpoint `845e619`.
All 17 findings have regressions covering configuration consistency/capacity,
terminal diagnostic races and wire results, clock skew, upload generations,
saved credentials, UI recovery/navigation and CLI cancellation/output. Final checks
passed 149 selected real integration tests, 183 web tests, 303 Go tests and 295
race tests, plus generated/build/production/smoke and documentation checks.
The real browser journey tests stored Basic/Bearer credentials and cancellation;
desktop/mobile screenshots in both themes were inspected. Full evidence and
preserved first failures are in [F2-T20](tasks/F2-T20.md). An earlier nonzero backend
shutdown remains unexplained; the final restart journey passed, and sanitized
shutdown classification is now captured if it recurs.

The original Prometheus datasource management and remote diagnostics increment was verified under
[F2-T20](tasks/F2-T20.md), on code checkpoint `f6086fe`. UI, CLI and API support
encrypted credentials, optimistic updates, explicit configuration readiness,
connection tests and query previews executed by the assigned remote agent.
Results above 64 KiB use bounded authenticated HTTPS transfer, up to 1 MiB.
Final checks passed 146 real integration tests, 169 web tests, 290 Go tests and
282 race tests, plus generated/build/production/smoke, documentation and Compose
startup/persistence checks. The coordinator inspected desktop/mobile in both themes.
Those results describe the original increment; the review correction evidence above supersedes them.

The previous delivery stopped after publishing that increment. F2 is still incomplete:
SLI definitions, scheduled collection, collected observations and direct producer
ingestion remain outstanding in the [delivery plan](plans/0019-first-observations.md).
Full source-key rotation/re-encryption and restore acceptance remain initial-release
gates under [ADR-0017](adr/0017-recoverable-secret-encryption.md) and
[SPEC-0014](specs/0014-delivery-and-verification.md).

The coordinator accepted the PR #36 review corrections under
[F2-T17](tasks/F2-T17.md). The corrected candidate passes
the 217-test Go race gate, focused agent client/page web suites (32/47), all 142
selected PostgreSQL integration tests, and the exact full `make verify` gate (156 web,
228 Go, builds, generated/production checks and smoke checks). Integration included
the real TLS/process and browser agent journey. A same-generation hello health reset
found by the first integration attempt is corrected. Its
configuration-failure status fields extend migration 0010 and generated OpenAPI
outputs integrated into the datasource increment above. The coordinator inspected
the final browser screenshots at desktop/mobile widths in both themes.

The connected-agent increment under [F2-T17](tasks/F2-T17.md) and
[F2-T19](tasks/F2-T19.md) passes 142 real PostgreSQL integration tests, including
separate TLS backend/agent processes, configuration application, browser status,
backend restart/resume, incompatible handshakes, copied-token fencing and revocation.
The coordinator inspected desktop/mobile screenshots in both themes. Full checks
passed 156 web and 222 Go tests. The expanded race gate passed 211 tests after
correcting a canceled outbound enqueue; the real integration run preceded that
isolated correction. Collected/direct observations remain the next F2 increment;
connected status alone does not establish healthy collection.

Durable agent ownership is verified under [F2-T13](tasks/F2-T13.md): transactional
acquisition, same-process resume, nonce-correlated renewal and generation fencing.
The combined candidate passes 138 real PostgreSQL integration tests and the full
gate (146 web/189 Go tests, build/generated/production checks). A deterministic
stalled-TLS shutdown regression drove a correction that closes pre-request
connections while draining active requests. The exact socket behind the earlier
intermittent shutdown failures was not recorded; this fixes the reproduced class,
without claiming forensic proof of every historical failure. F2-T17 extends this
foundation with remote transport, configuration application, health and grants.

The signed-in application shell is verified under [F2-T16](tasks/F2-T16.md):
desktop navigation, accessible mobile menu, account controls and direct `/agents`
links replace the identity confirmation page. Planned destinations are explicitly
marked “Not available yet.” Evidence: 145 web/178 Go tests, 42 focused catalogue
browser checks and 129 selected real database tests including browser journeys.
The datasource increment above extends this shell; observations remain outstanding.
The maintainer approved the accelerated delivery workflow on 2026-10-27; see
[ways of working](ways-of-working.md) and [command discipline](commands.md).

Registration usability correction is verified under [F2-T14](tasks/F2-T14.md).
[ADR-0019](adr/0019-session-authorized-agent-registration.md) accepts registration
using the current administrator session without an additional password prompt.
UI/API/CLI registration and explicit same-request recovery pass real-stack checks.
Final evidence: 140 web tests, 178 Go tests, 129 selected database tests and the
race gate passed. An earlier reported registration failure was not reproduced;
one intermediate combined-runtime restart shutdown exceeded its five-second
deadline and remains an unresolved intermittent observation. Neither is claimed
fixed by a subsequent passing run; details are preserved in the task record.

The first visual foundation task is complete. Shared controls, theme tokens,
the local heading font, catalogue and web verification harness are implemented.
The catalogue also includes a date picker, switches, shared loading feedback,
aligned timeline charts and exact-value tables.
A Go executable serves the embedded UI and authentication API with operational
health, a fixed-origin API proxy, version/help commands and bounded shutdown.
Explicit PostgreSQL upgrades establish immutable schema history and installation
metadata. Local authentication has a verified application service and protected
HTTP handlers. Noninteractive administrator setup and browser sign-in, restricted
password setup, signed-in identity and logout are verified in the combined runtime.
Reusable CLI login/profiles are verified under [F2-T10](tasks/F2-T10.md).
Agent registration, credential rotation/revocation and disable now have UI/API/CLI
coverage. [F2-T11](tasks/F2-T11.md) records verified CLI parity, protected one-time
token output and real TLS/PostgreSQL acceptance. Datasource diagnostics and the
first observation journey remain outstanding in F2.
[F2-T12](tasks/F2-T12.md) adds current-state machine credential authentication,
including rotation/expiry, revocation, restore epoch and database lock-order checks.
This prerequisite does not yet expose an agent connection or execution lease.
User management and observation storage remain unimplemented; Prometheus diagnostics
are implemented in F2-T20, while additional native providers remain future work;
Google SSO is a later increment.
The scope of the design pass is the product described in SPEC-0001; further product
requirements may legitimately create additional specifications and ADRs.

| Records | Status | Evidence |
| --- | --- | --- |
| SPEC-0001, ADR-0001 | Accepted | Completed maintainer reviews recorded in document headers |
| SPEC-0002, ADR-0002 | Accepted | Completed maintainer reviews recorded in document headers |
| SPEC-0003, ADR-0003 | Accepted | Crit review `b7b16f696fed` approved after all comments resolved |
| SPEC-0004–0015 | Accepted | Crit review `7fd22977b6c0` approved with all comments resolved, 2026-10-24 |
| ADR-0004–0013 | Accepted | Same completed review; encryption-library choice subsequently resolved by ADR-0017 |
| SPEC-0016–0017, ADR-0014–0015 | Accepted | Crit review `0de0c0e42db6` approved with no changes requested, 2026-10-24 |
| ADR-0016 | Accepted | Maintainer approved authentication outcome recovery without automatic mutation replay, 2026-10-26 |
| ADR-0017 | Accepted | Maintainer approved Tink Go 2.8.0, protected external keysets and rotation, 2026-10-26; implementation remains outstanding |
| Product implementation | F1 Foundation merged at `9c25fef`; local Compose follow-up verified | F1-T01–18 deliver the foundation slice; F1-T19 adds one-command local startup; F2 observations and later release requirements remain unimplemented |

## Foundation acceptance

F1 meets the bounded foundation outcomes in [SPEC-0014](specs/0014-delivery-and-verification.md)
on browser-auth revision `c12d221`, stacked on the unmerged runtime/setup increments.
The foundation stack subsequently merged at `9c25fef`. This is not a full-product
release acceptance. [F1-T19](tasks/F1-T19.md) adds the explicitly requested local
Compose follow-up: `make bootstrap`, `make run` and `make stop`, with container-only
build/setup, persistent volumes and real browser restart/onboarding evidence.
The follow-up implementation `b50c4d5` is published as
[PR #16](https://github.com/dsolsona/almanac/pull/16), with task and whole-branch
review approval. Its real Compose gate includes occupied-port recovery and
unchanged PostgreSQL container identity across app recreation.
PR review corrections also pass expired-certificate renewal and active-command
SIGINT/SIGTERM cleanup checks, including removal of each test-owned image tag.

| F1 outcome | Reproducible evidence |
| --- | --- |
| Repeatable local stack | [F1-T16](tasks/F1-T16.md): repeatable integration flow with the real combined executable, protected files, verified TLS, readiness, setup, restart persistence and cleanup. [F1-T05](tasks/F1-T05.md) separately verifies the executable schema-upgrade command. |
| Health/readiness | [F1-T04](tasks/F1-T04.md)/[F1-T14](tasks/F1-T14.md): operational HTTP, database/schema/setup readiness, ownership and shutdown boundaries. |
| Protected setup and local administrator login | [F1-T15](tasks/F1-T15.md)/[F1-T18](tasks/F1-T18.md): CLI and actual browser initial-password rotation, reload, logout and uncertain-response recovery against HTTPS/PostgreSQL. |
| Design catalogue | [F1-T01](tasks/F1-T01.md)–[F1-T03](tasks/F1-T03.md), with the current 96-case theme/viewport matrix and browser authentication captures recorded below. |
| CLI/API skeleton | Generated operational/authentication contracts, help/version, explicit database upgrade and noninteractive setup through the same authentication operations used by the browser. |

The latest candidate passes `make verify` (73 web/102 Go tests, build/generated and
production checks) and the following full integration lane (66 tests, PostgreSQL
18.6, including three actual Chromium journeys):

```sh
make verify VERIFY_MODE=integration ALMANAC_TEST_DATABASE_URL=postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable
```

Prerequisites are pinned tools/Chromium from `make setup` and the validated local
fixture from `make setup SETUP_MODE=database`. The lane builds current assets and
the executable, creates disposable databases per test, and cleans up owned
processes/databases. Reusing installed tools and the validated PostgreSQL fixture
keeps this repeatable without downloading dependencies or replacing the container
on every run. Persistent deployment commands are documented in the [README](../README.md).

F2 is the next slice: agent registration/token, Prometheus datasource and remote
diagnostics, SLI collection/health and direct ingestion. Management parity, remaining
identity administration, deployment packaging, restore and full UI qualification
retain their later-slice requirements; Google SSO remains F7.

F2 preparation is tracked in the [first observations plan](plans/0019-first-observations.md).
The first visible checkpoint is agent registration and credential management,
followed by actual agent connectivity. [F2-T01](tasks/F2-T01.md) supplies verified
local VictoriaMetrics and VictoriaTraces Compose sources: actual numeric Prometheus
and TraceQL results survive stop/start, and the combined Compose acceptance passes.
Agent management and collector implementation remain outstanding; source fixtures
do not constitute collection through Almanac.
[F2-T02](tasks/F2-T02.md) adds verified internal agent registration, credential
verifier persistence, atomic audit and safe retry receipts. Its integrated candidate
passes 73 web/105 Go unit tests and 75 real PostgreSQL integration tests. Management
service, HTTP/CLI/UI, token lifecycle and actual agent connectivity remain outstanding.
[F2-T03](tasks/F2-T03.md) adds the verified internal administrator proof and one-time
agent-token issuance service. Its corrected candidate passes 73 web/109 Go unit
tests and 77 real PostgreSQL integration tests, including existing browser journeys.
Public management, credential lifecycle and connectivity remain outstanding.
[F2-T04](tasks/F2-T04.md) adds verified safe agent/credential reads, revocation and
disablement storage. Its corrected candidate passes 73 web/115 Go unit tests and 99
PostgreSQL integration tests, including ordered pagination indexes and a populated
database upgrade/query-plan regression. Rotation, public management and actual connectivity
remain outstanding; internal storage completion is not the F2 user journey.

[F2-T05](tasks/F2-T05.md) adds verified credential rotation with bounded overlap,
safe retirement history and the remaining internal management service methods.
Its candidate passes 73 web/119 Go unit tests and 116 PostgreSQL integration tests,
including real lifecycle ordering and retry/authority boundaries. Public management
API, UI/CLI and remote agent connectivity remain outstanding.

The F2-T05 query-performance follow-up passes 73 web/119 Go unit tests and 117
PostgreSQL integration tests. A populated upgrade and actual generated-query plan
check verify that current-credential lookup skips inactive history for zero, one
and two current credentials while preserving deterministic locking and receipts.

[F2-T06](tasks/F2-T06.md) exposes agent registration, reads, credential lifecycle
and disablement through authenticated HTTP routes and generated contracts. Its
candidate passes 73 web/128 Go unit tests and 118 real PostgreSQL integration
tests, including TLS, executable and browser authority checks. Agent management
UI/CLI and remote connectivity remain outstanding. An intermittent pre-existing
restart-shutdown test failure has safe diagnostics now; its cause remains unproven,
and it did not recur in this candidate's real-stack gate.

The combined API/index revision exposed another intermittent backend shutdown
failure in the executable management test. Safe process-exit and shutdown-cause
booleans now preserve diagnostic evidence without raw logs. The diagnostic candidate
passes 119 real PostgreSQL integration tests, including executable/browser journeys;
this passing run does not establish the shutdown failure's root cause or a fix.

[F2-T07](tasks/F2-T07.md) adds a verified browser client for the management API,
with exact decimal versions, bounded/cancellable responses and explicit uncertain
mutation outcomes without automatic retry. Coordinator checks pass 98 web/128 Go
tests plus generated/build/production and process smoke checks. Screens and actual
browser agent-management acceptance remain outstanding.

[F2-T08](tasks/F2-T08.md) delivers administrator agent registration, paginated
list/detail/credential history and one-time token handling. Real HTTPS/PostgreSQL
browser journeys verify fresh issuance, rejected-password recovery, committed
response loss and same-key replay without token recovery, navigation and actual
session invalidation. Acceptance passes120 integration tests and114 web/128 Go
tests. Populated detail views were inspected in both themes at1440/1024/360px.
Credential lifecycle forms, CLI and connected-agent behavior remain outstanding.
An intermittent existing backend shutdown failure recurred during this work;
additional safe test diagnostics preserve its failure classifications, but its
root cause remains unproven and no shutdown fix is claimed.

[F2-T09](tasks/F2-T09.md) adds agent credential rotation, individual revocation
and agent disablement through the same management API. Explicit confirmations,
current-password proofs, original-key/version retries and one-time replacement
tokens are verified. The final candidate passes121 real PostgreSQL integration
tests and133 web/128 Go tests, including actual HTTPS/browser lifecycle actions
and persisted verifier/retirement/revocation/receipt checks. Empty lifecycle dialogs
were inspected in both themes on desktop and mobile. CLI authentication/profiles
and agent-command parity follow; connectivity and first observations remain
outstanding. PR review corrections preserve throttling across navigation and consume
revoke-dialog requests once, with135 web tests and focused coordinator verification.

[F2-T10](tasks/F2-T10.md) delivers reusable CLI login/context/logout and protected
Linux profiles. Final checks pass135 web/152 Go tests and145 race tests.
Real-executable/database acceptance passes121 tests. An intermittent browser rotation assertion
failed on the first integrated run and did not reproduce after adding precise
diagnostics; no browser fix is claimed. Agent-management command parity follows
this authentication boundary.

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
React/API packaging and measurable UI performance. Accepted ADR-0017 selects Tink
and its key/ciphertext format; encryption implementation remains outstanding. Web foundation checks now run;
Go, operational HTTP and PostgreSQL upgrade checks also run; full persistence
and product-journey acceptance remain outstanding.

## Next work and session handoff

- The initial accepted design is committed as `09cfdfd`.
- The maintainer selected a primary coordinator and `gpt-6-luna` implementers.
  Follow AGENTS.md and [ways of working](ways-of-working.md) on every new session.
- [Testing guidance](testing.md) requires focused iteration, independent coordinator
  review, a real-stack smoke gate and full boundary/release evidence. Web foundation
  timings are measured below; real-stack and later boundary gates remain future work.
- The [visual foundation plan](plans/0001-visual-foundation.md) and
  [F1-T01](tasks/F1-T01.md) are complete and integrated into `main` through `21505bd`.
  Its implementation worktree and ignored screenshots/review reports remain
  available for inspection.
- [F1-T02](tasks/F1-T02.md) and its [controls plan](plans/0002-catalogue-controls.md)
  are complete, integrated through `4a1f15b` with independent review and fresh checks.
- [F1-T03](tasks/F1-T03.md) and its [plan](plans/0003-charts-and-tables.md)
  are complete, integrated through `5ade04d` plus the reviewed test URL correction.
- [F1-T04](tasks/F1-T04.md) and its [runtime plan](plans/0004-go-runtime-foundation.md)
  are complete and integrated at `db1b9d4`. Preserve the main catalogue preview
  on port 5198 during checks.
- [F1-T05](tasks/F1-T05.md) and the [storage plan](plans/0005-storage-foundation.md)
  are complete, integrated at `28bafab`. PostgreSQL18.6 explicit upgrades and their
  isolated integration lane are verified. Next foundation work is local identity,
  protected onboarding and authenticated backend operations, within SPEC-0004.
- [F1-T06](tasks/F1-T06.md) is complete: reviewed internal password/session
  primitives at `334fb33`, with merged foundation fixes. It does not expose login
  or complete onboarding. Next is transactional initial-owner persistence.
- [F1-T07](tasks/F1-T07.md) is complete at `1f0bf0c`: atomic initial-owner persistence,
  stacked on the identity-primitives increment. Independent reviews and 14 real
  PostgreSQL integration checks passed; authentication endpoints remain subsequent work.
- [F1-T08](tasks/F1-T08.md) is complete at `ce1aec3`: offline common-password
  screening, stacked on initial-owner persistence. Full/race gates and independent
  reviews passed. Login/startup must apply the complete creation policy before
  accepting new passwords; persistent sessions are the next prerequisite.
- [F1-T09](tasks/F1-T09.md) is verified: persistent session issuance, validation and
  revocation at `0cccb8d`, stacked on password policy. Full gate passed30 UI/45 Go;
  PostgreSQL18.6 passed25 integration tests under race in5.731s. Task and final
  reviews approved the corrected authority and logout boundary cases.
  Login/throttling, first-password rotation and API/CLI/UI remain future work.
- [F1-T10](tasks/F1-T10.md) is verified at `493bde4`: atomic password rotation,
  session replacement and initial onboarding completion. Full gate passed 30 UI/46
  Go tests; PostgreSQL 18.6 passed 31 integration tests under race in 9.993s.
  Task and final reviews approved the corrected denial and rollback evidence.
  External password proof, shared login throttling and HTTP remain later.
- [F1-T11](tasks/F1-T11.md) is verified at `24810e5`: shared login admission and
  rolling failure limits. Full gate passed 30 UI/48 Go; PostgreSQL 18.6 passed 39
  integration tests under race in 13.030s. Review drove explicit READ COMMITTED
  transactions, with a reproduced over-admission regression now fixed. Application
  authentication and HTTP remain the next integration work.
- [F1-T12](tasks/F1-T12.md) is verified at `3d8fd7d`: protected initial setup, login,
  password rotation, authentication and logout through one backend service.
  Independent task and whole-branch reviews approved. Full gate passed 30 UI/52 Go;
  combined PostgreSQL 18.6 passed 51 tests under race. Final test-only additions
  passed 12 authentication integration tests under race in 10.086s. Public HTTP,
  cookie/CSRF protections and UI/CLI journeys are the next delivery work.
- [F1-T13](tasks/F1-T13.md) is verified at `dc6cfce`: generated authentication routes,
  protected cookies, session-bound CSRF and strict origin/source handling. Full gate
  passed30 web/65 Go, race62, and real PostgreSQL18.6 passed55 integration tests under
  race (HTTP12.243s). Task re-review and whole-branch review approved. Backend runtime
  exposure and UI/CLI follow this adapter.
- The maintainer merged PRs #2–#8; local `main` is `5cd60b2`, with the verified
  F1-T06–12 changes. The authentication HTTP increment remains separate.
- [F1-T14](tasks/F1-T14.md) is verified at `7b8c2ae`: backend-only startup, protected
  initial owner, TLS, database/setup readiness and single-owner lifecycle. Full gate
  passed30 web/75 Go, race69, and PostgreSQL18.6 passed62 integration tests including
  the real executable journey and shutdown/loss boundaries (command14.272s).
  Independent reviews approved. Combined serving, onboarding screens and CLI setup follow.
- [F1-T15](tasks/F1-T15.md) is verified at `9325a3a`: protected-file noninteractive setup through
  HTTPS, generated API operations and explicit uncertain-response handling. It builds
  on the backend runtime and does not introduce persisted CLI profiles or UI screens.
  Full checks passed30 web/98 Go, race92 and63 PostgreSQL18.6 integration tests
  (command8.774s). Independent reviews approved. The real executable journey covers
  rotation, repeat refusal and restart. An inherited shutdown fixture race was
  reproduced and corrected in the backend branch without changing timeout budgets
  or acceptance assertions; the setup branch includes that correction.
- [F1-T16](tasks/F1-T16.md) is verified at `94b2074`: one backend-owned HTTPS listener
  for the existing embedded UI and authentication API. Full checks passed 30 web /
  102 Go tests, race 95, and 65 PostgreSQL18.6 integration tests (command26.472s).
  Independent reviews approved. Both component orders and trusted TLS termination
  are exercised; browser onboarding screens and secure remote UI integration follow.
- [F1-T17](tasks/F1-T17.md) and [F1-T18](tasks/F1-T18.md) deliver browser sign-in,
  restricted initial-password setup and signed-in identity/logout in combined mode.
  Product `fd82f8a`, fixture/catalogue correction `75eb742`: 72 web/102 Go tests,
  race 95, real PostgreSQL 18.6 integration 66 (command 26.864 s), catalogue 96 (51.2 s).
  Task and final whole-branch reviews approve. Published as
  [PR #14](https://github.com/dsolsona/almanac/pull/14), stacked on the combined runtime. Actual
  Chromium 156.0.8078.4 screens were inspected at 360/1024/1440 px in both themes.
  Lost responses are exercised after real commits, without automatic replay.
  The PR review correction clears abandoned setup passwords immediately on logout,
  including failure. Updated verification passes 73 web/102 Go tests and 66 real
  PostgreSQL integration tests (command 26.805 s), including the logout/re-login
  regression. Independent scoped review approves.
- Follow accepted [ADR-0017](adr/0017-recoverable-secret-encryption.md) for encryption
  library/key format; verify tool/dependency versions when scaffolding.
- Documentation validation uses `python3 scripts/check-docs.py`. Web build, unit and
  catalogue browser entry points and Go/operational HTTP/database upgrade gates
  exist; local identity integration is covered, while agent integration remains future work.

## PostgreSQL foundation verification

PR #1 review corrections make the Go and race lanes build absent embedded web
assets before test discovery. The original Go failure was reproduced without
`web/dist`; corrected Go (30 tests) and race (27 tests) lanes passed from that
state. Storage integration already builds assets after storage-only discovery;
its six real PostgreSQL18.6 checks also passed from missing assets in 2.235s.

Isolated complete chart samples now render circles, partial samples use diamonds,
and shared chart/table styles load with the components independently of catalogue
styles. The combined `make verify` passed 30 UI tests, 30 Go tests (cached from
the preceding Go run), typecheck/vet/format, generation checks, production build
and process smoke. The focused chart browser matrix passed 42 checks across six
viewport/theme projects, including the actual isolated sample position and layout
with catalogue styles removed. Desktop-light and mobile-dark screenshots were
inspected. Intermediate test-harness failures are retained in the local review
report; no product assertions were weakened to obtain a pass.

Integrated-main `28bafab` passed `make verify`: 28 UI tests in 1.31s, 30 Go tests,
vet/format, both generation-drift checks, production build/routing/exclusion and
real-process UI smoke. Changed command/storage tests ran in 0.112s/0.003s;
unchanged config/HTTP package results were cached. The observed full gate took
about eight seconds with setup complete. `make verify VERIFY_MODE=go-race` passed
27 tests, with fresh command/storage runs in 1.120s/1.008s and cached HTTP results.

Six fresh PostgreSQL/executable integration tests with the race detector passed in
1.918s. They cover real concurrent upgraders, transaction rollback after DDL,
identity-preserving repeats, incompatible histories, observed lock cancellation,
real binary JSON/errors and SIGTERM exit6. Fixture checks verified PostgreSQL18.6
on loopback15487; no disposable test databases remained after cleanup. Missing
fixture configuration fails instead of skipping tests. PostgreSQL runs separately
from the ordinary unit loop; setup downloads tools/images and verification stays
offline. No browser matrix was repeated because UI behavior did not change.

Main binary: 17,703,680 bytes; gzip: 8,794,402 bytes; web assets: 371,292 bytes.
These include pgx and remain foundation measurements, not a full release benchmark.
The application dependency is pgx/v5 5.11.0; sqlc1.31.1 is a pinned generator.

Independent task and whole-branch reviews approved the change. One minor output
write-failure finding was fixed and approved by scoped re-review. Both T04 follow-ups
are resolved: a direct forced-shutdown regression and shared generator configuration.
Preserved first failures included setup/import issues, generator output precedence,
and a sandbox-denied chart subprocess; the latter passed in the coordinator's
approved execution environment without test changes. Preview5198 stayed available.
Backend readiness, credentials, observation storage, restore and complete F1
acceptance remain outstanding. Reports and implementation worktree are retained locally.

## Go UI runtime verification

Fresh integrated-main `make verify` passed 28 UI tests (1.31s), 13 Go tests
(command/config 0.003s each, HTTP 0.067s), typecheck, vet/format, generated drift,
production exclusion/routing, embedded binary build and real-process outage/SIGTERM
smoke. `make verify VERIFY_MODE=go-race` passed 10 tests (HTTP 1.088s, command
1.008s). No application runtime dependencies were added; generators are pinned
setup tools. The TypeScript generator has its own compatible compiler dependency.

Measured main binary: 11,883,808 bytes; gzip: 6,555,266 bytes; web asset tree:
371,292 bytes. Build paths affect binary size; these are UI-only foundation
measurements, not the final server-with/without-UI release benchmark. UI source
and behavior did not change, so the existing 90-check browser result was retained
and the runtime boundary was checked through real HTTP and process tests.

Review corrected invalid origin acceptance, unbounded failure waits and a
cancellation test whose safety timeout could cause a false pass. Removing the
forced close now fails that assertion at 0.52s, before safety deadlines. Tests
cover fixed-origin forwarding, traversal rejection, redacted outages, strict
config precedence/validation, embedded routing, in-flight drain and cancellation.
Unsupported backend/alert/combined modes fail before binding; no auth/DB readiness
or full API/CLI parity is claimed. Invalid package and generation selectors fail.

First failures included sandbox network/loopback restrictions, proxy path joining,
a readiness fixture, generator output selection/drift and unsupported-mode wording;
their corrections and verification are retained in task evidence. All required
integrated checks passed after correction. The two nonblocking final-review
follow-ups—automatic forced shutdown in command wiring and duplicated generator
options—were subsequently resolved and verified in F1-T05.

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
