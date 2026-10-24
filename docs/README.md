# Almanac documentation

Start with [AGENTS.md](../AGENTS.md) for project constraints and ways of working.
Then read [SPEC-0001: System overview](specs/0001-system-overview.md) and the specs
and ADRs relevant to the task. The overview is accepted; implementation has not
started. For the complete design package, begin with the
[review guide](review-guide.md).

| Location | What belongs here |
| --- | --- |
| [specs/](specs/README.md) | What the application must do and how we will verify it |
| [adr/](adr/README.md) | Why we chose a consequential technical approach |
| `plans/` (when needed) | Implementation tasks linked to accepted specifications |
| [status.md](status.md) | Delivery progress, evidence, and known gaps |
| [review-guide.md](review-guide.md) | Reading order, accepted choices, and deferred decisions |
| [ways-of-working.md](ways-of-working.md) | Proposed delivery strategy for discussion |

The design package contains 17 accepted specs and 15 accepted ADRs. Crit review
`7fd22977b6c0` accepted the main package; `0de0c0e42db6` accepted maintenance exclusions
and timeline annotations. Acceptance establishes
the design, not implemented behavior or verified performance. Explicitly deferred
choices remain visible in the review guide. Templates are starting points, not decisions.

Keep documentation about Almanac itself. Update affected documents alongside code,
and distinguish requirements, decisions, implementation status, and evidence.
