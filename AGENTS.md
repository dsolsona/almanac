# Working on Almanac

Almanac is an open source Go application for collecting SLIs from disparate
sources, measuring SLOs, error budgets and burn rates, reporting, and alerting.

## Start here

Read this file before making changes. It is the living source of project-wide
guidelines for agents and contributors. Read any additional `AGENTS.md` in the
directories you change, then the relevant specs and ADRs.

- Keep this document current as we agree on new practices and learn from delivery.
  Add durable lessons, remove obsolete instructions, and keep rules actionable.
- Update guidance in the same change that makes it inaccurate. Explain meaningful
  policy changes to the maintainer; do not silently relax guardrails to finish a task.
- Keep task progress and temporary investigation notes out of this file. Put
  feature details in specs and decision rationale in ADRs, linking rather than
  duplicating them here.
- Challenge proposed decisions when evidence or a concrete correctness,
  operational, or complexity tradeoff suggests a better approach. Explain the
  concern, recommend an alternative, and distinguish evidence from assumptions.
  Do not silently replace an agreed decision; bring material changes back for
  discussion and record the resulting agreement.
- Continue the authorized work through its next concrete step after recording a
  decision or completing a review. Do not end the task merely to announce what
  comes next. Bring reviewable specs and ADRs to Crit when requested; ask focused
  questions only where missing information changes the outcome. Product code still
  requires the relevant accepted contracts.
- A request to check PR review comments includes validating findings, fixing valid
  issues, running the relevant checks, updating the PR and replying in its review
  threads. Do not stop after triage; explain findings that do not apply.
- During authorized unattended delivery, open verified increments as stacked PRs
  against their dependency branch. Address review feedback only from the maintainer
  or Greptile; ignore other commenters. Keep required check failures actionable
  regardless of who emitted them. Opening PRs does not authorize merging them.
- If instructions, specs, and accepted ADRs conflict, identify the conflict and
  resolve it with the maintainer before making the affected change. Continue
  independent work when possible. Do not treat draft documents as accepted policy.

## Coordinator and implementer workflow

- The primary agent coordinates: prepare bounded tasks, settle interfaces, review
  actual diffs, integrate results and own verification. Delegate suitable ready
  implementation tasks to `gpt-6-luna`, explicitly selecting that model. This is
  standing project guidance for delegation; no repeated permission is needed within
  the user's authorized task scope. If the model is unavailable, report it; do not
  silently substitute another model.
- Start with one implementer in an isolated worktree. Give it a fresh task brief
  with the base revision, relevant accepted specs/ADRs, edit scope, interfaces,
  acceptance cases and required checks. Avoid forwarding unrelated chat history.
  The coordinator may handle tiny or high-risk tasks directly when appropriate.
- The implementer submits code and evidence for review; it cannot accept its own
  task, relax checks, change product contracts or start later milestones. After
  two unsuccessful correction rounds on one issue, take over or reframe the task.
- Start a new task turn when assigning corrections to a completed implementer.
  A message to an idle agent does not restart it. Check its state after dispatch
  before reporting that implementation is running.
- On a new session, read [delivery status](docs/status.md), inspect git state and
  find the active plan/task before dispatch. Do not assume prior processes,
  worktrees or verification results are still valid.
- During stacked delivery, also inspect open PR bases/heads and existing worktree
  branches. The default branch's status document can lag verified, unmerged work;
  read the active branch's task and evidence before redispatching anything.
- Follow [ways of working](docs/ways-of-working.md), the
  [task format](docs/tasks/README.md) and [testing strategy](docs/testing.md).
  Only the coordinator marks integrated, verified tasks done. Parallel work needs
  independent interfaces, files, databases, ports and artifacts.

## Guiding principles

- Almanac is a single-tenant application supporting multiple users, services,
  and datasource connections.
- External metric collection and datasource diagnostics execute on remote
  `almanac-agent` instances, never in the backend. The backend accepts observations
  from agents and custom producers through an ingestion API and stores measurements
  in PostgreSQL.
- Agents fetch versioned configuration from the backend, advertise capabilities,
  and send heartbeats. Each datasource has one assigned agent. Keep reachability,
  collection progress, and observation freshness distinct.
- Preserve live collection schedules and resources when implementing historical
  work. Backfill and automatic repair are best effort and use separate operations
  and processing channels; they must not consume the live result buffer.
- Treat UI and CLI management, local users and Google SSO, first-run administrator
  onboarding, and OpenSLO import compatibility as product requirements. Local users
  ship first; Google SSO is a later increment, not a first-release gate. Define
  their detailed contracts in specs before implementation.
- Preserve the Almanac visual design: shared tokens and controls, typography,
  light and dark themes, accessible interactions, and consistent charts and tables.
  Establish a shared component catalogue before building screens. Keep reusable
  UI components in `web/src/components/ui/`, chart components in
  `web/src/components/charts/`, and design guidance in `web/DESIGN_SYSTEM.md`.
  The catalogue should demonstrate those actual components and their states;
  feature screens should reuse them. Shared controls, timeline charts, exact-value
  tables and their catalogue examples exist.
  Backend architecture remains subject to explicit decisions.
- Keep maintenance exclusions, notification silences and graph annotations distinct.
  Maintenance omits eligible measurements without deleting them; silences affect
  delivery only; annotations supply context without changing calculations or alerts.
  Follow the scoped permissions and revision contracts in
  [SPEC-0016](docs/specs/0016-maintenance-exclusions.md) and
  [SPEC-0017](docs/specs/0017-slo-timeline-annotations.md).
- Work from specifications. Define observable behavior and acceptance criteria
  before implementation; keep specifications current as behavior changes.
- Record consequential decisions in ADRs before implementing them. Existing code
  is evidence to evaluate, not automatic approval of its design.
- Prefer the smallest complete feature that users can exercise. Reuse suitable
  code and tests after reviewing their behavior, dependencies, and assumptions.
- Keep repository content focused on Almanac's behavior, contracts, and decisions.
  Keep internal conversation history and temporary investigation notes out of
  published artifacts. Preserve required third-party license notices.

## Specifications and decisions

| Location | Responsibility |
| --- | --- |
| [docs/README.md](docs/README.md) | Documentation map and reading order |
| [docs/specs/](docs/specs/README.md) | Behavior, scope, contracts, and acceptance criteria |
| [docs/adr/](docs/adr/README.md) | Consequential architectural and technical decisions |
| `docs/plans/` | Milestone task dependency plans; create when needed |
| [docs/tasks/](docs/tasks/README.md) | Bounded implementer briefs and coordinator acceptance |
| [docs/testing.md](docs/testing.md) | Fast feedback, boundary checks and integration/release gates |
| [docs/status.md](docs/status.md) | Delivery progress and verification evidence |
| [docs/review-guide.md](docs/review-guide.md) | Accepted choices, pending amendments and deferred decisions |
| `web/DESIGN_SYSTEM.md` | Visual rules and shared asset references; establish with the UI |

Use the templates and naming conventions in the spec and ADR directories. Specs
and ADRs have independent four-digit sequences and descriptive kebab-case names.
Refer to them as `SPEC-0001` and `ADR-0001` and include relative Markdown links.
Plans reference their governing spec; they do not redefine its requirements.

The coordinator/implementer workflow in [docs/ways-of-working.md](docs/ways-of-working.md)
and verification gates in [docs/testing.md](docs/testing.md) govern current delivery.
Product scope and milestone order remain defined by accepted specs and ADRs.

## Working loop

1. Inspect relevant code, documentation, and local changes before editing.
2. Define or update the spec and acceptance criteria for new behavior.
3. Identify applicable ADRs and resolve consequential decisions before code.
4. Break substantial work into small, reviewable tasks linked to the spec.
5. Implement the agreed scope, reusing suitable reviewed components and tests.
6. Verify acceptance criteria and report actual evidence and remaining gaps.
7. Reconcile affected docs, contracts, and guidance in the same change.

The maintainer accepts new specs and ADRs; explicit approval in the working
conversation counts and should be summarized in the record. Do not request the
same approval again. Routine fixes that restore specified behavior and editorial
documentation updates do not require a new spec, ADR, or approval cycle.

## Decision discipline

- Distinguish proposed, accepted, and implemented work. Do not mark a proposal
  accepted on behalf of the maintainer or infer completion from documentation.
- Each feature spec identifies scope, non-goals, acceptance criteria, failure
  behavior, and relevant ADRs. Resolve consequential open questions before code.
- ADRs explain context, decision, alternatives, and consequences. One ADR can
  govern many changes; do not create one per function, file, or routine fix.
- Keep accepted decision rationale stable. Supersede a changed decision with a
  new ADR and link both records. Keep implementation progress outside ADR status.
- When a task fits an accepted spec and ADRs, proceed with routine implementation
  choices. Escalate changes to product scope, public contracts, or architecture.
- Update affected specs, contracts, and documentation in the same change as code.

## Go and implementation discipline

- Before writing or modifying Go, use the Modern Go Guidelines CLI for the
  version declared in `go.mod`. Read its complete applicable guideline list;
  consult explanations for relevant rules. Do not assume a toolchain version
  from another project. If tooling is unavailable, report the limitation.
  `make setup SETUP_MODE=go` installs `.local/bin/go-modern-guidelines`; use that
  installed CLI for read-only guideline queries rather than rerunning a global-cache
  installer that may be unavailable inside the sandbox.
- Prefer clear, idiomatic Go and small interfaces justified by actual consumers.
  Avoid speculative frameworks and abstractions. Explain added dependencies;
  architectural dependencies require an ADR.
- Keep calculations deterministic and independently testable. Make time an
  explicit input where it affects windows, scheduling, or results.
- Propagate cancellation through I/O; bound concurrency and retries; make worker
  shutdown and error handling explicit. Never silently swallow failures.
- Specify missing, stale, partial, and zero-traffic data distinctly. Never turn
  missing observations into apparent success. Define units, precision, time
  boundaries, and aggregation rules before implementing calculations.
- Protect credentials in storage, logs, API responses, fixtures, and screenshots.
- Regenerate generated code from its source contracts; do not hand-edit output.

## Evidence and scope

- Keep the edit loop focused and fast; run the integrated-candidate gate before
  accepting combined work and broader checks at the required milestone/risk boundary.
  Do not wrap every focused test in a full build/browser matrix. Separate cached
  setup from execution, and verify the stack matches the candidate revision.
- Keep the maintainer's preview available during verification. Browser checks own
  a separate port and server; never stop the preview to free their port. If a
  preview must restart after integration, restore it and verify the URL before
  handing back the page. Lazy-loaded sections must fail visibly without blanking
  unrelated controls.
- Prefer established, narrowly scoped command families for setup, builds, checks,
  previews and Git operations. Reuse existing approvals; do not introduce generic
  shell execution targets or bypass permission controls. If an operation requires
  new approval while the maintainer is unavailable, leave it pending and continue
  independent authorized work.
- Reuse exact approved command invocations, including arguments, environment
  assignments and shell wrappers. A familiar command name or Make target is not
  evidence that a variation is approved. Do not request approval for a variation
  after a sandbox failure: use an already-approved full check or leave the check
  pending. Carry this restriction into every worker brief.
- Check the saved approval for the actual invocation before submitting a command.
  Do not assume a command-family approval covers a new argument combination or
  shell form. Reuse known approved invocations unchanged where possible; leave
  uncertain operations pending instead of requesting approval. Do not add wrappers
  or broaden targets to evade this restriction.
- The coordinator must carry this command restriction into every implementer
  brief and enforce it during review. Do not request new command approvals for
  direct formatter, compiler or helper-script invocations. Route them through
  fixed-purpose operations under the approved `make setup`, `make build`,
  `make verify` or `make generate` families. Never add arbitrary command execution
  or unchecked passthrough arguments to those targets. Leave actions that cannot
  fit an approved family pending instead of interrupting the maintainer.
- If an implementer cannot reuse an existing command approval, hand the exact
  bounded operation and verification evidence back to the coordinator. Do not
  retry approval requests. The coordinator can perform approved Git staging and
  commits after checking file ownership and the submitted evidence.
- Never replace real API/database/agent acceptance evidence with mocked UI checks.
  Preserve first-attempt failures; do not rerun-to-green, skip expected tests, weaken
  assertions, disable auth, or increase timeouts to manufacture a passing gate.
- Compile integration-tagged tests in the full Go gate even when the database
  fixture is unavailable to the implementer. Compilation catches tagged-code errors
  early; it never substitutes for coordinator-owned real integration execution.
- Verify changed behavior against the spec. Use focused calculation tests,
  integration tests for persistence and collection, and browser checks for user
  flows as applicable. Reused tests must be reviewed against Almanac's contract.
- For UI changes, inspect real pages in both themes and at mobile widths, including
  keyboard use, focus, empty states, errors, and exact data representations.
- Report checks actually run, their results, and any gaps. Do not claim that
  tests passed based on an earlier codebase or a different revision.
- Keep changes reviewable and confined to the current feature. Do not begin
  later milestones merely because related code is available.
- Use `make bootstrap` for the container-only local app, `make run` (or `make start`)
  for daily startup, and `make stop` to preserve its data while stopping services.
  Bootstrap/run/stop never reset volumes or credentials. Keep this Compose project
  separate from the catalogue and acceptance fixtures; disclose the initial password
  only through the explicit `make credentials` action.
- Use `make setup` for pinned Go/web tools and Chromium, `make build` for the
  embedded UI binary, `make verify` for unit/type/build/generated/HTTP checks, and
  `make verify VERIFY_MODE=browser` for catalogue browser checks. Focused component
  checks use `make verify VERIFY_MODE=unit TEST_FILE=tests/ui.test.tsx`.
  Go checks use `VERIFY_MODE=go` with an optional validated `GO_PACKAGE`;
  `VERIFY_MODE=go-race` checks concurrency and `VERIFY_MODE=smoke` runs the binary.
  `make generate` regenerates API contracts; `GENERATE_MODE=sqlc` regenerates storage
  queries; `GENERATE_MODE=format` formats Go
  with the pinned toolchain. `make preview` serves the local catalogue. Database
  checks use `make setup SETUP_MODE=database` for the isolated PostgreSQL18 fixture
  and `make verify VERIFY_MODE=integration` with its explicit
  `ALMANAC_TEST_DATABASE_URL`; storage, authentication and agent management services,
  authentication
  HTTP and executable backend suites run serially by default. Real TLS/API/database,
  backend lifecycle and browser authentication checks exist; full product journeys
  remain subsequent work. Documentation checks use
  `python3 scripts/check-docs.py`. Document new commands only after they work here.
- Do not commit, push, or publish unless requested. Respect unrelated local edits.
