# Catalogue controls implementation plan

Goal: responsive loading feedback and real calendar/switch controls in the catalogue.
Architecture: existing React controls and native Dialog; no dependency changes.
Spec: [SPEC-0012](../specs/0012-user-interface.md); [F1-T02](../tasks/F1-T02.md).

## Global constraints

Preserve shared tokens, local Bevan, both themes, keyboard semantics and production
catalogue exclusion. One isolated Luna implementer; coordinator reviews and verifies.
Use the existing fixed Make commands. No backend/scheduling contract changes.

## Review focus

Calendar dates must not shift across timezones; month-end clamps correctly. Cancel
must not commit browsing state. Opening/closing preserves accessible focus. Multiple
dialogs must retain unique names. Animation respects reduced motion. No demo latency
timer should masquerade as real application performance.

## Task 1: Complete missing catalogue controls

Read [F1-T02](../tasks/F1-T02.md) for exact interfaces, behavior and checks.

- [ ] Implement and verify shared controls with meaningful regression coverage.
- [ ] Independently review code and inspect real screenshots in both themes.
- [ ] Integrate, verify and update delivery evidence.
