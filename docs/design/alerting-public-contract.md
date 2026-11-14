# Alerting public contract proposal

Status: proposed for maintainer review, 2026-11-14. The approved unwired foundation
sequence does not accept these public defaults or enable production alerting.
This closes the concrete API/activation questions in
[plan 0037](../plans/0037-alert-engine-persistence-and-api.md); it does not waive
the calculator, maintenance or 100/200-policy acceptance gates.

## Configuration takes effect atomically

Use the management transaction's authoritative server time after resource locks
as the effective configuration time. Create/enable/semantic edits start a fresh
evaluation revision at the first whole-minute boundary at or after that time.
Retained measurements can satisfy full windows immediately; never create an
occurrence before activation or replay a disabled period as new occurrences.

Disable and semantic supersession take effect in that same transaction: fence
claims, record every remaining due old boundary through effective time, close an
open occurrence with the administrative cause, then store the new intent/revision.
Metadata/severity edits preserve the occurrence and evaluation revision. Enabling
creates a fresh revision; a terminal occurrence never reopens. There is no future
configuration activation queue or user-selected activation time in this release.
Acknowledgement remains immediate, global and without expiry.

Confirmed SLO archive also applies atomically at confirmation, after revalidating
the complete dependency/configuration/open-occurrence identity under locks. Preview
shows the exact maximum cancellation envelope. Current progress may shrink it;
confirmation rejects expansion beyond that reviewed envelope, changed meanings,
new bindings or new occurrence IDs and asks for a refreshed preview. Acknowledging
an already listed occurrence does not invalidate confirmation. Already-resolved
occurrences reduce impact. Preserve enabled preferences and original evidence,
set binding reason `slo_archived`, and expose a historical SLO link.

This is the immediate-activation alternative to the previously open scheduled
archive clock. It requires approval before implementing those public mutations.

## API input profile

| Input | Proposed initial contract |
| --- | --- |
| Policy name | Required human-readable name, at most 256 Unicode characters and 1,024 UTF-8 bytes; no control characters; exact-name uniqueness as in the foundation schema. |
| Conditions | 1–8 ordered conditions per policy, with stable unique IDs of 1–64 printable ASCII characters. There is no limit of two and no separate occurrence per condition. |
| Threshold | Positive exact decimal string, at most 32 ASCII characters; no exponent, sign, NaN, infinity or fraction syntax. Reject unreachable thresholds using the exact SLO target. Aggregate evidence may use up to 96 decimal characters. |
| Windows | Integer seconds, whole minutes, positive, short <= long <= 30 days, and divisible by source cadence. Invalid values receive field-specific errors. |
| Request body | At most 64 KiB for alert commands; reject oversize before decoding. |
| Severity | `low`, `medium`, `high`; default `medium`, informative in this release. |
| Presets | Fast: 14.4× over 5 min/1 h; sustained: 6× over 30 min/6 h. Offer both initially. These are the retained 30-day reference presets; require explicit confirmation when using their exact values with another SLO period. Custom numbers remain explicit user choices. An unreachable preset remains invalid and explains the required correction. |
| List | Default 20, maximum 200 API rows; UI selects 10 or 20. Literal case-insensitive search, stable global ordering and filter-bound cursors. Group counts describe the complete filtered set. Live pages do not promise a frozen snapshot across requests. |

No policy-count capability is inferred from the condition limit. Initial engine
acceptance remains 100 and 200 policies, including policies sharing SLOs.

## Commands and reconciliation

Require unrestricted current Editor/Administrator authority, CSRF and same-origin
protection for mutations, plus `Idempotency-Key`. Existing resources also require
strong `If-Match`: policy configuration version or occurrence state version.
Viewers may read. Recheck authority after waits and at transaction completion.

Store command mutation, audit and receipt atomically. A successful replay returns
its original result before current-version checks; it must still reauthorize the
actor. Reusing a key with another body conflicts. Replay payload lifetime is
24 hours; retain the command identity/digest and audit for at least 365 days.
An expired known key returns explicit expiry and cannot silently execute again.
After retention, an unknown lookup does not establish that the original request
failed. No perpetual idempotency guarantee is implied beyond retained identity.

Actor-scoped command lookup returns `committed`, `expired` or `unknown`. A client
with a lost response looks up the original actor/operation/key, then refreshes the
resource. It never automatically substitutes a new key for an uncertain command.
Acknowledgement replay preserves the original actor/time even after recovery.
Acknowledging a terminal occurrence conflicts; there is no Close operation.

## Worker profile for acceptance, not a capacity claim

Use the existing bounded backend scheduler, one-second ticks and at most one
attempt per policy per tick. Initial worker concurrency is one, sharing the
unchanged two-connection derived pool with existing calculation work. Admit at
most 200 policies in one turn; this is a turn bound, not a configured-policy cap.
Leases are 30 seconds. No source locks span window calculation.

Pending source preparation, resource/admission deferral and retryable database
contention preserve the original pinned cutoff, release claims and retry no sooner
than the next tick. They do not consume failure attempts. Persist a safe reason,
waiting age and last known state. An exhausted canonical window is completed
`calculation_failed` evidence; healthy other conditions may still fire.

For genuine engine failures, use five attempts with 2/4/8/16-second retry delays,
then durable visible exhaustion and audited operator retry. Cancellation and lost
leases remain recoverable work, not failed SLO evidence. These numerical defaults
are a proposed starting profile; acceptance must verify them without increasing
the existing pool sizes or weakening ingestion/navigation budgets.

Catch-up remains 24 hours with exact beyond-horizon gap records. Preserve original
effective and detected times. An occurrence resolved before it was first detected
is labelled Detected after recovery in history. No external destinations exist in
this release. Retain 365-day history and protect active/pinned evidence.

## Reads and public source contract

Use the operations/components already enumerated in plan 0037, generated from
`api/openapi.yaml`, with equivalent CLI commands. Separate configuration version,
evaluation revision and occurrence state version. Return original/latest immutable
evidence, complete condition outcomes, exact counts, actual SLO identity, lifecycle
transitions, explicit history ranges and processing status separately.

Response timestamps are UTC with explicit scheduled boundary, effective cutoff,
commit/detection time. Durations are integer seconds; exact counts/thresholds and
versions are decimal strings. Problem responses distinguish invalid field, missing
resource, denied/expired session, version conflict, command-body conflict, expired
command and processing unavailability. Unavailable and zero traffic never become
successful zero measurements.

The proposed ready-to-commit targets remain p99 <=60 seconds and maximum
120 seconds after required evidence is ready. Total boundary-to-commit delay has
no target yet: report it, aggregate wait and oldest pending age separately. This
proposal does not accept failed fallback/capacity results or promise a total
detection bound that has not been demonstrated.
