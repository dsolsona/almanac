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
are tracked in [delivery status](../status.md). F1 foundation is verified through
F1-T18, merged at `9c25fef`. [F1-T04](F1-T04.md) adds the verified Go UI-only executable,
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

[F1-T16](F1-T16.md) adds the explicit combined backend/UI runtime under its
[plan](../plans/0016-combined-backend-ui-runtime.md), before browser onboarding.

[F1-T17](F1-T17.md) establishes the browser authentication client under the
[browser onboarding plan](../plans/0017-browser-onboarding.md), before its screens
and real browser acceptance.

[F1-T18](F1-T18.md) defines the dependent sign-in and initial-password screens,
with actual browser/backend/database acceptance in the same increment.

[F1-T19](F1-T19.md) adds container-only local bootstrap and daily startup under the
[local Compose plan](../plans/0018-local-compose.md), with persistent state and
actual onboarding/restart verification.

F2 begins with [F2-T01](F2-T01.md), local VictoriaMetrics and VictoriaTraces fixtures
under the [first observations plan](../plans/0019-first-observations.md).
[F2-T02](F2-T02.md) adds guarded registration persistence and safe retry receipts;
it precedes the management service and API/UI/CLI exposure.
[F2-T03](F2-T03.md) implements the internal administrator proof and one-time
agent-token issuance service before management endpoints are exposed.
[F2-T04](F2-T04.md) extends guarded storage with safe administrative reads,
credential revocation and agent disablement before rotation and public exposure.
[F2-T05](F2-T05.md) completes internal credential rotation and the management
service operations needed by the subsequent HTTP, CLI and UI boundary.
[F2-T06](F2-T06.md) exposes those operations through the authenticated management
API, generated contracts and combined/backend runtimes.
[F2-T07](F2-T07.md) adds the bounded browser management client before agent
administration screens and real browser acceptance.
[F2-T08](F2-T08.md) adds the first agent registration, details and credential
history screens with actual browser/backend/database acceptance.
[F2-T09](F2-T09.md) adds rotation, revocation and disable controls with explicit
versioned command recovery and one-time replacement-token handling.
[F2-T10](F2-T10.md) adds reusable CLI authentication and protected profiles before
agent-management command parity.
