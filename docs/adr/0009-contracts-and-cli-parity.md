# ADR-0009: Contracts and CLI parity

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related spec: [SPEC-0010](../specs/0010-api-and-cli.md)

## Context and decision

Almanac targets human UI use and automation agents equally. Define HTTP operations
in OpenAPI before handlers and generate Go/TypeScript types. The CLI is an API
client using the same operations and authorization. Agent WebSocket payloads have
separate versioned schemas; they do not bypass measurement validation.

Adopt optimistic concurrency, scoped idempotency, cursor pagination, bounded async
jobs, structured problems, and explicit missing-data states. Document an operation
inventory that maps each UI management action to API and CLI behavior. All generated
files are reproducible outputs from checked-in contracts and pinned tools.

## Alternatives

- Handwritten clients allow local flexibility but duplicate contract decisions and
  make automation parity harder to verify.
- CLI direct database access avoids an HTTP hop but bypasses credentials, audit and
  concurrency rules; reject it.
- A separate automation API duplicates user-facing use cases; use the same operations
  with different credentials/output presentation instead.

## Consequences and review

Contract evolution and generated fixtures become part of every API change. Backward
compatibility is intentional; new required fields or altered semantics require a
version plan. Schemas alone do not express all semantics, so examples and acceptance
tests remain necessary. **REVIEW-A01/A02** cover format and CLI defaults.
Revisit transport only if measured workloads demand another interface, preserving
the shared service and authorization boundaries.
