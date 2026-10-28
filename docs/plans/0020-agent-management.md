# Agent management implementation plan

Goal: deliver [SPEC-0018](../specs/0018-agent-management-usability.md) under
[ADR-0020](../adr/0020-session-authorized-management.md).

Use the project coordinator/implementer workflow and isolated worktrees. The
maintainer selected GPT-6.1 Terra for this session. Base: `217f83b` (PR #40).

- [x] F2-T24: Session-authorized management and dependency-guarded agent deletion.
  Own backend/API/storage/CLI, generated contracts, migrations, authentication UI
  and affected specifications. Publish the agreed API schema to the UI implementer
  before its consumers. Exclude agent UI, Makefile, Compose and launcher scripts.
- [x] F2-T25: Explicit credential-driven `make agent` launcher. Own Makefile,
  Compose/launcher scripts, launcher checks and README startup documentation.
  Existing backend registration/protocol interfaces are unchanged.
- [x] F2-T26: The UI implementer owns readable
  credential controls, session-authorized lifecycle and dependency-aware deletion.
  Consume F2-T24's generated contract; own agent UI/client/component tests and real
  agent browser journeys. Integrate these tasks as one complete usable increment.
- [x] F2-T27: Safe normal handoff of datasources with active SLIs, including
  source grant drain, ownership epochs and recoverable pending state.
- [x] Coordinator: integrate the increment, inspect actual diff and verify real user journeys
  with isolated Compose, full Linux gate and theme/mobile evidence. Update status
  only from the integrated candidate.

Publication targets a stacked PR against `fix/workspace-usability`; merging is not
authorized. Task records preserve first failures and the remaining diagnostic gaps.

Implementers preserve operator deployment and credentials, retain first-failure
evidence, use fixed-purpose approved commands and leave unavailable checks pending.
No worker may change public behavior beyond SPEC-0018 or relax acceptance gates.
