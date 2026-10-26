# Authentication backend runtime implementation plan

> One isolated Luna implementer; coordinator settles interfaces, reviews actual
> changes, verifies and publishes the stack. No PR merges.

Goal: make the verified authentication API runnable with protected startup and
single-backend lifecycle checks. Governing spec: [SPEC-0013](../specs/0013-runtime-and-operations.md).
Exact scope/interfaces: [F1-T14](../tasks/F1-T14.md).
Architecture: a net/http backend server around the existing authentication service,
explicit mounted configuration and a dedicated PostgreSQL startup ownership resource.
Stack: existing Go standard library, pgx and OpenAPI generators; no new dependency/schema.

## Constraints and review focus

Preserve protected initial ownership, mandatory rotation, TLS, source trust and CSRF.
Keep missing initial credentials distinguishable from operational/schema failure.
Ownership is held through draining, loss is fail-stop, and no HA/fencing claim is
made for future worker/agent workloads. Inspect cancellation, resource closure,
duplicate startup, file handling and existing UI-only compatibility explicitly.
Use only known approved commands, preserve preview5198 and serialize fixture access.

## Task 1: Runnable backend lifecycle

- [ ] Write focused failing config/file/lifecycle cases and real DB/process acceptance.
- [ ] Implement protected configuration and certificate input without weakening existing readers.
- [ ] Implement the dedicated ownership resource and bounded loss/close behavior.
- [ ] Wire backend health/auth/TLS and command startup/shutdown; keep unsupported modes explicit.
- [ ] Verify the real executable journey, restart, duplicate owner and loss behavior.
- [ ] Independently review actual diff and evidence, correct findings and record acceptance.
- [ ] Publish the verified increment as a stacked PR; keep review artifacts locally.

Combined/UI-only authentication integration, onboarding screens and setup CLI follow
this runnable API. This plan does not claim those user-facing journeys are complete.
