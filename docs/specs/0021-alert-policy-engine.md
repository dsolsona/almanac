# SPEC-0021: Alert policy engine

- Status: draft
- Created: 2026-11-12
- Accepted: pending
- Approval: pending; drafts and initial 100/200-policy verification scope authorized
- Implementation: not started
- Related ADRs: [ADR-0051](../adr/0051-durable-alert-policy-state-machine.md) (proposed)
- Plan: none; design review tracked in [F4-T01](../tasks/F4-T01.md)
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
No-data, partial, stale, zero traffic, pre-validity, fully excluded and calculation
unavailability retain distinct reasons. Historical reconstruction judges coverage
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

Review consequence: if fast burn fires during a new SLO's first six hours, then
clears while sustained burn is still warming up, the policy is indeterminate and
the occurrence stays open. Treating never-ready conditions differently for recovery
is an alternative requiring an explicit decision, not an implementation shortcut.

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
Referenced resources cannot be deleted through ordinary management operations.

Disabling or superseding a revision also records its pending evaluation range as
cancelled by that administrative change, atomically with the lifecycle action.
Those boundaries are not silently dropped or labelled clear. An old worker cannot
resume them as current alerts after the new revision activates. This deliberate
cancellation exception to catch-up must be shown in edit/disable confirmation and
retained in history; it is another review choice before implementation.

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
evaluation or explicit administrative closure. No retrospective page is generated.
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
failure is processor health, not evidence about the SLO. Exhausted work remains
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

Proposed latency targets for review: p99 at most 60 seconds, maximum 120 seconds
from a due evaluation's required evidence being ready to committed result during
healthy operation. Record collection/settling delay and total detection delay
separately; the ready-to-commit measure cannot hide source or calculation backlog.
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

## Dependencies and open questions

The following recommendations are draft decisions, not approvals inferred from the
request to prepare documents: full-window cold start; one occurrence per policy;
all-condition recovery; administrative terminal states; one-minute cadence;
24-hour automatic reconstruction; and deferring separate data-quality conditions.
The selected mock direction, global acknowledgement and 100/200 starting scale
are confirmed direction. Review the latency/recovery numbers and retained-evidence
cost before acceptance. Provider delivery contracts and retries can be decided
with their later specs; no transport is silently approved here.

Resolve API schemas, exact worker admission/lease defaults and the pinned runner
profile in an implementation plan after behavior/architecture acceptance. The
selected mocks still need multi-condition occurrence evidence and non-30-day
preset confirmation before UI implementation acceptance, as SPEC-0022 records.

## Verification evidence

No alert engine is implemented or benchmarked. Documentation checks and mock
evidence are recorded in F4-T01; they do not satisfy the engine acceptance criteria.

## Change notes

- 2026-11-12: Initial draft, with 100/200-policy verification scope selected by the maintainer. Proposed replacement contracts remain pending review.
