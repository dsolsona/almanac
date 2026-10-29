# First objective implementation plan

> Use subagent-driven development with two independent implementers once interfaces
> are settled. The maintainer selected GPT-6.1 Terra for this session. The coordinator
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
- [ ] Extend the objective journey with prospective semantic revision management,
  supported calendar windows and daily CSV/JSON reports with consistent snapshots.
- [ ] Add the accepted OpenSLO validation/preview/application/export profile using
  explicit mappings and visible inactive unsupported definitions.
- [ ] Add contextual timeline annotations under SPEC-0017 and complete F3 acceptance.

Later entries are dependency checkpoints, not speculative implementation briefs.
Prepare each bounded task after its preceding interfaces and evidence are established.
The first increment does not establish completion of the whole F3 milestone.

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
