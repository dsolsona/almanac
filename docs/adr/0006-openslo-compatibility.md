# ADR-0006: Explicit OpenSLO import and execution profiles

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related specs: [SPEC-0001](../specs/0001-system-overview.md), [SPEC-0005](../specs/0005-openslo-and-resource-model.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: none

## Context

OpenSLO is a YAML format for describing services, reliability indicators and their
objectives. For example, an SLO document can say “99.9% of checkout requests should
succeed over 30 days” and reference the metric query used to count those requests.
Import lets an operator bring that definition into Almanac instead of rebuilding it
through the UI or CLI.

There are two different checks:

1. **Is the definition valid OpenSLO?** Its fields and references follow the supported
   version of the format.
2. **Can this Almanac installation measure it?** The required provider is implemented,
   an assigned agent can execute the query, credentials are configured, and the
   requested calculation is supported.

For example, a valid definition may reference Elasticsearch while the installed
agent only supports Prometheus. Rejecting it as invalid would be misleading; silently
activating it would promise measurements we cannot collect. We need to preserve the
intent and explain what prevents it from running.

Another example is a success ratio: **successful requests / total requests**. Usually
both counts come from one datasource. OpenSLO can also describe the two components
separately. If one maps to Prometheus and the other to a different system, their
results can arrive at different times or cover different intervals. Almanac's initial
collection contract expects a complete observation from one datasource per SLI;
joining partial counts from different sources needs additional design.

This differs from a **composite SLO**, which combines complete indicators, such as
an API SLI and a worker SLI. Those can come from different agents because each
indicator already has a complete stored measurement. Combining them follows
[ADR-0005](0005-measurement-semantics.md).

The decision here is how import handles supported and unsupported definitions,
without changing the rule that only agents query external systems. The detailed
compatibility matrix is in [SPEC-0005](../specs/0005-openslo-and-resource-model.md).
The format references are the [OpenSLO specification](https://openslo.com/specification/),
[official SLO reference](https://openslo.com/schema/v1/slo/), and
[official SLI reference](https://openslo.com/schema/v1/sli/).

## Decision

Support `openslo/v1` through an explicit list of supported features, with three
import outcomes:

| Outcome | What the user sees |
| --- | --- |
| Invalid | A field/reference error; nothing is applied |
| Valid but unsupported | A saved inactive definition with the reason it cannot run |
| Supported | A preview of required datasource/agent bindings, then explicit activation |

“Mapping” means connecting a portable source reference in the YAML to a real Almanac
datasource, its assigned agent, collection frequency and protected credentials. It
does not mean rewriting the query or changing the objective to make import succeed.
Report limitations against specific fields with stable error codes.
Maintain normalized resources and a protected logical source document. Export omits
credentials and describes required mappings/extensions. Never claim full OpenSLO
compatibility from syntax validation or successful storage alone.

Import supports multi-document atomic application, reusable references, inline
indicators, standard multiple objectives, and composites. Named keys resolve in one
installation. Provider bindings explicitly supply agent, frequency, host, and secret
references. Cross-agent composites execute against stored observations without
changing ownership or sharing datasource credentials between agents.

Propose that a single ratio whose components map to different datasources remains
inactive initially, even if those datasources share an agent. Supporting it requires
a component observation/join contract beyond the current complete-observation SLI
model. Do not flatten such a ratio into unrelated objectives: that changes the math.
An operator may explicitly provide a direct-ingestion SLI producing complete counts,
but that is a reviewed replacement mapping, not an automatic equivalent conversion.

Require slice frequency to match its datasource cadence initially. Preserve unsupported
providers, calendar shapes, and alert resources as explicit inactive definitions.
Alert import never silently enables notifications. SPEC-0005 and SPEC-0006 enumerate
all initial limitations; REVIEW-M01 and REVIEW-M02 request acceptance of these
operational exclusions before implementation. The schema revision is pinned and
identified in validation results; SDK upgrades cannot silently change accepted input.

## Alternatives considered

- Reject every valid unsupported object: simpler persistence but loses foundational
  definitions and makes staged adoption of provider capabilities difficult.
- Claim broad compatibility while dropping fields: produces plausible but incorrect
  objectives, schedules, and notifications; unacceptable for reliability reporting.
- Immediately support cross-source ratio components: broader coverage, but needs
  immutable component identity, aligned completeness, partial arrival, conflict,
  repair ownership, and deterministic materialization contracts first.
- Query all component sources from the backend: conflicts with the accepted remote
  collection boundary and credential isolation.
- Automatically adjust datasource frequency to imported slice settings: changes
  every SLI using that datasource and violates explicit operator scheduling intent.

## Consequences

Operators can inspect and preserve valid definitions without assuming they run.
A capability matrix and inactive-dependency UI/CLI are part of the feature, not
optional diagnostics. Protected source retention and semantic export add storage and
secret-handling obligations. Initial valid-but-inactive cases are material product
limitations requiring maintainer review, especially cross-source ratios and alerts.

## Revisit conditions

Extend the profile when a tested provider contract, component-join design, new OpenSLO
version, or stronger upstream composite semantics justify it. Add fixtures for each
newly executable construct and retain diagnostics for definitions still unsupported.
