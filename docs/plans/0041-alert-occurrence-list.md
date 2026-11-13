# Alert occurrence list presentation plan

Use the executing-plans workflow inline for [F4-T10](../tasks/F4-T10.md). The
maintainer selected A's compact alert list and authorized continued implementation.
Build its reusable presentation while the independent calculation gate runs;
production routes, backend decisions and persistence remain in plan 0035.

Governing contracts: [SPEC-0022](../specs/0022-alert-management-ui.md), selected
[A reference](../design/mocks/alert-options/README.md), shared list grammar in
ADR-0031/0032 and global occurrence semantics in SPEC-0021. No new engine/API
contract or architecture is accepted by this component slice.

## Outcome and interfaces

- `AlertOccurrenceList` consumes a supplied, bounded page of occurrence summaries,
  backend group totals, filters, pagination state and action callbacks. It never
  downloads all pages or derives global counts from visible rows. Stable IDs
  distinguish SLOs with identical display names.
- Preserve six columns: Alert, SLO, State, Why it's open, Open for, Actions. Alert
  and SLO are separate real links. Severity is informational beneath the alert
  name. Use shared status chips and row-navigation rules; inner controls, modified
  clicks and text selection cannot activate whole-row navigation.
- Show supplied paired-window burn evidence and exact threshold; additional
  matching conditions are disclosed without raising severity. Processing waiting,
  behind-cursor state and unavailable recovery evidence retain the open lifecycle
  and show their supplied timestamps/reasons. No frontend calculation decides
  whether a condition breaches or recovers.
- Acknowledge sends occurrence ID and expected version to its callback. Pending
  disables that row's action; success/state changes come from supplied data.
  Viewers have links but no acknowledge action. Preserve visible failures and
  conflict feedback without optimistically claiming an acknowledgement.
- Search, optional SLO grouping and compact filter triggers use the current
  toolbar. Column controls open shared filter dialogs; no permanent dropdown row,
  summary counters or Needs acknowledgement text. Filters apply together, support
  clear/cancel and retain focus. Reuse shared fields, Dialog, Toolbar and Pager.
- History uses the same row composition with terminal state/cause, effective and
  detected times, including Detected after recovery. An unreconstructed gap is a
  separate supplied notice; it must not appear as an empty quiet history.
- A development catalogue fixture supplies the whole local dataset to a fake page
  controller, explicitly labelled fictional. That controller demonstrates filtering,
  grouping, pagination, acknowledgement/error/permission and history states; it is
  not a production API implementation. Real API/CLI/engine integration remains later.

## Verification and review

1. Write focused tests before components for row navigation/action independence,
   expected-version acknowledgement, server-owned counts, same-name SLO identity,
   multi-match evidence, missing/waiting states, filter focus/cancel, history/gaps,
   empty/error/pending and viewer permissions.
2. Implement shared-component composition and faithful A table spacing/typography.
   Keep table overflow inside its container on narrow screens, as in the reference.
3. Run focused tests and typecheck only after F4-T07 releases the measurement lane:
   `make shell CMD='cd web && npx vitest run tests/alert-occurrence-list.test.tsx && npm run typecheck'`.
4. Add browser checks at 1440/1024/360 px in both themes, plus actual touch and
   keyboard filters/ack/navigation. Inspect rendered screenshots beside A and fix
   observed usability/layout defects before publication:
   `make shell CMD='cd web && npx playwright test browser/alert-occurrence-list.spec.ts'`.
5. Review the final slice; document failures, actual results/durations and limits.
   Publish a verified draft dependency PR under existing authorization; no merge.

No backend suite or performance claim is justified by catalogue-only rendering.
Actual authenticated list latency, global concurrency and production data remain
required in the integrated alert delivery.
