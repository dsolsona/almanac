# SLO detail redesign implementation plan

Goal: implement approved B under [ADR-0049](../adr/0049-slo-budget-in-context-detail.md)
and [SPEC-0012](../specs/0012-user-interface.md). Task: [F6-T11](../tasks/F6-T11.md).
The primary agent implements directly in `.worktrees/slo-detail-concepts` on
`feat/slo-detail-redesign`; no delegation is needed.

## Review focus

- Missing/stale/partial/zero-traffic/expired states remain explicit, never successful.
- Counts and displayed arithmetic preserve exact decimals beyond safe integers.
- Current summary/disclosures share a snapshot; historical mismatch stays visible.
- Range, burn, timezone, route and authorization changes cannot restore obsolete data.
- Point inspection supports hover, keyboard and touch without mobile overflow.

## Steps

- [x] Add focused regression cases for the new composition, removed tables,
  negative budgets, structured counts/quality and point inspection; run them red.
- [x] Implement the shared bounded `SloHistoryChart`, completed-day inspection and
  catalogue states; reuse backend samples without inventing evaluations.
- [x] Compose B in `OperationalViews.tsx`, with `SloDetailPanels.tsx` and scoped
  `slo-detail.css`; preserve existing request cancellation, status polling,
  history controls, telemetry, rename and errors.
- [x] Compare reference/actual panels at 360/1024/1440 px in light/dark and exercise
  disclosures, gaps, keyboard, touch and real link navigation.
- [ ] Run focused frontend checks, browser verification, affected real container
  SLO journeys and `make test-perf`. Diagnose failures before rerunning; broaden
  only for concrete shared impact. Review and record evidence.
- [ ] Commit, push, open the PR against main and monitor required CI and CodeRabbit.
  Validate/fix/reply to actionable findings. Do not merge.
