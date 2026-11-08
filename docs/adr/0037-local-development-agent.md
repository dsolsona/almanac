# ADR-0037: Seed a local development agent

- Status: accepted
- Created: 2026-11-08
- Accepted: 2026-11-08
- Approval: The maintainer approved `make agent-seed`, automatic startup with `make run`, the name `local-dev`, and direct development-database writes in conversation on 2026-11-08.
- Related spec: [SPEC-0013](../specs/0013-runtime-and-operations.md)
- Supersedes / superseded by: Adds a local-development-only exception to [ADR-0007](0007-postgresql-and-storage-boundaries.md) and [ADR-0019](0019-session-authorized-agent-registration.md); normal registration is unchanged.

## Decision

`make agent-seed` prepares the local Compose database and creates `local-dev` with
a random agent token. The command may write directly to that development database,
including before administrator onboarding. It stores only the token verifier in
PostgreSQL and keeps the token in a private named volume, separate from application
and database secrets. Record issuance in the audit log.

Repeated seeding preserves the identity and token. Refuse an unrelated agent with
the same name, unsafe files, disabled/deleted agents, and expired/revoked or otherwise
invalid saved credentials. Never reset users, onboarding, existing records or authority.
The token uses the existing agent format and maximum lifetime of 365 days.

After seeding, `make run`/`make start` starts the agent as a background Compose
service, using the app's public certificate. `make stop` stops it and preserves its
token. Before seeding these commands retain their existing app-only behavior.
Authentication still requires completed onboarding. The agent restarts after
temporary failures, including rejection before onboarding is complete.

## Alternatives and consequences

Normal registration with an administrator password prompt would preserve the existing
registration boundary, but retain first-use friction and require completed onboarding.
The maintainer selected direct dev seeding instead. A hardcoded token would be simpler
but is unnecessary; generate a random private token once. Keep the seed script in
development tooling, outside the published images and public API.

The dev seed depends on the current agent tables and token format; real-stack tests
must exercise connection, repeat seeding and restart persistence. Production agent
registration and the explicit `make agent` launcher retain their current behavior.
