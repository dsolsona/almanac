# ADR-0021: Collect an initial settled interval and spread recurring queries

- Status: accepted
- Created: 2026-10-28
- Accepted: 2026-10-28
- Approval: The maintainer explicitly approved fetching and saving existing data immediately on Create, followed by queries spread across the selected frequency.
- Related specs: [SPEC-0002](../specs/0002-agent-behavior.md), [SPEC-0005](../specs/0005-openslo-and-resource-model.md), [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0015](../specs/0015-datasource-providers.md)
- Supersedes / superseded by: supersedes only the initial agent-collected SLI activation and separate historical-request requirement in [ADR-0005](0005-measurement-semantics.md); later revisions and general historical work are unchanged

## Context

A newly created SLI previously waited for future activation, a complete measurement
interval and the source settling delay before producing a result. Sources can
already contain a complete settled interval. Waiting provides no additional value
for that initial result. Aligning query dispatch to canonical interval ends also
concentrates source load when many SLIs share a frequency.

## Decision

Creating an agent-collected SLI authorizes one immediate initial measurement using
the most recent complete canonical interval whose source settling delay has elapsed.
It is a saved observation under the initial definition, not a disposable preview.
The initial definition's lower validity bound is the start of the latest settled
interval at creation. If the agent becomes ready later, select the latest settled
interval at that time, without replaying the intervening range. Ordinary execution
authority covers that definition from its fixed lower bound; no special historical
grant or general backfill operation is introduced.
Record its measurement validity separately from creation and operational activation;
the application must not imply that collection was running before creation.

The assigned agent executes after receiving the acknowledged configuration and
valid authority. Immediate means without an intentional cadence wait, not a promise
of zero latency or a result when the agent or source is unavailable. Query failures
and missing data retain their explicit states. Backend services never query the source.

Recurring dispatch uses a stable per-SLI offset across the frequency. Offsets change
when requests run, never the canonical measurement windows or provider evaluation
timestamps. Bounded concurrency, fair admission, lease fencing and existing query
timeouts still apply. Stable scheduling survives configuration reordering and
restarts; delayed work must not create an unbounded catch-up queue.
The initial algorithm takes SHA-256 of the canonical SLI ID string, interprets the
first eight digest bytes as an unsigned big-endian integer, and reduces it modulo
the cadence in microseconds. The result is a microsecond offset less than one
cadence. Treat an algorithm change as a schedule-version change, not a refactor.
The phase delays dispatch inside the existing live eligibility window; it does
not extend that window or the provider query timeout. Ready work uses rotating
admission under saturation so low offsets cannot monopolize the available slots.
Persisted attempt/coverage state distinguishes initial collection from a restart,
so restarts do not deliberately repeat completed intervals or the initial burst.

This exception is limited to initial creation. Later definition revisions and
ownership handoffs retain their prospective bounds. Existing observations remain
immutable and submission retries remain idempotent. Explicit setup backfill and
repair remain separate authorized operations using separate resources.

## Alternatives considered

- Keep future-only initial collection: simple, but unnecessarily delays useful
  feedback when the source already has data.
- Show an immediate unsaved preview: useful for query editing, but leaves a newly
  created SLI without the persisted measurement the operator expects.
- Move every measurement window to creation time: spreads requests, but changes
  canonical interval alignment and complicates aggregation across indicators.

## Consequences

The first saved interval can precede creation. UI and API must expose its actual
times and distinguish measurement coverage from operational history. Tests must
cover delayed configuration acknowledgement, restarts, duplicates, reassignment,
source errors and saturated dispatch. A thousand SLIs still require sufficient
agent and source capacity; spreading requests does not increase those capacities.

## Revisit conditions

Revisit dispatch distribution if measured workloads show harmful collisions or
unfairness. Broader historical coverage requires the separate backfill contract.
