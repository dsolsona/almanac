# ADR-0005: Preserve measurement meaning through deterministic evaluation

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related specs: [SPEC-0003](../specs/0003-ingestion-and-recovery.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: initial exclusion deferral superseded by [ADR-0014](0014-maintenance-exclusion-semantics.md); initial agent-collected sample authorization amended by [ADR-0021](0021-immediate-initial-collection.md); otherwise accepted

## Accepted amendment

[ADR-0014](0014-maintenance-exclusion-semantics.md), accepted in Crit review
`0de0c0e42db6`, supersedes only the initial maintenance-exclusion deferral below.
The remaining measurement decisions and original rationale stay in force.

[ADR-0021](0021-immediate-initial-collection.md), accepted on 2026-10-28,
additionally supersedes the separate historical-request requirement for the single
initial settled interval collected when creating an agent-collected SLI. Later
revisions and general historical work retain the decisions below.

## Context

Almanac must answer three questions: how reliable was the service, how much of its
error budget remains, and how quickly is it spending that budget? The answers depend
on what was measured and how the SLO counts success. Saving just a percentage is
not enough to answer them correctly.

For example, one minute has 99 successful requests out of 100; the next has 900 out
of 1,000. Across both minutes, 999 of 1,100 requests succeeded: about **90.82%**.
Averaging the two percentages gives **94.5%**, which answers a different question:
the average reliability of a minute, giving a quiet minute the same importance as
a busy one. We need to retain the counts so the chosen SLO determines the answer.

This count-weighted calculation is established practice: Google's SRE guidance
recommends good events divided by total events, and Datadog documents its metric-based
SLOs using summed event counts. That supports the Occurrences formula, not a claim
that all tools share every missing-data, composite or precision policy.
[Google SRE guidance](https://sre.google/workbook/implementing-slos/),
[Datadog metric-based SLOs](https://docs.datadoghq.com/service_level_objectives/metric/).
Datadog also documents time-slice SLOs separately, reinforcing that counting requests
and counting passing time intervals answer different questions.
[Datadog time-slice SLOs](https://docs.datadoghq.com/service_level_objectives/time_slice/).

Missing data also needs its own state. If an agent disconnects, we do not know whether
requests succeeded. Showing 100% would make an outage in monitoring look like an
improvement in reliability. Receiving an explicit zero-requests result is different:
the collection worked, but there was no traffic to judge.

Agents collect observations; the backend stores them and calculates SLO results.
This ADR decides what we retain and what those calculations mean. The formulas and
edge cases live in [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md).

## Decision

### Keep the measurements needed to explain the answer

Store successful and total counts when the source supplies counts. If the source
supplies only a ratio, keep it as a ratio; do not invent a request count. For a
threshold measurement, keep the value and unit: a latency of 201 ms fails a
“200 ms or less” condition, whereas 200 ms passes.

Accepted observations are immutable: retries cannot replace them with different
values. Record which query/definition produced each observation. A query change
applies from an agreed future boundary, so changing “under 500 ms” to “under 250 ms”
does not silently reinterpret earlier measurements. Initial setup may explicitly
request historical measurements under the first definition, with that retrospective
request recorded separately from when the SLO began operating.

### Give each budgeting method its stated meaning

| Method | Question it answers | Example using the two minutes above |
| --- | --- | --- |
| Occurrences | What fraction of requests succeeded? | 999 / 1,100 = about 90.82% |
| Timeslices | What fraction of time intervals passed their condition? | With a 95% success condition per minute, one of two minutes passes: 50% |
| RatioTimeslices | What is the average success ratio across equal time intervals? | (99% + 90%) / 2 = 94.5% |

The SLO selects the method; Almanac must not substitute one for another. Counts use
exact decimal arithmetic; scalar measurements use finite binary64 values. Intermediate
count/ratio calculations avoid rounding; output rounds only at the defined boundary.

An SLO target of 99% allows 100 failures in 10,000 requests. If 150 requests fail,
the remaining budget is **-50 requests** and consumption is **1.5 times the budget**.
Keep those values: clamping remaining budget to zero would hide how far it was exceeded.

### Show uncertainty and preserve report history

Use complete measurement intervals. If a source gives us a count for 12:00–12:01,
we cannot accurately split it at 12:00:30 without more detailed data. Reject or
explicitly align a requested range instead of assuming evenly distributed traffic.

An incomplete report window has an **unknown** full-window result. It may additionally
show a clearly labelled calculation for the data received. Show coverage and freshness
alongside performance, and distinguish no traffic from no observation. Alerts consume
those states explicitly; missing data is not evidence that a firing alert recovered.

Reports identify their time boundaries, definition versions and data snapshot. Late
data may change a newly generated report, but cannot change an already downloaded
report. Cached results must identify the calculation version and included data too.
Calendar boundaries follow the declared time zone; a local day need not be 24 hours.

### Combine several indicators with an explicit rule

Composite support is a later part of the initial-release proposal (F5), not a
prerequisite for the first working SLO flow (F3). First deliver one indicator and
its request-count objective, reports and budget. Then add and verify the composite
rules below as a separate increment. See [SPEC-0014](../specs/0014-delivery-and-verification.md).

A composite SLO combines several indicators, such as an API and a background worker.
Their measurements can come from different datasources and agents. They do not
necessarily count the same events, so adding all their requests into one total can
hide a poorly performing low-volume component behind a busy healthy component.

The proposed rules depend on the budgeting method:

- **Occurrences:** add each child's fraction of its own allowed budget, multiplied
  by its configured weight. If the API has used 50% of its budget and the worker 25%,
  both with weight 1, the composite has used 75%. With worker weight 2, it has used
  100%. This measures combined budget spending; it does **not** mean that 75% or
  100% of end-to-end requests failed. Do not display a combined request success rate.
- **Timeslices:** a minute passes only if every child passes in that minute. If A
  fails in minute 1 and B fails in minute 2, two minutes fail, even though each child
  failed only once. Initially require the same interval grid and outer target, with
  weights fixed at 1.
- **RatioTimeslices:** add the children's failure fractions within each interval,
  applying their weights, then average across intervals. If two children each have
  95% success and weight 1, their combined deficit is 5% + 5% = 10%. At a 99% target,
  that spends ten times the allowed 1% budget. This is a combined score, not a measured
  end-to-end request probability; it can fall below zero in severe failure. Initially
  require the same interval grid and target.

If a required child's data is missing, the composite is unknown; the available child
results remain visible. These are accepted interpretations for Almanac, not a claim
that every OpenSLO combination is supported. **REVIEW-M05** records the review of
these rules and their restrictions.

### Keep notification controls separate from measurement

Silencing notifications does not change reliability or restore error budget.
Excluding scheduled maintenance from SLO calculations would be a separate feature;
it is deferred and cannot activate initially. Calendar limits, missing-data policy,
precision and retention are detailed under REVIEW-M04, REVIEW-M06 and REVIEW-M07
in SPEC-0006.

## Alternatives considered

- Save only percentages: less storage, but loses traffic weighting and prevents
  reconstructing request-count budgets.
- Count missing intervals as success or failure: simpler output, but makes an
  unsupported claim about service behavior.
- Split interval counts proportionally: allows arbitrary report boundaries, but
  invents when requests happened within the interval.
- Average child results or use only the worst child: possible alternative composite
  policies, but they answer different questions from combined budget spending.
- Replace old observations when a query changes: convenient, but makes historical
  reports depend on later edits. A future correction workflow would need its own
  explicit audit and calculation contract.

## Consequences

Operators can trace a displayed result back to measurements and a stated formula.
The costs are more storage than percentages alone, careful numerical handling, and
visible unknown states when data is incomplete. Some valid imported SLOs cannot run
initially because their combinations are outside the supported rules. Those limits
must be shown before activation and accepted before implementation.

## Revisit conditions

Revisit when operators need different composite rules, corrections to historical
measurements, arbitrary report boundaries, or shorter retention. Any change must
explain its effect on existing reports and version the changed behavior.
