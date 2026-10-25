# PostgreSQL upgrade foundation implementation plan

> For agentic workers: use superpowers:subagent-driven-development with one isolated
> Luna implementer and coordinator-owned review, Git operations and integration.

Goal: explicit, repeatable and bounded database upgrades against real PostgreSQL.
Architecture: embedded transactional SQL, a dedicated pgx connection and advisory
lock, generated typed metadata queries. No runtime migration or domain tables.
Spec: [SPEC-0007](../specs/0007-storage-and-retention.md),
[SPEC-0013](../specs/0013-runtime-and-operations.md),
[SPEC-0010](../specs/0010-api-and-cli.md).
Task: [F1-T05](../tasks/F1-T05.md).

## Global constraints

Single tenant; PostgreSQL18, pgx/v5, sqlc and Go1.27.3. Explicit administrative
upgrade only, no automatic destructive rollback. Existing approved command
families, no secret-bearing output and no pretend backend readiness.

## Review focus

Cancelled or failed upgrades must release session locks and database connections.
Schema history cannot claim a failed SQL transaction committed. Concurrent callers
must inspect history after obtaining the lock. Test cleanup cannot target operator
databases. Connection errors and CLI output cannot expose credentials. Generated
drift checks must use the actual generator configuration.

## Task 1: Database upgrade and isolated persistence checks

Read [F1-T05](../tasks/F1-T05.md) for exact interfaces, ownership and acceptance.

- [ ] Add bounded failing tests and the isolated PostgreSQL setup lane.
- [ ] Implement embedded migrations, generated metadata access and the CLI command.
- [ ] Verify real concurrency/rollback/cancellation, CLI, generated output and race checks.
- [ ] Review, integrate and reconcile documentation and remaining scope.
