# SLI detail implementation plan

Goal: implement the approved [E reference](../design/mocks/sli-detail-options/refined.html)
under [ADR-0046](../adr/0046-sli-measurements-and-collection-detail.md) and
[SPEC-0012](../specs/0012-user-interface.md), using the current shell and contracts.
The primary agent implements and verifies directly in `.worktrees/sli-detail-redesign`.
Task and evidence: [F6-T10](../tasks/F6-T10.md).

## Constraints and review focus

- Missing, partial, empty, zero traffic and stale are never successful measurements.
- Decimal count totals remain exact, including counts beyond JavaScript's safe integer range.
- History inspection bounds, rather than receipt count, define the one-hour summary.
- Route changes, refresh, secondary-read failures and cancellation cannot show another SLI's data.
- Hover, keyboard, touch, both themes and 360/1024/1440 px must retain exact values and usable controls.

## Implementation

1. Add independent model tests for the approved 57/60 fixture, fractional and large
   counts, zero traffic, missing/failed/partial/empty intervals and shortened bounds;
   run them red, then implement `web/src/slis/measurements.ts`.
2. Build `web/src/components/charts/SliMeasurementsChart.tsx` from the approved
   geometry and shared tokens/controls. Cover inspection and keyboard boundaries
   in unit tests, expose its actual states in the design-system catalogue, and
   verify the accepted fixture against the HTML at all six viewport/theme combinations.
3. Compose the SLI detail in `SlisPage.tsx` / `slis.css`, including exact bound reads,
   linked objective status, collapsed disclosures, rename/refresh and support IDs.
   Update affected page tests and real E2E assertions without losing coverage.
4. Run `make check`, then `make test-e2e`, then `make test-perf` for the changed
   main-page journey. Inspect the real isolated deployment and reference comparison
   artifacts. Review the stable candidate while independent checks run.
5. Record results, commit, push, open a PR against `main`, and monitor required
   checks and CodeRabbit threads. Validate and fix findings, verify corrections,
   update the PR and reply in review threads. Do not merge.
