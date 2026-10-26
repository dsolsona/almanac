# Local Compose implementation plan

> Use subagent-driven development. One Luna implementer; coordinator owns real
> Docker verification, review, Git and publication.

Goal: run the current app locally using `make bootstrap`, then `make run`/`make stop`.
Architecture: container build embeds the UI; Compose orders a local secret initializer,
PostgreSQL, explicit schema upgrade, and the existing combined backend/UI binary.
Spec: [SPEC-0013 local Compose workflow](../specs/0013-runtime-and-operations.md),
[ADR-0008](../adr/0008-runtime-components-and-packaging.md).

## Global constraints

- Host prerequisites are Docker Compose and Make; no host Go, Node or OpenSSL.
- Persistent named volumes; no reset or volume deletion in normal commands.
- Local initialization keeps secrets out of logs, URLs and build context.
- App runs non-root with protected secret files and real HTTPS authentication.
- Preserve preview 5198 and the separate PostgreSQL integration fixture.
- No new command approval requests, generic wrappers or arbitrary passthrough.

## Review focus

- Repeated initialization preserves credentials and completed onboarding.
- Init/schema failures cannot race startup or report false readiness.
- Container builds exclude .git, .local, worktrees and host-generated outputs.
- Credential permissions satisfy the existing strict file reader.
- Ctrl-C/stop/restart preserve data; collisions do not destroy other resources.

## Task 1: Compose packaging and local command interface

Read [F1-T19](../tasks/F1-T19.md) for exact interfaces and scope.

- [x] Implement container build, Compose ordering and idempotent initializer.
- [x] Add Make command interface and focused initializer checks.
- [x] Verify real startup, onboarding, repeat bootstrap and restart persistence.
- [ ] Independently review, document actual commands/results and publish.
