# SPEC-0001: System overview

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Maintainer approved Crit review `1bd27178785f`; all comments resolved.
- Implementation: not started
- Related ADRs: [ADR-0001](../adr/0001-collection-and-ingestion-boundaries.md) and
  [ADR-0002](../adr/0002-agent-communication.md) (accepted)
- Plan: none
- Supersedes / superseded by: none

## Purpose

Almanac is an open source, single-tenant Go application for tracking service
reliability. It collects SLI observations from disparate systems through remote
agents or user-written producers, stores measurements in PostgreSQL, evaluates
SLOs, and provides error budgets, burn rates, reporting, and alerting.

The application serves both human operators and automation. The UI preserves the
Almanac visual design. A CLI provides management and inspection workflows for
automation agents as well as people.

This overview records product requirements and component responsibilities. It is
not an executable protocol specification or a commitment to deliver every feature
in the first milestone. Proposed details and unresolved choices are identified
explicitly; accepting this overview does not accept those choices.

## Scope and non-goals

The product includes remote collection, direct ingestion, SLI/SLO management,
measurement storage, reliability calculations, reports, alerts, user management,
local authentication, Google SSO, first-run onboarding, a UI, and a CLI.

Almanac is single tenant with multiple users, services, datasources, and agents.
The backend must not poll external metric systems, including as a fallback when
an agent is unavailable. Connection
checks and query previews against those systems also execute on the assigned
agent.

Exact API routes, schemas, query semantics, supported provider versions, retention
policies, alert destinations, transport, executable packaging, and deployment
topologies belong in subsequent specs and ADRs.

## Components and responsibilities

| Component | Responsibility |
| --- | --- |
| Backend | Own configuration and management APIs; authenticate and authorize operations; ingest observations; persist data in PostgreSQL; expose calculations, status, and reporting |
| PostgreSQL | Persist application configuration, user and agent records, and SLI observations; detailed schemas, retention, and derived-data storage require subsequent specs |
| `almanac-agent` | Connect to the backend, fetch assigned configuration, query external systems, execute diagnostics, submit observations, and report health and progress |
| UI | Present management, diagnostics, measurements, reports, and alerts using the shared Almanac design system |
| CLI | Provide programmatic management and inspection of SLIs, SLOs, agents, datasources, status, and alerts |
| Alert processing | Evaluate alert conditions and manage notification work; its process boundary and data access contract remain undecided |
| Custom producer | Submit observations through the ingestion API without requiring an Almanac provider implementation |

Logical responsibilities do not yet determine processes or binaries. A combined
server executable with independently runnable components is a deployment direction
to evaluate. Agent packaging, CLI packaging, runtime commands, and communication
between independently running components require an ADR. Independent scaling is
a design consideration; multi-instance coordination is not assumed to exist.

## Resource relationships

For agent-collected measurements:

```text
Agent: almanac-agent-aws-prod-us
  └── Datasource: prometheus-01
        Provider, host, collection frequency, authentication
        ├── SLI: successful requests
        │     Query and measurement definition
        └── SLI: requests below a latency threshold
              Query and measurement definition
```

Each datasource has one assigned agent. An agent can serve multiple datasources.
Each agent-collected SLI references a datasource and defines what to measure.
Collection frequency belongs to the datasource; per-SLI overrides are not an
established requirement. An SLO expresses a reliability objective over an SLI or
supported combination of indicators; exact cardinality and aggregation semantics
must be settled with the SLO model and OpenSLO compatibility work.

The resource model for directly submitted observations remains open. A custom
producer must not be forced to install or register a collector merely to ingest
data. Its measurements still need an explicit identity and authorization boundary.

## Collection and configuration

The operator registers an agent, creates a datasource assigned to that agent,
and defines SLIs using the datasource. The agent initiates communication with the
backend and obtains its collection configuration there.

Configuration is versioned so changes can be detected. Effective configuration
includes assigned datasource connection settings and their SLI definitions.
Changes to assignments, credentials, frequency, or queries must be reflected in
configuration synchronization. The version format, notification mechanism, and
application acknowledgement protocol remain open.

Agents advertise capabilities. A backend may support several providers while an
older agent supports only PromQL collection, for example. Work must be compatible
with the assigned agent; software version alone is insufficient to establish
support. The capability schema, protocol compatibility window, and handling of
capability changes will be defined in the agent spec.

The UI supports testing a datasource connection and testing an SLI query through
the assigned agent. Interactive tests must not wait for the next collection
interval. ADR-0002 selects WebSocket coordination. SPEC-0002 defines testing
unsaved settings without changing active collection configuration. Command
deadlines, cancellation, and result correlation need detailed wire contracts.

Datasource and SLI setup must support requesting historical backfill where the
provider supports it. The assigned agent executes the historical queries. Some
providers can retrieve a long interval in one request; others need smaller
chunks. Provider capabilities and limits determine the execution strategy.
The backfill spec must define range selection, progress, cancellation, retry and
resume behavior, unavailable history, and overlap with regular collection.
Backfill and automatic gap repair are best effort. They use operations and
processing channels separate from live collection and must not impact its current
schedule. Historical work yields under resource pressure. Chunk planning ownership
and detailed resource budgets remain open; unavailable history stays an explicit gap.

## Ingestion and measurement

The backend exposes an ingestion API for agent observations and submissions from
custom jobs. A user can periodically query a system without an available agent
provider and submit the resulting SLI observations directly.

Proposed contract principle: both producer types use common measurement validation
and persistence semantics, even if their authentication or transport envelopes
differ. Exact payloads, units, precision, time boundaries, duplicate handling,
partial batch acceptance, late arrivals, corrections, and history limits must be
defined before ingestion implementation.

Calculations and reports use stored observations. Missing observations must remain
distinguishable from success, zero traffic, stale observations, and failed queries.
The mathematical definitions of attainment, budgets, burn rates, and reporting
windows require a dedicated spec with numerical examples.

## Health and failure visibility

Agents send regular heartbeats to the backend so missing agents can be detected
independently of datasource collection frequency. A live connection or heartbeat
does not prove that collection is progressing.

The product must distinguish agent reachability, configuration synchronization,
scheduled collection progress, query failures, and observation freshness. The
backend needs enough information to detect missed expected polls. A query that
ran successfully and produced no observations differs from a query that never ran.

Exact heartbeat intervals, collection deadlines, grace periods, receipt-time
tracking, health states, and notifications remain open. Disconnection must be
visible and must never trigger backend-side collection. Offline scheduling,
buffering, reconnect delivery, and credential revocation need an explicit policy
before agent implementation; no unlimited buffering or guaranteed delivery is
implied by this overview.

## Identity and onboarding

An uninitialized installation presents an onboarding page where the operator
sets the initial administrator password. Successful setup persists the account
and installation state in the database. The administrator can then sign in,
manage users, and register remote agents. Local user login and Google SSO are
product requirements.

Proposed safety requirements for the identity spec: setup can complete only once,
concurrent requests cannot create competing initial administrators, and ordinary
restarts do not reopen onboarding. The method for proving the operator is entitled
to claim the installation remains undecided.

Roles, Google account admission and linking, recovery, sessions, machine
credentials, agent enrollment and revocation, and datasource-secret delivery must
be specified. Credentials must not leak through logs, ordinary API reads, command
output, diagnostics, or screenshots.

## UI, CLI, and OpenSLO

The UI uses shared Almanac components, themes, charts, and accessible interactions.
The CLI should cover the management and inspection actions available in the UI;
visual presentation itself does not require a CLI equivalent.

Proposed CLI principles are a shared management API, noninteractive operation,
structured output, stable exit codes, and repeatable automation workflows. The
CLI spec must enumerate supported operations and authentication behavior rather
than claim unspecified complete parity.

OpenSLO compatibility is foundational: users must be able to import SLO definitions
expressed using the [OpenSLO specification](https://github.com/OpenSLO/OpenSLO).
The supported version, resource kinds, references, and measurement semantics need
an explicit compatibility contract. Parsing a definition and having a compatible
agent capable of collecting it are separate concerns. Unsupported constructs
must produce actionable diagnostics rather than silently changing meaning.
Export, round-trip preservation, repeated import behavior, and representation of
Almanac-specific settings remain decisions for that contract.

## Acceptance criteria

These are product-level criteria to refine and verify in feature specs. They are
not claims of implemented behavior or a single milestone's delivery checklist.

- AC-01: An installation serves multiple users within one tenant.
- AC-02: Registering an agent, assigning a datasource, and defining an SLI enables
  that agent to obtain configuration and submit observations to PostgreSQL-backed
  storage. The backend does not contact the external metric system.
- AC-03: A user-written producer can submit observations through the ingestion API
  without running an Almanac collector.
- AC-04: Configuration changes are detectable by the agent; the agent spec defines
  how successful application and failure become visible to the backend.
- AC-05: An agent lacking the required capability cannot be given unsupported work
  without an explicit compatibility error visible to the operator.
- AC-06: A connection test or query preview executes on the assigned agent without
  waiting for the next collection interval, returning a result or explicit failure
  within the deadline established by the diagnostics spec.
- AC-07: Missing agents and missed collection executions are detected according to
  configured deadlines, including when an agent heartbeats but collection stalls.
- AC-08: Stored observations support SLO evaluation, budgets, burn rates, reporting,
  and alerting under specified mathematical and missing-data semantics.
- AC-09: First-run onboarding creates a persistent administrator who can log in,
  add users, and register agents. Local login and Google SSO are supported under
  the identity spec's admission rules.
- AC-10: The CLI supports the management and inspection workflows enumerated in
  its spec, including SLIs, SLOs, agents, datasources, status, and alerts.
- AC-11: OpenSLO documents within the declared compatibility contract import with
  their defined meaning; unsupported constructs receive explicit diagnostics.
- AC-12: User-facing screens follow the shared Almanac design system and its
  accessibility and theme requirements.
- AC-13: An operator can request supported historical backfill during datasource
  and SLI setup. The assigned agent retrieves the requested history using
  provider-appropriate query ranges, with progress and unavailable history made
  explicit under the backfill spec.
  Historical work does not delay live collection and does not guarantee recovery
  of unavailable history.

## Decision sequence and open questions

| Next document | Questions it must resolve |
| --- | --- |
| Collection architecture ADR | Agent-owned collection, backend ingestion, and responsibility boundaries |
| Agent behavior spec | Enrollment, assignment, capabilities, configuration lifecycle, collection, diagnostics, heartbeat and offline behavior |
| Agent communication ADR | WebSocket versus long polling, protocol compatibility, reconnects, command dispatch, and routing across backend instances |
| Ingestion and measurement spec | Observation identity and payload, direct-producer resources, validation, retries, duplicates, corrections, and time semantics |
| Backfill spec | Range selection, provider capabilities and limits, chunk planning ownership, execution, progress, retries, cancellation, and scheduling alongside current collection |
| Identity and onboarding spec | Initial ownership, local users, Google SSO, roles, and human/producer/agent credentials |
| OpenSLO compatibility spec | Version and feature coverage, SLI/SLO model, import behavior, and unsupported integrations |
| Runtime architecture ADR | Combined and separate runtime modes, binary packaging, worker coordination, and component data access |

Start with the collection ADR and agent behavior spec after overview review. Draft
shared backend contracts alongside agent behavior. Resolve ingestion and identity
dependencies before implementing the affected agent operations. Detailed backend
storage, calculations, alerting, reporting, and CLI specs follow their dependencies.
The first provider, notification destination, and release scope remain open.

## Verification evidence

Not implemented or behaviorally verified. Documentation review checks consistency
with the product brief, explicit separation of requirements and proposals, and
valid local links. Runtime evidence will be recorded by the implementing specs.

## Change notes

- 2026-10-24: Initial draft for maintainer review.
- 2026-10-24: Accepted after Crit review; clarified product scope and made
  PostgreSQL an explicit component.
- 2026-10-24: Added historical backfill requirements requested by the maintainer
  in Crit comment `c_5f3085` on ADR-0001; detailed execution remains to be specified.
- 2026-10-24: Maintainer confirmed best-effort historical recovery and isolation
  from the live collection schedule in the working conversation.
