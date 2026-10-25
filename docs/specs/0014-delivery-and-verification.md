# SPEC-0014: Delivery and verification

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: in progress
- Related specs: [system overview](0001-system-overview.md), [runtime](0013-runtime-and-operations.md), [providers](0015-datasource-providers.md)

## Accepted amendment

Crit review `0de0c0e42db6` adds [SPEC-0017](0017-slo-timeline-annotations.md)
to F3 and [SPEC-0016](0016-maintenance-exclusions.md) to F4. Maintenance exclusions
are part of initial delivery; the sequence and checks below reflect that approval.

## Purpose

Reach a working application through complete demonstrable increments. Specifications
define intended behavior; implementation status and evidence are recorded separately.
The sequence below follows the accepted contracts; explicitly deferred decisions
must still be resolved before their affected implementation.

## Delivery slices

| Slice | User-visible result | Governing contracts |
| --- | --- | --- |
| F1 Foundation | Repeatable local stack, health/readiness, protected setup, local admin login, design catalogue, CLI/API skeleton | SPEC-0004/0010/0012/0013 |
| F2 First observations | Register agent/token; create Prometheus datasource; remote test and query preview; create SLI; see collected counts and health; direct ingestion works | SPEC-0002/0003/0007/0009/0015 |
| F3 First objective | Define event-count SLO; view budget/burn/coverage/history and daily CSV/JSON; import/export supported OpenSLO; graph annotations; corresponding CLI operations | SPEC-0005/0006/0010/0012/0017 |
| F4 Recovery and notifications | Bounded offline buffer, periodic/manual repair, backfill isolation, webhook burn/data-quality alerts, notification silences, maintenance exclusions and history | SPEC-0008/0011/0016 and ADR-0003 |
| F5 Complete initial release | Local users, full agreed roles/keys, supported slice/composite calculations, inactive import diagnostics, separate runtime modes, Docker/Helm setup and restore evidence | All accepted contracts in release scope |
| F6 Second source | Tempo metrics executes alongside Prometheus on appropriately capable agents, including bounded historical queries | SPEC-0015 and provider fixtures |
| F7 Google sign-in | Invite-only Google SSO, explicit account linking and browser-approved CLI login | SPEC-0004 Google follow-up and matching UI/API contracts |

F3 is a development preview, not the full promised product. F5 is the first release
candidate, with native Prometheus and external producers; F6 validates native disparate
source execution. Scoped machine permissions are part of initial release completion.
Google SSO follows in F7 and does not block a release with local database users.
It remains a product requirement. CLI/API parity is delivered within each slice.

## Explicit initial exclusions

Proposed initial release limitations must be visible in product documentation:
cross-source numerator/denominator within one SLI, unsupported OpenSLO versions,
some calendar/composite combinations, automatic activation of imported alert policies,
retrospective observation/query corrections, backend active-active replication,
durable local agent spool, Google SSO (planned F7), Elasticsearch adapter, native paging/email channels,
recurring silences, and PDF/email reports. Valid unsupported OpenSLO input is retained
inactive only where SPEC-0005 permits it. Do not advertise universal compatibility.

All three budgeting methods and the supported composite forms are specified.
F3 delivers single-indicator request-count objectives first; composites are a separate
F5 increment and must not delay demonstrating that first working SLO flow. Initial burn-alert
bindings are restricted as SPEC-0011 states. Report that limitation separately from
whether an SLO can be measured.

## Reuse review and implementation discipline

For each slice, inspect candidate components and tests against its accepted contract.
Record keep/adapt/replace judgments in the task plan without publishing private
investigation history. Preserve suitable algorithms and shared visual components,
but revalidate interfaces, assumptions and dependencies here. Existing tests are
useful evidence inputs, not proof of Almanac behavior.

No broad code import before contract review. Prefer small changes with traceable
acceptance criteria. Resolve architectural contradictions before product code.
Use Modern Go Guidelines for the selected Go version; keep formatting, static analysis,
generated-code consistency and meaningful tests in the eventual verification workflow.

## Verification workflow

[Testing guidance](../testing.md) maps this feature matrix to focused edit checks,
independent task review, the integrated-candidate gate and broader release checks.
F1 delivers runnable focused/full entry points and records setup/build/test timings.
Preserve real authenticated UI/API/database/agent evidence without making every edit
run the full browser matrix. Initial time budgets are measurement targets, not a
claim of tested throughput or authorization to remove acceptance coverage.

## Verification matrix

| Boundary | Required evidence before release |
| --- | --- |
| Pure math | Exact numerical examples; randomized ordering/chunk invariance; missingness, DST, revision and rounding boundaries |
| PostgreSQL | Transactions, duplicate/conflict races, receipt cleanup, lease fencing, partition retention, crash/restore and secret decryption |
| Agent protocol | Version/capability incompatibility, duplicate processes, missed notices, heartbeat stalls, reconnect, revocation and forced takeover |
| Ingestion | Both producer types, partial batches, lost responses, terminal/retryable failures, conflicting replay and precision boundaries |
| Recovery | Overflow/restart loss, bounded scans, exhausted retry persistence, manual retrigger, actual unavailable provider history |
| Live isolation | Saturated historical queries/submissions, provider throttling, memory accounting, no live slots blocked by historical work |
| Identity | Concurrent bootstrap, restricted initial login and mandatory rotation, roles, token expiry/rotation/revocation, CSRF and secret redaction; Google account linking/admission in F7 |
| Alerts | Deterministic paired windows, no-data recovery, stale evaluation fencing, outbox crash points, duplicates/order and silence boundaries |
| UI/CLI | Shared operation outcomes, both themes/mobile, keyboard/focus, exact chart tables, SPEC-0012 bundle/load/interaction budgets, noninteractive JSON/exit behavior |
| Packaging | Combined/separate modes, real Docker startup/restart, Helm rendered/install smoke evidence, upgrade/readiness/restore |
| Maintenance/annotations | Exclusion arithmetic and revision races, historical permissions, raw/adjusted reports, alert fencing, event idempotency, dense accessible overlays and unchanged math for notes |
| OpenSLO | Each compatibility matrix row, invalid schemas, inactive graphs, atomic imports and semantic export |

Use isolated ephemeral databases and provider fixtures. Destructive/failure checks
must never target an operator's live deployment. Fix test clocks and seed inputs for
repeatability; include real-process/provider checks where mocks cannot establish behavior.

## Initial workload envelope to validate

Start with a measured envelope of 100 SLIs at 60-second frequency, 10 agents, and
10 interactive users; add one history job per datasource while live work runs.
These are test scenarios, not published capacity guarantees. Track p95/p99 ingest
latency, live scheduling lateness, memory high-water marks and DB contention.
Under injected historical saturation, historical admission must pause before consuming
reserved live slots. A slow upstream can itself miss deadlines; attribute the cause
and demonstrate historical work is not the queue blocking live execution.

Set numeric service objectives only after baseline measurements on a documented
machine/database. Provisional verification bound: agent memory remains within stated
buffer budgets plus measured bounded runtime overhead; no unbounded retries or queues.

## Completion and evidence

A slice is complete when its acceptance criteria pass at the current revision,
generated contracts agree, documentation reflects reality, known exclusions are
explicit, and a reviewer can run its demonstrated flow. Record exact command, result,
revision, environment and artifact link in `docs/status.md`; do not claim commands
exist before implementing them. Re-run relevant checks after changes, not indefinitely
after the same passing revision.

Release also requires dependency/license inventory, pinned build inputs, vulnerability
review with findings triaged, reproducible artifact checksums, protected configuration
examples and a restore runbook. Select the repository license before copying assets
with redistribution requirements; preserve required notices. This is a release task,
not a reason to invent legal conclusions during design.

## Acceptance criteria

- AC-01: Each release requirement maps to a spec, delivery slice and verification
  category; exclusions are documented rather than silently dropped.
- AC-02: Each completed slice has reproducible current evidence and UI/CLI parity.
- AC-03: Runtime/collector/provider/load guarantees are backed by appropriate tests,
  not inferred from diagrams, mocks or documentation review.
- AC-04: First release includes initial-password onboarding, local users, scoped credentials, remote
  collection, direct ingestion, SLO/reporting, annotations, maintenance exclusions,
  repair and alerts within declared limits.
  Google SSO remains explicitly scheduled for F7.

## Review notes

- **REVIEW-V01:** Confirm F5/F6 release split and first-release restrictions. This is
  the main scope decision for reaching a working version quickly.
- **REVIEW-V02:** Capacity scenario and verification bounds are provisional. Production
  sizing must follow measurement; the spec makes no supported-throughput claim today.
- The shared catalogue, UI-only Go runtime and explicit PostgreSQL upgrade
  foundation have build, unit, browser, HTTP/process and database evidence in
  [delivery status](../status.md). Full F1 onboarding and later product journeys
  remain unimplemented; no release or workload acceptance is claimed.
