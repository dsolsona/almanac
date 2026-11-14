# Bounded alert calculation experiment

**Recommendation: do not adopt this candidate yet.** Exact calculation succeeded
in the exercised cases; the combined contention fixture failed existing workload
budgets. Production calculation and the alert-engine proposals are unchanged.
See [F4-T02](../../tasks/F4-T02.md) for scope, commands and limitations.

## Recorded run

[Sanitized results](2026-11-13-contention.json) pin the application base, SQL and
harness hashes. The runner had two AMD EPYC vCPUs and 3.85 GiB RAM, PostgreSQL 18.6,
200 SLIs/SLOs at 60-second cadence, and one day of initial history. The offered load
combined two paired-query workers, independent raw references, ten browser users,
live collection and unpaced SQL backfill over three older days. Four historical
cutoffs crossed an hour boundary; this was not a running alert-engine rollover.

| Measurement | Result |
| --- | --- |
| Paired four-window batches | 800 completed |
| Independent raw equivalence | All 3,200 candidate windows matched |
| Baseline pending / candidate pending | 236 / 0 batches |
| Candidate query p50 / p95 / maximum | 7.323 / 46.066 / 162.349 ms |
| Baseline query p50 / p95 / maximum | 6.987 / 41.962 / 169.425 ms |
| Maximum emitted raw contributions | 455 per batch |
| Measured period | 180.894 seconds; completed queries span 124.580 seconds |
| First list renders | Nine successes, 7,941–8,609.6 ms; all violate 1,000 ms |
| Browser workers | All ten failed; no completed detail samples |
| Fresh live observations during load | 249 intervals from 190 of 200 SLIs |
| Maximum observed receipt delay | 140.517 seconds |
| Backfill | Exceeded its 180-second process deadline; incomplete |

The completed-query result is promising for exact dirty-hour substitution. It is
not a capacity or safety result: the fixture combines several workloads and cannot
attribute the interference to fallback. In particular, backfill uses bulk SQL
transactions with 12,000 rows and work marking for all 200 SLIs per older hour,
not the normal bounded ingestion endpoint. That can hold source-generation locks
across much more work than live ingestion. This is a possible confounder, not a
demonstrated production cause. A three-minute observation gap does not establish
permanent data loss. No ingestion-handler latency percentile was collected.

Subsequent oracle review found that the fresh-interval count above ignored the
settling/dispatch/completion allowance in ADR-0043. It cannot establish collection
lag. Later measurements check clock-derived due intervals instead; the original
count remains preserved as an observation, not a missed-deadline result.

## Decision and next evidence

Retain the accepted wait path and the draft status of ADR-0051/SPEC-0020's
amendment. Before adoption, the next measurement must use bounded backfill batches,
capture ingestion-handler latency against an ingestion-only baseline, and record
admission delay, completed backfill batches and incomplete-work ages. Establish
the workload baseline before attributing changes to fallback. Investigate the
navigation failures with server/browser traces; do not extend timeouts to make
this result pass. A separate physical-plan check observed zero raw visits on
refusal and 455 on admission, including filtered/rechecked rows; this is one
fixture's plan, not a universal physical-row guarantee.

The final harness adds process-deadline/input-pipe diagnostics and per-committed-
batch backfill progress. Those diagnostics were added after this failed run and
have not been measured in another contention run. The recorded source hashes
identify the exact measured harness; the SQL is unchanged.

## Reproduction and retained evidence

Run from the experiment worktree through Docker:

```sh
make shell CMD='go test -tags=integration ./internal/storage -run TestIntegrationAlertFallback -count=1 -timeout=5m -v'
make shell SOCKET=1 CMD='E2E_CONFIG=playwright.alert-calculation.config.ts sh scripts/e2e.sh'
```

The second command is an explicit experiment, outside the ordinary performance
suite. Its failed outcome is retained, not a new passing gate. Private raw output
is under `.local/alert-calculation/`: paired query JSONL, process output, profile,
summary and results. The public JSON contains their SHA-256 digests and no session
credentials or fixture identities. [The run-specific analysis](analyze.py) verifies
every completed window using decimal arithmetic and produces that public record:

```sh
make shell CMD='python3 docs/evidence/alert-calculation/analyze.py'
```

The analysis requires the original local raw artifacts. It records this failed
run; it is not a generic acceptance command for a later workload. Onboarding and
seeding passed; contention failed after 3.0 minutes (5.7 minutes total reported by
Playwright). Final focused calculator checks passed in 3.323 seconds. No production
engine, alert lifecycle, excluded-window support or end-to-end detection target
is claimed.

## Bounded-backfill follow-up

[The corrected run](2026-11-13-bounded-contention.json) uses 720-row, single-SLI
backfill batches and the F4-T03 immutable-reference lock correction. All 1,400
backfill batches completed in 124.820 seconds, but the full gate still failed.
149 paired queries independently match all 596 completed windows; both query
workers then reach their one-second statement deadline in the reference query.
Ten browser workers fail opening details, with status 503s; nine first lists take
7.376–9.671 seconds. Ingestion p95 is 106.076–111.380 ms versus the 16.612–17.443 ms
baseline, with four unsuccessful batches. The corrected due-interval check finds
9/400 missing at measurement end, establishing lateness but not permanent loss.

A representative list trace spends 9.671 seconds end to end, with its overview
HTTP request taking 0.676 seconds. The fixture runs all ten browser renderers on
the same two-vCPU machine as the server. SPEC-0019 requires ten concurrent API
users and separately measured rendered journeys; these loads need distinct
reporting so renderer contention is not mistaken for database cost. No acceptance
limit is changed. Dirty-current 503s and per-SLI source-generation contention are
also actionable and remain unresolved. Raw evidence is retained in the ingestion
worktree under `.local/alert-calculation-ten-browser-failure/`.

## Separate API load and rendered journeys

[The API-user run](2026-11-13-api-contention.json) uses F4-T04's explicit last-known
current status and separates ten API clients from one browser renderer. All 800
paired batches independently match 3,200 windows; all 1,400 bounded backfill
batches finish in 125.590 seconds and all 400 due live intervals are present.
Candidate p95 is 30.519 ms. The overall gate still fails: ingestion p95 is
83.114–87.269 ms against 19.231–20.192 ms, overview p95 is 1,564 ms and the three
completed cold list renders reach 2,531 ms. Two API workers fail on overview 503s;
the browser worker also fails. Successful samples are partial evidence.

The finite API cycles ended before the query lanes, so this is not sustained
concurrency acceptance. The subsequent harness correction keeps clients running
through the other lanes and records per-request overlap; it is not yet measured.
A representative trace attributes 931 ms to the list SQL and 250 ms to group-count
SQL. Post-load literal plans do not reproduce that cost; prepared plans and
concurrent JSON/classification work require investigation. External experiment
queries also bypass the backend's derived pool, so this fixture cannot establish
production admission or attribute interference to fallback alone.
