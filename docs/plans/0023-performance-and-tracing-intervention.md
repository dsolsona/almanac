# Performance, accuracy and tracing intervention

- State: accepted; implementation in progress
- Accepted: 2026-10-31, explicit maintainer approval after Crit session `ede9414e6ca5`; all five comments resolved
- Created: 2026-10-31
- Priority: replaces the current execution order; unrelated feature delivery stays paused
- Owner: primary agent; delegation permitted only for test execution
- Existing work: [PR #48](https://github.com/dsolsona/almanac/pull/48), branch `perf/backend-stabilization`
- Governing records to amend: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [ADR-0024](../adr/0024-precomputed-operational-reads.md), [previous plan](0022-backend-stabilization.md)

## Required outcome

An accurate SLO application that feels fast when people use it, with a year of
history and 10, 100 or 1,000 SLOs in the installation. Retention is one year; the
proposed precise duration is 365 days, matching the existing `1y` graph range.
The old provisional 400-day value is not a product requirement of this plan.
Reconcile retention constants, partitions, derived boundaries and documentation in
the implementation. Preview/audit the cutoff before applying cleanup to existing
operator data; editing this plan does not delete any observations.

Lists show 20 records per page by default, with a 10-record option and a UI maximum
of 20. Pagination and search navigate the remaining records. Installation size
affects the acceptance fixture, not the number of rows rendered at once. Counts
and urgency grouping still cover the full filtered result, not just the page.

| User action | Required elapsed time | Preferred target | Completion means |
| --- | --- | --- | --- |
| Open, paginate or filter the SLO list | < 1,000 ms | <= 500 ms | Up to 20 matching rows, urgency groups and global filtered counts are visible and usable |
| Open, paginate or filter the SLI list | < 1,000 ms | <= 500 ms | Up to 20 matching rows and their summaries are visible and usable |
| Open an SLO detail page | < 500 ms | Required, not aspirational | Definition, status and the requested graphs are rendered and usable |
| Change graph range/burn window | < 500 ms | Required, not aspirational | The requested result is visible; retain the existing <= 200 ms feedback/chart-update budgets |

Measure from navigation/click through usable content, including network, backend,
JSON parsing, JavaScript loading and chart rendering. A shell, spinner, placeholder,
HTTP 200 or prepared-response percentile is not page-load acceptance. Record every
observed sample: a list sample >= 1,000 ms, or detail/range/burn sample >= 500 ms,
fails that run. Report p50/p95/max as well; do not turn the requirement into a
percentile that hides a slow first visit.

Normal navigation must not enqueue a job and poll for its completion. The same
requirement applies on a browser-cache miss, after a minute boundary, after new
observations arrive, after backend restart with retained data, and when another
user chooses a different range. Explicit historical import or disaster recovery
may be background operations, but cannot be relabelled as ordinary page loading.

Preserve exact counts and calculations, target equality, negative budgets, original
window bounds, unknown/empty/zero-traffic distinctions, extrema, gaps, authorization
and immutable exports. Faster but incorrect, rounded or stale-as-current is failure.
These requirements remain release constraints for every subsequent feature.

## What must change

The current candidate caches a complete graph result against exact request times,
source generation and retention boundary. A miss enters a durable preparation
queue and the browser retries after two seconds. Display blocks can be reused, but
their bounds also follow the moving requested range. Therefore a previously opened
SLO can still enter preparation on an ordinary later visit. This design does not
meet the requirement above. It is not accepted by this plan merely because its
already-prepared responses are fast.

The existing local Compose deployment already includes VictoriaTraces. There is
no application OpenTelemetry SDK or browser-to-backend trace propagation in the
current dependency/source configuration. The internal metrics listener does not
provide the missing journey traces.

Canonical SLI observations are retained in the database. The candidate also stores
current SLO projections and calculated history payloads/display blocks, but the
history records are request-oriented caches with five-minute expiry/cleanup. That
is not a continuously maintained one-year SLO history. The durable historical
burn/budget requirement below therefore remains work to implement and verify.

## Proposed technical direction

### Maintain SLI and SLO time series independently of page requests

An SLI is a time series: each immutable observation identifies its SLI/revision,
interval start/end, exact good/total counts and coverage outcome. PostgreSQL remains
the proposed initial store, with time partitions, indexes for SLI/revision/time
range reads and exact reusable summaries. A time series does not require a separate
database product; choose a storage change only from measured query/size evidence.

Maintain three distinct read structures:

| Structure | Updated when | Read by |
| --- | --- | --- |
| Canonical SLI observations and exact source summaries | New/late accepted observations; duplicates contribute nothing | Calculation, audit and bounded exact drill-down |
| Latest SLO summary and recorded SLO time series | Affected ingestion, rolling-window expiration, eligibility/freshness boundaries or definition/exclusion change | Lists, current status, alerts and graph data |
| Stable graph-resolution summaries/tiles | Their underlying SLO points change | Bounded graph range requests, independent of page visits |

Persist the SLO time series for the full one-year history, independently of
ephemeral browser/HTTP caches. It includes attainment, error budget remaining
(fraction and exact event amount), and historical burn for both supported 1-hour
and 6-hour calculation windows. A graph may show the 1-hour burn rate over a month
or year: the displayed history period and the burn calculation window are separate
parameters. Selecting a one-year view does not mean calculating one-year burn.

Key recorded points by objective, evaluation time, relevant definition/measurement/
exclusion revisions and metric/window. Retain exact values, coverage/qualification,
source generation and calculation provenance. Store reusable resolution summaries
separately; repeated metadata can belong to a versioned stream rather than every
point. The latest summary is an indexed read of the corresponding current result.
Restart, cache eviction or an unvisited page must not erase historical SLO points
or require reconstructing the whole year before rendering it. Late data corrects
only its affected evaluation ranges with coherent publication; definition changes
must not silently mix incompatible histories. Preserve pinned immutable exports.

Current attainment, remaining error budget and both burn windows should already be
calculated when read. Advance exact window state incrementally for affected SLOs
when observations arrive; expire old contributions as windows move. Publish coherent
revision-fenced results and record corresponding historical points. Do not recalculate
every SLO from its full history on every arrival. Preserve separate live admission
and bounded maintenance; measure commit-to-visible lag with OTEL.

Current values are not always constant until the next ingestion: an old failure
can leave a 1-hour burn window or a 28-day objective window while no new observations
arrive. Coverage/freshness can also change. Schedule those due time boundaries so
the cached result remains truthful; lack of new data must not manufacture success.
Historical values at a fixed evaluation time remain reusable unless late data or
a relevant revision changes their inputs. The current candidate already has a
worker maintaining current summaries; this proposal retains that useful separation
and removes navigation-triggered graph preparation.

A 28-day, one-minute SLI contains 40,320 nominal intervals. Rendering a graph does
not require sending all of them to the browser. Choose resolution from the time
range and graph width, with <= 1,500 plotted points per numerical series. Read
stable precomputed resolution summaries and preserve first/last/min/max and gap
boundaries; zooming requests finer resolution. Exact values remain available by
bounded pages/drill-down. Never use averaged percentages to calculate attainment,
budget or burn: calculate from exact source counts and the specified windows.

Returning 1,500 points alone is insufficient: the backend must not fetch all
40,320 raw intervals, or a year's observations, just to reduce every request.
OTEL and query plans must show bounded indexed reads and unchanged tile reuse as
the requested range advances. Retained SLO points can preserve previously computed
year-boundary results; missing expired source inputs cannot be fabricated during
later recalculation. Measure raw range retrieval separately for drill-down without
making it a dependency of normal graph rendering.

The product references are Nobl9, Grafana SLO and Honeycomb SLOs. Grafana documents
recording SLO metrics for dashboards and bounding query points by panel width;
Nobl9 documents SLI/reliability/budget/burn outputs; Honeycomb documents event-based
SLOs with current and historical burn views. These support the product direction;
they do not prove undisclosed vendor storage internals or Almanac's performance.
See the linked primary references below.

### Historical graph acceptance references

Use the maintainer-provided files in the main checkout's `evidence/` directory as
behavioral references. They are separate from the existing page-composition rules;
this intervention does not require copying another product's layout.

| Reference | Required graph capability |
| --- | --- |
| `evidence/nolb9/burn-rate-io.png` | Burn rate across a historical range, including peaks and the 1x reference line |
| `evidence/nolb9/rbd-io.png` | Reliability/attainment history with a visible target reference, preserving dips |
| `evidence/honeycomb/slo-fullscreen-display.webp` | Error budget remaining/burndown and historical compliance over time, alongside current summary values |
| `evidence/honeycomb/slo-historical-burn-rate.avif` | Historical burn-rate reference supplied by the maintainer; inspect through an AVIF-capable viewer before claiming visual comparison |

The PNG/WebP references above were inspected. The current image viewer cannot
decode the AVIF reference; its visual comparison remains pending. Preserve all
maintainer-provided files in place; they are not currently tracked in this worktree.
Their presence is not evidence that Almanac implements the illustrated behavior.

Demonstrate burn, budget and attainment across 28-day and 365-day views using
retained calculated history, with both burn windows selectable and the underlying
exact values available on demand. Verify historical incidents, negative/exhausted
budgets, recovery and data gaps survive graph reduction. Do not infer new budget
reset semantics from a screenshot; the existing calculation/revision contracts
still govern the values.

### Serve bounded foreground reads

1. **Read reusable data immediately.** Keep fixed-time source summaries and derived
   points/tiles independent of the browser's exact range. Maintain them as data
   changes. Assemble a bounded graph response from those records and a bounded
   recent/partial tail during the request. Moving the right edge must not invalidate
   unchanged historical work or require a newly saved whole-page response.
2. **Bound list work.** Read indexed current summaries, page 10 or 20 rows and calculate
   global counts from compact indexed metadata. No per-row history calls, raw
   observation scans, or assembling 1,000 complete detail payloads before pagination.
   One failed objective must not block the rest of the list.
3. **Preserve accurate graphs.** Reuse ordered rolling-window algorithms, prefix
   coverage accounting, clean hourly summaries and exact partial-edge handling.
   Retain stable derived extrema/gap blocks where hourly sums alone are insufficient.
   Do not replace minute-level extrema with hourly averages. Verify actual plans and
   work counts before choosing finer storage/index changes.
4. **Separate caches from prerequisites.** Cache hits may accelerate reads, but a
   missing finished HTTP response cannot make normal navigation wait on a worker.
   Keep current status independent. Retain confirmed visible data during refresh
   with its actual evaluation time; never present invalidated results as fresh.
5. **Keep payload and rendering bounded.** Return <= 1,500 points per numerical
   series; avoid repeating large quality/count structures per plotted point when
   shared metadata suffices. Fetch bounded exact-value pages on demand, preserving
   decimal strings. Measure compressed bytes, parse cost and render cost before
   proposing an additive compact wire format. Preserve existing API compatibility.
6. **Keep heavy work in the background.** Imports, repair, historical recomputation
   and compaction retain durable work and separate admission. They must not exhaust
   live collection or interactive resources. Measure the supported lag and recovery
   envelope; do not claim a queue is fast enough without actual journey evidence.

This direction was accepted in Crit. If profiling proves a storage change is necessary,
record its schema/index, write amplification, retained size, migration and rollback
cost before implementing it. Do not preserve a failing design to avoid changing it.

## Execution order and concrete deliverables

Implementation checkpoint: [F3-T09](../tasks/F3-T09.md) provides verified tracing
and the initial normal-container baseline, including enabled/disabled costs and
retained slow samples. [F3-T08](../tasks/F3-T08.md) remains responsible for the
read-path correction and the required latency, retention, pagination and scale
acceptance. Tracing acceptance does not complete this intervention.

### 1. Record the contracts and establish an honest baseline

- Amend SPEC-0019's 2.5-second detail budget, ready-only measurement and 100-SLO-only
  scale fixture. Add the table above, 10/100/1,000 installation scale, 10/20-row UI
  pagination, required < 500 ms detail/range loads, and first-visit/no-job-wait
  acceptance. Amend SPEC-0007 and ingestion/storage retention contracts to one year
  with an explicit cutoff and impact review. Keep mathematical, security and
  collection guarantees. Preserve existing non-UI client pagination compatibility
  unless its contract change is separately specified.
- Add an ADR partially superseding ADR-0024's request-triggered preparation policy.
  Preserve its original rationale and unaffected pool, precision and
  durability decisions. Update SPEC-0010/0012/0014 and testing/delivery guidance to
  reference the new requirements; explicitly reconcile conflicting old clauses.
- Record running image/source identity, host resources, database/schema, row counts,
  browser/network settings and authenticated URL. Exercise the three pages in the
  actual production build. Capture the first visit and a repeat, including a visit
  after the minute boundary. Do not rebuild or warm the page before recording it.
- Obtain operator access through the normal signed-in flow; do not reset passwords,
  fabricate sessions, change source data or use operator volumes as test fixtures.

Deliverable: a short baseline table with elapsed time, waterfall/trace reference
where available, correct visible values and the dominant delay for each page.
Missing access or evidence is an explicit gap, never a passing result.

### 2. Instrument the application and make traces usable locally

Use the standard OpenTelemetry Go and browser SDKs with W3C Trace Context and
OTLP/HTTP. Pin compatible dependencies and document bundle/runtime cost. Prefer
direct backend export to the existing VictoriaTraces service; a new collector is
not required for the initial implementation.

| Layer | Required spans and attribution |
| --- | --- |
| Browser | Navigation/action root; route/definition loading; fetch; parse; chart preparation and committed paint; result/error/cancellation |
| Backend HTTP | Route-template server span; auth/session work; handler/service; encoding/compression; request cancellation/errors |
| PostgreSQL | Pool acquisition separately from query/transaction time; stable query name; rows read/returned and traffic class |
| SLO engine | Summary evaluation; source aggregate reads; exact edge reads; rolling calculation; tile/block reads/reuse; response assembly |
| Background work | Admission, queue wait, claim, execution, publication and retry/failure; link to triggering work without holding an HTTP span open |
| Agent and CLI | Configuration/heartbeat, provider requests, collection, delivery/ingestion and CLI HTTP calls; propagate context across process boundaries |

Implementation touchpoints: a small owned telemetry package; backend lifecycle and
HTTP composition; `internal/storage/runtime.go` and typed query boundaries; SLO
services; agent HTTP/provider clients; CLI clients; browser navigation/fetch/cache
and chart completion; `compose.yaml` and operator documentation. Cover normal
management, authentication, ingestion and failure paths, not only SLO endpoints.

- Configure local backend export to
  `http://victoriatraces:10428/insert/opentelemetry/v1/traces`. The host trace UI is
  `http://localhost:19428/select/vmui`. Verify both against the pinned deployed
  version before announcing readiness; do not confuse trace ingest with provider
  query endpoints.
- Export browser spans through a fixed same-origin authenticated telemetry intake,
  bound to the configured exporter destination. Enforce origin/session/CSRF policy,
  body/batch/rate limits, allowed attributes and sanitization. It is not an arbitrary
  proxy, and browser code never receives backend exporter credentials.
- Use resource service names `almanac-web`, `almanac-backend`, `almanac-agent` and
  `almanac-cli`, with build revision/environment. Record fixture/run identity and
  bounded range/cache/result attributes. Keep trace IDs available for debugging.
- Do not export passwords, sessions, tokens, SQL parameters, raw queries, source
  credentials, unbounded URLs or arbitrary request bodies. Avoid dynamic span names
  per objective/user. Exclude exporter traffic from its own instrumentation.
- Use bounded batch export, bounded flush/shutdown and explicit dropped/export-error
  metrics. VictoriaTraces failure must not block page loads or collection. Evidence
  capture fails visibly if required traces are absent or incomplete.
- Capture 100% of local acceptance journeys with parent-based consistent sampling;
  production sampling is configurable and must be disclosed in reports. Measure
  instrumentation enabled versus disabled; proposed p95 overhead allowance is <= 5%
  and it may never push a journey past its table-specific 500/1,000 ms limit.

Deliverable: retrieve one complete browser -> HTTP -> database/calculation trace
from VictoriaTraces, show where the time went, and retrieve an agent -> ingestion
trace. Prove rendering occurs inside the journey's measured completion boundary.
Merely sending spans, or looking only at a server root, does not satisfy this step.

### 3. Correct the read path using those traces

- Start with the actual slow detail navigation. Fix the dominant cost, then repeat
  that same authenticated action before broadening the work. Record before/after
  trace IDs and visible values. Do the SLO and SLI lists next.
- Replace whole-response timestamp coupling and request-triggered preparation in
  `derived_history.go`, its SQL, browser time selection and pending-read handling.
  Establish stable historical tile/point keys and bounded response composition.
  Keep exact-value generation pinning and truthful evaluation timestamps.
- Add retained historical SLO point storage, time-range indexes, incremental
  publication and resolution summaries for budget, attainment and each supported
  burn window. Separate their one-year retention from five-minute response-cache
  expiry. Document migration/bootstrap cost, historical gaps and late-data repair;
  do not claim that the existing short-lived history cache satisfies this step.
- Inspect raw fact access, shifted block reuse, repeated metadata validation,
  JSON size, compression, code loading and chart rendering individually. Do not
  assume that changing a query or adding a cache fixes the full page load.
- Check both fresh and missing response-cache paths. With retained source summaries
  present, a cold browser/response cache must return useful graphs in the first
  response. Restart must not turn ordinary reads into graph-preparation polling.
- Keep arithmetic independent and deterministic. Compare plot points, min/max
  spikes, budget, both burn windows and exact tables against independently derived
  expected values, including late data and non-hour edges. Traces establish time,
  not mathematical correctness.

Deliverable: lists load in < 1,000 ms and detail/range/burn actions in < 500 ms
on the current deployment and isolated candidate. No implementation-complete claim
before these action-specific requirements are demonstrated.

### 4. Resolve PR #48 review findings in the corrected design

These are findings to validate and resolve, not claims that fixes exist:

| Greptile thread | Required disposition and evidence |
| --- | --- |
| [Queue limits can overflow](https://github.com/dsolsona/almanac/pull/48#discussion_r4431250180) | Enforce retained background admission atomically with a fresh counter/serialization contract. One focused concurrent database case must prove both caps. Removing foreground queueing alone does not fix retained background admission. |
| [Blocked objectives stop healthy ones](https://github.com/dsolsona/almanac/pull/48#discussion_r4431250183) | Persist fair next-attempt scheduling for blocked dependencies. Prove healthy work progresses past a batch of >= 100 blocked objectives. |
| [Failed work looks pending](https://github.com/dsolsona/almanac/pull/48#discussion_r4431250185) | Return an honest failed/unavailable state, isolate it to the affected objective and keep unrelated rows usable. Verify the actual list and status behavior. |
| [Dropped collection can pass](https://github.com/dsolsona/almanac/pull/48#discussion_r4431250187) | Prevent missing intervals/failed attempts from producing an isolation pass. Deterministic healthy-source acceptance requires no unexplained losses; report per-agent completeness and baseline/load separately. |

Reply in each review thread with the validated fix/evidence, or explain a finding
that no longer applies and link its replacement behavior. Update the existing PR,
its description and CI evidence; do not open unrelated feature PRs or merge it.

### 5. Establish a reusable performance and accuracy acceptance fixture

- Exercise 10, 100 and 1,000 SLOs with one year of retained history; include both
  shared and independent SLIs. Do not demonstrate 1,000-SLO performance solely by
  duplicating objectives over one tiny source. Record counts and distributions.
- For sizing, 1,000 independent SLIs at one-minute cadence over 365 days represent
  525.6 million nominal intervals before gaps. Measure storage and setup cost on a
  smaller representative seed and check disk/CPU/RAM before the full fixture.
  Provision once, reuse retained fixtures and report setup separately. Replace the
  400-day fixture with one-year boundary/expiry checks, including cutoff alignment
  and historical rolling-window results at the retained boundary.
- Test 10/20-row first/next/filter pages and global urgency counts; detail ranges
  7/28/90/365 days; both burn windows; exact-value pages; new/late observations;
  restart; concurrent collection and ten interactive users. Keep outages and blank
  derived-store disaster recovery separate and honestly labelled.
- Prove a month/year burn and budget graph still loads from retained SLO time
  series after response-cache eviction and backend restart, even if the page has
  never been visited. Verify both supported burn windows, exact sampled historical
  values, year-boundary retention and coherent correction after late data. Include
  this durable-history fixture's storage/write cost in the capacity evidence.
- Use production builds, the normal container deployment, real authenticated HTTP,
  real PostgreSQL and rendered browser pages. Preserve both themes/mobile and
  keyboard usability. Use the existing documented browser CPU/network fixture;
  report those settings with every result rather than silently relaxing them.
- Capture first visits and warm visits independently. For the final scale matrix,
  collect at least 20 page-load samples per critical page and 50 interactions, plus
  the existing >= 100 API samples per operation. Retain all slow/error samples.
  Freeze fixture data/source revision for comparison; do not pre-request the page
  to manufacture a warm first visit.
- Query VictoriaTraces for the recorded run and join journey, request and DB spans.
  Compare browser monotonic duration with trace duration; cross-process clock skew
  cannot manufacture a passing result. Retain trace IDs/links, p50/p95/max, failures,
  query/row counts, payload bytes, cache state and independently checked values.

Deliverable: a source-bound report proving accuracy and page speed across the
matrix, with no ordinary navigation waiting for job execution. Resource shortfalls,
missing traces or failed checks remain failures/gaps, not green placeholders.

## Change the development loop permanently

1. **Before editing:** identify the affected user journey and capture its current
   trace and visible result. For a reported defect, use the reported inputs.
2. **During edits:** implement and inspect that journey. Add only focused tests for
   concrete arithmetic, boundary, security, persistence or reproduced-regression
   risks. Each added test must name the failure it can detect.
3. **Before accepting a feature:** run a small persistent-stack replay of the three
   critical journeys and the feature's changed path; compare trace timings and
   independent expected values against the source-bound baseline. Proposed warm
   replay budget: <= 90 seconds, with setup outside that budget. Retain the required
   < 1,000 ms list and < 500 ms detail/range limits; flag > 10% p95 regressions for
   investigation even below the relevant limit.
4. **At the completed candidate/PR boundary:** run the relevant database/concurrency,
   protocol and Compose suites once. Reuse matching passes for publication. After
   a failure, diagnose and use a focused check; repeat only the affected gate on a
   corrected candidate. The full year/scale/saturation run belongs here when the
   read model, storage, calculation or resource admission changes, not every edit.
5. **Enforce in CI and reviews:** a feature cannot be accepted with incorrect values,
   slow real journeys, missing trace coverage or a failed required boundary check.
   A unit-test count is never a substitute. Missing/zero selected journeys fail.

Audit slow/redundant tests by what they detect. Consolidate repeated process builds,
database setup and duplicate journeys. Keep the small valuable transaction/race
cases that protect real invariants. Propose deletion or rescheduling with the
replacement evidence; do not silently discard correctness coverage to appear fast.
Record command, invocation count, failures, setup/execution/cleanup and cumulative
invocation versus occupied wall time. Investigate a check that exceeds its budget;
do not spend repeated long runs discovering the same problem.

## Commands, review and scope controls

Use existing exact approved invocations. Dependency/setup changes and any new fixed
journey replay entry must be concrete, bounded and reviewed before execution. Do
not hide the new workload in an existing selector, add shell passthrough or request
random terminal approvals. This plan does not automatically approve new commands.
The new replay operation must specify its fixed journeys, <= 90-second runtime,
isolated fixture identity, protected credential handling and artifact destination.

No implementation/review subagents; a test-only worker may run a selected check
while the primary does independent useful work. Do not run a test merely to keep a
worker busy. Do not add more feature work during this intervention. Do not change
operator data/credentials, deploy, commit, push or publish further changes without
the applicable session authorization. Crit approval settles the design; it does not
retroactively establish performance or authorize merging PR #48.

## Review decisions

The maintainer approved this plan, including the technical direction,
instrumentation intake, overhead/regression thresholds, fixture execution and
contract amendments, after resolving all five comments in Crit. Execute steps 1-5
in order, continuing through reviewable increments without repeated design approval.
[ADR-0025](../adr/0025-fast-operational-journeys-and-tracing.md) records the decision.

## Technical references

- [VictoriaTraces OTLP ingestion](https://docs.victoriametrics.com/victoriatraces/data-ingestion/opentelemetry/)
- [VictoriaTraces trace UI and query API](https://docs.victoriametrics.com/victoriatraces/querying/)
- [OpenTelemetry Go instrumentation](https://opentelemetry.io/docs/languages/go/instrumentation/)
- [OpenTelemetry browser instrumentation](https://opentelemetry.io/docs/languages/js/getting-started/browser/)
- [Grafana graph resolution and maximum data points](https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/query-transform-data/)
- [Grafana SLO recorded metrics and dashboard reads](https://grafana.com/docs/grafana-cloud/observe-and-act/alert-and-measure-reliability/slo/set-up/additionaldatasources/)
- [Nobl9 SLO calculations](https://docs.nobl9.com/guides/slo-guides/slo-calculations/)
- [Honeycomb event-based SLOs](https://www.honeycomb.io/platform/slos)
- [Honeycomb current and historical burn-rate views](https://changelog.honeycomb.io/burn-rate-on-the-slo-page-276120)
