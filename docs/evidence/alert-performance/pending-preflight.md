# First-window readiness diagnostic

This bounded diagnostic informs [F4-T16](../../tasks/F4-T16.md), under
[plan 0045](../../plans/0045-alerting-performance-investigation.md). It does not
establish loaded performance acceptance or change canonical pending-row outputs.

## Inputs and method

The probe used the retained 200-SLO fixture at source revision `bf34f01`. One
selected SLO was read in a repeatable-read transaction. It fixed the calculation
time, rolling-first/1-hour/6-hour order, validity and retention bounds, cadence and
LCM-hour alignment. The requested cutoff was 22:46 UTC on 2026-11-13; dispatch and
settling rules yielded 22:43, and an already-arrived observation advanced the live
evaluation cutoff to 22:45. Cadence was 60 seconds and the common grid 3,600 seconds.

Only the dirty-range table reference was redirected to a transaction-local copy.
The probe injected an interior dirty hour, a raw-edge dirty hour and exhausted
interior work. It changed no observations, aggregate rows or application state;
all temporary state rolled back. Four samples alternated canonical/preflight order
for each ready/pending condition; edge and exhaustion cases each ran once.

Source fingerprints (SHA-256):

- Canonical `retained_window_batch.sql`:
  `b3a693ca016b0740b29d5ed9b563aa84d7215f2f4082d661c16377175f960700`
- Original `derived_evaluation.go`:
  `38ebfd4ef7cf05f6979e0489eebb12b55f172c79cb0f22ab0f9e1c3e58c4034c`
- Private diagnostic script:
  `634bae679744f3970cb8562aae4e232e1a8812dead4437ebbba441a635a57c17`

## Results and limits

Median milliseconds from the bounded sample:

| State | Canonical server | Preflight server | Canonical roundtrip | Preflight roundtrip |
| --- | ---: | ---: | ---: | ---: |
| Ready | 2.511 | 0.173 | 3.033 | 0.487 |
| Pending interior | 2.276 | 0.190 | 2.926 | 0.472 |

The shortcut saves approximately 2.455 ms per pending roundtrip in this probe;
ready calculations would add approximately 0.487 ms and one roundtrip. Server
timings include dynamic SQL planning. Roundtrip timings include a persistent
Docker-exec/psql pipe, not pgx. This small warm sample is neither a p95 estimate
nor a concurrency, currentness or application-latency result.

The pending canonical plan still reads approximately 100 hourly aggregate rows
and 114 raw-edge observations. Its plan reports 0.892 ms planning and 0.894 ms
execution. The readiness plan reads only the temporary dirty table, with two
local buffer hits, 0.088 ms planning and 0.016 ms execution.

The injected interior makes ordinal 1 pending while ordinals 2 and 3 remain ready.
An exhausted interior preserves the first-row failure flag. Edge-only dirt leaves
all windows ready. Canonical totals, counts and timestamps are identical across
these overrides; no pending numeric field is redefined. The probe does not prove
raw-oracle equivalence after temporarily overriding dirty metadata.

The first invocation failed before pending measurements because its injected
temporary queue row omitted required timestamps. Supplying the fixed probe time
corrected that setup defect; the failure and rollback are retained privately.

## Snapshot audit and implementation boundaries

All production calls to `evaluateDerivedWindows` use the caller's repeatable-read
transaction: current refresh/status, `attachExtremaBudget`, extrema hour/day
building, and detail/history live edges. Detail calculation finishes its database
reads before releasing that snapshot. The later read-committed transaction only
rechecks authority and publishes the detached result. Some test helpers use direct
connections; that is not evidence that production calculations do.

A ready first window must still execute the unchanged canonical batch. In
particular, a later pending window cannot hide an earlier prepared window's
inconsistent-facts error. The gate cannot remove pool, transaction setup, context,
ready-tail, rollback or defer costs. Scheduling, retry intervals and work bounds
are outside this correction. Implementation verification and integration remain
tracked in F4-T16.

## Frozen implementation candidate

Focused regression red: **0.961 s**; original focused green: **1.052 s**;
final nine integration/four unit entry-point selection: **3.431 s**. The task
records exact commands and names, the SQLC inference compile failure, and the
same-snapshot concurrent-write regression. Full-load verification remains pending.

SHA-256 fingerprints of the tested source:

| File | SHA-256 |
| --- | --- |
| `internal/storage/derived_evaluation.go` | `32088029567dcb0459908f04dd3cd29a7ac7821e2cfada534b23183e9d4271fb` |
| `internal/storage/query/derived_readiness.sql` | `d0c527fef21e4ef51fa64d3b1d312500108fb3b2832dff74f6153c5fff62a723` |
| `internal/storage/dbgen/derived_readiness.sql.go` | `563fa35da55ff82e157ac796fd73e49e91b036545aed570351e4c92357a99295` |
| `internal/storage/dbgen/querier.go` | `c0588a96287b8b3910a482b254ddfde4f8ea5f654837f38a0cde764f812808d2` |
| `internal/storage/derived_preflight_integration_test.go` | `cdbdad4a5653122285cb11b410e78745e5e980eba45b88829a3c2101a8f2bb6a` |
| `internal/storage/derived_evaluation_test.go` | `f39c964c3877b7641dfe3840aae658a17a61bf69f80e326c18c0c0952150f94a` |
| `internal/storage/derived_facts_plan_integration_test.go` | `95be4e08782cd6266be784f0f4c7a2b44a9ef39e532c13f000e177efcd25a56c` |

Canonical `retained_window_batch.sql` retains the original fingerprint above.

## Integrated control

The source was integrated onto `0dfbca5` and SQLC regenerated. The six other
source fingerprints above remain identical; `querier.go` now also includes the
scheduler query comments, with SHA-256
`4604b58c50e006bb16ebf7bdc023f1bfab087b4e80efb8b3c11744bfda5766d4`.
The combined focused selection passes in 11.497 s.

The [200-SLO control](2026-11-13-scheduler-preflight-control.json) verifies that
all 8,226 waiting current evaluations avoid canonical reads. It still fails
overview admission, list latency and relative ingestion isolation. All due
intervals and workload batches complete; see F4-T16 for exact timings, sources
and limitations. This is a verified local optimization, not a passing capacity gate.
