# Ways of working

Status: active project workflow. The maintainer agreed to a coordinator with
`gpt-6-luna` implementers and fast, evidence-based verification on 2026-10-24.
This workflow does not alter accepted product contracts or authorize publishing.

## Roles

The primary agent is the coordinator. It owns task decomposition, architectural
consistency, independent review, integration and truthful delivery status. It
continues through ready tasks within the user's authorized scope, rather than
asking the maintainer to supervise each handoff.

Use `gpt-6-luna` for bounded implementation tasks. The implementer reads the task,
relevant project instructions and cited spec sections, implements only its scope,
runs required checks and submits evidence. It cannot accept its own work, change
product contracts, relax checks, or start a later milestone on its own.

The coordinator handles unresolved design, security/calculation/concurrency risks,
and tasks needing broad context. It may implement a task directly when delegation
would add more overhead than useful work; explain that choice briefly. Do not
silently switch the requested implementer model. If unavailable, report the limitation
and agree on a replacement. Runtime configuration must request the model explicitly;
documenting its name does not configure the tool automatically.

## Start or resume a session

1. Read [AGENTS.md](../AGENTS.md), directory instructions and [status.md](status.md).
2. Inspect the branch, working tree and recent commits. Preserve unrelated edits.
3. Read the current milestone plan and assigned [task](tasks/README.md), then only
   the relevant accepted spec/ADR sections. No task plan currently exists for F1.
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

Start with one active implementer. Use isolated worktrees for implementation, with
unique ports, test databases and artifact directories. The shared baseline must
contain the task's approved dependencies. Never have workers edit the same checkout
or mutate a shared test database. Only the coordinator integrates results.

Increase concurrency only for tasks with stable interfaces, disjoint edits and
independent fixtures. A shared contract/schema change precedes its consumers; it
cannot be delegated as conflicting parallel guesses. No worker may delegate further
without a coordinator-assigned need and explicit scope.

## Implement, review, integrate

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

Use focused correction requests referencing observed failures and expected behavior.
After two unsuccessful correction rounds on the same issue, the coordinator takes
it over or splits/reframes the task. Do not burn cycles by repeating the same prompt.
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

The first three bounded F1 tasks form a delegation pilot. Record elapsed time,
review/correction rounds, repeated failure causes and available usage data. Compare
accepted results and total rework, not just implementation speed. Decide whether to
expand delegation after that evidence; no cost savings or quality parity is assumed.

## Delivery and acceptance

Follow [SPEC-0014](specs/0014-delivery-and-verification.md) for release sequencing.
The maintainer accepts new product specs and ADRs; routine work within accepted
contracts needs no repeated approval. Test or documentation edits do not authorize
relaxing an accepted behavior. UI work includes real browser checks and the shared
component catalogue; copied or mocked screenshots cannot establish working behavior.
