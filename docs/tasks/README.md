# Implementation tasks

Use `FN-TNN.md` for a bounded task within a delivery slice, for example `F1-T01.md`.
Copy [TEMPLATE.md](TEMPLATE.md). These identifiers are independent of spec/ADR numbers.
The coordinator owns readiness, dependencies, assignments, review and completion.
Follow [ways of working](../ways-of-working.md) and [testing](../testing.md).

A milestone plan belongs in `docs/plans/NNNN-topic.md`; create one when work is ready
for decomposition. It links tasks and dependencies, without redefining product policy.
Do not create empty future task inventories or hand unresolved design to an implementer.

States: `draft` → `ready` → `in_progress` → `review` → `done`; any active task can become
`blocked` with a concrete reason and resumption condition. Worker completion is a
request for coordinator review. Only integrated, verified work is `done`.

The [visual foundation plan](../plans/0001-visual-foundation.md) is complete.
[F1-T01](F1-T01.md) establishes the shared controls, local catalogue and focused
verification harness. [F1-T02](F1-T02.md) adds calendars, switches and continuous
loading feedback; its [plan](../plans/0002-catalogue-controls.md) is also complete.
[F1-T03](F1-T03.md) provides charts and exact tables; its review and verification
are tracked in [delivery status](../status.md). The wider F1 milestone remains
in progress. [F1-T04](F1-T04.md) adds the verified Go UI-only executable,
generated operational contracts and lifecycle checks. [F1-T05](F1-T05.md) is the
completed PostgreSQL upgrade task; its [plan](../plans/0005-storage-foundation.md)
covers explicit schema upgrades and isolated real-database verification. Identity
work continues with [F1-T06](F1-T06.md) and its
[bounded primitives plan](../plans/0006-identity-primitives.md), before the
transactional bootstrap/login service and API/CLI/UI wiring.

[F1-T07](F1-T07.md) adds atomic initial-owner persistence under its
[plan](../plans/0007-initial-owner-persistence.md), stacked on F1-T06.

[F1-T08](F1-T08.md) adds offline common-password screening under its
[plan](../plans/0008-offline-password-policy.md), before password-creation flows.

[F1-T09](F1-T09.md) adds persistent local sessions under its
[plan](../plans/0009-persistent-local-sessions.md), before login/API exposure.

[F1-T10](F1-T10.md) adds atomic password rotation and onboarding completion under
its [plan](../plans/0010-atomic-password-rotation.md), still before API exposure.

[F1-T11](F1-T11.md) adds shared login admission and failure limits under its
[plan](../plans/0011-shared-login-throttle.md), before password checks are exposed.

[F1-T12](F1-T12.md) connects the local authentication application service under its
[plan](../plans/0012-local-authentication-service.md), before HTTP/UI/CLI delivery.

[F1-T13](F1-T13.md) adds the protected local authentication HTTP boundary under its
[plan](../plans/0013-local-authentication-http.md), before runtime/UI/CLI wiring.

[F1-T14](F1-T14.md) wires the runnable authentication backend under its
[plan](../plans/0014-authentication-backend-runtime.md), before combined serving and UI/CLI.

[F1-T15](F1-T15.md) adds noninteractive initial administrator setup through the
verified API under its [plan](../plans/0015-noninteractive-setup.md), including
uncertain-response handling without mutation replay.
