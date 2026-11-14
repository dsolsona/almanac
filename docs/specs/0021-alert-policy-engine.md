# SPEC-0021: Alert policy engine

- Status: draft
- Created: 2026-11-12
- Accepted: pending
- Approval: full draft pending; continued implementation and unwired foundations authorized, with initial 100/200-policy verification scope
- Implementation: in progress
- Related ADRs: [ADR-0051](../adr/0051-durable-alert-policy-state-machine.md) (proposed)
- Plan: [0035](../plans/0035-alerting-delivery.md); initial design review in [F4-T01](../tasks/F4-T01.md)
- Supersedes / superseded by: proposed replacement of SPEC-0011's initial evaluation/lifecycle and release scope; no accepted contract is superseded yet

## Purpose

Give operators one durable, explainable alert for a policy's burn conditions,
with global acknowledgement and recoverable evaluation progress. Deliver it in
Almanac before requiring notification destinations.

## Scope and non-goals

Include policies, exact paired-window evaluation, active alerts, acknowledgement,
history, data-quality/processor visibility and equivalent UI/API/CLI operations.
Initially support regular, single-objective, non-composite Occurrences SLOs with
one SLI and `0 <= target < 1`, preserving SPEC-0011's capability restrictions.
Unsupported definitions remain visible with an explicit inactive reason.

External delivery, silences, standalone data-quality alert conditions, reminders,
acknowledgement expiry, assignment, notes, escalation and expression languages
are deferred. Email, Slack and PagerDuty follow in that order. This does not
remove maintenance exclusions from calculation semantics or relax their fencing.
Deferring standalone data-quality conditions deliberately removes SPEC-0011's
initial automatic no-data/stale warning alert. The first release shows data
quality and processor health but does not open an alert solely because observations
stop. This scope tradeoff is explicit in the review direction below.

### Relationship to accepted contracts

This draft records a substantial scope change separately so accepted text remains
stable during review. On acceptance, update the linked records together:

| Existing contract | Proposed amendment |
| --- | --- |
| [SPEC-0011](0011-alerting-and-notifications.md) | Replace independent rule occurrences, latest-only restart behavior and webhook-first initial scope with this spec. Retain delivery/security requirements for later delivery work, subject to adapter-specific review. |
| [SPEC-0012](0012-user-interface.md) | Add the selected alert UI contract in SPEC-0022; shared foundations remain. |
| [SPEC-0014](0014-delivery-and-verification.md) | Make the first alert outcome UI/API/CLI with acknowledgement. Delivery, silences and configurable data-quality alerts remain later F4 outcomes. Other F4 requirements stand. |
| [SPEC-0006](0006-slo-calculation-and-reporting.md), ADR-0043 | Reuse exact calculations and due-measurement timing; full alert windows differ intentionally from shortened current SLO status. |
| [SPEC-0007](0007-storage-and-retention.md) | Preserve 365-day alert history and active-record protection; add bounded evaluation recovery records. |
| [SPEC-0016](0016-maintenance-exclusions.md) | Preserve adjusted populations, full-exclusion behavior and explicit maintenance-change resolution reasons. |
| [SPEC-0005](0005-openslo-and-resource-model.md), SPEC-0007 | Retain dependency rejection for ordinary delete/archive requests; propose an explicitly confirmed SLO archive that atomically deactivates affected alert policies while retaining historical references. Other dependent-resource restrictions remain. |
| [SPEC-0020](0020-cumulative-slo-calculation.md), [ADR-0040](../adr/0040-cumulative-sums-and-compact-extrema.md) decision 1 | Propose bounded raw replacement of dirty interior portions in the shared exact calculator, gated on experiment evidence and acceptance. If calculation remains pending, retain the original boundary for retry; no unbounded full-window fallback or partial policy commit. |

## Behavior and contracts

### Vocabulary and policy

- **Alert policy:** one saved SLO selection, name, enabled state, severity and
  ordered conditions. Multiple policies may select the same SLO.
- **Condition:** one short window, long window and burn threshold, with a stable ID.
- **Alert:** one occurrence for a policy/SLO, with a stable ID and transition history.
- **Acknowledged:** someone is handling that alert for the team; not a personal read flag.
- **Destination/channel/notification/delivery:** respectively a recipient, transport
  kind, transition message and recipient-specific attempt lifecycle, for later scope.

Start with fast burn (5 min / 1 h at 14.4×) and sustained burn (30 min / 6 h at
6×), editable and removable with at least one condition required. These are
30-day SLO starting points. For another period require explicit preset confirmation
or custom values; do not silently present the same budget-spend assumptions.
The initial UI reference uses 30-day fixtures; its non-30-day state remains to render.

Severity is Low/Medium/High, policy-level and informational for the initial
release. It does not alter thresholds, evaluation cadence or acknowledgement.
Different severity/routing needs use separate policies. There is no AND/OR picker.
Validate finite positive thresholds, `short <= long`, whole-minute durations
aligned with the supported calculation cadence and retained data. The initial
window maximum is 30 days. Reject a threshold above `1 / (1-target)` as unreachable;
do not copy the mock's fixture-specific 1,000× bound into general validation.
Wire durations use integer seconds; thresholds use exact decimal strings.

### Evaluation boundaries and inputs

Propose fixed one-minute scheduling boundaries initially. Derive the effective
measurement cutoff using the canonical source settling, dispatch and completion
allowances in [ADR-0043](../adr/0043-slo-status-from-due-eligible-measurements.md).
Align to the common supported cadence; coarser sources may repeat a cutoff.
Repeated effective boundaries are idempotent. Do not add SPEC-0011's older
two-minute allowance on top. Record scheduling time, effective cutoff, decision
commit time, and actual half-open UTC bounds `[T-duration,T)` separately.

Evaluate all conditions at one consistent cutoff/revision snapshot. Use exact
good/total decimals, target and comparison by cross multiplication:
`bad >= threshold * total * (1-target)`. Display rounding never decides state.
The same canonical counts must explain the decision in API/CLI/UI evidence.

#### Pending aggregate preparation and proposed bounded fallback

A dirty hour intersecting a window interior returns **pending** from the canonical
aggregate/raw-edge path. Pending is an evaluation-work state, not a fourth
condition truth value and not a committed unavailable result. If any required
window needs pending aggregates, first attempt the candidate bounded calculation
defined in [SPEC-0020's proposed amendment](0020-cumulative-slo-calculation.md#proposed-amendment-bounded-raw-calculation-for-dirty-interiors):
combine clean aggregates, exact raw replacements for dirty portions and raw edges
within one snapshot and explicit resource limits. If every window can be completed,
evaluate all conditions and commit the complete policy decision normally. This
candidate is gated on the calculation experiment below; it is not current behavior.

If preparation is still pending because fallback is ineligible, lacks admission
or cannot complete within budget, keep the whole policy boundary due at its
original effective cutoff. Release the worker claim and resources; retain the
waiting reason and age. Do not advance its cursor, open/resolve from a partial policy
snapshot or process a later boundary first. Other policies remain independently
eligible for fair worker scheduling.

The existing bounded scheduler makes the still-due boundary eligible on its next
tick, subject to fair admission. Use the same scheduling mechanism for new and
pending work; no separate aggregate-dependency table or publication-triggered retry
path is required. A pending boundary gets at most one attempt per scheduler tick;
it cannot hot-loop or monopolize claims while other policies wait. The scheduler
tick/retry bound must be explicit in the implementation plan and count toward total
latency; it is distinct from the one-minute measurement evaluation cadence.
Persisted due work survives restart without a publication signal. Re-read every
condition against one consistent snapshot, including new source/exclusion
generations, and use the usual commit
fencing. Freshness/coverage are judged at the original evaluation instant, not
the later retry wall clock; present-day source freshness remains separately visible.
Only the shared calculator's explicit bounded option may substitute raw portions.
No unbounded full-window scan or alert-specific calculation bypasses its limits.

This applies at hourly rollover and when a late observation dirties an interior
hour. A breach at T followed by recovery at T+1 minute must still be evaluated in
that order if aggregate publication happens five minutes later. Those reconstructed
decisions use the retained-facts provenance rules below. A ready fast condition
waits only if sustained burn cannot be calculated through either clean aggregates
or the bounded fallback; this is processing lag, not the full-window warmup/unknown
recovery rule. Do not introduce independent condition commits to hide the delay.

Pending alone does not exhaust evaluation retries, become a no-data condition or
silently expire at a preparation target. Explicit canonical aggregate failure
after its retry budget is exhausted yields unavailable with `calculation_failed`;
record the failure separately from missing observations. Until then, retain due
work unless a documented administrative cancellation or the 24-hour catch-up
horizon ends it with an explicit range outcome. Surface waiting age/reason,
oldest due boundary and preparation failure to UI/API/CLI diagnostics. Record
fallback usage, work, duration and budget/admission deferrals separately from source
quality. A successful fallback does not repair or conceal the aggregate worker's
backlog or failures.

Require full configured windows within the SLO's measurement validity and retained
data; do not shorten them to create an alert. Existing eligible measurements can
make a newly created policy immediately evaluable. Never open occurrences before
policy activation just because older measurements exist. A newly created SLO may
make fast burn evaluable after one hour while sustained burn still needs six.
Expose readiness per condition; this does not force current SLO status to Wait.

Every condition result is **matching**, **not matching** or **unavailable**.
Both windows need definitive, complete, fresh eligible coverage and positive
traffic before the condition can be matching or not matching. With usable inputs,
matching requires both burns `>= threshold`; either below yields not matching.
No-data, partial, stale, zero traffic, pre-validity, fully excluded and terminal
calculation unavailability retain distinct reasons. Pending preparation defers
evaluation as defined above. Historical reconstruction judges coverage
and freshness relative to its effective evaluation time, not the recovery wall clock.

| Policy's condition results | Evaluation outcome |
| --- | --- |
| At least one matching, including when another is unavailable | Breaching; one occurrence can open or stay open. |
| Every condition not matching | Clear; an open occurrence can resolve. |
| No matching condition and at least one unavailable | Indeterminate; preserve an open occurrence and show the reasons. |

Missing evidence cannot establish recovery. No implicit additional firing delay,
minimum failure count or recovery hysteresis is introduced. Exact examples: at
99.9% target and 10,000 total events, 144 bad events in each window meet 14.4×;
143 in either window does not. Fast-to-sustained handoff preserves one occurrence.

Full-window consequence, supported in review: if fast burn fires during a new SLO's first six hours, then
clears while sustained burn is still warming up, the policy is indeterminate and
the occurrence stays open. Treating never-ready conditions differently for recovery
would change the reviewed default; do not introduce it as an implementation shortcut.

### Lifecycle, acknowledgement and changes

Normal describes a policy with no open occurrence, not a synthetic alert record.
Policy readiness/processor health is separate; Normal must not imply healthy data.
Resolved, Superseded and Disabled are terminal occurrence states retained in history.

| Current state | Cause | Result |
| --- | --- | --- |
| No open occurrence | Breaching evaluation | Open Firing. |
| Firing | Editor/Administrator acknowledges this occurrence | Acknowledged; record first actor and time. |
| Firing or Acknowledged | Breach continues or another condition matches | Preserve occurrence/state; update per-condition evidence. |
| Firing or Acknowledged | Indeterminate evaluation | Preserve state; expose unavailable evidence. |
| Firing or Acknowledged | Clear evaluation | Resolved, with exact recovery evidence. |
| Firing or Acknowledged | Policy disabled | Disabled, with actor/reason; not service recovery. |
| Firing or Acknowledged | Evaluation definition changes | Superseded; new revision evaluates independently. |
| Firing or Acknowledged | Confirmed selected-SLO archive takes effect | Disabled with cause `slo_archived`; binding becomes inactive. |
| Terminal occurrence | New breach under an enabled policy | Create a new occurrence, never reopen the old one. |

Acknowledgement has no timeout in the initial release. Retrying acknowledgement
of the same already-acknowledged occurrence is idempotent and preserves its first
actor. If recovery wins the transaction race, acknowledgement returns a conflict
with current state; it cannot acknowledge the next occurrence. If acknowledgement
wins, later valid recovery resolves normally. Request IDs support uncertain-response
reconciliation; the UI does not blindly replay a mutation after transport failure.

Window/threshold/condition changes, SLO rebinding and semantic SLI/SLO changes
supersede open occurrences and fence old evaluations. Re-enabling uses a new
evaluation revision and starts from its activation boundary. Name/description and
severity-only edits preserve occurrence identity, with audited metadata changes
and original transition snapshots retained. Configurations require expected versions.
Ordinary deletion of referenced resources fails with dependent IDs, as in SPEC-0005.

Disabling or superseding a revision also records its pending evaluation range as
cancelled by that administrative change, atomically with the lifecycle action.
Those boundaries are not silently dropped or labelled clear. An old worker cannot
resume them as current alerts after the new revision activates. This deliberate
cancellation exception to catch-up must be shown in edit/disable confirmation and
retained in history. The maintainer supported this explicit cancellation record
in review; it must not be replaced by an unrecorded cursor jump.

There is no standalone manual-close operation in this release. To stop an open
alert administratively, an authorized user disables the policy; definition changes
supersede it. Confirmed SLO archive is the other named scheduling-stop cause below.
Each records the actor, time, resource revision and cause. Acknowledge does not
close an occurrence, and administrative actions never label it Recovered.

#### SLO archive and policy binding

Do not bypass SPEC-0005's dependent-resource guard. An ordinary SLO delete/archive
request with referencing alert policies fails and returns those policy IDs. The
proposed confirmed-archive operation must explicitly authorize deactivating the
affected policies, identify the expected SLO and policy versions, and show any
open occurrences and pending ranges it will close/cancel. Reject a stale impact
confirmation if a policy is added/rebound or a referenced version changes.
Require both SLO-archive and policy-management authorization. Revalidate the
dependent set and expected versions at effective activation; a changed impact
requires fresh confirmation before archival can take effect. Do not admit new
bindings to an SLO with a confirmed archive pending activation.
Other resource dependencies continue to block archival under their own contracts;
this exception covers alert-policy references only and requires spec acceptance.

At the archive's effective boundary, atomically stop new evaluation scheduling,
mark every affected binding inactive with reason `slo_archived`, fence existing
claims, close open occurrences as Disabled with that cause, and record cancelled
pending ranges. Audit the initiating actor and preserve policy configuration,
original enabled preference, SLO identity, alert evidence and historical links.
The archive is logical; it deletes neither the SLO's history nor alert references.
An already-terminal occurrence retains its state. Reject new policy bindings to
an archived SLO. The effective archive boundary cannot pass while its alert
scheduling/closure changes remain unapplied.

Show Archived SLO as the policy's inactive reason, distinct from a user-disabled
policy. Rebinding explicitly to a supported live SLO creates a fresh evaluation
revision; it does not reopen the Disabled occurrence. No unarchive/automatic
reactivation operation is introduced by this spec.

Maintenance does not equal silence. Fully excluded windows are unavailable,
preserving open alerts. Historical exclusion edits fence older work and affect
subsequent decisions without modifying past evidence. A resulting resolution
records `resolved_after_maintenance_change`, not ordinary service recovery.

### Restart, late data and retention

Persist a continuous evaluation cursor and process every due boundary in order.
Do not collapse breach/recovery transitions into the latest state. Repeated
processing commits at most one logical outcome/transition per evaluation identity.
Source/configuration changes invalidate uncommitted results and cause retry.

Proposed automatic catch-up horizon: **24 hours**. Committed decisions retain
their original evidence. Uncommitted boundaries inside that horizon are
reconstructed from currently retained canonical facts and labelled reconstructed,
with effective and detection times and source/exclusion revisions. They cannot
claim exactly what was knowable before late data arrived. Every boundary has an
evaluation outcome or an explicit unavailable outcome; errors cannot vanish.

A breach and recovery discovered entirely in catch-up appear in history as
Detected after recovery, never as a current firing alert once catch-up completes.
An occurrence still breaching after catch-up appears active, with its original
effective start and later detection time. While behind, show last evaluated state
and lag rather than presenting historical decisions as current. Future delivery
must gate notifications on completed current-state reconciliation: no historical
page or backlog of old transitions.

If downtime exceeds 24 hours, record the unreconstructed interval as a visible
evaluation gap before resuming within the horizon. Unavailable history is not
proof that no breach occurred. Preserve any open occurrence until a later clear
evaluation, policy disablement, definition supersession or confirmed SLO archival
as defined in the lifecycle table. There is no unspecified manual-close path.
No retrospective page is generated.
The same applies to absent definitions, expired observations or unrecoverable inputs.

Late/backfilled observations can affect pending and subsequent evaluations. They
do not rewrite committed outcomes or retroactively create alerts for previously
completed boundaries. Full retrospective re-analysis is outside this release.
This is a bounded detection contract, not a claim never to miss an unobservable
breach. Creation/acknowledgement/transition durability remains within PostgreSQL
and backup guarantees; missing backup history is not recoverable by the engine.

Retain transition evidence, terminal alerts, evaluation-gap summaries and audit
for 365 days under SPEC-0007. Preserve active occurrences regardless of age.
After the catch-up horizon, unchanged evaluation outcomes can be compacted into
contiguous ranges with their quality/revision identity. Transition evidence
includes every condition's exact inputs/result, not just the matching condition.

## Failure and recovery

Viewers read; Editors/Administrators create/edit/disable policies and acknowledge
alerts. Administrative-only operations retain existing identity/retention rules.
Audit changes atomically; never expose credentials or raw datasource queries in
alert payloads. Future destination administration remains administrator-only.

Worker claims have bounded leases, concurrency and retry/backoff. Lease expiry
permits retry, not a second transition. Configuration/measurement generation
fencing and occurrence state versions prevent stale commits. A transient engine
failure is processor health, not evidence about the SLO. Pending aggregate
preparation remains due and follows the deferral contract above. Exhausted work remains
visible with an explicit unavailable outcome and audited retry; it is never
silently discarded or interpreted as non-matching. A late retry cannot rewrite
already committed lifecycle history.

Report due/committed boundaries, oldest backlog age, unavailable ranges, failures,
claim expiry and replay progress separately from observation freshness. An
external monitor is needed to detect an unavailable Almanac installation itself.
UI navigation and ingestion retain their own admission budgets during catch-up.

### Initial verification workload

The maintainer selected 100 and 200 configured policies as the starting scale.
These are fixture sizes, not a configured maximum or proven capacity.

| Primary fixture | SLOs / SLIs | Policies | Conditions | Maximum simultaneous open alerts |
| --- | --- | --- | --- | --- |
| Initial | 100 / 100 | 100 | 200 | 100 |
| Next | 200 / 200 | 200 | 400 | 200 |

Each policy monitors its own 30-day, 99.9% SLO with the two default conditions;
SLIs have 60-second cadence, positive event counts and complete six-hour warmup
data. Run 60 minutes of live evaluation, including all-policy breach/recovery,
condition handoff, acknowledgements and an isolated missing-data cohort.
Use a separate small correctness fixture with several policies sharing one SLO;
it proves fan-out and independent lifecycles, not independent-SLO capacity.

Proposed ready-to-commit latency targets for review: p99 at most 60 seconds, maximum 120 seconds
from a due evaluation's required evidence being ready to committed result during
healthy operation. Record collection/settling delay and total detection delay
separately; the ready-to-commit measure cannot hide source or calculation backlog.
**No numeric end-to-end detection-delay target is set in this draft.** The 60/120
second targets are not such a guarantee. Record total boundary-to-commit delay
from the original scheduled due time, aggregate-wait duration, source settling/
arrival delay and ready-to-commit time separately. Never reset the original due
time on publication, generation changes or retry. Report distributions plus the
oldest still-pending boundary and its age; pending evaluations cannot disappear
from the report because they have not committed.

SPEC-0020's five-minute closed-hour target applies to retained chart extrema,
not source aggregate preparation or alert detection. Source aggregates rebuild as
observations arrive, including before an hour closes; a five-minute pause on every
rollover is not inherent. Set a proposed end-to-end alert target using the calculation
experiment before accepting the complete contract and starting engine implementation.
Validate it across rollover, late-data rebuilding and fallback with the real engine
at both fixtures before shipping. Until then, no measured bound on total detection
delay is claimed; calculation-query latency alone is not engine detection evidence.
Verification must cross an hourly rollover and dirty an interior hour through late
data, proving both successful bounded fallback and waiting when its budget is
exceeded, scheduler-driven same-boundary retry and ordered breach/recovery after
publication without a publication wakeup. Include condition handoff, a stopped
aggregate worker with ingestion continuing, evaluator restart and fairness between
persistently pending and ready policies; preserve global acknowledgement throughout.
After a 30-minute evaluator outage with ingestion continuing, recover the 200-policy
backlog within five minutes while incoming work continues. Verify chronological
outcomes and zero unexplained missing boundaries, not only processing throughput.
Exercise the 24-hour horizon and beyond-horizon gap deterministically as well.

Record CPU, memory, database/storage configuration, concurrency, fixture revision,
observation volume and warm/cold conditions for every result. Lock that runner
profile before measurements; no unreported hardware changes between fixtures.
The UI contract retains the <1,000 ms journey requirement and preferred <=500 ms
target. The broader existing SLO scale obligations are unchanged; this proposal
does not claim a 1,000-policy alert benchmark.

### Calculation experiment before engine implementation

Keep the pre-adoption gate at the shared calculator boundary; it requires no
policy engine, durable alert scheduler or occurrence prototype:

- Run deterministic per-SLI query-equivalence tests against an independent direct
  canonical-observation reference at the same snapshot. Cover clean/mixed/dirty
  interiors, cadence/hour edges, exact threshold inputs, missing/stale/zero/expired
  data, null totals and invalid-row rejection, exclusions and revision changes,
  and concurrent publication/ingestion. Unsupported exclusion behavior must be
  identified explicitly rather than claimed equivalent; its later alert acceptance
  remains required. Test query budget/admission deferral without partial totals.
- Run one contention measurement with 200 SLIs using the default alert-window
  query mix across an hourly rollover while multi-day backfill floods the dirty
  queue and live ingestion continues. Pin the backfill span/rate, runner, cadence,
  query load and dirty-hour/raw-row/time/concurrency limits. Compare the existing
  wait path and fallback on equivalent fixtures. Record rows examined, query
  duration (including admission/wait), fallback frequency, budget deferrals and
  interference with ingestion and authenticated SLO navigation under their existing
  budgets. Include incomplete requests and their ages; completed-query percentiles
  must not hide waiting work.

Adoption requires exact equivalence and enforced resource bounds without violating
existing workloads' budgets. Record failures and select calculation limits with
the maintainer before accepting ADR-0051 and the SPEC-0020 amendment. The query
evidence informs a proposed end-to-end target; it cannot verify durable alert-commit
latency. Ordered breach/recovery, condition handoff, publication/stopped-worker
recovery and acknowledgement are later engine acceptance under AC-02/05/06/08/13,
at the unchanged 100/200-policy fixtures. If calculation evidence is insufficient,
retain the accepted path and revisit alternatives; do not silently change ordering,
relax bounds or mark the candidate implemented.

## Acceptance criteria

- AC-01: Exact threshold equality and paired-window AND produce independent expected results without display rounding.
- AC-02: OR conditions yield one occurrence; handoff preserves its identity and acknowledgement; clear requires every condition not matching.
- AC-03: Full-window readiness, existing history on activation and missing/stale/zero/excluded reasons follow the contract without false recovery.
- AC-04: Timers advance due work without ingestion; duplicate wakeups cannot skip eligible boundaries or duplicate results.
- AC-05: Real PostgreSQL crash, retry, lease expiry and generation/version races preserve atomic cursor/state/evidence and reject stale commits.
- AC-06: Global acknowledgement permissions, idempotency and acknowledgement/recovery races preserve the intended occurrence and audit.
- AC-07: Semantic edits, disable/re-enable, metadata edits and maintenance changes have distinct lifecycle causes; cancelled pending evaluation ranges remain explicit.
- AC-08: Ordered catch-up preserves committed transitions, labels reconstruction and already-recovered alerts, and records beyond-horizon gaps without historical paging.
- AC-09: Late data, expired evidence, unavailable outcomes and retention compaction preserve immutable decisions and active history protection.
- AC-10: UI/API/CLI expose equivalent policy, condition evidence, acknowledgement, history and processor/quality outcomes with authorization and conflict handling.
- AC-11: Both declared policy/SLO fixtures meet reviewed lag/recovery targets with exact expected transitions; shared-SLO policies remain independent.
- AC-12: Collection and authenticated alert/SLO navigation retain their budgets during live evaluation and catch-up, with traces identifying costs.
- AC-13: At the 100/200-policy engine fixtures, rollover/late-data dirty interiors either produce a complete exact evaluation or retain the original due boundary without a lifecycle/cursor commit. Budget deferral releases claims; the existing scheduler retries fairly without publication wakeups or hot loops. Ordered breach/recovery, handoff and acknowledgement survive aggregate-worker stoppage/publication and evaluator restart. Total delay includes preparation, scheduler wait and fallback; terminal failure and horizon expiry have explicit outcomes.
- AC-14: Ordinary referenced-SLO archival rejects with dependent IDs; confirmed archival atomically inactivates bindings, fences claims, records cancelled ranges and disables open occurrences with `slo_archived`. Version/creation races, already-terminal occurrences and archived-SLO rebinding rejection preserve history and authorization.
- AC-15: Before candidate adoption, deterministic per-SLI query tests prove exact equivalence against independent raw-observation results, including concurrent updates, quality states and budget deferral without partial/double-counted totals. A 200-SLI rollover/backfill contention measurement proves enforced resource bounds and preservation of ingestion/SLO-navigation budgets; evidence records limits and incomplete requests without requiring an engine prototype or claiming alert-commit latency.

## Dependencies and open questions

The maintainer's review supports full-window readiness with indeterminate retaining
open occurrences, the 24-hour catch-up horizon and explicit cancelled-range history.
It also supports deferring standalone data-quality alerts, with the loss of the
automatic no-data/stale detector made explicit in Scope. These record review
direction, not acceptance of the complete draft or authorization to implement.
The selected mocks, global acknowledgement and 100/200 starting scale are confirmed.

Review the remaining lifecycle details, confirmed-archive dependency exception,
bounded fallback and remaining pending-aggregate deferral, one-minute cadence and
numerical ready-to-commit/recovery targets before accepting the complete contract.
The maintainer approved investigating bounded fallback as the preferred candidate;
adoption requires the calculation experiment and acceptance of the SPEC-0020
amendment. End-to-end detection latency has
no target yet and must not be conflated with ready-to-commit latency. Review the
retained-evidence cost. ADR-0010's delivery/security decisions remain accepted;
later adapter contracts may refine them explicitly, not silently replace them.

Resolve API schemas, exact worker admission/lease defaults and the pinned runner
profile in an implementation plan after behavior/architecture acceptance. The
selected mocks still need multi-condition occurrence evidence and non-30-day
preset confirmation before UI implementation acceptance, as SPEC-0022 records.

## Verification evidence

The maintainer explicitly approved the unwired foundation sequence on 2026-11-14.
[F4-T29](../tasks/F4-T29.md) through [F4-T34](../tasks/F4-T34.md) implement exact
decisions, durable journaling/processing, canonical empty-mask evidence,
authenticated commands and catalogue/history reads. Their focused PostgreSQL/race
evidence does not establish production activation, nonzero maintenance integration
or capacity acceptance. The [public contract proposal](../design/alerting-public-contract.md)
closes remaining defaults/activation questions for review. The full draft,
fallback adoption and release gates remain pending.

## Change notes

- 2026-11-12: Initial draft, with 100/200-policy verification scope selected by the maintainer. Proposed replacement contracts remain pending review.
- 2026-11-12: Review corrections distinguish pending aggregates from completed unavailable results, retain delivery ADR scope, define the absence of manual close and specify confirmed SLO archival. Supported review defaults are recorded without marking the full draft accepted.
- 2026-11-13: Propose bounded shared-calculator fallback gated on a 100/200-policy experiment; preserve complete ordered evaluations and clarify the separate chart-history target. No candidate adoption or engine performance is claimed.
- 2026-11-13: Narrow pre-adoption evidence to query equivalence and 200-SLI contention; retain 100/200-policy lifecycle cases for engine acceptance. Retry pending work through the existing bounded scheduler without a separate publication-wakeup mechanism.
