# Persisted maintenance authority and revision fencing plan

Use the executing-plans workflow after primary review and authorization of each
implementation slice. Slice A internal authority and focused PostgreSQL tests
are authorized. The primary agent owns publication under the maintainer’s
unattended-delivery authorization; production calculation adoption remains gated.

**Goal:** provide the persisted authority, reproducible revision identity and
publication fence required for maintenance-aware alert evaluation under
[SPEC-0016](../specs/0016-maintenance-exclusions.md),
[ADR-0014](../adr/0014-maintenance-exclusion-semantics.md) and
[SPEC-0021 AC-07](../specs/0021-alert-policy-engine.md).

**Task/base:** [F4-T08](../tasks/F4-T08.md), `f17717ed363c6290f457f6bee6fde76e7385fc3e`.
F4-T05 pure occurrence masks are now present. Production calculator adoption
still belongs to slice B. Migration 0053 is the coordinated F4-T07 dependency.
The primary clarified that A+B and engine integration can verify persisted
exclusion AC-07 for supported occurrence SLOs independently. Composite/slice
models, OpenSLO import and the full public maintenance UX are separate feature
dependencies; they do not expand this task or prevent that bounded AC-07 claim.

**Architecture:** immutable maintenance revisions and explicit SLO memberships,
a monotonic per-SLO exclusion revision, and durable change/work records commit
atomically. Calculators read one source/definition/exclusion snapshot; publishers
lock and check its exclusion revision before committing. Shared SLI facts and
hourly summaries remain unmodified. Reuse PostgreSQL, SQLC, current human session
authority, management receipts and audit infrastructure; no new infrastructure.

## Existing paths and dependencies

| Concern | Existing implementation | Consequence for this work |
| --- | --- | --- |
| Session authority | `agent_reads.go`: `beginPooledAgentRead`, `finishAgentRead`; `agent_lifecycle.go`: completion-time checks | Reuse installation/user/session locks and reauthorization, adding elapsed-time permission checks after resource waits and immediately before commit. |
| Optimistic writes/replay | `objectives.go`: `CreateObjective`, `UpdateObjectiveMetadata`; `agent_lifecycle.go`; `almanac_management_receipts` | Reuse actor/operation/key receipt identity, deterministic request digest, 24-hour replay and expected revisions. Objective receipts are restricted to service/SLO create and are not the right table. |
| Audit | `almanac_audit_events`, `InsertAgentAudit` | Events lack detailed metadata. Immutable maintenance revisions must retain reason, bounds, scope, actor, predecessor and request ID, linked to the atomic audit event. |
| Definition authority | `objectives.sql`: `GetSlo`, `InsertSloDefinition`; `slis.go`: `reconcilePendingSliActivation` | Current management creates definition revision 1 and edits metadata; no general SLO/SLI semantic revision/import apply path exists. Validate current effective grids now; expose a shared validator for future revision/activation paths. |
| Cadence changes | `datasources.go` rejects cadence changes while SLIs reference the datasource | Preserve this guard. Maintenance must not create an alternate cadence-change path. |
| Canonical/totals math | F4-T05 `WindowInput.Exclusions`, `WindowTotals.Excluded`; `derived_evaluation.go` | Add SLO/revision identity to storage calculation inputs. Current totals accept SLI/revision only and cannot safely infer an exclusion scope. |
| Derived publication | `derived_projections.sql`, `derived_overview.go`, `slo_list_references.sql`, `slo_extrema.sql` | Generation stamps and many queries assume exclusion revision zero. Every read/currentness/publication path needs the actual revision before exposing mutations. |
| Extrema schema | `0045_slo_extrema.sql` requires `exclusion_revision=0`; cursor key omits it | A forward migration and versioned publication/read predicates are required; never edit the historical migration. |
| Import and archive | No OpenSLO import/apply implementation, general semantic definition editor or SLO archive authority exists in this base | These are dependencies, not existing hooks to claim tested. Future import/activation/archive must share scope/alignment validation and preserve references. Coordinate with the alert contract/archive work. |
| Consumer support | Public quality/sample enums, browser validators and retained series do not yet accept `excluded` | Do not expose nonempty production masks until those consumers, exact values and alert unavailable handling are integrated. |

## Policy constraints and clarification boundaries

- Scope resolves to explicit SLO IDs, never an SLI-wide or dynamic service mask.
  At most 100 affected SLO IDs per mutation; count the old/new scope union so a
  replacement cannot escape the bound. The primary approved this bounded-write
  implementation interpretation during plan review.
- Half-open bounds are UTC instants aligned to every applicable effective
  observation/slice grid. Report exact suggested common bounds, never apply them
  automatically. Composite expansion and slice grids require their own supported
  resource model; do not pretend the existing occurrence-only model implements them.
- Viewer reads; Editor creates/changes wholly future exclusions. Any ordinary
  change affecting an active/historical exclusion requires Administrator.
  A separate prospective-end operation changes only the explicitly chosen end
  of an active record, preserving elapsed coverage, title and scope; its reason
  is recorded. Recheck that the selected boundary is still future at commit.
- Future cancellation and active/historical withdrawal append tombstones, not
  deletes. All changes require a reason and expected revision; create has no
  predecessor. Same-body idempotent replay does not repeat the mutation or audit.
- SPEC-0016 specifies at least **400 days** of maintenance provenance; SPEC-0007
  currently states at least 365 days. Retaining at least 400 satisfies both lower
  bounds. Reconcile the cross-reference wording; do not shorten the stronger rule.
  References from retained evidence can require longer retention.
- Maintenance is not silence. Fully excluded windows are unavailable to alerts,
  preserve open occurrences and reset the continuous no-data timer. They cannot
  resolve as recovery or fire no-data solely because they are excluded.
- Preserve all of SPEC-0016. Storage-only slices are internal prerequisites;
  UI/API/CLI parity, overlays, raw/adjusted reports, composites, permissions and
  end-to-end alert behavior remain required for feature completion.

## Slice A persisted model and deferred extensions

Use forward migration 0054. F4-T07 reserves 0053; the loader requires contiguous
versions, so the authority branch explicitly depends on that migration.

1. `almanac_maintenance_exclusions`: UUID identity, current revision, creator and
   created/updated timestamps. A deferred `(id,current_revision)` foreign key
   references its immutable revision, following the SLO definition pattern.
2. `almanac_maintenance_exclusion_revisions`: `(exclusion_id,revision)` primary
   key; predecessor is implicitly `revision - 1`, with no separate predecessor
   column/FK. It stores title, required change reason, start/end, operation
   (`create`, `edit`, `end`, `cancel`, `withdraw`), tombstone state,
   actor, recorded server time, request ID and audit event ID. Ordered finite bounds,
   nonempty title/reason, positive revisions and relational FKs are DB constraints.
   Triggers forbid UPDATE of revision/scope/change content. DELETE remains possible
   for a future bounded retention transaction after the 400-day/reference checks;
   no runtime delete/cleanup operation is exposed in slice A.
3. `almanac_maintenance_exclusion_scopes`: `(exclusion_id,revision,slo_id)` key,
   foreign keys to the revision and stable SLO. No cascading deletion of evidence.
   Scope snapshots remain available after resource metadata or folder changes.
4. `almanac_slo_exclusion_state`: one row per SLO, monotonic nonnegative revision,
   changed-at timestamp. Backfill zero for existing SLOs and insert zero in the
   same transaction as new SLO definitions; there must always be a row to fence.
5. `almanac_slo_exclusion_changes`: immutable `(slo_id,revision)` change record
   identifying the maintenance revision and whether that SLO remains included.
   Previous SLO revision is implicitly `revision - 1`; actor, reason and old/new
   declaration bounds are available through the referenced maintenance revisions.
   Store recorded time and whether the declaration's membership/bounds changed.
   Removed scopes receive an explicit change record too. This reconstructs a
   previous SLO exclusion snapshot by selecting the latest change for each
   maintenance identity at or before the requested SLO revision.
6. `almanac_slo_exclusion_work`: one coalescible row per SLO, desired exclusion
   revision, conservative affected-range envelope, work version and next-attempt
   time. This durable invalidation record commits with the revisions even before
   a consumer runs. Acknowledgement checks desired revision and work version;
   an older acknowledgement cannot erase a newer change. Claim/lease/failure
   fields and execution are deferred to B. The envelope is a queue hint; B derives
   actual affected ranges from immutable changes rather than invalidating gaps
   between distant old/new declarations.

Reuse `almanac_management_receipts` for mutation replay with versioned receipt
payloads and operations `maintenance.create/edit/end/cancel/withdraw`. Reuse the
audit table with a dedicated insert query returning the event ID. No source
observation, ingestion generation or SLI aggregate is changed by maintenance.

Each resource edit appends its own revision. The per-SLO revision identifies the
complete exclusion/provenance snapshot, including reason changes. Record whether
declaration coverage changed separately: metadata-only revisions must not falsely
attribute a later service recovery to changed maintenance mathematics. Even a
changed declaration can leave the effective union unchanged because of another
overlapping declaration. Alert cause attribution must compare effective inputs at
the same source snapshot; this flag alone is not evidence of changed mathematics.

## Proposed storage interfaces and transaction order

Create `internal/storage/maintenance.go`, `maintenance_snapshot.go` and
`query/maintenance.sql`; regenerate SQLC rather than editing `dbgen` manually.

```go
CreateMaintenance(context.Context, string, CreateMaintenanceInput) (MaintenanceRecord, error)
EditMaintenance(context.Context, string, EditMaintenanceInput) (MaintenanceRecord, error)
EndMaintenance(context.Context, string, EndMaintenanceInput) (MaintenanceRecord, error)
CancelMaintenance(context.Context, string, MaintenanceCommandInput) (MaintenanceRecord, error)
WithdrawMaintenance(context.Context, string, MaintenanceCommandInput) (MaintenanceRecord, error)
ReadMaintenance(context.Context, string, ReadMaintenanceInput) (MaintenanceRecord, error)
ListMaintenance(context.Context, string, ListMaintenanceInput) (MaintenancePage, error)
```

Inputs carry `DatasourceProof`, server `Now func() time.Time`, request/idempotency
identity, and expected revision for existing resources. Create/edit carry title,
reason, UTC bounds and explicit SLO IDs; end carries only new end and reason;
cancel/withdraw carry ID, expected revision and reason. Read/list use current
Viewer-or-higher authority and existing bounded cursor conventions. Errors
distinguish invalid alignment/scope, forbidden historical change, stale revision,
missing/archived dependency and conflicting idempotency digest. Alignment errors
include affected SLO/grid identities and exact suggested common bounds.
Slice A list performs one identity query plus two queries per returned record
(up to 401 at API-style limit 200). It is internal only; batch the records/scopes
and measure before exposing a public API latency claim.

Internal snapshot interfaces:

```go
readSloExclusionSnapshot(ctx context.Context, q *dbgen.Queries,
    sloID pgtype.UUID, start, end time.Time) (sloExclusionSnapshot, error)
fenceSloExclusionSnapshot(ctx context.Context, q *dbgen.Queries,
    sloID pgtype.UUID, expectedRevision int64) error
```

The snapshot contains SLO ID, exclusion revision, exact effective union, referenced
maintenance revisions/reasons, and validity bounds. Load it inside the same
repeatable-read snapshot as source facts and definitions. Retained report/alert
evidence carries this manifest, not merely an unexplained revision number.

Mutation order:

1. Validate bounded inputs and canonicalize scope/digest; begin current human
   authority transaction. Serialize the actor/operation/key receipt using the
   existing transaction advisory-lock pattern. Reauthorize replay and return the
   original saved response without a second revision/audit.
2. Lock existing maintenance identity; compare expected revision and determine
   old/new scope. Lock affected SLO roots in stable UUID order, then their
   exclusion-state rows in that same order. Read current/effective definitions
   under these locks; reject missing/archived or unsupported dependencies.
3. Sample server time **after** lock waits. Validate role against old/new elapsed
   coverage and operation, all effective grids and explicitly confirmed bounds.
   Do not use request-start time or PostgreSQL's fixed transaction-start timestamp
   to excuse a window that became historical during a wait.
4. Append maintenance revision/scope, increment each affected SLO exclusion
   revision, append change/audit, and upsert bounded invalidation work atomically.
   Save the exact replay response. Sample time again before commit, repeating
   elapsed-coverage and session checks; any failure rolls everything back.

Definition activation/import obtains SLO authority in the same order and runs
the same grid validator against all scheduled effective maintenance. It must not
lock maintenance roots after SLO rows: maintenance writers take those in the
opposite order. Reading committed maintenance under the SLO lock is sufficient
because a concurrent maintenance writer must subsequently acquire that SLO lock
and validate the newly activated grid before committing.

## Calculator, invalidation and publication integration

- Introduce SLO-scoped storage calculation input carrying the exact exclusion
  snapshot. Resolve it once for the union of requested window bounds; pass its
  mask to F4-T05 for raw and totals quality/arithmetic. Mask selection, excluded
  counts, eligible timestamps and source readiness all use the same snapshot.
- Extend the shared window SQL to split eligible/excluded populations. Fully
  covered shared hourly aggregates can contribute to either population unchanged;
  a partly excluded hour requires exact interval-level counts. Never subtract
  event totals without also adjusting first/last observation, receipt metadata,
  observed/empty/zero counts and eligibility. Do not sum overlapping masks twice.
- Many mask boundaries can create many raw edges. The accepted bounded-work and
  admission limits still apply; do not add an unlimited raw scan or adopt the
  experimental dirty-hour fallback implicitly. Before this slice, measure a
  bounded partition/edge strategy and specify how maintained SLO results satisfy
  highly fragmented masks. Exceeding a work budget remains pending/unavailable,
  not raw no-data or fabricated success. This is a real integration dependency.
- Add exclusion revision to extrema cursor identity/claims, publication predicates,
  retained payload identities and actual generation stamps. Replace hardcoded
  zero comparisons in `derived_due.sql`, `derived_projections.sql`,
  `derived_overview.sql`, `slo_list_references.sql`, `slo_sparkline.sql`,
  `slo_extrema.sql`, relevant retention queries and Go generation consumers.
- A changed source interval can affect later rolling evaluations through its
  end plus the largest applicable SLO/burn window. Dirty only the conservative
  union of those ranges, clipped to retained evaluation time; include old and new
  scope/bounds. Future scheduling must wake due work at start/end without ingestion.
  Do not invalidate every SLO sharing the SLI or rebuild unrelated source facts.
- Advancing the SLO-wide exclusion revision must not force all retained history
  to rebuild before unaffected ranges are usable. Before slice B, define bounded
  reuse of old physical extrema rows whose input windows do not intersect changes since
  their exclusion revision, with explicit new snapshot provenance. Merely adding
  `exclusion_revision = current_revision` to every extrema lookup would violate
  SPEC-0019's unaffected-range availability rule. Test a future exclusion and a
  narrow historical edit against distant retained ranges; no new preparation wait
  or unbounded revision-chain scan is acceptable.
- At publication, lock the SLO exclusion-state row `FOR SHARE`, compare the
  computed revision and keep the lock through the publish transaction. Mutation
  increments require a conflicting lock. Under repeatable-read, a concurrent
  revision change may produce serialization failure; discard and retry bounded
  work. A bare MVCC `WHERE revision = expected` without locking is insufficient.
- Last-known values from another exclusion revision must never appear current.
  Preserve original evidence explicitly, or return unavailable where incompatible;
  exact-value pages stay pinned. Wire/sample enums, client validators and charts
  must understand excluded values before production masks can reach them.
- Alert claim/evidence transactions use the same exclusion revision fence. A
  maintenance edit retries still-pending ordered boundaries with the new snapshot;
  it does not supersede the policy, cancel its pending range or replay historical
  notifications. Preserve sent evidence. A resolution caused by changed coverage
  records `resolved_after_maintenance_change`; all-excluded resets no-data timing
  and preserves the open occurrence. These lifecycle operations belong to engine
  integration, not the storage-authority slice alone.

## Implementation slices and checks

### A. Persist internal authority without exposing unsupported public behavior

- [x] After authorization, read full Go 1.27.3 guidelines through the Docker target.
- [x] Add forward schema, SQLC queries, storage operations and snapshot reads.
- [x] Verify PostgreSQL cases: 100-ID boundary,
  two SLOs sharing an SLI, overlapping maintenance, immutable revision/tombstone,
  same/different-body replay, Viewer denial, Editor future success, historical
  Admin-only, prospective end, revision conflict and complete rollback on audit/work
  failure. Use real row locks to cross the historical boundary while waiting.
- [x] Test revoked-session replay/session expiry, scope remove/add, missing dependencies,
  current/future grids, explicit alignment suggestions, and retained old snapshots.
- [x] Run, after lane release:
  `make shell CMD='go test -tags=integration ./internal/storage -run TestIntegrationMaintenanceAuthority -count=1 -timeout=3m -v'`.
  Expected: all new authority cases pass; no production maintenance route exists yet.
  Final combined authority/alignment selection passed in 8.292 s; exact evidence
  and the fixture failures are recorded in F4-T08.

### B. Fence actual derived and alert consumers

The concrete [ADR-0052 candidate](../adr/0052-compiled-maintenance-populations.md)
addresses the fragmentation and old-range compatibility prerequisites below. It
remains proposed; implementation/adoption cannot silently replace the accepted
source path or adopt failed fallback limits.

- [ ] Integrate F4-T05; implement the bounded storage population strategy only
  after resolving the fragmentation/admission dependency above.
- [ ] Add forward exclusion-aware extrema/generation schema changes and update
  every currentness, publication, read, failure-budget and retained-value consumer.
- [ ] Add real publication races: old computation commits before mutation or is
  fenced afterward; it never publishes old math as the new revision. Race two
  edits, work coalescing and claim completion; prove no lost invalidation.
- [ ] Prove raw/totals adjusted values, overlapping exclusions, expired/missing
  populations and scope independence through the storage path. Check current,
  history and alert inputs against the same independently derived expectations.
- [ ] Run focused `TestIntegrationMaintenanceSnapshot` and
  `TestIntegrationMaintenancePublicationFence` selections through `make shell`,
  plus `go test ./internal/slo -count=1`; record actual durations and failures.
- [ ] Coordinate the engine acceptance test for AC-07, preservation of historical
  evidence, no-data timer reset and maintenance-caused resolution. The absent
  engine is a dependency, not a passing integration result.

#### Concrete B sequence for the next implementation authorization

1. **Record the narrow contract extension.** SPEC-0020's accepted retained-history
   scope explicitly keeps `exclusion_revision=0`; its dirty-interior fallback is
   still a proposal. Before changing those consumers, record the authorized
   occurrence-maintenance extension in SPEC-0020/ADR-0040 without presenting the
   independent fallback experiment as accepted. No new resource model or public
   maintenance workflow is needed for this extension. Pin work limits with the
   primary after the calculation gate, rather than promoting experiment defaults
   (8 dirty hours/7,200 raw rows) into a production contract by accident.

2. **Build a single scoped calculation entry point.** Add an internal
   `evaluateSloWindows` in `derived_evaluation.go` taking the SLO definition, one
   already-open repeatable-read transaction, explicit window inputs and admitted
   work limits. Return results plus the immutable exclusion snapshot. Obtain the
   exclusion revision with the definition/context read; revision zero keeps the
   existing empty-mask math and query shape. For a nonzero revision, load one mask
   for the union of requested windows using `maintenance_snapshot.go`. Replace
   the unbounded history reconstruction on this production path with bounded
   current-mask access; historical snapshot reconstruction remains separate.
   Decide and test its indexed access/compaction strategy before enabling callers.

3. **Split populations without duplicating source work.** Add source SQL beside
   `retained_window_batch.sql` for eligible/excluded segments. Union declarations
   first, intersect with validity/retention and partition into disjoint portions.
   Reuse identical portions across requested windows. Whole cadence-compatible
   hours can reuse unchanged SLI aggregates; partial hours require exact raw
   intervals. Feed eligible sums/counts/observation bounds/receipt time and separate
   `ExclusionTotals` to F4-T05. Never subtract only numeric totals. An unavailable
   excluded population keeps excluded event counts null and cannot manufacture
   freshness or make complete eligible intervals missing. Eligible dirty interiors
   retain pending/failed behavior unless a separately accepted fallback admits
   them. Check full alert-window readiness independently of current-status
   eligibility; masks do not create eligible time before activation.

   Admit the entire distinct source plan before raw work, counting mask edges,
   ordinary edges and any accepted dirty replacements together. Fragmentation
   cannot multiply per-window/per-policy allowances. Over-budget work returns no
   partial numeric answer and stays durably pending. If fragmentation repeatedly
   prevents completion, define a bounded maintained-result strategy with the
   primary; permanently refusing a legitimate exclusion is not delivery. Verify
   actual examined/filtered rows, time, cancellation and admission, not LIMIT rows
   or an estimated row count alone.

4. **Fence publication and preserve unaffected extrema.** Add a forward migration
   removing the zero-only extrema constraint and carrying exclusion revision in
   cursor claims and generation publication. Update `derived_current.go`,
   `derived_overview.go`, `derived_history.go`, `derived_values.go`,
   `slo_operational.go`, `derived_budget.go`, `slo_extrema_worker.go`,
   `slo_extrema_edges.go` and their source queries. Every writer checks the
   snapshot with `fenceSloExclusionSnapshot` in its final publication transaction;
   keep existing source-generation/definition/lease checks. Acquire the exclusion
   lock only when needed for publication, not across admission waits.

   Add a bounded claim/lease/failure consumer for `almanac_slo_exclusion_work`.
   Derive disjoint affected evaluation ranges from old/new immutable declarations,
   expanding by the applicable rolling/burn windows and completed-day semantics.
   The queue envelope alone must not invalidate the gap between distant ranges.
   Start/end boundaries schedule current work even without ingestion. Versioned
   acknowledgement must survive restart, newer edits and expired claims.

   Specify a directly checkable compatibility proof for an unaffected old extrema
   row under the current exclusion snapshot. It must not require walking every
   intervening revision or rebuilding a whole retained year. The concrete proof
   representation and update cost need review before this migration/query work;
   blindly filtering every read to the new global revision is not sufficient.

5. **Carry the result through supported consumers.** Add the `excluded` quality
   and null numeric semantics to affected OpenAPI output definitions, generated
   types, browser validators and existing SLO current/history displays. This is
   truthful consumption of occurrence results, not a maintenance-management UI.
   Reject incompatible old cached values or retain their original evidence with
   explicit state, following F4-T04; never relabel their revision as current.
   Exact-value/report evidence retains its source, definition, calculation and
   exclusion snapshot. Public maintenance routes remain disabled until these
   consumers are coherent. The internal list's batching concern belongs to the
   later public endpoint, not the alert calculation critical path.

6. **Verify storage math and engine AC-07 separately.** Add
   `maintenance_calculation_integration_test.go` with an independent raw SQL oracle:
   the 900/1,000 versus 1,000/2,000 example, overlap/adjacency, 60/70-second edges,
   gaps inside/outside, all-excluded, zero traffic, unknown excluded totals,
   retention, old snapshots and two SLOs sharing an SLI. Add explicit budget
   deferral and examined-row tests. Extend publication races to actual current/
   extrema writes and test unaffected distant ranges during a narrow edit.

   The engine owner then tests a persisted maintenance edit while an ordered
   boundary is pending/in flight: retry that boundary under the new snapshot,
   preserve policy revision and already-sent evidence, and do not cancel the
   pending range or replay pages. Fully excluded input preserves an open
   occurrence and resets its continuous no-data timer. Attribute
   `resolved_after_maintenance_change` only when effective maintenance inputs
   explain resolution at the same source snapshot; the declaration's
   `coverage_changed` flag alone cannot prove causality. These occurrence AC-07
   checks do not claim the complete SPEC-0016 public feature is delivered.

Run focused storage calculation/publication selections and pure SLO tests first.
After adoption authorization, repeat the existing 200-SLI contention/browser/
ingestion gate with persisted masks and both empty and fragmented populations;
record incomplete requests and preserve unchanged acceptance targets. No B source
changes or runtime verification occurred during the slice A handoff.

### C. Separate follow-up: complete the public maintenance workflow

- [ ] Define OpenAPI and CLI parity for read/list/create/edit/end/cancel/withdraw,
  version conflicts, alignment preview and change history; apply normal CSRF,
  session and body-limit rules. Do not publish a reduced substitute contract.
- [ ] Deliver the required UI, UTC/DST confirmation, overlay/event table,
  raw/adjusted report evidence and supported composite/slice paths in their
  owning tasks. All use the same authority, not duplicate persistence logic.
- [ ] Preserve at least 400-day revision/audit provenance and longer evidence
  references; verify bounded cleanup cannot remove pinned evidence.
- [ ] Run real API/CLI/browser and alert acceptance plus the established latency/
  ingestion gate before marking the complete SPEC-0016 feature delivered.
  Occurrence-based persisted-exclusion alert AC-07 is verified in B plus engine
  integration without claiming these separate feature dependencies are complete.

SQLC generation uses `make shell CMD='make generate GENERATE_MODE=sqlc'`;
OpenAPI generation belongs only to slice C. Exact follow-up fixture commands and
migration numbers are confirmed in each authorized task before its code edits.
Do not run suites while another stream owns the shared measurement lane.

## Review focus and unresolved dependencies

Review lock order, post-wait permission time, zero-to-first exclusion fencing,
scope-removal invalidation, old-snapshot reconstruction and ordinary-read budgets.
There is no identified contradiction in accepted maintenance policy requiring a
reduced contract. Remaining dependencies are implementation gaps: supported
definition/composite/import/archive models, nonzero derived revisions, excluded
wire/display states, bounded fragmented-mask evaluation, and the durable alert
engine. Slice A can be reviewed and tested internally while those owners work;
it must not be advertised as usable public maintenance until the dependent slices
are integrated and verified. A+B remains the scope of this foundation; later
public maintenance work owns C rather than expanding the alert task into new
resource models.
