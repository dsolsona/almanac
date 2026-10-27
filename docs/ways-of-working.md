# Ways of working

Status: active project workflow. The maintainer agreed to a coordinator with
`gpt-6-luna` implementers and fast, evidence-based verification on 2026-10-24.
This workflow does not alter accepted product contracts or authorize publishing.

On 2026-10-27 the maintainer approved larger usable increments, up to two independent
Luna implementers, early real-journey and visual checks, consolidated review and
strict reuse of approved command invocations to reduce delivery time and rework.

## Roles

The primary agent is the coordinator. It owns task decomposition, architectural
consistency, independent review, integration and truthful delivery status. It
continues through ready tasks within the user's authorized scope, rather than
asking the maintainer to supervise each handoff.

Use `gpt-6-luna` for bounded implementation tasks. The implementer reads the task,
relevant project instructions and cited spec sections, implements only its scope,
runs required checks and submits evidence. It cannot accept its own work, change
product contracts, relax checks, or start a later milestone on its own.

The coordinator resolves interfaces and reviews security/calculation/concurrency
risks; Luna owns implementation and test writing for complete bounded features.
Do not delegate only supporting pieces while the coordinator writes the main
feature. Risk is a reason for precise constraints and review, not automatic
coordinator implementation. Substantial coordinator coding requires explicit
maintainer agreement. Prefer one consolidated review, repeating only for changed
code or unresolved findings. Do not
silently switch the requested implementer model. If unavailable, report the limitation
and agree on a replacement. Runtime configuration must request the model explicitly;
documenting its name does not configure the tool automatically.

For a full vertical increment, Luna owns implementation, debugging, test writing and
task evidence through a review-ready diff. The coordinator settles interfaces and
provides one concrete brief, then reviews the consolidated result. Avoid incremental
coordinator reviews or status-only turns while the implementer is progressing; report
only material conflicts or blockers.

## Start or resume a session

1. Read [AGENTS.md](../AGENTS.md), directory instructions and [status.md](status.md).
2. Inspect the branch, working tree and recent commits. Preserve unrelated edits.
3. Read the current milestone plan and assigned [task](tasks/README.md), then only
   the relevant accepted spec/ADR sections. Find active plans through delivery status
   and the task index; completed plans remain as evidence.
4. Identify the next ready task and its dependency revision. Do not infer that an
   earlier session's agent, worktree, service or test result still exists or is valid.
5. Restore task ownership and verification context before resuming. A new session
   can follow this workflow without chat history; uncertain acceptance evidence is
   a reason to inspect the record, not to mark work done.

## Task preparation and dispatch

Use `docs/plans/NNNN-topic.md` for a milestone dependency plan and
`docs/tasks/FN-TNN.md` for individual briefs. Use the [task template](tasks/TEMPLATE.md).
A task is ready only when it has a concrete outcome, accepted contracts, exact
interfaces or a designated interface-design prerequisite, bounded edit scope,
acceptance checks and no unresolved consequential decision in its implementation path.
Do not create speculative briefs for the entire product in advance.

Give the implementer a fresh task context: repository/worktree, base revision, task
path, required reading, scope and reporting rules. Do not forward the full project
conversation or private investigation notes. For reusable components, the coordinator
reviews suitability and provides the relevant constraints; existing code never
overrides Almanac's contract.

Use up to two active Luna implementers when interfaces are agreed and edits and
fixtures are independent. A useful split is backend/agent and UI/CLI, not two
workers guessing the same contract. Otherwise use one. Use isolated worktrees, with
unique ports, test databases and artifact directories. The shared baseline must
contain the task's approved dependencies. Never have workers edit the same checkout
or mutate a shared test database. Only the coordinator integrates results.

Assign each shared verification port or database lane explicitly. Its owner may
continue checks after diagnosed corrections using approved commands until releasing
the lane or receiving a reassignment; do not request the same reservation before
every invocation. Coordinate handoffs directly between active implementers and
report live handles and terminal results to the coordinator. An observation failure
alone does not authorize restarting a gate: inspect its existing process first.

Increase concurrency only for tasks with stable interfaces, disjoint edits and
independent fixtures. A shared contract/schema change precedes its consumers; it
cannot be delegated as conflicting parallel guesses. No worker may delegate further
without a coordinator-assigned need and explicit scope.

## Implement, review, integrate

Size tasks around a usable outcome, such as a connected agent or a datasource query
test. Keep internal service/storage/HTTP steps within that bounded outcome where
practical; do not require a PR per layer. Publish reviewable vertical increments,
not arbitrary file-count batches. Accepted contracts remain prerequisites.

Run a minimal real user journey as soon as the path is wired, before broad checks.
Inspect the real rendered application early for UI work. Then use focused checks
while editing, one consolidated coordinator review, and the integrated gate before
publication. Review corrected areas and unresolved findings without restarting the
whole review. Do not repeat a passing gate without changed inputs or a specific
unresolved concern. Keep required boundary evidence; this changes sequencing, not
acceptance standards. Finish the current milestone's usable outcomes before optional
polish or unrelated refactors.

Follow [command discipline](commands.md). A new test selector or Make mode does not
inherit approval merely because a similar command worked. Exact command availability
is part of task readiness; a missing approval must not interrupt unattended work.

1. The implementer confirms scope, reads applicable Modern Go guidance before Go
   edits, writes relevant behavior checks and implements the task.
2. It runs the task's focused verification and required boundary checks from
   [testing.md](testing.md). A narrow passing test is not full acceptance evidence.
3. It hands off file/diff details, criterion-to-check mapping, exact commands,
   exit status, timings, test selection/counts, fixture identity, and remaining gaps.
4. The coordinator reads the actual diff and independently evaluates correctness,
   contract fit and test adequacy. For math/security/concurrency, establish expected
   outcomes independently of the worker's implementation and test assertions.
5. The coordinator runs the relevant checks on the integrated candidate. Worker
   results from another revision do not prove the combined application works.
6. Update task and delivery status only after that evidence exists. Keep known gaps
   explicit. Commit or publish only within the user's current authorization.

When publishing stacked pull requests, set each base to the preceding branch and
add the pull request to GitHub's native stack. Branch ancestry and a description
link alone do not establish stack membership. Verify the returned stack identifier,
position and size after creation or extension before reporting it as stacked.

Use focused correction requests referencing observed failures and expected behavior.
After two unsuccessful correction rounds on the same issue, the coordinator
splits/reframes the task or asks before taking over substantial implementation.
Do not burn cycles by repeating the same prompt.
Escalate immediately for conflicting specs, changes to public contracts or dependencies,
security boundaries, data loss, missing required environment, or suspected bad tests.
Report the attempted action, evidence and smallest decision needed. Continue independent
work where possible; do not use escalation to stop routine authorized work.

## Evidence and progress

Task states are `draft`, `ready`, `in_progress`, `review`, `done`, and `blocked`.
The coordinator sets `ready` and `done`; worker completion only moves a task to
`review`. A task marked done identifies its integrated revision and evidence.
A blocked task records the concrete dependency and next action, not just “blocked”.

Keep decisions in ADRs, observable behavior in specs, tasks in task records, and
milestone progress/evidence in [status.md](status.md). Do not duplicate product
requirements across these documents. Record durable workflow lessons in AGENTS.md;
keep transient logs and private investigation material out of repository content.

Record meaningful elapsed time, correction rounds and repeated failure causes in
task evidence. Compare accepted usable outcomes and rework, not activity or number
of PRs. Avoid exhaustive process reporting that does not change the next action.

## Delivery and acceptance

Follow [SPEC-0014](specs/0014-delivery-and-verification.md) for release sequencing.
The maintainer accepts new product specs and ADRs; routine work within accepted
contracts needs no repeated approval. Test or documentation edits do not authorize
relaxing an accepted behavior. UI work includes real browser checks and the shared
component catalogue; copied or mocked screenshots cannot establish working behavior.
