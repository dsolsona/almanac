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

The [visual foundation plan](../plans/0001-visual-foundation.md) is active.
[F1-T01](F1-T01.md) establishes the shared controls, local catalogue and focused
verification harness. Later tasks are prepared after this foundation is reviewed.
