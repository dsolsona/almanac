# Working on Almanac

Almanac is an open source Go application for collecting SLIs, measuring SLOs,
error budgets and burn rates, reporting, and alerting.

## Start here

- Before changes, read applicable directory `AGENTS.md` files and inspect git state.
  Preserve unrelated edits. For delivery, read [status](docs/status.md), find the
  active plan/task and inspect its branch and evidence before resuming. Do not
  assume previous processes, worktrees or test results remain valid.
- Read relevant accepted specs and ADRs. Drafts are proposals. Resolve conflicting
  instructions or contracts with the maintainer before the affected change;
  continue independent authorized work.
- Keep this file limited to durable rules and required reading. Update affected
  guidance alongside changes and explain meaningful policy changes. Put behavior
  in specs, decisions in ADRs and progress in status/task records. Keep temporary
  investigation material and private conversation history out of published artifacts.

## Ownership and authorization

- Use one primary agent, normally Sol or Astra, for planning, implementation,
  debugging, tests, review and delivery. No mandatory Luna delegation or separate
  permission for primary-agent implementation. Model selection belongs to runtime
  configuration; this file does not change the active model.
- Delegate only when independent work lets the primary agent make useful progress
  elsewhere, such as running checks while reviewing another area. If the primary
  agent would only wait, do the work directly. Follow the optional-delegation rules
  in [ways of working](docs/ways-of-working.md).
- Continue authorized work through concrete, reviewable outcomes. Challenge choices
  with evidence and explain tradeoffs, but do not silently replace agreed decisions.
  Routine fixes and editorial updates need no new spec/ADR approval cycle.
- The maintainer accepts new specs, ADRs and changes to scope, public contracts or
  architecture. Explicit conversation approval counts; record it without asking again.
- Do not commit, push, publish or merge without authorization. Authorized unattended
  delivery uses verified stacked PRs against their dependency branches; publishing
  does not authorize merging. See [ways of working](docs/ways-of-working.md).
- Checking PR comments includes validating findings, fixing valid issues, verifying,
  updating the PR and replying in review threads. During unattended delivery, address
  maintainer/Greptile feedback only; required check failures remain actionable
  regardless of their source.

## Required reading by task

These documents govern applicable work; read them before that work begins.

| Work | Required guidance |
| --- | --- |
| Delivery, implementation, delegation or PRs | [Ways of working](docs/ways-of-working.md); [task format](docs/tasks/README.md) for task records |
| Build, setup, verification or command approval | [Command discipline](docs/commands.md) and [testing strategy](docs/testing.md) |
| New or changed behavior | Relevant [specs](docs/specs/README.md), [ADRs](docs/adr/README.md) and active plan; [documentation map](docs/README.md) and [review guide](docs/review-guide.md) locate them |
| UI | [Design system](web/DESIGN_SYSTEM.md); SLI/SLO work also requires the [retained visual reference](docs/design/slo-visual-reference.md) |

## Product constraints

- Almanac is single-tenant with multiple users, services and datasource connections.
  UI/CLI management, local users, first-run onboarding, later Google SSO and OpenSLO
  compatibility are requirements; accepted specs define scope and sequencing.
- External collection and datasource diagnostics run on remote `almanac-agent`
  instances, never in the backend. The backend ingests agent/custom-producer
  observations and stores measurements in PostgreSQL.
- Agents fetch versioned configuration, advertise capabilities and send heartbeats.
  Each datasource has one assigned agent. Distinguish reachability, collection
  progress and observation freshness.
- Historical backfill/repair is best effort. Preserve live collection schedules and
  resources; use separate operations/channels, never the live result buffer.
- Maintenance exclusions, notification silences and graph annotations are distinct:
  exclusions omit eligible measurements without deletion, silences affect delivery,
  annotations provide context. Follow [SPEC-0016](docs/specs/0016-maintenance-exclusions.md)
  and [SPEC-0017](docs/specs/0017-slo-timeline-annotations.md).
- Reuse shared UI controls in `web/src/components/ui/` and charts in
  `web/src/components/charts/`; demonstrate their actual states in the catalogue.
  Preserve tokens, typography, both themes and accessible interactions.
- SLI/SLO pages must copy retained slo-tracker markup, layout and chart geometry
  with the current left navigation. Do not approximate or redesign them. Compare
  actual pages side by side and record missing data/interaction requirements.

## Implementation and decisions

- Define observable behavior and acceptance criteria before implementation. Record
  consequential decisions in ADRs first; existing code is evidence, not approval.
  Proceed with routine choices inside accepted contracts. Update affected specs,
  contracts and guidance in the same change.
- Use spec/ADR templates and independent four-digit sequences. Plans link governing
  specs rather than redefining them. Supersede changed accepted ADRs with a new
  linked ADR; keep delivery progress outside decision status.
- Prefer small complete user outcomes, idiomatic Go and interfaces justified by
  consumers. Review reused code/tests against Almanac's contracts. Explain added
  dependencies; architectural dependencies require an ADR. Preserve license notices.
- Before Go edits, read the complete applicable Modern Go guideline list for the
  version in `go.mod` and relevant explanations. Use the installed
  `.local/bin/go-modern-guidelines` CLI; setup and command details are in the linked
  guides. Report unavailable tooling rather than assuming another version.
- Keep calculations deterministic with explicit time inputs. Define units,
  precision, boundaries and aggregation; distinguish missing, stale, partial and
  zero-traffic data. Missing observations must never appear successful.
- Propagate cancellation through I/O; bound concurrency/retries and make shutdown
  and errors explicit. Protect credentials in storage, logs, responses and artifacts.
  Regenerate generated code from source contracts; never hand-edit output.

## Commands and evidence

- Follow [command discipline](docs/commands.md): approval applies to the actual
  invocation. Reuse exact approved commands or the approved closed verification
  catalog within its validated scope. Never bypass restrictions with wrappers,
  passthrough targets or another tool. Do not request new approvals during unattended
  work; leave unavailable operations pending. Pass these restrictions to subagents.
- Follow [testing](docs/testing.md): focused checks while editing, one real journey
  early, required broad gates on the completed integrated candidate. Reuse evidence
  only for matching inputs; diagnose failures before rerunning affected gates.
- Reproduce reported steps/values before fixes. Exercise the integrated user journey
  and normal isolated container deployment; mocks, screenshots and suite counts
  cannot replace real API/database/agent or usability evidence. UI acceptance includes
  both themes, mobile, keyboard/focus, errors, exact values and reference comparison.
- Preserve failures and unresolved investigations. Never weaken checks, disable
  auth, increase timeouts or rerun to manufacture green. Only mark integrated,
  verified work done; report checks actually run and unverified gaps.
- Keep the persistent timing ledger required by the command/testing guides. Report
  cumulative invocation time and occupied wall time separately, retaining failures
  and unknown timings. Keep the maintainer's data, credentials and preview available;
  verification owns separate fixtures, ports and artifacts.
- Use `make bootstrap` for initial local setup, `make run`/`make start` for daily
  startup and `make stop` to preserve data. Verify host HTTPS readiness before
  announcing availability. Disclose the initial password only via `make credentials`.
  Exact setup/build/generation/verification commands live in the linked guides.
