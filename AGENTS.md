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
- If instructions, specs, and accepted ADRs conflict, identify the conflict and
  resolve it with the maintainer before making the affected change. Continue
  independent work when possible. Do not treat draft documents as accepted policy.

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
  feature screens should reuse them. These locations will be created with the UI.
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
| `docs/plans/` | Optional implementation task breakdowns; create when needed |
| [docs/status.md](docs/status.md) | Delivery progress and verification evidence |
| [docs/review-guide.md](docs/review-guide.md) | Accepted choices, pending amendments and deferred decisions |
| `web/DESIGN_SYSTEM.md` | Visual rules and shared asset references; establish with the UI |

Use the templates and naming conventions in the spec and ADR directories. Specs
and ADRs have independent four-digit sequences and descriptive kebab-case names.
Refer to them as `SPEC-0001` and `ADR-0001` and include relative Markdown links.
Plans reference their governing spec; they do not redefine its requirements.

The delivery strategy in [docs/ways-of-working.md](docs/ways-of-working.md) remains
a proposal. This file and the directory conventions govern current work.

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

- Verify changed behavior against the spec. Use focused calculation tests,
  integration tests for persistence and collection, and browser checks for user
  flows as applicable. Reused tests must be reviewed against Almanac's contract.
- For UI changes, inspect real pages in both themes and at mobile widths, including
  keyboard use, focus, empty states, errors, and exact data representations.
- Report checks actually run, their results, and any gaps. Do not claim that
  tests passed based on an earlier codebase or a different revision.
- Keep changes reviewable and confined to the current feature. Do not begin
  later milestones merely because related code is available.
- Document commands only once they exist and work here. The repository currently
  has no application build or test commands.
- Do not commit, push, or publish unless requested. Respect unrelated local edits.
