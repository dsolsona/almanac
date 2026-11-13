# Alert policy form implementation plan

Use the executing-plans workflow inline. This is [F4-T09](../tasks/F4-T09.md).

Goal: build reusable presentation components for the approved single-page creation
form in [SPEC-0022](../specs/0022-alert-management-ui.md), AC-01/03, and the
[selected reference](../design/mocks/alert-options/README.md). React/TypeScript and
existing shared controls; no new dependencies. The maintainer selected this design
and authorized implementation. This slice exposes a development catalogue example,
not a production alert route, mutation API or engine. Production integration remains
gated by plan 0035 and accepted engine/source contracts.

## Scope and interfaces

- `web/src/alerts/policyDraft.ts`: local form types, default paired conditions,
  exact decimal/preset comparison, duration parsing/formatting and form validation.
  `parseAlertDuration(text, previousSeconds, cadenceSeconds): number | null` accepts
  minute/hour/day units; bare values retain the displayed unit. Reject nonfinite,
  nonpositive, unaligned, fractional-minute and >30-day durations. Threshold checks
  compare exact decimal rational products against `1/(1-target)`.
- `web/src/components/ui/InlineValue.tsx`: controlled text/edit behavior with
  labelled button/input, padded optional suffix, blur/Enter normalization, Escape
  restoration, visible invalid text and focus return. One active editor is owned
  by the form; invalid entries remain in its draft and cannot silently submit.
- Extend shared `Combobox` with an opt-in compact selected presentation. Existing
  summary presentation stays the default. Compact selection has the same control
  height, complete accessible title and Change action with focus restoration.
- `web/src/alerts/AlertPolicyForm.tsx` and `policy-form.css`: three shared FormSection
  cards, name/SLO/severity row, conditions, and in-app destination disclosure.
  Consume supplied SLO options and mutation callbacks; no HTTP/global state.
  Preserve stable condition IDs, at least one condition, disabled duplicate presets,
  add/remove focus, policy-level severity and fixed AND-within/OR-between explanation.
- `web/src/catalogue/AlertPolicyExample.tsx`: explicit local fixture/example with
  normal, no-match, non-30-day, pending and error states, imported by the catalogue
  only. No synthetic real occurrence or production save claim.

## Review focus

Invalid edits cannot disappear or submit the previous hidden number. Exact threshold
strings cannot be rounded into a preset. A selected SLO cannot move the aligned
name/severity controls. New conditions and collapsed editors must remain keyboard
accessible. Non-30-day SLOs require the already-specified preset confirmation;
backend limits/receipt/lifecycle contracts are not invented by this form slice.

## Execution

- [x] Add focused parsing/threshold/default tests in `web/tests/alert-policy-draft.test.tsx`;
  run red after the shared measurement lane is released.
- [x] Implement pure draft helpers, rerun those cases.
- [x] Add interaction tests in `web/tests/alert-policy-form.test.tsx` and existing
  operational-control tests before implementing controls/form. Verify text editing,
  invalid submission, focus/escape, picking SLO/severity, >2 conditions and pending.
- [x] Implement shared controls and approved form composition; demonstrate actual
  states in the catalogue. Do not enable production navigation.
- [x] Run focused Vitest files and typecheck through `make shell`.
- [x] Add `web/browser/alert-policy-form.spec.ts`; exercise both themes at
  360/1024/1440 px, stable picker/editor geometry, touch and keyboard, long names,
  exact values and correction flow. Compare actual screenshots with retained mocks.
- [ ] Review final diff, record failures/results/durations, publish a verified
  dependency PR under existing authorization. No merge.

Commands, when the measurement lane is released:

```sh
make shell CMD='cd web && npx vitest run tests/alert-policy-draft.test.tsx tests/alert-policy-form.test.tsx tests/operational-controls.test.tsx && npm run typecheck'
make shell CMD='cd web && npx playwright test browser/alert-policy-form.spec.ts'
make shell CMD='python3 scripts/check-docs.py'
```

No backend/database/performance suites are justified by this presentation-only
slice. API-backed creation and full alert UI acceptance remain later work.
