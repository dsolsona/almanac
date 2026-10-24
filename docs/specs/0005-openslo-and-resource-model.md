# SPEC-0005: OpenSLO and resource model

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: not started
- Related ADRs: [ADR-0005](../adr/0005-measurement-semantics.md), [ADR-0006](../adr/0006-openslo-compatibility.md)
- Plan: none
- Supersedes / superseded by: none

## Purpose

Operators can define services, measurements, and objectives once and manage them
through the UI, CLI, or OpenSLO import. Import reports precisely which definitions
can run, which need mapping, and which require an unsupported capability. This is
a proposed initial interoperability profile, not a claim of full OpenSLO support.

## Scope and non-goals

Include service, datasource, SLI, SLO, objective, and definition-revision identity;
OpenSLO v1 YAML/JSON import, validation, preview, atomic application, and export.
Include multi-objective SLOs and composites spanning agents and providers. Defer
cross-datasource components inside one ratio, arbitrary expression graphs, v1alpha
and v2alpha conversion, and byte-for-byte preservation of YAML formatting.
Unsupported execution is visible and never silently converted to a simpler model.

## Resource identity and lifecycle

Use opaque immutable internal IDs and separate unique `(kind, name)` import keys.
Names follow the RFC1123 label subset, at most 63 characters; display names are
human-readable and mutable. Preserve labels, including multiple values, and
annotations. One installation is one naming scope; there are no implicit namespaces.

| Resource | Proposed contract |
| --- | --- |
| Service | Named ownership/grouping record; one SLO belongs to one service |
| Datasource | Provider, host, protected authentication, frequency, one assigned agent |
| SLI | Collection mode, typed measurement, query, unit, canonical interval, revision history |
| SLO | Service, budgeting method, window, one or more independently identified objectives |
| Objective | Indicator reference, target, optional threshold/slice settings and composite weight |

An agent-collected SLI references exactly one datasource. Its good/bad and total
queries may differ but execute against that same connection and interval. An
external SLI has no datasource or agent and declares its measurement schema and
cadence, or explicitly irregular arrival. External producers own replay and repair.
An irregular SLI is eligible for occurrence evaluation over accepted intervals,
but cannot activate slice budgeting without a canonical expected cadence.

One SLI may serve several SLOs without duplicate collection. Inline imported SLIs
receive private identities derived from parent ID and structural location, with
stable mapping across reimport; they do not collide with named standalone SLIs.
A standard multi-objective SLO evaluates each objective independently; no implicit
composite is inferred. A composite explicitly places indicators on its objectives.
SLOs cannot reference other SLOs recursively.

Archive preserves historical queries and blocks new scheduling after its effective
boundary. Delete of a referenced resource fails with dependent IDs; initial deletion
is logical archival, not deletion of observations. Datasource reassignment changes
ownership configuration but does not itself change measurement meaning. Changing
query, unit, collection mode, interval, or normalization creates an SLI revision.
Changing objective target, budgeting, window, or indicator creates an SLO revision.
Metadata edits do not. Use expected revision preconditions to reject lost updates.

The first SLI revision records separate `measurement-valid-from` and live
collection activation timestamps. During an authorized Editor/Admin setup-backfill
preview, the operator may explicitly apply this initial definition to an earlier
canonical range within retention and provider availability. The preview identifies
the historical query, source mapping, range, and retrospective interpretation;
applying it records the operator’s request and authorization. Historical validity
does not imply collection was operational then. It never overwrites observations
or grants an agent permission to execute without a current historical-work grant.

An initial SLO similarly has an `evaluation-valid-from` distinct from its operational
activation. The operator may explicitly request evaluation of the selected historical
measurement range under the initial target; do not infer that target was an operational
commitment in the past. Preserve that choice and provenance in reports. Without an
explicit historical request, validity begins at activation. Later revisions remain
prospective: setup backfill is not a way to rewrite effective history or backdate
an ordinary query/target edit.

Operational activation and later revisions use a scheduled canonical interval boundary exposed
in UI/API/CLI. A revision can be cancelled before activation; effective history
cannot be rewritten. For agent-collected definitions, activation uses the next
common old/new cadence boundary after the affected agent has acknowledged replacement
configuration and effective bounds, or after prior execution authority expires. The
common cadence period must be at most one hour; otherwise disable the old schedule
explicitly and create a new SLI. Waiting for lease expiry is separate and may add
delay; do not activate immediately by assuming an offline agent has received new
bounds. Direct-ingestion revisions have no agent lease wait, but enforce the same
prospective interval and producer authorization bounds. Preserve old revision settings
for authorized historical queries. Reports split at effective boundaries; an ordinary
edit never retrospectively applies a new target or query.

## OpenSLO profile and validation

The upstream v1 model allows separate sources for ratio components and distinguishes
inline indicators, references, and composite objectives. Therefore single-source
support must not be advertised as universal v1 compatibility. Upstream also has
three budgeting methods and implementation-specific metric-source payloads.
[OpenSLO specification](https://openslo.com/specification/)

The following is Almanac's proposed profile. Validate against a vendored, identified
v1 schema plus documented semantic checks; do not delegate the entire contract to
an SDK. Record the schema revision/digest in import results. SDK discrepancies such
as numeric slice windows must not cause an otherwise valid definition to disappear.
The official reference documents numeric minutes in the specification but a string
restriction in its Go model. [OpenSLO SLO reference](https://openslo.com/schema/v1/slo/)

| Input | Import behavior | Initial activation |
| --- | --- | --- |
| Service, DataSource, SLI, SLO | Preserve and map resource identity | Supported subject to mappings |
| AlertPolicy, AlertCondition, AlertNotificationTarget | Preserve definitions and references as inactive attachments | No automatic notification activation; explicit native policy mapping required |
| Good/total or bad/total ratio, same mapped datasource | Preserve both queries; normalize bad to total minus bad | Counts with occurrence or slice methods |
| Raw success/failure ratio | Preserve interpretation; failure becomes success `1-r` at evaluation | Slice methods; occurrence budgeting unsupported without counts |
| Threshold metric | Preserve raw value and unit | Slice methods as defined in SPEC-0006 |
| Multiple objectives sharing one indicator | Preserve separate targets/results | Supported; no fabricated aggregate budget |
| Composite objectives on different sources/agents | Preserve references, targets, weights | Supported combinations in SPEC-0006 |
| Good/bad and total on different mapped datasources | Preserve each source reference | Inactive: `unsupported_cross_source_ratio` |
| Unknown provider/query dialect | Preserve protected source payload | Inactive: `unsupported_provider` |
| Missing window | Structurally importable | Inactive until explicit window supplied |
| Unsupported version | Reject with version diagnostic | No coercion |

Source payloads are not portable query languages. A mapping identifies an existing
connection or creates a proposed datasource with provider, host, frequency, assigned
agent, and credential reference. Never infer credentials, assign the first agent,
or perform backend network probes. Source `type` and referenced datasource must
agree. A provider adapter validates query fields and scalar reduction requirements;
unknown fields remain preserved but block activation if their meaning is unknown.
Counter inputs require explicit reset-aware interval conversion in that adapter;
a counter reading is not an interval count. Raw ratios never gain invented counts.
The good/bad/total and raw variants are mutually exclusive shapes.
[OpenSLO SLI reference](https://openslo.com/schema/v1/sli/)

Slice windows are fixed durations; numeric values mean minutes and must resolve to
positive whole seconds. For initial activation, each slice window equals the
referenced datasource frequency or external SLI cadence. This deliberately avoids
silently overriding the datasource schedule when importing `timeSliceWindow`.
Different objectives on one SLI cannot activate conflicting slice frequencies.
Occurrence composites may combine different cadences if report boundaries align;
slice composites require identical cadence and alignment across all children.
A cross-agent composite joins stored observations in the backend; it never causes
one agent to read another agent's datasource or transfers credentials between them.

## Import, mapping, and export workflow

1. Parse bounded YAML multi-document or JSON input (proposed limits: 5 MiB and
   1,000 documents). Reject duplicate keys, unsafe YAML tags, alias expansion beyond
   limits, malformed numbers, duplicate import keys, and unknown top-level fields.
2. Validate structure, resolve references within the bundle then existing records,
   and produce per-document and field-path diagnostics. Unresolved references can
   be retained only in an explicitly inactive import, never an active graph.
3. Preview creates, updates, unchanged objects, conflicts, secret placeholders,
   required source mappings, and execution limitations. Default apply requires all
   selected objects activatable; `store-inactive` explicitly retains valid blocked
   graphs. Invalid syntax/schema is rejected in either mode.
4. Apply one transaction using preview digest and expected resource revisions.
   Concurrent changes invalidate preview. No partial resource graph is committed;
   agent configuration publication occurs only after durable commit. Reapplying
   the same normalized document and mappings is a no-op, including revisions.
5. Activation is a separate, auditable state change after all dependencies and
   permissions validate. An imported alert attachment cannot start notifications
   merely because the associated measurement becomes active.

Maintain the original logical document with protected secret fields, normalized
resources, and a machine-readable capability report. Raw source material is not
returned in ordinary reads or logs. Unknown connection fields are conservatively
secret; user exports replace credentials with explicit placeholders. Export
preserves unsupported semantics and references, but reports those placeholders and
any required Almanac mapping sidecar. It is semantic export, not credential-complete
round trip. Never claim an edited native definition is unchanged OpenSLO input.

Export active supported resources as v1; external ingestion settings use documented
Almanac annotations/sidecar settings and are identified as extensions. Mathematical
SLO-exclusion extensions are preserved but inactive initially; native maintenance
under [SPEC-0016](0016-maintenance-exclusions.md) does not define a portable import
mapping for arbitrary extensions. They cannot activate silently through import.
An import does not delete omitted existing resources. Native import keys are immutable.
A changed input name creates a new object unless the operator explicitly maps that
input to an existing immutable key; the preview and export disclose this mapping.

## Failure and recovery

Authorization covers every created/updated resource and referenced secret or source.
Permission failure prevents the transaction. Validation errors include document
index, resource key, field path, stable code, and remediation without credential
values. Database failure produces no successful apply receipt. An idempotency key
returns the original applied outcome after reauthorization. Inactive dependencies
remain visible after restart; provider availability alone does not activate them.

## Acceptance criteria

- AC-01: Reimporting unchanged input creates no duplicate resources or revisions;
  concurrent modification fails rather than overwriting edits.
- AC-02: A composite using two supported datasources assigned to two agents receives
  separate collection configurations and evaluates only persisted observations.
- AC-03: A valid cross-source numerator/denominator import preserves both references,
  reports its inactive reason, and never queries either source from the backend.
- AC-04: Raw failure ratio `0.02` evaluates as success `0.98`; it cannot activate
  occurrence budgeting or acquire a synthetic event count.
- AC-05: Numeric slice `5` and shorthand `5m` normalize equivalently; incompatible
  datasource cadence blocks activation with a field-specific explanation.
- AC-06: Missing references, unknown providers, and unsupported alerts remain explicit
  in inactive import/export; malformed documents cannot use inactive mode to bypass validation.
- AC-07: A multi-document failure leaves no partially updated graph. Previews, errors,
  ordinary reads, and exports do not reveal source credentials.
- AC-08: Archived and revised definitions retain historical identity. An SLO crossing
  an effective revision boundary exposes separate results, not reinterpreted history.
- AC-09: An explicitly authorized initial setup backfill evaluates earlier retained
  measurements under recorded initial validity bounds, while reports distinguish
  retrospective target application from operational activation. An ordinary later
  edit cannot backdate those bounds or reinterpret accepted historical observations.

## Dependencies and review assumptions

Depends on [SPEC-0003](0003-ingestion-and-recovery.md) and
[SPEC-0006](0006-slo-calculation-and-reporting.md). The following review notes record the accepted initial choices:

- REVIEW-M01: Approve the initial inactive cross-source-ratio boundary, or extend
  ingestion with component identities and deterministic cross-agent joins first.
  Import alone is not full operational support for this valid OpenSLO construct.
- REVIEW-M02: Approve strict slice/cadence equality and explicit inactive alert
  attachments. Both reduce initial execution coverage; neither silently changes input.
- REVIEW-M03: Approve immutable prospective revisions and logical archival, with
  explicitly reviewed historical validity for initial setup backfill only. This
  protects reproducible historical meaning without blocking initial history import.

## Verification evidence

Documentation research only, 2026-10-24. Official specification and SLI/SLO references
were inspected; no importer or calculations exist. Implementation must add schema
fixtures, graph transaction/authorization tests, provider-mapping tests, and semantic
round-trip tests for every matrix row, including unsupported-but-valid inputs.

## Change notes

- 2026-10-24: Proposed resource lifecycle and explicit OpenSLO v1 execution profile.
