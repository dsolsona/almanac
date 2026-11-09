# Immediate datasource reassignment implementation plan

> Execute inline as the primary agent under the repository ways of working.

**Goal:** Recover datasource collection without waiting for a lost agent's lease.

**Architecture:** Atomically change assignment, ownership epochs and both agent
configurations. Persist update progress until the replacement acknowledges
configuration; reject submissions from any other current assignment before replay.

**Tech stack:** Existing Go/PostgreSQL, generated OpenAPI/sqlc and React controls.

**Spec:** [ADR-0039](../adr/0039-cancel-pending-datasource-reassignment.md), [ADR-0038](../adr/0038-datasource-configuration-acknowledgement.md), [ADR-0037](../adr/0037-immediate-datasource-reassignment.md),
[SPEC-0009](../specs/0009-agent-protocol-and-ownership.md),
[SPEC-0018](../specs/0018-agent-management-usability.md).

## Constraints and review focus

- Preserve accepted measurements, revisions, credentials and unrelated collection.
- Editors change assignment only; viewers cannot write; administrators retain connectivity management.
- No old-agent buffered submission or receipt replay is accepted after reassignment.
- Repeated moves before the next boundary cannot create invalid or overlapping epochs.
- Replacement acknowledgement completes Updating; measurement freshness remains separate.
- Existing pending moves recover without lease expiry, and empty datasources finish on acknowledgement.
- Returning to the pending source cancels immediately without a reverse pending row; stale cancellation conflicts and refreshes the completed assignment.
- Docker verification uses the command catalog. Local startup recovery, review fixes and PR publication are authorized; merge is not.

## Task 1: Integrated recovery (F2-T33)

Files: `internal/storage/datasource_reassignment.go`, `datasources.go`,
`sli_ingestion.go`, `agent_reads.go`, query sources and a preserving migration;
`api/openapi.yaml`, generated consumers, datasource clients/page and CLI;
their existing integration/unit tests and `web/e2e/collection.spec.ts`.

Interfaces: retain `UpdateDatasource` and `ReconcileDatasourceReassignments`;
return the replacement in `agent_id` immediately and retain bounded pending
progress with replacement configuration revision. Both configuration notices use
the existing `PreviousAgentID`/revision and current configuration revision fields.

- [x] Add a real-database regression: unexpired old lease, immediate new assignment/configuration,
  strict old-write denial, replacement ACK completes without a first observation or extra configuration revision.
- [x] Run `make test-integration` to observe the regression against current behavior.
- [x] Implement atomic transfer, fenced ingestion, completion evidence and adoption of legacy pending moves.
- [x] Cover editor assignment/forbidden settings, viewer denial, replay, repeated moves,
  unavailable replacement, empty datasource ACK and deletion concurrency.
- [x] Regenerate with `make shell CMD='make generate GENERATE_MODE=sqlc'` and
  `make shell CMD='make generate GENERATE_MODE=api'` after source changes.
- [x] Update existing UI/client tests and add a real disconnected-agent replacement journey.
- [x] Run `make check`, then `make test-e2e` sequentially; diagnose failures before reruns.
- [x] Review the whole diff, record command durations and remaining gaps in F2-T33,
  and update affected specifications/status. Publish the authorized PR after follow-up verification.
