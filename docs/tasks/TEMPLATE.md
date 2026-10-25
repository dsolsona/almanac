# FN-TNN: Concrete outcome

- State: draft
- Coordinator: primary agent
- Implementer: gpt-6-luna (confirm runtime availability; no silent substitution)
- Governing spec/criteria: relative links and specific AC identifiers
- Applicable ADRs: relative links
- Depends on: task IDs and accepted integrated revisions, or none
- Base revision: exact commit
- Worktree/branch: assigned by coordinator before dispatch
- Integration/commit authorization: state current scope; no push by default

## Outcome and non-goals

One independently verifiable result. State what must remain outside this task.

## Required context

AGENTS.md, any directory instructions, relevant spec/ADR sections and existing code.
Supply enough context for a fresh session; omit unrelated conversation history.

## Interfaces and edit scope

Exact consumed/produced signatures, data contracts, error behavior and allowed paths.
Name generated sources and required regeneration. A missing consequential decision
is a readiness blocker, not permission for the worker to invent a contract.

## Acceptance and expected cases

Map each task outcome to governing acceptance criteria. Include independently derived
examples and negative/boundary cases; identify the test layer that proves each one.

## Execution and verification

- [ ] Confirm base revision, clean owned worktree and isolated test resources.
- [ ] Read version-specific Modern Go guidance before any Go edit.
- [ ] Add relevant behavior/regression tests and implement the scoped result.
- [ ] Run the exact focused checks listed here by the coordinator.
- [ ] Run required boundary checks from docs/testing.md and record their selection.
- [ ] Inspect changes for scope drift, secrets and generated-source consistency.
- [ ] Submit evidence and unresolved concerns; do not mark the task done.

Replace this checklist's generic verification entries with existing, runnable commands
before marking ready. If the task creates a harness, specify its entry-point contract
and expected passing/failing checks; verify and record the commands in its handoff.

## Escalation conditions

Specific ambiguity, contract/dependency changes, failing invariants or missing
infrastructure that requires coordinator input. Stop the affected change and provide
evidence; continue independent authorized steps where possible.

## Worker handoff

- Candidate revision/diff and changed files:
- Criterion → executed check → result:
- Commands, exit statuses, selected-test counts and elapsed times:
- Fixture/service/schema/build identity; cached versus fresh execution:
- Failure artifacts and reproduction:
- Limitations, unverified behavior and decisions needed:

## Coordinator acceptance

- Diff and contract review:
- Independently checked expectations and relevant negative cases:
- Integrated candidate revision and verification evidence:
- Correction rounds and remaining gaps:
- Final state, integration result and next dependency:
