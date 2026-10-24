# SPEC-0011: Alerting and notifications

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: not started
- Related ADRs: [ADR-0010](../adr/0010-alert-evaluation-and-delivery.md) (accepted)
- Plan: none
- Supersedes / superseded by: none

## Accepted amendment

[SPEC-0016](0016-maintenance-exclusions.md), approved in Crit review
`0de0c0e42db6`, governs maintenance-adjusted evaluation and revision fencing.
Delivery-only silences remain a separate operation.

## Purpose

Notify operators about sustained error-budget consumption without confusing a
collection outage, missing observations, or notification failure with recovery.
Expose the same policies, state, history, and delivery controls to UI and CLI.

## Scope and non-goals

Propose paired-window burn alerts, data-quality warnings, generic HTTPS webhooks,
and scheduled notification silences. Email, chat-specific adapters, paging
escalations, on-call schedules, and incident acknowledgement are deferred.
Alert evaluation uses stored observations through the backend calculation API;
it never queries an external metric source or asks an agent to scrape on demand.

## Behavior and contracts

### Policies and evaluation

A versioned policy selects explicit SLO IDs, enabled state, severity, destination
IDs, evaluation interval, and one or more rules. Each rule has a stable ID, short
and long durations, and a positive burn threshold. Both windows must meet the
threshold (`>=`) to fire; rules are evaluated independently. Validate short <=
long, windows supported by retained data, and window bounds aligned to calculation
buckets. Initial support is single-objective, non-composite Occurrences SLOs with
`0 <= target < 1`. Other valid SLO methods, multi-objective SLOs and composites
remain visible but alert bindings are capability-inactive with explicit reasons.
Irregular-arrival inputs have no definitive expected coverage or common cadence;
their bindings are capability-inactive with `unsupported_irregular_alert_input`.
A 100% target fails validation rather than dividing by zero.

The evaluator requests a consistent snapshot with definition revisions, coverage,
and evaluation instant `T`. Windows are half-open `[T-duration,T)` in UTC;
`burn = (bad/total)/(1-target)` uses the canonical calculation precision before
comparison. The private evaluation response includes exact good/total count
decimals and target, effective SLI/SLO definition identities, and snapshot identity
for each window. Workers compare the exact rational burn against the threshold by
cross multiplication; rounded display burn is never a decision input. The backend
supplies the same underlying calculations shown on SLO detail.
Propose a one-minute scheduling tick and a configurable ingestion allowance
(initial proposal: two minutes). Derive effective `T` by rounding the requested
instant after that allowance down to the common interval boundary defined in
[SPEC-0006](0006-slo-calculation-and-reporting.md). Record both requested and
effective times; repeated effective times are idempotent. Windows must align with
the participating cadence; no interpolation supplies missing data.

A pair is evaluable only when both canonical calculation results are definitive,
fresh, and have positive total events. Coverage alone is insufficient: known-empty
intervals, semantic revision boundaries and other unavailable calculation states
must retain their canonical reasons. Incomplete coverage is `no-data`; overdue coverage
is additionally `stale` under the SLI freshness contract; a complete zero-event
window is `zero-traffic`. These are distinct from query execution failure and
from an evaluator/API error. Record these reasons alongside the last known burn
condition; none automatically resolves a firing incident. An independently
configurable data-quality rule fires after an explicit continuous duration in
no-data/stale state, and resolves on complete fresh coverage, including genuine
zero traffic. Default proposal: five minutes; evaluator errors instead affect
processor health. The warning reason preserves collection failure evidence.

Paired windows improve reset behavior while requiring both recent and sustained
burn. Thresholds remain explicit operator choices; the UI may explain budget
consumption as `threshold * long_window / SLO_period` for compatible fixed periods.
Presets must identify their period and assumptions before adoption. See the
[Google SRE Workbook](https://sre.google/workbook/alerting-on-slos/) for the
paired-window approach and its low-traffic tradeoffs.

Maintenance-adjusted evaluation follows SPEC-0016: use the same exclusion snapshot
as reports and reject stale worker results after exclusion edits. Fully excluded
windows do not fire no-data alerts or imply recovery; show excluded evaluation,
reset the continuous no-data timer and retain incident history. After maintenance,
missing eligible intervals start a new timer. Partly excluded windows require complete,
fresh eligible observations. An exclusion-driven resolution records
`resolved_after_maintenance_change`; it is not evidence of service recovery.
Historical changes do not retract notifications or replay historical pages.

### Lifecycle and history

An incident identity is `(policy revision, SLO ID, rule ID, occurrence ID)`.
A first qualifying evaluation opens `firing`; a complete evaluable pair below
threshold on either window resolves it. A subsequent breach starts a new
occurrence. Record evaluation times, actual window values, coverage, source and
policy revisions, transition reasons, and notification event IDs. Policy changes
or disabling close old occurrences as `superseded` or `disabled`, never recovered;
new revisions start fresh evaluation and retain old history. At an effective SLI
or SLO semantic revision boundary, supersede affected open occurrences and reset
data-quality timers. Do not carry an incident across a changed query, target or
budget method. Revalidate bindings against the new definitions; unsupported bindings
become capability-inactive with reasons. Supported bindings start fresh evaluation; burn remains unavailable until canonical
windows under the new revisions are evaluable, with the reason shown separately
from no-data/stale timers.

A single transition event is created atomically with incident state. Evaluation
retries at the same snapshot are idempotent and older results cannot overwrite
newer state. After downtime evaluate the current closed interval, recording the
missed evaluation range; do not replay historical pages. Backfilled measurements
can influence the next evaluation but never rewrite previously sent alerts.

### Suppression and delivery

Notification silences specify explicit SLO IDs, reason, author, and UTC start/end,
with `[start,end)` semantics. The UI accepts a timezone and previews the resulting
UTC range; ambiguous or nonexistent local times require correction. Initial scope
is one-time scheduling; recurring schedules are deferred. Maintenance exclusions
under SPEC-0016 affect evaluation; silences do not create or edit those exclusions.
Suppression changes
notification eligibility only: evaluations, incident history, collection, and
error-budget calculations continue. Overlapping windows combine by union.

The backend checks suppression both when creating work and when claiming it.
Suppressed events remain visible but are not replayed as a backlog. At suppression
end, a current firing occurrence gets one current-state notification per
destination; an occurrence already resolved gets none. Unsuppressed recovery is
sent only to destinations with a firing delivery attempt; delivery uncertainty
must not silently erase that recovery. Reminders are deferred.

Webhook envelopes are versioned JSON with stable event/delivery IDs, transition,
SLO and policy identity, severity, timestamps, window values, and a UI link.
Secrets and raw provider queries are excluded. Propose HMAC-SHA256 signatures over
exact request bytes with a timestamp and configurable receiver replay tolerance;
receiver guidance must explain signature verification and event-ID deduplication.
A manual destination test is visibly marked synthetic and cannot change incidents.

## Failure and recovery

Backend transactions own state and the outbox. Separate logical evaluation and
delivery workers use the same scoped private application operations through an
in-process adapter when embedded and authenticated Worker HTTP APIs remotely,
with leased, fenced claims. A crashed
worker's claim expires; duplicate webhook delivery is possible after a successful
send with a lost acknowledgement. The guarantee is at least once within the retry
budget, not exactly once. Receivers deduplicate event IDs. Serialize deliveries
per incident/destination; include a transition sequence for receiver ordering.

Propose a 10-second request timeout and eight attempts within 24 hours, exponential
backoff with jitter capped at one hour; bounded Retry-After applies to 429/503.
Retry connection/timeouts, 408, 429 and 5xx; other non-2xx responses fail permanently.
Do not follow redirects. Store sanitized status and bounded error details. Expose
pending, leased, retrying, delivered, exhausted and suppressed states. Manual retry
creates an audited attempt cycle retaining the original event ID; it does not
reopen resolved incidents or bypass current suppression.

Viewers read redacted policies, incidents, silences and delivery history. Editors
manage policies and notification silences. Administrators manage destination
settings/secrets, run destination tests and retry deliveries. Source query tests
using an existing datasource follow the identity/diagnostic contract; no alert
permission grants direct source access. Encrypt signing keys; never return existing keys. Permit HTTPS only,
verify TLS, bound payload/response size, and enforce configured egress policy at
DNS resolution and connect time, including IP literals and redirect prohibition.
Block metadata and loopback addresses; private networks require an explicit
administrator allowlist. Recheck policy on every attempt. Audit destination,
policy, suppression and retry changes. History retention follows the storage
contract; purging cannot remove pending deliveries or active incidents.

## Acceptance criteria

- AC-01: Known counts reproduce paired-window thresholds and exact boundary behavior.
- AC-02: Missing, stale, zero-traffic, source failure and evaluator failure remain distinct;
  only complete evaluable data can resolve a burn incident.
- AC-03: Concurrent/retried evaluations yield one transition and older results are rejected.
- AC-04: Restart and backfill preserve history without replaying historical pages.
- AC-05: Scheduled/overlapping suppression preserves calculations and sends only the
  applicable current firing state when suppression ends.
- AC-06: Crash before/after send, claim expiry and reordered results demonstrate bounded
  retry and stable deduplication identities without claiming exactly-once delivery.
- AC-07: UI and CLI expose matching policies, incidents, tests, retries and delivery failures.
- AC-08: Unauthorized changes, secret disclosure, redirects and prohibited network targets
  are rejected; authorized changes and manual retries have audit records.

## Dependencies and open questions

Canonical calculation, coverage, retention, identity, API and runtime contracts
must be accepted before implementation. Private API schemas must specify snapshot
identity, fencing, payload limits and acknowledgement operations.

<!-- REVIEW-U01: Accept complete-coverage gating and separate five-minute data-quality warnings? Partial-window burn detection is more responsive but changes confidence semantics. -->
<!-- REVIEW-U07: Initial alerts support only single-objective, non-composite Occurrences. Other valid SLOs remain visible with capability-inactive alert bindings; expand only with method-specific fixtures. -->
<!-- REVIEW-U02: Accept generic webhook first, retry defaults and delivery-only silences? Dedicated paging integrations and budget exclusions remain separate decisions. -->

## Verification evidence

Not implemented or verified. Criteria require deterministic clock-driven tests,
transaction/concurrency failure injection, hostile webhook endpoint tests, and
UI/CLI parity checks against the implemented API.

## Change notes

- 2026-10-24: Initial draft for review; defaults and channel scope are proposals.
