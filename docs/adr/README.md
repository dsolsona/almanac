# Architecture Decision Records

Store ADRs here as `NNNN-short-decision-name.md`, starting with `0001`. Use the next
unused number, independently of spec numbering. Never renumber accepted records.
Copy [TEMPLATE.md](TEMPLATE.md) and refer to the result as `ADR-NNNN`.

An ADR records a consequential choice: correctness semantics, architecture,
security boundaries, public contracts, storage, operational behavior, or a major
dependency. Several features can share an ADR. Routine implementation choices
and fixes within accepted decisions do not need their own records.

## Lifecycle

- `proposed`: under discussion; not authorization to implement the decision.
- `accepted`: the maintainer approved it; record the date and approval reference
  or a concise summary of explicit approval in the working conversation.
- `rejected`: considered and declined; preserve the rationale.
- `superseded`: replaced by an accepted ADR; link the old and new records.

Keep accepted rationale stable. Correct typos and links directly; use a new ADR
for a changed decision. Implementation progress belongs in specs or delivery
status, not in ADR status. Link affected specs and explain the practical costs as
well as benefits. Do not invent alternatives or measurements to justify a choice.

## Index

| Identifier | Title | Status |
| --- | --- | --- |
| [ADR-0001](0001-collection-and-ingestion-boundaries.md) | Collection and ingestion boundaries | accepted |
| [ADR-0002](0002-agent-communication.md) | Agent communication | accepted |
| [ADR-0003](0003-bounded-buffering-and-historical-isolation.md) | Bounded buffering and historical isolation | accepted |
| [ADR-0004](0004-identity-and-access.md) | Identity and access | accepted |
| [ADR-0005](0005-measurement-semantics.md) | Preserve measurement meaning through deterministic evaluation | accepted |
| [ADR-0006](0006-openslo-compatibility.md) | Explicit OpenSLO import and execution profiles | accepted |
| [ADR-0007](0007-postgresql-and-storage-boundaries.md) | PostgreSQL and storage boundaries | accepted |
| [ADR-0008](0008-runtime-components-and-packaging.md) | Runtime components and packaging | accepted |
| [ADR-0009](0009-contracts-and-cli-parity.md) | Contracts and CLI parity | accepted |
| [ADR-0010](0010-alert-evaluation-and-delivery.md) | Alert evaluation and delivery | accepted |
| [ADR-0011](0011-ui-design-and-tooling.md) | UI design and tooling | accepted |
| [ADR-0012](0012-agent-leases-and-protocol.md) | Agent leases and protocol | accepted |
| [ADR-0013](0013-provider-capabilities-and-execution.md) | Provider capabilities and execution | accepted |
| [ADR-0014](0014-maintenance-exclusion-semantics.md) | Exclude maintenance from the evaluated population | accepted |
| [ADR-0015](0015-timeline-annotation-boundary.md) | Keep timeline annotations separate from measurement policy | accepted |
| [ADR-0016](0016-authentication-retry-behavior.md) | Recover authentication state without automatic replay | accepted |
| [ADR-0017](0017-recoverable-secret-encryption.md) | Recoverable secret encryption | accepted |
| [ADR-0018](0018-agent-websocket-library.md) | Agent WebSocket library | accepted |
| [ADR-0019](0019-session-authorized-agent-registration.md) | Register agents using the administrator session | accepted |
| [ADR-0020](0020-session-authorized-management.md) | Authorize signed-in management through the current session | accepted |
| [ADR-0021](0021-immediate-initial-collection.md) | Collect an initial settled interval and spread recurring queries | accepted |
| [ADR-0022](0022-restartable-agent-ownership.md) | Replace agent processes without an ownership-expiry wait | accepted |
| [ADR-0023](0023-slo-operational-urgency.md) | Separate SLO operational urgency from performance | accepted |
| [ADR-0024](0024-precomputed-operational-reads.md) | Precompute operational reads with exact derived data | accepted |
