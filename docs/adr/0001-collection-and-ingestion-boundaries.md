# ADR-0001: Collection and ingestion boundaries

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Maintainer approved Crit review `6da785cabe02`; all comments resolved.
- Related specs: [SPEC-0001: System overview](../specs/0001-system-overview.md)
- Supersedes / superseded by: none

## Context

Almanac measures reliability using observations from external systems. Datasources
define a provider, host, collection frequency, and authentication settings. Each
datasource is assigned to one remote agent; SLIs define measurements using that
datasource. Agents may run different software versions and support different
providers or operations.

Operators need to test connections and queries from the environment that will
execute collection. They also need to submit observations from custom jobs when
no agent provider exists. The system overview requires the backend to receive
observations without querying external metric systems itself.

## Decision

Separate external collection from ingestion and evaluation along these
boundaries:

1. **Remote agents execute collection and diagnostics.** `almanac-agent` queries
   the external systems for its assigned datasources. Datasource connection tests
   and SLI query previews execute on that same agent. The backend never performs
   those operations, including as a recovery mechanism.
2. **The backend owns desired configuration.** Agents initiate communication,
   obtain versioned configuration, advertise capabilities, and report health and
   collection progress. Assignments must respect the agent's capabilities.
3. **The backend accepts observations through a supported ingestion API.** Both
   agents and custom producers have a supported submission path. A custom producer
   need not run or register an Almanac collector.
4. **Measurement rules are shared.** Agent and custom-producer observations use
   common validation and persistence semantics. Authentication and transport
   envelopes may differ; they do not silently change measurement meaning.
5. **PostgreSQL stores observations for backend evaluation.** SLO calculations,
   reports, and alert evaluation use stored data. Viewing a result or evaluating
   an alert must not initiate a query against an external metric system.
6. **Failures remain visible.** Agent reachability, collection progress, and data
   freshness are distinct. A missing agent or failed query does not produce an
   implicit successful observation.
7. **Historical backfill executes on the assigned agent.** Datasource and SLI
   setup must support requesting historical observations where the provider can
   supply them. Backfill uses the same measurement validation and persistence
   semantics as regular collection. Provider-specific capabilities determine
   whether a requested interval can be queried in one operation or must be split
   into smaller intervals. No universal query duration is assumed.

For example, one provider may retrieve a month in a single query while another
requires daily requests. Those are illustrative execution strategies, not fixed
limits attached to particular provider names. The supported range and splitting
behavior must reflect the agent implementation and datasource constraints.

This decision establishes logical ownership, not executable packaging. An agent
mode in a shared executable would still be a separately operated collector with
these responsibilities. WebSocket versus long polling, ingestion payloads,
registration credentials, and independently runnable server components remain
separate decisions.

## Alternatives considered

### Agent-only submission

Require all producers to implement the collector protocol. This would reduce the
number of producer identities to model, but would burden simple custom jobs with
configuration and lifecycle behavior they do not need. It would not satisfy the
requirement for direct user-written ingestion clients.

### Separate measurement semantics for each producer type

Give agents and custom producers independent ingestion implementations with their
own validation rules. This allows separate evolution but risks different SLO
results depending on how equivalent observations arrived. Separate envelopes may
be useful; shared measurement semantics are preferred.

### Query external systems when calculating results

This could reduce local observation storage, but would make historical reporting
dependent on external availability, retention, and query semantics. It would also
conflict with agent-owned collection and the PostgreSQL measurement store.

## Consequences

- External connectivity and provider execution belong to the assigned agent's
  environment. Backend availability does not imply a datasource can be reached.
- Interactive diagnostics require a command path that does not wait for the next
  scheduled collection interval. Connection and command lifecycle need a spec.
- The agent protocol must support capabilities, configuration reconciliation,
  heartbeat reporting, and enough collection progress to detect missed polls.
- Ingestion needs explicit identity, authorization, timestamp, duplicate, retry,
  correction, and partial-batch rules before implementation. Custom producer
  resource identity is still open.
- Backend outages require an explicit agent offline policy. This ADR does not
  promise buffering capacity, lossless delivery, or a specific retry guarantee.
- Backfill needs a dedicated contract for requested ranges, provider limits,
  chunk planning, progress, cancellation, retries, and resumability. It must
  address overlap with regular collection, duplicate observations, unavailable
  history, and resource limits so historical work does not starve current polls
  or interactive diagnostics. Whether the backend or agent plans individual
  chunks remains open; execution always belongs to the agent.
- PostgreSQL capacity and retention must be planned for the supported workloads.
  Derived caches and rollups need their own correctness and lifecycle contracts.
- Future process separation must preserve these responsibilities; it does not
  require changing who queries external systems.

## Revisit conditions

Revisit the shared ingestion design if a new measurement type cannot be represented
without losing its meaning, or supported workloads demonstrate a concrete storage
constraint. Changes to agent-owned collection require an explicit product-scope
decision and an update to the governing spec.
