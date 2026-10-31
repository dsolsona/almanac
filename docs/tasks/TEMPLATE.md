# FN-TNN: Concrete outcome

- State: draft
- Owner: primary agent (normally configured Sol/Astra)
- Optional delegation: bounded scope, subagent and useful concurrent primary-agent work; otherwise none
- Governing spec/criteria: relative links and specific AC identifiers
- Applicable ADRs: relative links
- Depends on: task IDs and accepted integrated revisions, or none
- Base revision: exact commit
- Worktree/branch: current task branch; isolated worktree for delegated edits
- Integration/commit authorization: state current scope; no push by default

## Outcome and non-goals

One independently verifiable result. State what must remain outside this task.

## Required context

AGENTS.md, any directory instructions, relevant spec/ADR sections and existing code.
Supply enough context for a fresh session; omit unrelated conversation history.

## Interfaces and edit scope

Exact consumed/produced signatures, data contracts, error behavior and allowed paths.
Name generated sources and required regeneration. A missing consequential decision
is a readiness blocker, not permission to invent a contract.

## Acceptance and expected cases

Map each task outcome to governing acceptance criteria. Include independently derived
examples and negative/boundary cases; identify the test layer that proves each one.

Write a short user acceptance checklist before implementation:

- User starting state, ordinary input values and actions:
- Expected visible result and persisted record:
- Invalid input, actionable feedback and correction path:
- Navigation into/out of the flow and browser history:
- Reported defect's exact reproduction, observed failure and confirmed cause:
- For UI work, accepted reference and concrete side-by-side comparison criteria:
- Normal container-deployment journey and its fixture/build identity:

## Execution and verification

- [ ] Confirm base revision, clean owned worktree and isolated test resources.
- [ ] Read version-specific Modern Go guidance before any Go edit.
- [ ] Add relevant behavior/regression tests and implement the scoped result.
- [ ] Run the exact focused checks listed here by the primary agent.
- [ ] Run required boundary checks from docs/testing.md and record their selection.
- [ ] Inspect changes for scope drift, secrets and generated-source consistency.
- [ ] Record evidence and unresolved concerns; only the primary agent marks verified work done.
- [ ] Freeze product and verification inputs during candidate checks; isolate concurrent work.
- [ ] If delegated, submit for primary-agent review before further edits.

Replace this checklist's generic verification entries with existing, runnable commands
before marking ready. If the task creates a harness, specify its entry-point contract
and expected passing/failing checks; verify and record the commands in the evidence.

## Escalation conditions

Specific ambiguity, contract/dependency changes, failing invariants or missing
infrastructure that requires a maintainer decision (or primary-agent input for
delegated work). Stop the affected change and provide evidence; continue independent
authorized steps where possible.

## Implementation evidence (subagent handoff if delegated)

- Candidate revision/diff and changed files:
- Criterion → executed check → result:
- Commands, exit statuses, selected-test counts and elapsed times:
- Fixture/service/schema/build identity; cached versus fresh execution:
- Failure artifacts and reproduction:
- Original reported case after correction (not just related passing cases):
- Limitations, unverified behavior and decisions needed:

## Primary-agent acceptance

- Diff and contract review:
- Independently checked expectations and relevant negative cases:
- Integrated candidate revision and verification evidence:
- Independently exercised user checklist and container-deployment result:
- UI reference comparison, themes/mobile and continuous navigation result:
- Correction rounds and remaining gaps:
- Final state, integration result and next dependency:
