# Ways of working

Status: active project workflow. On 2026-10-31 the maintainer replaced mandatory
coordinator/Luna delivery with a single primary agent, normally Sol or Astra.
Delegation is optional and justified only by useful concurrent progress. This
supersedes the earlier role restrictions; accepted product contracts and command,
verification and publication requirements still apply.

## Ownership and optional delegation

The primary agent owns planning, implementation, debugging, tests, review,
integration and truthful delivery status. It can implement directly without a
separate maintainer approval. Use the configured Sol/Astra model; documentation
does not select or switch the runtime model. There is no required subagent model.

Delegate a bounded task only when the primary agent has independent useful work
to do concurrently. For example, a subagent may run a stable candidate's checks
while the primary agent reviews documentation or another independent area. If the
primary agent would simply wait for the result, run the task directly. Do not create
handoffs merely to separate planning from implementation or obtain a second agent.

For delegated work:

- Supply a fresh brief: base/candidate revision, relevant instructions and accepted
  contracts, bounded scope, acceptance cases, exact approved commands and evidence
  paths. Omit unrelated conversation history. No further delegation without an
  explicit primary-agent assignment.
- Isolate code changes in a worktree. Allocate independent databases, ports and
  artifacts; do not mutate a shared checkout or fixture. A shared schema/contract
  change precedes consumers. The primary agent integrates results.
- Assign verification resources and their owner explicitly. The owner may continue
  approved checks after diagnosed corrections until releasing the lane. Inspect
  an existing process before restarting a check after an observation failure.
- Freeze product and verification inputs for a candidate under test. Concurrent
  work must not invalidate that candidate. Reports identify the tested revision or
  source fingerprint; another revision's result is not integrated acceptance.
- A subagent returns its diff, exact commands, results, timings, fixture identity
  and gaps. It cannot accept its own work, relax checks, alter product contracts or
  start later milestones. The primary agent inspects the actual evidence and diff.
- Start a new task turn for corrections to a completed subagent and confirm its
  state. After two unsuccessful handoffs across an increment, change the approach;
  keep the failure history even when errors, briefs or checkouts change. The primary
  agent may take over directly. Do not repeat a failing handoff loop.

Historical records may call the primary agent the coordinator and delegated agents
implementers/workers. Those terms do not require separate agents for new work.

## Start or resume

1. Read [AGENTS.md](../AGENTS.md), applicable directory instructions and
   [status](status.md). Inspect git state and preserve unrelated edits.
2. Find the active plan/task and relevant accepted specs/ADRs. Inspect recent commits,
   worktrees and, during stacked delivery, open PR bases/heads. Main's status may lag
   verified unmerged work; inspect the active branch's task/evidence before repeating it.
3. Confirm the next dependency revision, ownership and verification context. Do not
   assume previous processes, services, checkouts or results remain valid.

## Prepare and implement

Size work around a usable outcome, such as a connected agent or datasource query
test, rather than a PR per internal layer. Finish the authorized milestone before
optional polish or unrelated refactors. Accepted contracts remain prerequisites.

Use `docs/plans/NNNN-topic.md` for dependency plans and [task records](tasks/README.md)
for substantial bounded work. Follow the [template](tasks/TEMPLATE.md). Do not create
speculative inventories or task records merely to support agent handoffs. Readiness
requires an outcome, accepted contracts, edit scope, interfaces, ordinary and boundary
acceptance cases, required checks and no unresolved consequential decision.

Write the user acceptance checklist before implementation: starting state, ordinary
inputs, expected visible/persisted results, navigation and error correction. For a
reported defect, reproduce the exact steps and values first; distinguish confirmed
causes from hypotheses and related passing fixtures.

Review reused code/tests against Almanac's contracts. Apply version-specific Modern
Go guidance before Go edits. Define independent expected outcomes for calculations,
security and concurrency rather than copying implementation assertions into tests.

Readiness also includes exact permitted focused commands, required final gates and
evidence paths. Follow [command discipline](commands.md); a missing approval must not
interrupt unattended work. New fixed-purpose entry points need their actual invocation's
authorization and cannot introduce arbitrary passthrough execution.

## Verify and review

Follow [testing](testing.md) for gate selection and evidence. Inspect one running
real page/journey early; use focused checks while editing. Reserve Compose end-to-end,
the catalogue matrix and the database/API integration suite for the completed integrated
candidate and PR verification. A handoff, small edit or status request does not trigger
them. Reuse passing evidence for unchanged relevant inputs, including PR publication.

After a failure, preserve diagnostics, investigate with focused checks and rerun the
affected gate when the corrected candidate is ready. Keep unaffected evidence. Never
waive a required gate because a narrow check passed. Retain unexplained failures as
open investigations; a later passing run alone does not establish a fix.

Review the actual diff for correctness, contract fit, test strength, secrets, generated
sources and scope. The primary agent exercises the integrated user checklist itself,
including the normal deployment through the isolated Compose check when applicable.
Keep default origin/authentication/configuration and persisted records in that path.
Preserve operator data, credentials and the maintainer's preview. Isolated API fixtures
and subagent summaries support acceptance but do not replace the user journey.

For UI work, compare actual reference and candidate pages side by side in both themes
and at mobile widths. Assess navigation, hierarchy, density, typography, controls,
keyboard/focus, errors and exact values. Exercise continuous navigation/history and
ordinary form entry, submission and correction. Screenshots alone are not acceptance.
Lazy-loaded failures must remain visible without blanking unrelated controls.

Compile integration-tagged tests in the full Go gate even when the real fixture is
unavailable; compilation does not replace required real integration execution.
Record candidate identity, commands, selected tests/counts, results, artifacts and
gaps. Maintain timing receipts as specified in [command discipline](commands.md),
reporting invocation time and occupied wall time separately. Avoid process reporting
that does not help assess the outcome or next action.

## Progress and publication

Task states are `draft`, `ready`, `in_progress`, `review`, `done` and `blocked`.
The primary agent owns readiness and completion. Delegated completion requests review;
only integrated verified work is done. Blocked records name a dependency and next action.
Keep decisions in ADRs, behavior in specs, task evidence in tasks and milestone status
in [status](status.md). Keep proposed, implemented, verified and unresolved work distinct.

Escalate conflicts, consequential contract/architecture changes, security/data-loss
risks and missing required environment with evidence and the smallest decision needed.
Continue independent authorized work. Routine implementation within accepted contracts
needs no repeated approval. Follow [SPEC-0014](specs/0014-delivery-and-verification.md)
for release sequencing; this workflow does not relax its gates.

Commit, push, publish and merge only when authorized. During authorized unattended
delivery, publish verified increments against their dependency branches. Add stacked
PRs to GitHub's native stack and verify returned membership/position before calling
them stacked. Publishing does not authorize merging.

Checking PR comments includes validating findings, fixing valid issues, verifying,
updating the PR and replying in its review threads; explain findings that do not apply.
During unattended delivery, address feedback only from the maintainer or Greptile.
Required check failures remain actionable regardless of their source.
