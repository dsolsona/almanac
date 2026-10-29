# First objective implementation plan

> Use subagent-driven development with two independent implementers once interfaces
> are settled. F3-T01 used the maintainer's session selection of GPT-6.1 Terra;
> subsequent work uses the standing gpt-6-luna workflow unless explicitly changed.
> For the current F3-T07 session the maintainer explicitly selected primary-agent
> implementation, with a subagent restricted to executing and reporting checks.
> The coordinator
> owns integration and acceptance under [ways of working](../ways-of-working.md).

Goal: define a request-count objective on an existing Prometheus SLI and inspect
trustworthy attainment, error budget, burn and collection coverage through UI and CLI.

Architecture: calculations read immutable PostgreSQL observations; the backend never
queries the source for an SLO report. Reuse the Go HTTP/authentication/storage boundary,
generated OpenAPI contracts, React controls and lazy-loaded shared charts.

Governing contracts: [SPEC-0005](../specs/0005-openslo-and-resource-model.md),
[SPEC-0006](../specs/0006-slo-calculation-and-reporting.md),
[SPEC-0010](../specs/0010-api-and-cli.md),
[SPEC-0012](../specs/0012-user-interface.md),
[SPEC-0014 F3](../specs/0014-delivery-and-verification.md),
[ADR-0005](../adr/0005-measurement-semantics.md),
[ADR-0009](../adr/0009-contracts-and-cli-parity.md) and
[ADR-0011](../adr/0011-ui-design-and-tooling.md).

## Delivery sequence

- [x] First usable objective: [F3-T01](../tasks/F3-T01.md), service and single-indicator
  occurrence objective creation, fixed rolling window, exact evaluation, bounded
  recent timeline, UI/API/CLI parity and real Compose acceptance.
- [ ] Operational SLO overview and investigation: [F3-T02](../tasks/F3-T02.md),
  backend urgency projection, separate 1 h/6 h burn, rolling-budget history,
  grouped list, target/window presets and timeline/status-rail composition.
- [ ] Complete the numerical timeline: [F3-T03](../tasks/F3-T03.md), UTC day-end
  rolling-objective compliance graph, summary and exact values.
- [ ] Contextual event lifecycle: [F3-T06](../tasks/F3-T06.md), real annotations
  and an independent event layer under SPEC-0017.
- [ ] Restore SLI presentation: [F3-T04](../tasks/F3-T04.md), dense operational
  table and explained measurement/query editor using supported collector modes.
- [ ] Extend the objective journey with prospective semantic revision management,
  supported calendar windows and daily CSV/JSON reports with consistent snapshots.
- [ ] Add the accepted OpenSLO validation/preview/application/export profile using
  explicit mappings and visible inactive unsupported definitions.
- [ ] Complete F3 acceptance, including the operational visual reference and
  performance fixture; annotations are delivered by F3-T06 above.

Later entries are dependency checkpoints, not speculative implementation briefs.
Prepare each bounded task after its preceding interfaces and evidence are established.
The first increment does not establish completion of the whole F3 milestone.

## Operational visual completion

The maintainer confirmed the [visual target](../design/slo-visual-reference.md)
and all four numerical detail lanes on 2026-10-29, with the left navigation retained.
These tasks make the missing accepted budget/burn/history behavior concrete; they
are required F3 outcomes, not optional polish. F3-T01's functional evidence does not
establish acceptance of the simplified list/detail composition.

[F3-T07](../tasks/F3-T07.md) restores the reported creation and visual user
experience after the maintainer rejected the implemented screens during actual
use. Existing passing checks do not establish acceptance of those screens.

The maintainer authorized implementation and accepted the detailed policy in
[ADR-0023](../adr/0023-slo-operational-urgency.md) on 2026-10-29. F3-T02 starts with
coordinator review of the designated API contract prerequisite. F3-T03 remains
dependent on that contract and F3-T02. F3-T04 is an independent presentation increment, but its
bounded list projection must be settled before dispatch. No product implementation
is authorized by an unaccepted proposal. API design is part of preparing these
complete usable increments, not a separate series of storage/API/UI-only deliveries.

Keep numerical computation on the backend and use bounded list projections rather
than one full evaluation per row. Reuse the actual shared controls and charts;
extend their catalogue examples alongside feature changes. The UI/client worker
begins after the coordinator reviews the generated wire contract. Run a real
list → create → detail → SLI → back journey early, before broad verification.

The maintainer explicitly deferred external submission on 2026-10-28 and then
requested F3. F2 remains incomplete. The proposed external-datasource mental model
has not been accepted; this plan does not change it. Backfill, automatic repair,
notifications and maintenance remain F4. Slice methods and composites remain F5.

## First-journey acceptance

An authenticated Editor/Admin creates a service, selects an existing Prometheus
count SLI, enters a target such as 99.9% and a rolling window such as 30 days,
and creates an SLO. The detail page prioritizes the result and its coverage,
with a timeline, exact values and named links back to its service and SLI.
100% target is rejected with actionable feedback and preserved draft inputs.
No data, zero traffic and incomplete coverage cannot become apparent success.

Use existing observation history only when the operator explicitly chooses an
initial historical evaluation boundary. Default validity begins at SLO activation;
report the whole-interval intersection and pre-validity range as not applicable.
No backfill is triggered. Target/window changes never rewrite historical meaning.

## Reuse and verification

The merged base is `e174291`; its tree equals the verified PR45 candidate `73c65b4`.
Existing datasource mutation guards prevent cadence changes while SLIs reference
that datasource. Existing SLI measurements are immutable in this product increment.
Retain those guards and do not expand revision behavior implicitly.

The observation list endpoint mixes attempts with canonical results and is paginated;
calculations need a dedicated consistent database read. Use exact rational arithmetic,
round half-even only at serialization and compare unrounded values for status.
The full-window summary and bounded timeline must come from one database snapshot.

A real user journey precedes broad matrices. The coordinator independently exercises
ordinary creation, correction, navigation and results on an isolated Compose build.
Full Linux, normal Compose, document and generated checks govern acceptance. Existing
operator services/data and all unrelated worktrees remain untouched. Reuse pinned
cached tools; no new approval requests or arbitrary command wrappers/selectors.
