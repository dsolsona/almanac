# Alert engine persistence and API implementation plan

This is the implementation map for stages 2–3 of
[plan 0035](0035-alerting-delivery.md), prepared under [F4-T06](../tasks/F4-T06.md).
It does not accept draft contracts or authorize bypassing the calculation gate.
The maintainer authorized continued implementation on 2026-11-13; the primary
agent owns integration and the prerequisite evidence. Base inspected: `0d028cd`.

Governing behavior remains in [SPEC-0021](../specs/0021-alert-policy-engine.md),
[SPEC-0022](../specs/0022-alert-management-ui.md) and
[ADR-0051](../adr/0051-durable-alert-policy-state-machine.md). Delivery, credentials,
leases and egress retain [ADR-0010](../adr/0010-alert-evaluation-and-delivery.md).
Storage and archive requirements retain
[SPEC-0007](../specs/0007-storage-and-retention.md) and
[SPEC-0005](../specs/0005-openslo-and-resource-model.md).
The choices below are proposed implementation details; the explicitly identified
contract decisions must be settled before their affected public operation ships.

## Existing seams and implementation order

There is no alert schema, package, HTTP handler or CLI command. The backend starts
derived/history/maintenance workers in `internal/command/command.go`; wire the
alert worker into the same cancellation and shutdown ownership. Use the existing
`storage.Runtime` and bounded `DerivedTraffic` pool. Do not add database credentials
to agents, another connection pool, or an external stream dependency.

`internal/storage/derived_evaluation.go` supplies exact batched window results;
its current batch limit is 960 inputs. The candidate four-window experiment is
not a four-window product contract. Its adoption remains gated by F4-T02 and the
follow-up contention work. No engine migration/runtime implementation starts
before that gate is resolved under plan 0035.

After that gate, implement in this order:

1. Pure policy validation and reduction, then schema/query contracts with real
   database tests for uniqueness, receipts, claims and concurrent transitions.
2. Shared calculation input adapter and fenced atomic publication, including
   maintenance authority, crash recovery and ordered catch-up.
3. Authenticated policy/occurrence/health operations and CLI parity; register
   handlers in `internal/backendhttp/server.go` before its `/api/v1/` fallback.
4. SLO dependency inspection and confirmed archive transaction, including the
   ordinary archive rejection path, then the selected UI in plan 0035.
5. Integrated 100/200-policy acceptance; neither successful unit tests nor the
   pre-engine calculation experiment establish that acceptance.

Allocate the migration number when implementation starts; another dependency can
add migrations first. Source paths: `internal/storage/migrations/NNNN_alerts.sql`,
`internal/storage/query/alerts_*.sql`, `internal/storage/alerts_*.go`,
`internal/alert/`, `internal/alerthttp/`, `internal/alertclient/`,
`internal/command/alerts.go`, and `api/openapi.yaml`. SQLC output belongs in
`internal/storage/dbgen/`; OpenAPI output is `internal/api/generated.go`.
Regenerate both from their source contracts; do not edit generated output.

## Persistence model

All times are UTC `timestamptz` with canonical microsecond validation. IDs are UUIDs;
versions/epochs/sequences are positive `bigint`. Every historical reference uses
restricting foreign keys; retention deletes children explicitly in bounded batches.
The table names below have the `almanac_` prefix in SQL.

| Table | Columns and constraints | Read/claim indexes |
| --- | --- | --- |
| `alert_policies` | `id` PK; stable `name` UNIQUE; `display_name`, `description`; `slo_id` FK; positive `version` for configuration/metadata; `evaluation_revision`; `enabled`; `binding_state` and nullable `inactive_reason`; severity constrained to low/medium/high; actor and created/updated times. Preserve `enabled` when an archive inactivates the binding. Current revision FK is deferred like SLO definitions. | `(slo_id,id)` for dependency impact/fanout; list indexes on state/severity/name with ID tie-break. |
| `alert_policy_revisions` | PK `(policy_id,revision)`; immutable SLO/SLI definition references, calculation version, effective boundary, activation validity and definition snapshot. Distinguish human edit reason from source revision propagation. Keep metadata/severity snapshots in occurrences, not mutable revision joins. | Effective revision lookup by policy and activation time. |
| `alert_conditions` | PK `(policy_id,revision,condition_id)`; UNIQUE `(policy_id,revision,position)`; label; integer short/long seconds; exact positive decimal threshold; checks short ≤ long and whole-minute durations. Cadence alignment, target-dependent reachability and ≤30-day limits are validated with the referenced definition. | PK/revision order is sufficient. |
| `alert_cursors` | PK `(policy_id,revision)`; `next_scheduled_at`, `last_effective_cutoff`, `last_evaluation_id`; `available_at`, `last_dispatch_at`, `last_attempt_tick`; `lease_epoch`, nullable token/deadline; state version, open occurrence ID, processing state/error and failure counters. At most one current schedulable revision per policy. No cursor advance without a committed outcome or explicit range record. | Partial due index `(available_at,last_dispatch_at,policy_id)` on schedulable rows; expired-lease index on deadline. |
| `alert_evaluations` | UUID PK; UNIQUE `(policy_id,revision,effective_cutoff)`; original scheduled boundary, cutoff, evaluation bounds, commit time, truth in breach/clear/indeterminate, all matched condition IDs, canonical quality/provenance identity, snapshot evidence and reconstructed flag. All-condition evidence includes exact counts, eligibility and each window's quality; never only the triggering condition. | `(policy_id,revision,effective_cutoff DESC)`; retention age. |
| `alert_occurrences` | UUID PK; policy/revision FK; lifecycle state; positive state version; firing evaluation FK; latest evaluation FK; immutable original policy/SLO/severity snapshot; opened effective/detected times; first acknowledgement actor/time; terminal effective/detected times/cause; reconciliation watermark. | UNIQUE `(policy_id)` WHERE state IN ('firing','acknowledged'); active state/time/ID and terminal time/ID indexes; `(slo_id,opened_at,id)` for grouped lists. |
| `alert_transitions` | UUID PK, occurrence FK, UNIQUE `(occurrence_id,sequence)`; from/to state, cause, effective/detected times, actor/request ID, evaluation FK when evidence-driven. Preserve exact original/latest evidence referenced by retained transitions. Stable ID/sequence is the future outbox event identity. | `(occurrence_id,sequence)` and retention age. |
| `alert_ranges` | UUID PK; policy/revision FK; half-open scheduled-boundary range, cadence, kind: repeated cutoff / compacted unchanged outcome / unreconstructed gap / administrative cancellation; quality/provenance identity where applicable; source evaluation reference or explicit reason; actor/request/effective time for cancellation. Disjoint coverage is enforced by writing under the cursor lock and validated by continuity tests. | `(policy_id,revision,start_at)` and expiry. |
| `alert_commands` | PK `(actor_id,operation,idempotency_key)`; normalized SHA-256 request digest including expected versions; request ID; result kind/resource ID/version/redacted result; created time and replay expiry. Serialize first use with the existing transaction advisory-lock receipt pattern. Audit remains durable after replay expiry. | PK supports actor/operation/key reconciliation; actor/request lookup and expiry index. |

Use unconstrained PostgreSQL `numeric` or validated canonical decimal text for
derived sums and threshold storage; do not squeeze summed counts into the
observation `numeric(38,12)` column and silently overflow or round. Final decimal
precision/length for user thresholds is a public-contract decision below. JSONB
evidence is schema-versioned and bounded, with numeric values represented as
decimal strings; frequently filtered fields stay typed columns. Do not depend on
searching JSON evidence for list performance.

The policy API's `version` is its configuration version; `evaluation_revision`
identifies evaluation semantics. Cursor `state_version` fences internal ordering,
and the occurrence API's `version` is its lifecycle/evidence state version. These
are different concurrency domains. A name/severity edit must not
invalidate a valid calculation or rewrite the occurrence's original presentation;
an acknowledgement must not be lost because a worker read the previous state.
Use the occurrence version plus cursor state version in commit preconditions.

## Evaluation and durable scheduling

The internal consumer API is deliberately small. Define concrete types in
`internal/alert` for `PolicyRevision`, `Condition`, `WindowEvidence`,
`Evaluation`, `Occurrence`, `Transition`, `SnapshotIdentity` and `Claim`.
`SnapshotIdentity` contains SLO/SLI revisions, source generation, exclusion
generation and calculation version, in addition to the exact cutoff/bounds.

```go
// Pure: explicit time and canonical evidence; no I/O or floating-point threshold tests.
Validate(policy PolicyRevision, objective Objective) error
Evaluate(policy PolicyRevision, windows []WindowEvidence) (Evaluation, error)
Reduce(current *Occurrence, evaluation Evaluation, at time.Time) TransitionResult

// Storage-owned, embedded backend operations; no remote worker API in this slice.
ClaimAlertWork(ctx context.Context, tick time.Time, limit int) ([]Claim, error)
ReadAlertInputs(ctx context.Context, claims []Claim) ([]EvaluationInput, error)
CommitAlertEvaluation(ctx context.Context, claim Claim, result Evaluation) error
DeferAlertWork(ctx context.Context, claim Claim, nextTick time.Time, reason string) error
```

`TransitionResult` distinguishes unchanged, create, resolve and evidence advance;
`EvaluationInput` contains immutable policy and all windows plus the snapshot
identity. Pending calculation is an error/result at this work boundary, never an
`Evaluation` truth value. Typed errors distinguish pending admission, pending
aggregation, stale claim/snapshot, exhausted source, invalid definition and
cancelled I/O. Evaluate specific exhausted-source errors before their broader
wrapped error classes.

1. A cancellable scheduler uses an explicit shared tick (initial implementation:
   one second, consistent with the existing derived worker). Persisted due times,
   not channels or process memory, are authoritative. Acquire bounded calculation
   admission before claiming more work than can run; pool pressure defers work.
2. In a short transaction, select eligible current heads using indexed due state,
   `FOR UPDATE SKIP LOCKED`, and a bounded limit. Filter expired/no lease and
   `last_attempt_tick < tick`. Order least recently dispatched eligible policy
   first, then due time/ID. Increment epoch, install token/deadline and update
   dispatch/tick. Commit before calculation. A policy gets at most one boundary
   attempt per tick; pending work cannot busy-loop or monopolize a batch.
3. For a head older than the 24-hour horizon, atomically write the exact skipped
   scheduled range and advance only to the first eligible boundary in horizon.
   Keep an existing occurrence open. The following claimed work starts there;
   never skip its remaining backlog in favor of current time.
4. Calculate the canonical cutoff from the original scheduled boundary, including
   settling, dispatch and completion allowances. If it repeats the last committed
   cutoff, record/extend repeated-cutoff coverage and advance the schedule without
   duplicating evaluation or transitions. Use the old boundary's clock during
   replay; wall time does not turn historical fresh evidence into stale evidence.
5. Read all required windows in one repeatable-read snapshot. Deduplicate duration
   and bounds across every condition. A three-condition policy can need six unique
   windows, or fewer if durations overlap. Chunking for the calculator's input
   limit must preserve the same snapshot and one total dirty-hour/raw-row/query
   budget. Never reset admission for each chunk or permit a partly read policy.
6. Share exact window results between policies using the same SLO, cutoff and full
   snapshot identity; thresholds remain policy-specific. Readiness and quality are
   inputs too. Reuse within a bounded admitted batch first; avoid a new persistent
   cache. Every policy still commits independently with its own claim/revision.
7. If any required calculation is pending, release connections and claim, keep the
   same original boundary, and set `available_at` to the next bounded tick. Do not
   record unavailable evidence or consume a failure retry for admission pressure.
   A timer guarantees retry; correctness does not depend on a publication event.
8. Reduce usable evidence under SPEC-0021's exact AND-within/OR-between conditions.
   Full-window unavailable is distinct from pending work. Preserve open state on
   indeterminate. Compare exact rational products, not rounded burn displays; an
   error in the input contract aborts publication, rather than manufacturing clear.
9. Commit through the fenced transaction below. A crash before commit leaves only
   an expiring claim. A crash after commit can repeat the request but cannot repeat
   the unique evaluation or transition. Expiry/epoch checks reject the old worker.

Publication/ingestion already write canonical source generations and dirty work
atomically. Indexed SLO→policy dependencies and persisted cursor times recover all
scheduled work after a lost wakeup. If publication also makes existing alert heads
eligible sooner, that update belongs in its transaction, is idempotent and cannot
move a pending head earlier than its next allowed tick. Do not expand each ingested
point into one queue item per policy or create a blocked-publication dependency
table. Measure any fanout writes against ingestion budgets.

## Fences and transaction boundaries

Do not hold source locks while scanning windows. The snapshot read can close before
publication; its identity is validated under locks in a short commit transaction.
All fact-changing writes must advance the corresponding authority generation in
the same transaction. Otherwise a generation comparison is not a fence.

At publication, take locks in the same global order as applicable source/config
mutations: SLI authority, SLO authority, exclusion authority, policies in UUID order,
cursors, then occurrences. Confirm this order against every existing ingestion and
definition path before adding SQL; avoid holding a policy while waiting for its SLO.
Use compatible immutable-reference locks where possible. The source-generation
row must be locked against updates until commit (plain SELECT, or KEY SHARE on an
unchanged primary key, does not suffice). Keep this interval limited to conditional
checks and writes, and measure the ingestion impact.

Source-generation rows are currently created lazily by the first ingestion. An
absent row cannot be locked: take the existing SLI authority lock while creating
generation zero, then lock/recheck the resulting source row. Respect ingestion's
SLI-before-generation order. Test the exact first-observation race. Do not treat
`coalesce(generation,0)` alone as concurrency protection.

Validate current evaluation revision, active binding/effective boundary, SLO and
SLI meanings, exclusion generation, source generation, calculation version, claim
token/epoch/deadline, unchanged next boundary and expected cursor/occurrence state.
A mismatch rolls back the entire candidate and schedules its current head for
retry/reconciliation. A concurrent acknowledgement requires re-reduction against
the acknowledged occurrence; do not overwrite acknowledgement with a stale state.

One successful transaction writes evaluation, original/latest evidence references,
occurrence mutation, append-only transition(s), durable audit when applicable,
cursor advance and next due state. Future outbound intent can be inserted beside
the transition under ADR-0010. No external call happens in this transaction.

Management mutations validate a current session/role inside the transaction,
serialize the command receipt, check its digest, then acquire resource locks.
Successful receipt replay precedes version conflict checks and returns the original
result. Fresh operations require expected versions. Store mutation, audit and
receipt together. Uncertain transport results are reconciled using receipt/current
resource reads; clients must not create a new key and blindly retry.

## Catch-up, lifecycle administration and retention

- Semantic policy/source revisions supersede an open occurrence, record cancelled
  old pending ranges, fence the old cursor, and create the new revision/cursor in
  one transaction. Metadata/severity-only edits preserve the active occurrence.
- Disable records cancellation and terminal `disabled` cause, without fabricated
  recovery. Re-enable starts a fresh schedulable revision/epoch and occurrence
  opportunity; the terminal occurrence never reopens. The first boundary after
  enable/semantic activation needs the explicit decision listed below.
- Acknowledge targets occurrence ID/version. First successful actor/time remains
  stable. Duplicate acknowledgement of that occurrence can return its already
  acknowledged result; a terminal occurrence conflicts and cannot acknowledge a
  replacement. The command receipt preserves the result of the original request
  even if subsequent recovery has changed current state.
- Keep every effective evaluation inside the replay horizon. Beyond it, compact
  only contiguous unchanged outcomes sharing truth, quality and revision identity.
  Preserve transition-pinned evidence and enough range identity to prove continuity;
  do not compact through a gap, handoff, source-quality change or admin cancellation.
- Retain terminal transitions/evidence, gaps and audit for 365 days; never prune an
  active occurrence's original or latest evidence because its age exceeds a year.
  Expired command replay payloads can be pruned separately once replay policy is
  agreed; their audit identity remains. Maintenance uses bounded batches and no
  unbounded DELETE over live tables.
- Expose lag/reconciliation separately from last known state. Historical firing
  and recovery remain inspectable as detected-after-recovery; no future delivery
  may use a backlog firing transition until current reconciliation is complete.

## SLO archival and maintenance prerequisites

`internal/slohttp/handler.go` currently returns 405 for SLO DELETE. Implement both
ordinary dependency rejection and confirmed archive; this is not extension of an
already working SLO archive endpoint. Add persisted archive state/effective boundary
and dependency versioning to SLO storage. Historical definitions remain queryable.

An archive-impact read returns SLO configuration version, the complete dependent
policy set with configuration/evaluation revisions, open occurrence IDs, the
scheduled effective boundary and the authorized pending-work envelope, plus other
blocking dependents. State versions and current pending ranges may be returned for
display, but are not interchangeable with configuration/dependency identity.
Confirmation supplies that impact identity and expected configuration versions.
Every policy create/rebind locks the SLO authority and checks
archive state; while archive is pending it cannot add a new binding. Revalidate
the complete set under the same locks at activation, not only the supplied IDs.

At the effective boundary one transaction archives the SLO, inactivates bindings
with `slo_archived`, preserves enabled preferences, fences claims, records cancelled
ranges and closes open occurrences as Disabled. Terminal history stays unchanged.
Both SLO archive and policy-management permissions are required. Reconcile this
interpretation in SPEC-0021 before implementing the confirmation operation:

- Confirmation authorizes cancellation of remaining due work through the displayed
  effective boundary. Normal cursor progress can shrink that envelope; it cannot
  authorize a larger range or move the boundary later.
- Revalidate the complete dependency set and expected SLO/policy configuration
  versions. New dependencies, semantic revisions or newly opened occurrence IDs
  invalidate confirmation. Already committed evaluations and resolved occurrences
  reduce impact and need no new confirmation. Acknowledgement of an already listed
  occurrence does not introduce a new occurrence; read its latest state under lock.
- A future activation that cannot revalidate visibly fails and requires refreshed
  confirmation. It cannot silently close an unreviewed occurrence or remain in an
  unexplained pending state. The operation's activation clock must be explicit.

This avoids mistaking each ordinary cursor/evidence-version advance for an expanded
administrative authorization. It is an interpretation to reconcile in the spec,
not an independently accepted amendment in this plan.

No persisted maintenance exclusion catalogue/activation transaction exists at this
base: retained calculations carry exclusion revision fields but readers largely
use revision zero. Pure excluded-window arithmetic is a prerequisite, not the
complete AC-07 implementation. Reuse the shared model from
[SPEC-0016](../specs/0016-maintenance-exclusions.md); provide a canonical exclusion
snapshot/generation whose writes atomically invalidate derived work and fence
alert input publication. Do not introduce alert-only maintenance state. Cover
fully excluded windows, partial-cadence eligibility, edit activation and
`resolved_after_maintenance_change` provenance using real persisted exclusions.

## Public API and CLI source contract

Add operations to `api/openapi.yaml`; preserve existing session/CSRF validation,
request IDs, Problem responses and strong ETag conventions. Viewer may read;
Editor/Administrator may mutate. All new mutations have Idempotency-Key receipts;
existing versioned resources additionally require strong If-Match. ETags identify
`alert-policy:<id>:<version>` or `alert-occurrence:<id>:<version>`.

| Proposed operation | Contract and equivalent CLI command |
| --- | --- |
| `GET /api/v1/alert-policies` | Search/filter by SLO, service, enabled/binding state and severity; stable global sort and cursor page. `alert-policies list`. |
| `POST /api/v1/alert-policies` | Name/display name, description, SLO ID, enabled, severity, ordered conditions, and non-30-day preset confirmation where required; returns resource, ETag and receipt identity. `alert-policies create --input-file … --idempotency-key …`. |
| `GET/PATCH /api/v1/alert-policies/{policy_id}` | Typed configuration update; server classifies metadata vs semantic changes and returns resulting revision, cancelled range/terminal outcome where applicable. No delete operation. `alert-policies get/update --if-match …`. |
| `POST /api/v1/alert-policies/{policy_id}/enable` and `/disable` | Explicit auditable lifecycle command, expected version and optional human reason; fresh activation is visible in result. `alert-policies enable/disable`. |
| `GET /api/v1/alerts` | Occurrence list: active/history, lifecycle, SLO/service, severity, literal search, stable sort, cursor and limit; `group_by=slo` uses server grouping with totals over the filtered set. `alerts list`. |
| `GET /api/v1/alerts/{occurrence_id}` | Original policy/SLO snapshot and real SLO link identity; current lifecycle/version, latest committed evidence, processor lag/reconciliation and terminal cause. `alerts get`. |
| `GET /api/v1/alerts/{occurrence_id}/transitions` | Stable sequence pagination, exact effective/detected times, actor/cause and evaluation IDs. `alerts transitions`. |
| `GET /api/v1/alerts/{occurrence_id}/evidence` | Select firing/latest/specific transition evidence; all conditions/windows with exact decimal counts, quality/reasons, bounds and provenance; no live recalculation of original evidence. `alerts evidence`. |
| `POST /api/v1/alerts/{occurrence_id}/acknowledge` | Expected occurrence version and key; first actor/time global; terminal conflict includes current state and safe refresh path. `alerts acknowledge --if-match … --idempotency-key …`. |
| `GET /api/v1/alert-processing` | Aggregate and policy-filtered due age, lag, pending reason, exhaustion, gaps and last evaluation; never synthesize alert state from health. `alerts processing`. |
| `POST /api/v1/alert-policies/{policy_id}/retry` | Audited retry of exhausted alert work with expected version and receipt; preserves boundary/history. `alert-policies retry`. |
| `GET /api/v1/alert-commands?operation=…&idempotency_key=…` | Actor-scoped reconciliation using the command identity the caller knew before sending; server request ID is returned for tracing but is not required after a lost response. Unknown is unknown, not proof that the original request failed. `alerts command-result`. |
| `GET /api/v1/slos/{slo_id}/archive-impact`; `POST /api/v1/slos/{slo_id}/archive` | Expected SLO version and exact impact confirmation; ordinary archive without confirmation returns dependent IDs; other dependencies still block. `slos archive-impact/archive`. |

OpenAPI component schemas: `AlertPolicy`, `AlertPolicyWrite`, `AlertCondition`,
`AlertOccurrence`, `AlertTransition`, `AlertEvidence`, `AlertWindowEvidence`,
`AlertProcessingStatus`, `AlertHistoryRange`, `AlertCommandResult`,
`SloArchiveImpact`, `SloArchiveConfirmation` and typed paged/grouped results.
Window evidence distinguishes missing/stale/partial/zero traffic/fully excluded/
warmup/failed source; processing status separately expresses pending calculation.
Chart points use existing SLO chart endpoints with explicit bounds/revisions where
available; immutable transition evidence must not be mislabeled as a historical
chart reconstructed later from different facts. Do not invent stored samples.

API lists return matched condition IDs plus short/long burn summaries so the row
explains why it fires without N+1 detail requests. Show 10/20 rows in the UI. Global
filter/group/sort totals must come from the complete filtered relation; page-local
grouping is incorrect. Use keyset cursors including sort values and ID, validated
against the filter identity. If exact cross-page snapshot consistency is promised,
define it explicitly rather than pretending live cursor pages are a fixed snapshot.

Keep CLI action validation strict like `internal/command/objectives.go`; reuse
protected profiles, cancellation, `--input-file`, `--if-match`, output text/JSON
and typed failure handling. Never add a CLI-only database path or recalculation.

## Contract decisions to close before affected implementation

1. **Calculation limits/end-to-end target:** numeric dirty-hour/raw-row/admission
   limits and total detection-delay target must come from the completed experiment.
   Per-policy budget must include every custom condition and be tested at rollover.
2. **Custom-condition bounds:** drafts specify arbitrary ordered conditions, but no
   finite maximum count, threshold decimal precision/length or request-body budget.
   Define validated limits together with calculator admission. Do not silently cap
   the UI at two conditions or implement only four windows; validate a >2-condition
   fixture with shared and distinct durations regardless of the chosen maximum.
3. **Activation/enable replay:** define the first minute boundary for new/re-enabled
   policies and semantic revisions, including an already breached SLO. Recommendation:
   start at the first eligible scheduled boundary at/after activation, using retained
   evidence for full windows but never inventing pre-activation occurrences. Confirm
   alignment with effective source-definition activation before publishing examples.
4. **Retry and receipt duration:** define bounded failure backoff/exhaustion and
   operator retry authority, and a receipt replay period/expiry response. Pending
   admission is not failure exhaustion; expired/unknown receipt cannot authorize a
   blind new mutation. Keep 365-day audit distinct from replay payload retention.
5. **Archive activation and impact stability:** specify the activation clock and
   reconcile the shrinking-envelope interpretation above in SPEC-0021. Require
   configuration/dependency identity, not byte-identical cursor ranges. Ordinary
   progress may reduce impact; new dependencies, semantics or occurrences require
   fresh confirmation. Configuration and state versions need distinct API fields.
6. **Maintenance delivery scope:** the accepted maintenance model needs persisted
   authority and integration before claiming alert exclusion acceptance. Agree the
   bounded implementation slice; revision-zero placeholders cannot meet AC-07.
7. **History charts and reconciliation reads:** choose exact chart/evidence response
   boundaries and public command lookup semantics in OpenAPI; preserve current
   original/latest distinction and never convert absent receipt into failed command.

These are specific gaps, not a request to repeat already accepted UI or lifecycle
decisions. Continue independent work while an affected contract is being resolved.

## Verification map and command selection

Implement failing cases before behavior. Planned tests are named below so focused
commands select the intended assertions; adding tests with these names is part of
their implementation step. All application commands execute through Docker after
the primary agent releases the shared measurement lane.

| Criteria | Independent cases and test location |
| --- | --- |
| AC-01–03 | `internal/alert`: 99.9% target, 10,000 total, 144 bad matches 14.4; 143 on either side does not. OR handoff, three+ custom conditions, full warmup, zero/missing/stale/excluded distinctions and no floating-point boundary drift. |
| AC-04–05,13 | `internal/storage/alerts_engine_integration_test.go`: two real workers; duplicate cutoff; commit/crash on either side; expired lease; source mutation and first-ingestion race; one snapshot across custom windows; pending at rollover releases resources and preserves breach→recovery ordering; fairness with one permanently pending policy. |
| AC-06–07,14 | `internal/storage/alerts_lifecycle_integration_test.go`: ack/ack and ack/recovery orderings, stale request cannot ack replacement; disable/edit cancellation, metadata preservation, persisted maintenance edit race and archive impact changed by new binding, evaluation, ack or semantic edit. |
| AC-08–09 | `internal/storage/alerts_history_integration_test.go`: 24-hour deterministic clock, explicit beyond-horizon gap, no cursor hole, historical breach/recovery, active evidence older than 365 days, compaction cannot cross provenance/quality transitions. |
| AC-10 | `internal/alerthttp`, `internal/alertclient`, `internal/command`: viewer rejection, CSRF/session expiry, expected versions, request-key digest mismatch, lost response reconciliation, malformed exact decimals/durations, real API/CLI persisted result parity. |
| AC-11–12,15 | Plan 0035's real 100/200-policy fixture, same-SLO fanout, more-than-two-condition case, continuously overlapping browser/ingestion/worker measurements and all required live/catch-up durations. This remains separate from focused tests. |

Future focused commands, once the corresponding package/tests exist:

```sh
make shell CMD='make generate GENERATE_MODE=go-guidelines'
make shell CMD='go test ./internal/alert -count=1'
make shell CMD='make generate GENERATE_MODE=sqlc'
make shell CMD='go test -tags=integration ./internal/storage -run TestIntegrationAlert -count=1 -timeout=5m -v'
make shell CMD='make generate GENERATE_MODE=api'
make shell CMD='go test ./internal/alerthttp ./internal/alertclient -count=1'
make shell CMD='go test ./internal/command -run TestAlert -count=1'
make shell CMD='go test -tags=integration ./internal/command -run TestIntegrationAlert -count=1 -timeout=5m -v'
```

Split the storage selection by the exact implicated test names while iterating;
run the complete alert selection once its integrated scope warrants it. Add harness
entry points for the sustained workload in its task before execution, not guessed
commands here. Record command duration, schema/build identity, cutoff, fresh/cache
conditions and failure artifacts. CI owns broader PR gates.

For this docs-only planning task: review source references, run `git diff --check`,
and run the existing documentation checker when the shared lane is released.
Do not run application tests or restart containers to validate this plan.
