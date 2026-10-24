# ADR-0013: Provider capabilities and execution

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related spec: [SPEC-0015](../specs/0015-datasource-providers.md)

## Context and decision

Backend and agent releases can differ. Propose versioned provider descriptors for
schemas and operations, with actual execution capability advertised by each agent.
Backend owns schema validation/configuration/planning; agent owns network execution
and conversion to typed observations. Neither a UI editor nor a release version
alone proves that collection is supported.

Begin with Prometheus, then Tempo metrics; use direct ingestion for sources without
an adapter. Restrict initial component queries to one series and explicit conversion
semantics. Provider adapters return structured incomplete/empty/error results rather
than zero-filled observations. History and preview are independent capabilities.

## Alternatives and consequences

- Inferring support from release numbers breaks custom builds and partial provider
  implementations; explicit capabilities describe reality.
- Backend-side connection tests would test a different network and violate collection
  ownership; diagnostics belong on the assigned agent.
- Automatically reducing multiple series makes results convenient but hides dimension
  mistakes; require explicit source query aggregation initially.
- Runtime executable plugins extend support without a full release but add compatibility
  and trust surfaces; use compiled adapters and generated descriptors initially.

Provider fixtures and versioned conversion contracts are essential. Correct ingestion
cannot repair a query whose interval meaning was guessed. **REVIEW-D01/D02** record
the proposed first providers and restrictions. Revisit plugins or multi-series support
when a concrete use case justifies the added model.
