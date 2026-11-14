# Selected alert detail presentation

- State: in progress
- Task: [F4-T12](../tasks/F4-T12.md)
- Base: `a6f47ca`, selected occurrence-list presentation
- Design authority: selected A detail/card mocks and explicit implementation
  authorization. [SPEC-0022](../specs/0022-alert-management-ui.md) describes the
  selected presentation; [SPEC-0021](../specs/0021-alert-policy-engine.md) supplies
  lifecycle/evidence semantics. Both full production contracts remain drafts.

## Composition and ownership

Use one alert title, a supplied real SLO href, severity/lifecycle and controlled
global acknowledgement. Preserve A's distinct reason, evidence, collapsed friendly
calculation and initially open lifecycle cards at 1440/1024/360 px in both themes.
Reuse shared page/buttons/status controls, exact decimal formatters and charts.

Create `web/src/alerts/AlertOccurrenceDetail.tsx` and `occurrence-detail.css`.
The view model supplies occurrence identity/version, lifecycle/severity, timestamps,
reason, original/latest evidence snapshots, per-condition outcomes, exact window
bounds/counts/error rates/burn/thresholds, revisions and lifecycle events. The view
only formats and selects evidence to inspect. It does not derive lifecycle,
condition truth, severity, error rates or burn. Missing values stay null with their
supplied reasons. Condition handoff retains the occurrence and acknowledgement.

The evidence card exposes matching, not-matching and unavailable conditions and
their paired windows. Original firing evidence is separately disclosed with its
own time, bounds and revisions. Latest evidence and processing-wait information do
not overwrite original evidence or advance an uncommitted due boundary. Calculation
steps connect supplied bad/total, observed/allowed error rates and burn threshold.
Lifecycle shows supplied actors, effective/detection times and administrative causes.

Acknowledgement is controlled by permission, supplied lifecycle, pending/error
state and `onAcknowledge(id, expectedVersion)`. No optimistic transition. Preserve
focus on failure and after confirmed acknowledgement/resolution. No Notes,
assignment, manual-close, destination controls, public route or API is added.

## Shared chart extension

Add opt-in inspection to `TimelineChartView`, retaining its existing ECharts SVG
renderer, validated timeline/gap handling and current default behavior. Use the
existing SLO inspection semantics (arrows, Home/End, Escape, outside dismissal,
pointer hover, persistent touch selection), reusing practical helpers. Exact point
values/reasons come from the same supplied samples; floating-point conversion is
only plotting geometry. Keep unavailable samples as gaps and preserve their
inspection reason. No mock chart copying or new library.

## Catalogue and verification

Create a clearly fictional catalogue example and fixtures demonstrating selected
default layout plus handoff, unavailable recovery evidence, processing lag,
administrative terminal state, viewer, acknowledgement pending/error/concurrent
resolution and long names. Catalogue callbacks simulate supplied results only;
they do not establish production global acknowledgement or multi-client behavior.

- [x] Component regressions first: single title/link, supplied evidence/outcomes,
  exact decimal/counts, original/latest bounds/revisions, lag versus unavailable,
  immutable controlled acknowledgement and focus/error/terminal states.
- [x] Shared inspection regressions: exact multi-series keyboard inspection,
  gaps/reasons, reset on input changes, default renderer behavior unaffected.
- [x] Implement after meaningful focused red checks when the lane is released.
- [x] Run focused components, existing timeline tests and TypeScript.
- [x] Browser matrix: both themes, 360/1024/1440 px, keyboard/pointer/touch exact
  inspection, overflow, disclosures, SLO navigation, acknowledgement feedback.
- [x] Capture and inspect actual default/expanded/state renders against selected A;
  record evidence and remaining API/engine/performance gaps.

Planned commands (only after shared verification lane release):

```sh
make shell CMD='cd web && npm test -- tests/alert-occurrence-detail.test.tsx tests/timeline-inspection.test.tsx tests/timeline-render.test.tsx tests/charts.test.tsx tests/slo-detail-redesign.test.tsx'
make shell CMD='cd web && npm run typecheck'
make shell CMD='cd web && npm run test:browser -- browser/alert-occurrence-detail.spec.ts browser/slo-tooltip.spec.ts'
```

No application/test/build commands while the independent measurement lane is
reserved. No commits/pushes, E2E fixture changes or full application suites in this
stream. Parent owns final integration and production acceptance.

Red evidence: the two new component files ran on 2026-11-13 in 3.74 s. The
new detail module was absent; both exact keyboard/touch inspection cases failed
at the absent accessible inspection group. Existing default/invalid-chart behavior
passed. The shared lane was returned before implementation.

## Verified handoff

Presentation implementation and focused verification are complete; primary review
and publication remain. [Retained evidence](../evidence/alert-occurrence-detail/README.md)
contains 15 PNGs and source/capture hashes. The task records all command outcomes,
including the unexplained wrapper terminations; no production alert acceptance is
claimed. The catalogue preserves existing lazy chart loading and isolates detail
import failures. Final source is based on `a6f47ca`; integration onto the scheduler
stack is owned by the primary agent.
