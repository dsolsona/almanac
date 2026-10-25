# SPEC-0003: Ingestion and recovery

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Maintainer approved Crit review `b7b16f696fed`; all comments resolved.
- Implementation: not started
- Related ADRs: [ADR-0001](../adr/0001-collection-and-ingestion-boundaries.md),
  [ADR-0002](../adr/0002-agent-communication.md), and
  [ADR-0003](../adr/0003-bounded-buffering-and-historical-isolation.md) (accepted)
- Plan: none
- Supersedes / superseded by: none

## Purpose and scope

Define how observations enter Almanac, how producers learn what was accepted, and
how interrupted collection can recover without double counting. This refines
[SPEC-0002](0002-agent-behavior.md). It covers live agent collection, delayed live
submissions, user-written producers, historical backfill, and automatic gap repair.

The agreed direction is bounded live buffering, separate historical operations,
and best-effort repair. The identity, payload, and acknowledgement rules below are
accepted. SQL schemas, exact HTTP routes, wire versioning,
numerical storage types, and SLO evaluation formulas remain separate contracts.

## Producer and resource identity

Every submission says which SLI the data belongs to and which saved version of that
SLI's measurement settings was used to produce it. The backend checks the sender's
token or API key to establish who is submitting and what they may write. Merely
putting another agent or SLI identifier in the request does not grant access.

### What a definition revision means

A definition revision is a saved version of the settings that determine what an
SLI measures: its queries, measurement type, and interval rules. The backend assigns
a revision when those settings change and keeps the old settings available for
interpreting old observations. Display-name edits do not change measurement meaning.

For example, revision 1 measures requests completed in under 500 ms. At 10:00 the
operator changes its query to measure requests under 250 ms, creating revision 2.
An agent reconnecting at 10:05 may upload buffered 09:59 results from revision 1.
Those results must not be presented as if they measured the new 250 ms condition.
The observation therefore carries revision 1, regardless of its upload time.

Agents receive this revision in backend configuration. A custom producer obtains
it from the SLI definition API and includes it when submitting data; it does not
invent revision numbers. Credentials, the revision, and the interval are checked
together. Versioning preserves meaning; it does not automatically authorize old
work. The exact edit/effective-time policy remains a decision for the SLI contract.

An SLI declares its collection mode as either
agent-collected through an assigned datasource or externally submitted. An
externally submitted SLI does not require a datasource host or collector identity.
Its definition specifies measurement kind, interval boundaries, and an expected
cadence when freshness or missing-interval detection is required. An explicitly
irregular producer has no inferred periodic schedule.

For these externally submitted SLIs, the user owns collection, replay, and backfill.
Almanac can show missing intervals when a cadence is configured, but cannot fetch
data from a system for which it has no collection integration. Users can submit
historical observations themselves within the ingestion permissions and retention
rules. A manual repair action must explain this limitation rather than claim to
start agent work for an externally submitted SLI.

Custom jobs use API keys with RBAC scopes authorized to submit to specific SLIs and
read the measurement settings needed to form those submissions. Administrators
can issue, revoke, and replace these keys. Historical submission permission is
explicit; an ingestion key does not imply unrestricted writes to old data. These
keys do not grant agent management, datasource secrets, user administration, or
diagnostic permissions. Exact roles and key lifecycle belong in the identity spec.
Changing collection mode or measurement meaning requires a new definition revision
and an explicit effective boundary. Two producer modes cannot implicitly write
competing values for one SLI interval.

Agent submissions additionally identify assignment ownership and the configuration
that authorized execution. Receiving credentials does not authorize arbitrary SLIs,
historical ranges, or revisions. Reauthorization applies to retries as well as new
requests. Assignment fencing and historical authorization windows must be specified
before accepting delayed results after reassignment.

## Measurement envelope

Logical fields, independent of final JSON names:

| Field | Meaning |
| --- | --- |
| SLI identity | The indicator receiving the observation |
| Definition revision | Backend-assigned version of the SLI settings used to produce the data; preserves the meaning of buffered and historical observations after edits |
| Interval start and end | Original half-open measurement interval `[start, end)` |
| Measurement kind and payload | Typed data, as described below |
| Execution identity and outcome | The query or producer operation that generated the result |
| Delivery identity | Stable identifier reused when retrying an unchanged submission |
| Operation kind | Live, historical backfill, or repair; assigned by authorized operation |
| Assignment/configuration identity | Agent ownership evidence where applicable |

Represent timestamps in UTC with explicitly defined precision. A definition states
its interval alignment and resolution. Query completion and backend receipt times
are additional diagnostic timestamps; neither replaces measurement time.

### Preserve measurement meaning

OpenSLO distinguishes ratio metrics, including precomputed ratios, from threshold
metrics. Its budgeting methods also differ. An ingestion model must preserve the
information needed to evaluate the chosen definition; a percentage alone does not
contain event counts. [OpenSLO specification](https://openslo.com/specification/)

Observation kinds:

| Kind | Canonical payload | Validation |
| --- | --- | --- |
| Event counts | Good and total values for the interval | Finite, `0 <= good <= total`; fractional values permitted when the definition explicitly uses estimates |
| Raw ratio | Value and success/failure interpretation | Finite value in `[0, 1]`; interpretation is part of the definition |
| Threshold value | Value and measurement unit/meaning | Finite value matching the declared definition; no conversion to a good/bad event before objective evaluation |

The OpenSLO compatibility spec must determine the supported combinations of
measurement kind and budgeting method. Accepting this envelope does not claim full
OpenSLO support. Unsupported combinations fail explicitly. Counter reset handling,
rate conversion, query joins, and query-series reduction belong in provider and
measurement definitions. Ingestion does not guess those transformations.

Do not clamp invalid values into validity. Reject inconsistent counts or invalid
numeric values with an actionable code. A documented provider normalization, if
needed, must be explicit and tested separately.

### Execution outcomes and coverage

An execution can produce observations, succeed with no observations, or fail.
These outcomes are separate from whether its submission was accepted by storage.
A valid zero-traffic count observation has `good=0, total=0`; it does not represent
missing data or imply full attainment.

A successful empty query can record a completed execution for its requested range
without synthesizing zero-valued observations. Partial query output is not evidence
that the entire requested range was covered. Coverage requires a provider contract
that explicitly establishes completeness; otherwise unobserved intervals remain
unknown. Permanent provider errors are not recorded as successful empty coverage.

## Observation identity and duplicates

Use two independent identities:

1. A delivery identifier, scoped to an authorized producer, identifies one logical
   submitted item and its immutable content. An item can contain an atomic group
   of observations and its execution outcome.
2. A canonical observation key identifies an SLI, measurement-definition revision,
   and interval. It excludes delivery identifier, collector process, and operation
   kind so backfill cannot count an existing live observation a second time.

Interval rules prevent a month-long result and daily results from becoming
overlapping additive measurements. Provider query chunk size is an execution
choice; returned observations must match the definition's canonical intervals.
Do not split a single aggregate across smaller intervals by inventing values.

Comparison uses normalized measurement content rather than raw JSON bytes or
receipt timestamps. The precision and canonicalization rules need a wire contract.

| Situation | Outcome |
| --- | --- |
| Same delivery identifier and unchanged content | Return the original durable outcome after authorization checks |
| Same delivery identifier with different content | Conflict; do not overwrite the original request |
| Different delivery, same observation key and same measurement | Duplicate; no additional contribution to calculations |
| Same observation key with different measurement | Conflict; do not silently apply last-write-wins |
| Invalid or unauthorized observation | Reject without recording successful coverage |

A changed definition must not manufacture a second contribution for an already
evaluated interval. Effective-definition history determines which revision applies
to that interval. Retrospective reinterpretation is an explicit operation, not a
side effect of an edit or retry.

### Corrections and repair precedence

The initial repair operation fills missing intervals; it does not overwrite
accepted observations. Providers can return different values later as their own
data settles. Conflicting values remain visible and require an explicit correction
policy. Do not resolve such differences by choosing whichever response arrives last.

A future correction operation needs authorization, audit history, expected-version
checks, and recalculation of affected reports and derived state. It is outside the
initial ingestion contract. This deliberate limitation keeps retries and automated
repair from silently rewriting history.

## Acceptance and acknowledgements

Bound the size and observation count of each request and submitted item. Use
per-item atomic acceptance: persist its observations, execution outcome/coverage,
and required deduplication state in one transaction before acknowledging success.
If one observation in that item is invalid or conflicts, reject that item without
partially writing it. Separate items in a batch may have different outcomes.

Responses identify each submitted item as accepted, duplicate, rejected, conflicting,
or temporarily retryable. Transport success alone does not mean every item succeeded.
A timeout or lost response leaves an unknown outcome; retry unchanged content with
the same delivery identifiers. A crash after commit and before response must not
cause a double count on retry. Derived caches may update asynchronously, but accepted
data and recomputation work must survive backend restart.

The live buffer releases accepted/duplicate items. Terminal failures are surfaced
and removed according to the loss-reporting policy, preventing infinite poison-item
retries. Retryable or unknown items remain eligible for retry until acknowledged or
evicted by the bounded buffer. Batch retries preserve per-item identity.

Database unavailability produces no false acceptance. Backend overload returns a
retryable response and retry guidance. Apply bounded exponential retry with jitter;
historical retries use their own capacity and must yield to live submissions.

Deduplication and coverage retention must cover the supported replay horizon.
Expired history and expired delivery receipts need explicit rejection/replay rules;
cleanup must not make an old retry count as fresh data. Exact horizons remain open.

## Live buffering

Live results use the bounded in-memory ring defined in SPEC-0002. Retain recent
results and evict older unacknowledged results at capacity. Proposed eviction order
is oldest measurement interval first, with stable insertion order for ties.
Historical observations never enter this ring.

The total live delivery budget includes queued payloads, bounded metadata, and
in-flight batches. Taking a batch for transmission must not silently remove it from
memory accounting. A late acknowledgement matches delivery identity rather than a
reused ring slot. If a result exceeds the entire admissible item size, reject it
explicitly without evicting the whole ring trying to fit it.

Record bounded loss counters and interval summaries. Detailed loss logs are not
the authoritative coverage record and must not grow without bound. Restart loses
the ring; the backend must detect gaps from persisted expected schedules and accepted
coverage even if the agent cannot report what it lost.

Exact byte budgets, item sizes, fairness between SLIs, and optional maximum ages
need workload-informed defaults. They remain configurable limits, not an assertion
that buffering is always cheap. Offline configuration validity remains independent
of buffer fullness and will be addressed with ownership and revocation.

## Historical submission and isolation

Backfill and repair are separate operations from live collection. Their execution,
queues, buffers, and submission concurrency have separate budgets. Historical work
pauses when its result path cannot keep up; it cannot borrow the live ring or live
worker capacity. Job progress advances only for durably accepted observations or
explicit terminal outcomes, not for a successful query or socket write alone.

Historical ingestion uses the same validation and canonical observation keys as
live ingestion, through separately admitted operations. Backend database connection
and transaction budgets must also protect live ingestion. Short bounded historical
transactions and provider-specific query limits prevent historical work from
monopolizing shared resources. Their precise settings require implementation tests.

Separate local queues cannot guarantee that an external datasource has unlimited
capacity. Historical admission must account for provider rate limits and available
headroom. If a provider cannot safely isolate historical work while live queries
are due, pause historical work. Do not describe separate goroutines alone as proof
that live schedules are protected.

## Best-effort gap repair

The backend runs a periodic self-healing loop. Each pass compares expected
intervals under effective definition history with
persisted observations and explicit coverage outcomes. An acknowledgement watermark
alone is insufficient when earlier intervals can be missing. Use a delay allowance
before declaring a gap eligible for repair so ordinary query latency and buffered
delivery do not immediately trigger duplicate work.

For eligible gaps, the backend creates bounded historical repair requests for the
currently assigned, compatible agent. Provider capabilities determine query chunking.
Avoid creating overlapping active jobs for the same gap. Late live submissions and
repair results converge through canonical observation keys and conflict rules.

Repairs have finite retry budgets and visible outcomes: recovered, pending,
temporarily failed, unavailable history, unsupported replay, or conflict. Persist
attempt counts and the next eligible retry time so subsequent loop passes and
backend restarts do not reset the budget. Retry temporary failures a bounded number
of times with delay. When that budget is exhausted, mark the affected intervals
as missing with an exhausted-repair status and the reason; ordinary loop passes
must not automatically create a fresh budget for the same failed work. Gaps remain
visible while repair is pending too; exhaustion does not create the gap.

An administrator can manually retrigger healing for selected missing ranges through
the UI or CLI. The action creates an auditable new repair attempt cycle with its own
finite budget, rechecks current capabilities and provider history, and uses the same
historical channel. It does not overwrite accepted observations, bypass permissions,
or take capacity from live collection. If work for that range is already active,
reuse or reject the request explicitly rather than duplicate it. Loop frequency,
retry counts, delays, and manual-request idempotency need concrete configuration.

A known successful empty interval is not repeatedly retried as missing. Permanent
gaps remain visible in reporting; best effort does not imply eventual complete
coverage. A manual request can fail again if the datasource no longer has the data.

Automatic repair of externally submitted SLIs is unsupported unless an explicit
replay integration exists. The backend can report their gaps, but cannot request
data from an arbitrary user's job. Do not fabricate an assigned agent to do so.

Repair admission and progress yield to live collection. Automatic repair detection,
provider availability limits, and resume/cancellation will be detailed in the
backfill spec; this contract supplies their ingestion and coverage semantics.

## Concrete acceptance examples

| Example | Expected result |
| --- | --- |
| Submit interval A with 99 good / 100 total, lose the response, retry unchanged | One stored observation; replay receives a durable outcome |
| Backfill submits the same interval A with 99/100 under a different delivery ID | Duplicate; totals remain 99/100 |
| Backfill submits interval A with 98/100 | Conflict; accepted value remains unchanged |
| Submit 101 good / 100 total | Rejected; no silent clamping |
| Submit a valid interval with 0/0 | Zero traffic; not a missing interval or automatic success |
| Query succeeds with no observations and explicitly complete coverage | Completed empty coverage; no fabricated samples or endless repair |
| Two-item batch contains one valid item and one invalid item | First item may commit; second is rejected; both outcomes are explicit |
| Live ring is full when the next result arrives | Evict oldest retained live result; continue collecting within capacity |
| Historical submissions saturate their budget | Pause historical admission; preserve live workers, buffers, and submission budget |
| Agent restarts after losing buffered observations | Backend identifies gaps from persisted schedules/coverage and repairs eligible history |

## Acceptance criteria

- AC-01: Authenticated agent and custom-producer submissions are limited to their
  authorized SLIs, revisions, operation kinds, and ranges.
- AC-02: Typed measurements retain original time and meaning; invalid counts and
  unsupported kind/budget combinations fail explicitly.
- AC-03: Duplicate deliveries and overlapping live/repair observations do not double
  count; conflicting contents do not silently overwrite accepted data.
- AC-04: Acknowledgement implies durable atomic item acceptance. Lost responses,
  backend restart, and partially accepted batches are safe to retry by item identity.
- AC-05: Empty coverage, zero traffic, failed execution, missing execution, and
  uncertain submission remain distinguishable.
- AC-06: The live ring, metadata, and in-flight batches stay within defined limits;
  oldest-result eviction and restart loss remain observable without stopping live polls.
- AC-07: Saturated historical queries or submission paths cannot consume live slots
  or buffer capacity. Historical admission yields to protect the current schedule.
- AC-08: Repair identifies gaps under effective definition history, handles delayed
  live submissions, and terminates unavailable or conflicting work visibly.
- AC-09: Direct-ingestion SLIs work without an agent; gaps are reported without
  claiming automatic recovery from a custom producer with no replay integration.
  Scoped API keys allow authorized submissions and required definition reads,
  without exposing unrelated SLIs or datasource secrets.
- AC-10: Retention and definition changes cannot turn retries into duplicate
  contributions or silently reinterpret historical observations.
- AC-11: Periodic repair persists retry budgets and stops automatically retrying
  exhausted ranges, leaving their missing state and reason visible across restarts.
  An authorized administrator can retrigger selected ranges in a new bounded,
  auditable cycle through the UI or CLI without duplicating active repair work.

## Dependencies and open questions

Before implementation, settle canonical time/numeric precision, observation and
item size limits, replay/retention horizons, definition effective boundaries,
assignment ownership and offline validity, wire formats and error codes, and the
supported OpenSLO measurement/budget combinations. The resource model and correction
policy in this draft require review. Default capacities must be justified by
representative workloads rather than copied without measurement.

## Verification evidence

Not implemented. The examples and criteria require unit and database integration
coverage, failure injection at commit/response boundaries, and concurrent live plus
historical load checks. Documentation checks establish link and text consistency
only; they do not establish delivery or scheduling guarantees.

## Change notes

- 2026-10-24: Initial proposal for ingestion, bounded buffering, and best-effort recovery.
- 2026-10-24: Clarified SLI definition revisions with a delayed-data example; added
  external-producer API keys, user-owned replay, periodic bounded healing, and manual
  administrator retriggering following review.
