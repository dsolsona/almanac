# Specifications

Store behavior specs here as `NNNN-short-feature-name.md`, starting with `0001`.
Use the next unused number, independently of ADR numbering. Copy
[TEMPLATE.md](TEMPLATE.md) and refer to the result as `SPEC-NNNN`.

Describe observable behavior, scope, contracts, failure cases, and acceptance
criteria before implementation. Keep specs small enough to review and deliver.
Put architectural rationale in linked ADRs and implementation tasks in an optional
plan. A spec may reference several ADRs; an ADR may support several specs.

## Lifecycle

- `draft`: proposed behavior with unresolved questions clearly identified.
- `accepted`: the maintainer approved the behavior; record when and how.
- `superseded`: replaced by another spec; link both records.

Accepted specs describe the current agreed contract. Update them with approved
behavior changes and record a short change note; use a replacement spec when the
scope is substantially replaced. Editorial fixes need no renewed approval.

Track implementation separately as `not started`, `in progress`, or `verified`.
Only mark it verified when all in-scope acceptance criteria have evidence for the
current implementation. Record partial progress and blocked checks honestly.

Number acceptance criteria `AC-01`, `AC-02`, and so on; keep identifiers stable as
tests and plans reference them. Evidence should identify the relevant revision or
working state, checks run, results, and any remaining gaps. Scale detail to the
feature; mark irrelevant sections as such with a brief reason.

## Index

| Identifier | Title | Spec status | Implementation |
| --- | --- | --- | --- |
| [SPEC-0001](0001-system-overview.md) | System overview | accepted | not started |
| [SPEC-0002](0002-agent-behavior.md) | Agent behavior | accepted | not started |
| [SPEC-0003](0003-ingestion-and-recovery.md) | Ingestion and recovery | accepted | not started |
| [SPEC-0004](0004-identity-and-access.md) | Identity and access | accepted | not started |
| [SPEC-0005](0005-openslo-and-resource-model.md) | OpenSLO and resource model | accepted | not started |
| [SPEC-0006](0006-slo-calculation-and-reporting.md) | SLO calculation and reporting | accepted | not started |
| [SPEC-0007](0007-storage-and-retention.md) | Storage and retention | accepted | not started |
| [SPEC-0008](0008-backfill-and-repair.md) | Backfill and repair | accepted | not started |
| [SPEC-0009](0009-agent-protocol-and-ownership.md) | Agent protocol and ownership | accepted | not started |
| [SPEC-0010](0010-api-and-cli.md) | API and CLI | accepted | not started |
| [SPEC-0011](0011-alerting-and-notifications.md) | Alerting and notifications | accepted | not started |
| [SPEC-0012](0012-user-interface.md) | User interface | accepted | not started |
| [SPEC-0013](0013-runtime-and-operations.md) | Runtime and operations | accepted | not started |
| [SPEC-0014](0014-delivery-and-verification.md) | Delivery and verification | accepted | not started |
| [SPEC-0015](0015-datasource-providers.md) | Datasource providers | accepted | not started |
| [SPEC-0016](0016-maintenance-exclusions.md) | Maintenance exclusions | accepted | not started |
| [SPEC-0017](0017-slo-timeline-annotations.md) | SLO timeline annotations | accepted | not started |
| [SPEC-0018](0018-agent-management-usability.md) | Usable agent startup and management | accepted | not started |
| [SPEC-0019](0019-backend-performance-and-derived-data.md) | Backend performance and derived data | accepted | not started |
