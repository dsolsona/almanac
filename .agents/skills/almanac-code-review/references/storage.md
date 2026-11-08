# Storage and derived data

Read [storage](../../../../docs/specs/0007-storage-and-retention.md),
[derived operational reads](../../../../docs/specs/0019-backend-performance-and-derived-data.md),
and [derived recovery](../../../../docs/adr/0034-self-healing-derived-data.md).

- Inspect SQL, schema constraints, indexes and callers before declaring an invariant
  unenforced. Check transaction boundaries, concurrent claims, lost updates, conflict
  handling and database isolation on reachable write paths.
- Confirm that observation values, provenance, source generation and dirty-work
  publication remain consistent. Imports, seeds and repair paths must not expose
  intermediate facts that permanently poison derived state.
- Compare derived results with canonical evaluation at identical time, definition,
  exclusion and source-generation bounds. Check late data, expiry, invalidation,
  interrupted workers and restart for stale or mixed-generation publication.
- Verify missing-work detection, bounded retry/recovery and visible failure reasons
  against the accepted worker contract. A transient failure must not silently strand work.
- Check join cardinality, deterministic pagination and current-state reads before
  operational writes. Verify schema/index evidence rather than assuming it from
  the changed query. Use [Performance](performance.md) for query cost, shared-resource
  pressure and measured operational latency when those behaviors change.

For concurrency findings, supply a concrete interleaving. Keep storage correctness
and measured cost distinguishable, combining findings when they share a root cause.
