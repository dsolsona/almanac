# Performance investigation: alerting prerequisites

Updated 2026-11-14. Owner: primary agent. Work is tracked in
[F4-T14](../../tasks/F4-T14.md) and [plan 0045](../../plans/0045-alerting-performance-investigation.md).

The earlier [instrumented baseline-only control](2026-11-13-instrumented-baseline.json)
at `925d156` uses 200 SLIs/SLOs, live collection, 1,400 historical SQL batches,
ten API clients and one cohosted browser. All 800 baseline batches and all 80
browser journeys complete; the proposed fallback and raw oracle are absent.
All 400 due intervals are present. Authentication succeeds, but overview admission
and latency plus ingestion isolation still fail. The database observer failed
before the measured phases; resource samples and application traces are retained.
The [earlier baseline](../alert-calculation/2026-11-13-baseline-only-control.json)
remains evidence, but its browser stopped after 27 journeys, so timings are not an
unchanged-work comparison.

## Scheduler and readiness control

[F4-T15](../../tasks/F4-T15.md) and [F4-T16](../../tasks/F4-T16.md) are measured
in the [combined control](2026-11-13-scheduler-preflight-control.json). The normal
seed passes in 254.995 s; the control fails in 249.851 s. All 800 baseline batches,
1,400 backfill batches and 80 browser journeys complete. All 400 due intervals are
observed with zero unsuccessful ingestions. Two overview requests fail interactive
pool admission at 250.106/251.305 ms, before page SQL. Cold-list p95 is 1,993.6 ms;
ingestion p95 is 18.315–19.231 ms alone and 75.386–79.156 ms loaded.

The observer covers both phases without errors (625/1,614 database samples).
Ingestion-only PostgreSQL averages 49.48% of one CPU, down from 98.49% in the prior
control. Due/selection query means are 39.544/35.611 ms, but faster scheduling also
increases their frequency: 474/178 calls, 25.083 s cumulative query time. All 8,226
waiting current evaluations skip canonical sums; cumulative duration is 80.318 s
and 776 current publications complete. This verifies the intended mechanisms,
while different fixture dispatch phases and publication rates limit broader
before/after attribution.

Loaded overview page SQL p95 remains 665.576 ms. Correlated samples predominantly
show active work without a wait event (406 samples); I/O/client-write waits are
rare on that query. Across the database, transaction-lock waits are sparse and
WAL/I/O/client waits also occur. Sampling is not CPU profiling; `track_io_timing`
is off. The next investigation separates actual prepared-query planning,
execution, serialization and client transfer before choosing another correction.

**Prepared-plan diagnosis:** the
[bounded single-reader and eight-reader probes](2026-11-13-overview-planning.json)
use the actual pgx-cached query and identical complete ordered results. The default
policy chooses five custom plans, then a generic plan. Warm generic planning is
0.037–0.089 ms, with 12.46–14.38 ms server execution including serialization;
repeated 22-ms planning is therefore not the steady path in that snapshot.
In the eight-reader ABBA comparison, automatic cohorts take 1,601/1,580 ms versus
831/858 ms with forced generic planning. All 192 executions match. This proves
initial custom planning contributes to a concurrent startup burst, but forced
generic first calls still reach 287 ms and do not reproduce the loaded 620–758-ms
initial calls. No production plan-mode change is adopted.

Loaded traces show the first five observed calls on each of eight backend PIDs
averaging 492 ms, versus 174 ms for the later 136 calls. Those ordinals are not
actual server plan counters, and later p95 remains 478 ms. The next bounded
experiment tests SQL reshaping that reuses scalar selection and postpones metadata
construction until after pagination, preserving exact filter/keyset behavior.
Planning policy, pool sizes, workload budgets and public responses stay unchanged.

The subsequent [query reshape](2026-11-13-overview-reshape.json) reuses scalar
classification and constructs metadata only after page selection. Two initial
versions are rejected because maximum pages sort assembled JSON to disk. The
corrected ordered-subquery boundary keeps both sorts in memory (126/208 KiB),
with zero temporary I/O and exact generated-query results. Eight-reader prototype
cohorts fall from 1,502/1,445 ms to 578/754 ms; focused PostgreSQL checks pass in
15.188 s. [F4-T17](../../tasks/F4-T17.md) records implementation and verification;
the [new loaded control](2026-11-13-overview-control.json) still fails. All 800
baseline batches, 1,400 backfill batches, 80 browser journeys and 600 due intervals
complete. Page SQL p95 is 317.589 ms; cold-list p95 is 1,626.8 ms. Two overview
requests still fail pool admission before SQL at 253.345/257.390 ms. Ingestion p95
is 28.412–29.833 ms alone versus 79.156–83.114 ms loaded. The same observer covers
both phases without errors. No full performance or alert-calculation pass is claimed.

The newly separated canonical-query parents show 4,784 calls/62.120 s in historical
extrema rebuilding. Waiting current work still skips canonical sums. Maintenance
and current-deferral writes remain competing work, with affected-row counts not
yet captured; sparse lock samples do not identify a dominant ingestion blocker.

## Pending historical work

[F4-T18](../../tasks/F4-T18.md) separates historical attempts by trace outcome.
All 3,451 waiting extrema turns execute one canonical batch and publish no hours;
they consume 47.166 s of cumulative database time. Thus repeated rollback of
already completed hours is not the cause in this measured run. A fixed-time
[PostgreSQL reproduction](2026-11-14-history-retry.json) confirms a ready first
window followed by pending at ordinal 237/240, unchanged cursor and recovery.

The history loop's minimum pause is computed from turn start, so a calculation
longer than ten milliseconds consumes it. The scoped correction waits 100 ms after
an unsuccessful turn with connections released, while successful turns drain
immediately. The deterministic-clock regression fails before correction, and
seven focused checks plus a ready-neighbor fairness case pass. The [full control](2026-11-14-history-retry-control.json) has no API errors and
completes every query/backfill/browser/due interval. Waiting extrema query time
falls to 5.261 s (453 calls); successful cursor advances increase to 823, with
3,951 hour/day publications. Current work publishes 1,179 times. List latency and
ingestion isolation still fail: cold-list p95 is 1,530.8 ms, loaded ingestion
p95 87.269–91.633 ms versus 16.612–17.443 ms alone. The same backfill completes
in 129.404 s, so its offered write pressure increases; minimum pacing is not a
fixed arrival rate. The next attribution must retain that distinction and the
historical SQL path's missing production admission.

## Priorities

Rank reflects observed user impact. Related symptoms are grouped; the suspected
cause is not counted as a separate proven defect. No open item below has an
integrated capacity pass.

| Order | Issue | User impact | Bottleneck evidence | Current status |
| --- | --- | --- | --- | --- |
| 1 | Slow SLO overview and interactive admission exhaustion | Lists still exceed one second; latest control has no API errors | Latest cold-list p95 1,530.8 ms; page SQL p95 274.2 ms; prior admission failures remain retained | No admission failure in latest run; read/transfer/resource budgets remain open |
| 2 | Live ingestion slows under competing work | Latency isolation fails | Latest p95 16.6–17.4 → 87.3–91.6 ms; 446 accepted and all 400 due intervals present | Absolute p95 passes; relative limit fails |
| 3 | Expensive scheduling and repeated pending calculations | Background work consumes capacity without publishing current values | Idle PostgreSQL ~49.5% of one CPU; due queries consume ~25.1 s in 62.6 s. Loaded phase has 8,226 waiting attempts /776 publications | Current waiting calculations skip canonical sums. Historical waiting work falls to 453 calls /5.261 s; more useful work and faster synthetic writes still keep PostgreSQL busy |
| 4 | Pending aggregate windows and unproven alert capacity | Proposed alert evaluations would retain pending boundaries | Latest 11/800 baseline batches pending (prior 102; publication progress differs); bounded fallback exactness has separate passing fixtures | Calculation-adoption gate remains open |
| Corrected | Authentication fails during navigation | Previously prevented page opening | Shared inspection queries preserve authority; 40/40 loaded auth-context calls succeed and all 80 journeys complete | Original conflict corrected and verified under load; other 503s are pool admission failures |

## Authentication failures: corrected database lock contention

**Observed:** the corrected baseline browser completed 27 of 80 planned journeys
before a cold-detail navigation failed. `POST /api/v1/auth/context` returned 503
at 20:53:21.943 UTC. Trace `f1b5196e293496d6e95c02256b95f5c6` reports
`db.LockSessionUser` failing after **256.082 ms**, with **365.611 ms** total
request duration. An earlier source-first paired run also failed auth context.

**Located mechanism:** `InspectSession(touch=false)` acquires `FOR UPDATE` locks
on the user and session even though it does not touch the session. Protected SLO
reads hold `FOR SHARE` locks on those records through their transaction. The load
clients share an administrator principal/session. Separate sessions for the same
account still conflict on the user row. See
[session inspection](../../../internal/storage/sessions.go) and
[protected reads](../../../internal/storage/agent_reads.go).

**Evidence:** an overlapping overview trace acquired its shared user lock at
20:53:21.739949; page SQL took **408.411 ms** and its transaction ended around
20:53:22.170684. The failed exclusive acquisition spans approximately
20:53:22.053161–22.309243. Other protected reads overlap too. This establishes a
reachable contention path, not the original trace's exact blocking chain.

The focused real-PostgreSQL reproduction holds an ordinary protected transaction
open. User-row and session-row-only cases fail inspection after **251.356 ms**
and **250.982 ms**. `pg_blocking_pids` identifies the actual holder/waiter;
`wait_event=transactionid` and a generated-query diagnostic returns **55P03**.
Separate-user and authority/expiry/no-idle-touch controls pass. The intended
regression was red in **1.388 s** on unchanged production code.

**Verified correction:** only `touch=false` inspection now uses the
existing shared user/session queries. Touching authentication and authority
mutations keep their exclusive locks and validation. The original regression and
four concurrent-writer cases pass in **2.468 s**, nine existing session checks in
**2.670 s**, and the existing shared-reader/exclusive-writer fencing selection in
**13.718 s**. Primary integration passes the reproducer in **2.224 s** with exact
source hashes. Committed disablement, credential-revision change, installation
epoch change and session revocation are still rejected; inspection does not touch
idle time. No timeout, schema or generated query changes.

**Normal deployment:** onboarding and all three account/session Settings journeys
pass in **11.578 s**, with unchanged session source fingerprints. The subsequent full-load result is recorded below. The first deployment attempt ran zero tests:
new-worktree Loki/Tempo nonsecret files had mode 0600 and unreadable container
mounts; correcting only those two nonsecret file modes enabled the passing run.
That diagnosed setup correction is separate from application performance.
**Loaded verification:** on `925d156`, all 80 browser journeys complete and all
40 traced auth-context requests return 200 (p95 **25.042 ms**, max **55.265 ms**).
The three remaining overview 503s fail interactive pool acquisition at
**250.113–250.839 ms**, before page SQL; they are a distinct issue.

## Overview latency: database execution, transfer and admission

**Earlier instrumented control:** overview API p95 **1,496.739 ms**; cold/warm list p95
**1,830.6/808.6 ms**, cold/warm detail **1,037.8/374.8 ms**. Each browser cohort
has 20 completed journeys. Three sampled overview API requests return 503; their
traces identify failed interactive pool admission. Page SQL p95 is **604.820 ms**.
The resource samples show the cohosted load-client/observer container averaging
**46.75% of one CPU** during load, application **31.91%**, PostgreSQL **94.20%**.
These shared two-vCPU constraints require attribution, not a budget waiver.

**Earlier:** corrected baseline overview API p95 **984.823 ms**, maximum
**1,177.165 ms**; cold-list maximum **1,799.3 ms**. Its 110 sampled overview API
responses succeeded, but browser coverage is incomplete after issue 1. An earlier
disk paired run measured **1,254 ms** API p95 and **2,262 ms** cold-list maximum.
The source-first paired run recorded two overview 503s at the **250-ms interactive
admission limit**. Do not merge these admission failures with auth-lock failures.

**Located cost:** in corrected baseline traces, page SQL p95 is **480.317 ms**
and group-count SQL p95 **25.589 ms** (124 spans each). Earlier query diagnosis
found repeated classification and extraction of deadlines from large chart JSON.
The verified count/materialized-classification and deadline-scalar changes in
[F4-T07](../../tasks/F4-T07.md) improve direct queries, but did not close the
integrated gate. Forced custom planning was slower and was not adopted.

[Transfer evidence](../slo-overview-performance/page-transfer-diagnosis.json)
shows roughly **1.18 MB** for 21 internal rows and **1.02 MB** public JSON in that
fixture: about **895 KB** sparkline data, including **420 KB** quality values.
Removing the internal budget cache alone did not help: query p95
**170.559 → 181.773 ms**. Required public fields cannot silently disappear.

**Latest isolated experiments:** identical public output, ten connections, paired
queries in repeatable-read snapshots, 50 recorded samples per alternative:

- Separate payload/metadata and merge typed values in Go: query mean
  **91.800 → 57.926 ms**, but query/decode/encode p95 only
  **187.049 → 183.148 ms**.
- Scalar failure classification, with both queries at the same fixed clock forcing
  stale rows: query mean **86.201 → 81.208 ms**; total p95
  **222.905 → 191.647 ms**. A private probe SQL-column error was corrected and
  preserved; no application generated source was edited.

These are diagnostic experiments, not deployed fixes or controlled full-workload
improvements. They omit authorization, group counts, HTTP and rendering. Timing
variation outside SQL remains significant. Details and limitations are in F4-T14.

**Next:** inspect actual page execution plans and correlate query spans with
database waits and container CPU/I/O. Separate time waiting for admission/locks
from SQL execution, transferring/decoding rows and browser rendering. Preserve
count/order/filter/keyset and exact-value behavior in every alternative. Recheck
full cold and in-app navigation only after a causal correction is demonstrated.

## Ingestion isolation: remaining source/transaction contention

**Earlier instrumented control:** 208 ingestion-only accepts versus 524 loaded accepts; neither cohort
has unsuccessful ingestion. Histogram p95 increases **14.350–15.068 →
87.269–91.633 ms**. All 400 canonically due intervals are observed. Trace-start
cohorts contain 208/523 calls; their means are **7.622/37.669 ms**, loaded p95
**90.229 ms**. A trace maximum is **383.886 ms**; the acceptance budget here is
p95, not a maximum-latency guarantee. No database wait samples cover these phases.

**Earlier:** corrected baseline ingestion p95 increases from
**12.396–13.016 ms** to **62.021–65.122 ms**. This is below the **250-ms absolute
limit** but exceeds the permitted **1.25× relative limit**. All 344 measured
ingestions succeed and all 400 due intervals are present. The same failure persists
without candidate/oracle calculations. Historical writes in this experiment are
SQL fixture operations, not the real agent's historical channel.

**Earlier impact:** [F4-T03](../../tasks/F4-T03.md) retained a run with four failed
requests and **9/400 due intervals absent at cutoff**. This is lateness at that
checkpoint, not proof of permanent measurement loss. Subsequent corrected runs
retained all due intervals, while latency still failed.

**Proven prior cause and correction:** historical observation foreign keys take
`KEY SHARE` locks; agent/grant/measurement `FOR UPDATE` authority reads conflicted.
One trace spent **166.980/198.842 ms** in agent-access locking. Real uncommitted
inserts reproduced the conflict. The three appropriate locks changed to
`FOR NO KEY UPDATE`, with authority/revocation checks passing in **6.173 s**.
This correction remains valid but does not explain all remaining slowdown.

**New decomposition:** [deduplicated baseline spans](2026-11-13-baseline-decomposition.json)
contain 209 ingestion-only and 343 loaded request-start cohorts (the accepted
histogram uses completion boundaries, hence 344 loaded accepts). Mean request time
increases **6.295 → 23.956 ms**; summed database-operation elapsed time per request
increases **5.478 → 22.752 ms**. Pool acquisition p95 is only
**0.027 → 0.030 ms**. The increase is spread across statement/transaction calls,
credential lookup, inserts and marking; application work outside those database
spans is a small part of the increase. The loaded source-generation operation's
p95 is **3.976 ms**, maximum **25.484 ms**: the older isolated 2.997-second source
write does not establish that operation as the dominant current cause.

**Unresolved:** database spans include client/driver transfer and scheduling time;
they do not distinguish lock waits, server CPU, I/O or VM scheduling pressure.
No complete allocation among those causes has been established. A lock-prefixed
query name is not evidence that its elapsed time was spent blocked on a lock.

**Next:** compare ingestion-only and loaded per-operation distributions from the
same corrected fixture. Correlate source-generation mutations and background work
with live database wait/blocker samples and per-container CPU/I/O. Keep exact due
interval accounting, accepted/duplicate/failed outcomes and both latency gates.
Do not weaken authority locks or slow the workload merely to make it pass.

## Background preparation: expensive due scans and pending evaluations

**Before scheduler correction:** during 62.084 s of ingestion-only activity, PostgreSQL averages
**98.49% of one CPU**. `ReadDerivedDue` runs 127 times, totaling **40.260 s**
(mean **317.006 ms**); `ListDueDerivedObjectives` runs 59 times, totaling
**18.467 s** (mean **313.008 ms**). Only 402 current publications occur, totaling
**3.340 s**. Both due queries compare extrema publication times against metadata
inside the cached chart payload. A bounded same-snapshot probe reproduces the expensive branch with 198/200
source-current projections. Six alternating samples give due/list medians
**400.470/389.642 ms** originally, versus **28.692/29.201 ms** when publication
text is extracted once in a materialized relation. Ordered outputs match exactly.
Both plans keep 198 extrema loops and about 118 filtered rows per loop; shared
buffer hits fall **94,339 → 12,211**. Repeated metadata extraction is the dominant
cost in this reproduced branch. Generic plans are explicit; this is not full-workload
acceptance.

**Verified scheduler correction:** [F4-T15](../../tasks/F4-T15.md) materializes
uncast publication text once per projection in both queries. Conditional parsing,
eligibility, ordering and retry behavior stay unchanged. The
[implemented-source probe](2026-11-13-scheduler-metadata.json) preserves exact
ordered outputs and measures medians **398.850 → 30.092 ms** for due checks and
**389.737 → 29.316 ms** for selection. When all versions mismatch, materialization
instead adds **5.3/4.6 ms**; that tradeoff is retained. Fourteen integrated storage
checks pass in **11.774 s**; six normal-container collection/SLO journeys pass in
**254.440 s** with exact values. The combined control above verifies lower scheduler cost but still fails workload acceptance.

**Before readiness correction:** **7,866 waiting attempts /139.665 s cumulative duration**,
across all 200 opaque objective keys (39.33 attempts per key on average), versus
13 publications. Same-outcome attempt starts average **3.898 s** apart. No captured
SQLSTATE errors or failed `DeferCurrentPreparation` spans occur in those attempts.
This narrows the error/cleanup hypothesis but does not distinguish every source
readiness cause. Full resource/database attribution remains incomplete.

**Earlier:** corrected baseline has **6,112 current evaluations returning waiting**
with **59.698 s cumulative span duration**, versus **548 publications /22.347 s**
during **106.407 s** of load. The earlier source-first paired run still had
3,385 waiting attempts. Summed spans are not wall-clock CPU utilization.

**Proven prior cause and correction:** current evaluation ran before its source
batch in the same scheduler turn, deferring work even when that turn published
the prerequisites. F4-T11 reproduced `dirty=0, waiting=1, projections=0` and now
prepares one bounded source batch before refreshing current selection. Focused
checks cover healthy-tail fairness, lease/failure handling, clock changes and
revision races. Integrated scheduler tests pass in **5.554 s**; combined workload
acceptance still fails.

**New decomposition:** each of the 6,112 waiting attempts runs
`ReadRetainedWindowBatch`, totaling **23.343 s** elapsed, plus ready-tail/context
and transaction work. Derived pool acquisition totals **5.261 s**. All waiting
spans lack a stable work key, and bounded cleanup starts from a background context
without the parent trace. Opaque work identity and trace-only cleanup linkage are
now implemented and pass focused tests without changing scheduling. They are exercised in the latest control described above.

**Unresolved:** why each remaining attempt is pending, how long the same eligible
interior stays dirty, whether scheduling repeatedly selects known blocked work,
and how much that work contributes to ingestion/list tails. Global source
generation changes and historical invalidation are relevant mechanisms, not yet
a quantified attribution or authorization to change consistency semantics.

**Code-level distinctions for the next control:** `waiting` includes retryable
SQLSTATEs and exhausted source work as well as dirty interiors; joined cleanup
failures can retain that label. The new child SQLSTATE and parented deferral spans
must distinguish these before attributing the count. Ordinary missing observations
alone are not pending. Current retries are deferred two seconds, while each source
turn rebuilds at most 100 newest-first keys independently of the next 100 current
objectives. The fixture's rolling window includes the older backfill, so short
burn-window readiness does not imply complete current readiness. These mechanisms
are supported by code; their contribution to the measured failures is not yet known.

**Next:** group attempts by SLO/source generation and reason; decompose their child
queries and compare with source publication progress. Reproduce redundant attempts
with fixed time and specific dirty ranges. Any selection change must retain exact
cadence/validity/retention bounds, wake after publication without new ingestion,
surface exhausted work and keep unrelated healthy work moving. Check both due
selection and scheduler wakeups to avoid replacing transactions with a tight poll.

## Calculation readiness and alert capacity remain unproven

The latest baseline returns pending in **252/800 batches**; the earlier control
returned **150/800**. This is accepted
calculator preparation state, not a proven arithmetic defect. The proposed alert
engine must preserve those boundaries rather than treating them as clear.

The [bounded fallback experiment](../alert-calculation/README.md) has stronger
exactness evidence than capacity evidence: earlier paired runs matched all 3,200
windows, while physical-plan fixtures observed zero raw visits on refusal, 455 in
one admitted case, and 1,820 of 80,640 retained observations in another. Separate
runtime admission tests refused within about 250 ms without partial values or
extra connections. External SQL query workers do not exercise that production pool.

**Next:** resolve existing-workload failures, then run fresh candidate-only and
paired controls, retaining all bounds and exact comparisons. Production admission,
maintenance exclusions, policy limits, actual wall-clock rollover, committed alert
transitions and total detection delay require their own later evidence. The
100/200-policy engine acceptance is not replaced by this 200-SLO query experiment.

## Corrected defects that remain regression requirements

| Defect | Correction and evidence | Remaining limit |
| --- | --- | --- |
| Foreign-key locks block live agent authorization | F4-T03 reproducer and narrower non-key-update locks | Other ingestion contention remains open under ingestion isolation |
| SLO detail returns 503 merely because current calculation is pending | [F4-T04](../../tasks/F4-T04.md) preserves compatible published values with explicit stale/failed provenance; incompatible/absent values remain unavailable | Does not fix admission or authentication 503s |
| Overview repeats classification and detoasts large JSON deadlines | F4-T07 materialized counts and validated generated deadline scalar | Page/transfer/resource cost remains open under overview latency |
| Malformed/infinite deadlines abort the list or falsely retain Healthy | F4-T07 safe parsing and explicit repair eligibility | Regression correctness, not a capacity result |
| Current work precedes ready source publication | [F4-T11](../../tasks/F4-T11.md) bounded source-first turn | Remaining waiting work remains open under background preparation |

## Experiment flaws and environment limitations

These affect interpretation; they must not be reported as application fixes.

| Finding | Evidence and correction | Remaining limitation |
| --- | --- | --- |
| Seed history readiness is coupled to live currentness | On `404b638`, all 200 persisted histories and closed-hour extrema were ready at 22:07:34 UTC, but the 22:10:53 API sweep returned 197 ready and 3 stale/preparing rows, all with history generations. The overview deliberately rewrites stale current projections' history state. The seed was interrupted after 9.5 minutes at that poll, preserving its report | A history-specific, revision/cache/extrema-checked predicate passes twelve rollback cases; the fresh v3 seed passes two tests in 243.601 s. No measured workload started; this delay is not proof of historical calculation slowness. Subsequent currentness/accuracy/latency gates remain required |
| PostgreSQL tmpfs competes with the VM's RAM | 6-GiB tmpfs on a roughly 3.85-GiB/no-swap VM; 895-MiB data +1,008-MiB WAL; available memory fell to 90 MiB. [Storage evidence](../slo-overview-performance/storage-isolation.json) records verified backup/restore and explicit disk-backed experiment | No matching OOM kill was found. Fresh disk runs still fail; do not attribute every earlier failure to memory |
| SQL history precedes declared eligibility | 864,000 observations were outside both validity bounds. F4-T13 declares eligibility before seed and validates every planned range; corrected check finds zero invalid among 1,153,250 observations | Initial missing coverage changes, so legacy/corrected runs are not identical baselines. SQL history bypasses real historical grants/buffering/admission |
| Oversized/cohosted/combined workload obscures attribution | Initial 12,000-row multi-SLI transactions became at most 720 rows for one SLI. Ten cohosted browsers became ten API clients plus one browser. F4-T13 separates baseline/candidate/paired query modes | Browser still shares server resources; remote capable-client acceptance is outstanding. Paired mode intentionally includes raw-oracle work |
| Incomplete load and incorrect due accounting | API cohorts now remain active and include slow completions; due oracle includes settling/dispatch/completion allowances instead of counting only fresh observations | An older 149-pair run failed in raw-reference work, not necessarily candidate execution; retain incomplete results |
| Numeric/evidence reporting could hide errors | F4-T13 compares decimal text exactly, preserves incomplete/mismatched evidence before latency assertions and reports unexecuted equivalence as not_checked | Correct reporting is not a passing capacity result |
| Observer database framing | Nested JSON emitted literal line breaks with multiple backend rows; the latest run has only 16 pre-phase DB samples, versus 8,604 resource samples. [Regression and correction](observer-framing.md) preserve this failure | Fixed normalization passes a real multi-backend check; it does not restore missing samples in the completed run |
| Evidence/tool failures | Monitoring configuration readability, duplicate tracing, EPIPE capture and bounded trace export were corrected | Non-TTY wrapper exit 143 remains unexplained. PTY/polling is a precaution, not a proven fix; preserve supervisor and child state on recurrence |

## Observability needed for the next causal checks

- Existing spans locate named SQL calls and application phases, but the original
  auth error lacks SQLSTATE and blocker identity. The focused reproduction closes
  that mechanism gap, not the original historical blocking chain.
- `pg_stat_statements` is not preloaded. Use bounded plans and available spans now;
  changing server startup instrumentation requires an explicitly identified fresh
  fixture and must not silently alter a comparison.
- Add only the measurements required by an active hypothesis: database wait type,
  blocker relationships, safe operation/error identifiers, resource utilization,
  and transfer/decode phases. Never capture secrets or SQL parameter values.
- Older paired results lack separate oracle duration. New controls identify actual
  executed work; they still require fresh seeds and their own ingestion baseline.
- Currentness and engine detection/endurance remain separate acceptance gaps.
  Catalogue UI tests, exclusion-unit tests and passing exact-query fixtures do not
  establish those operational guarantees.

All baseline reports and earlier failures remain linked from
[F4-T02](../../tasks/F4-T02.md), [F4-T03](../../tasks/F4-T03.md),
[F4-T04](../../tasks/F4-T04.md), [F4-T07](../../tasks/F4-T07.md),
[F4-T11](../../tasks/F4-T11.md) and [F4-T13](../../tasks/F4-T13.md).
