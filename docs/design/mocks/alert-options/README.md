# Alert design reference

The selected baseline is **A · Overview** for the list and alert details, with
the current single-page creation form shared by A/C. The maintainer selected
these compositions in the design review. B, D and E remain exploratory alternatives.

These are static, interactive mocks using fictional data. They do not implement
an alert engine, notification delivery or production API behavior. The proposed
production contracts are [SPEC-0021](../../../specs/0021-alert-policy-engine.md),
[SPEC-0022](../../../specs/0022-alert-management-ui.md) and
[ADR-0051](../../../adr/0051-durable-alert-policy-state-machine.md).

## Open and review

After checking out the repository, open [index.html](index.html) locally. Assets
and fonts are local; no backend or credentials are needed. The top bar switches
A–E and themes. Direct routes include `#a/list`, `#a/new`, `#a/history` and
`#a/detail/checkout-sustained`.

1. Review A's table: separate alert/SLO names, paired-window trigger evidence,
   whole-row navigation, inline acknowledgement, SLO grouping and column filters.
2. Open an alert. Follow the actual SLO preview link, inspect chart points with
   pointer/touch/keyboard, and expand the calculation and lifecycle cards.
3. Acknowledge an alert. It becomes Acknowledged throughout this browser page;
   Jamie Morgan and the action time appear in its history. This demonstrates
   global-state intent, not cross-browser synchronization.
4. Create a policy. Enter a name, search/select an SLO, choose severity, and edit
   the fast/sustained conditions by clicking their displayed values. Type a
   duration such as `10 min` or `1 h`; blur/Enter applies it, Escape restores it.
   Add a custom condition, try invalid text/windows, correct them and save.
5. The policy appears as Normal. Saving does not fabricate a firing occurrence.
   Review History for resolved and Detected after recovery examples.
6. Compare light/dark and mobile layouts. Reset demo restores fixtures; reloading
   clears in-memory changes.

| Direction | Alert experience | Creation |
| --- | --- | --- |
| A · Overview (selected) | Compact table, grouping, filters and separate detail cards | Current single-page section cards |
| B · Evidence | Paired-window evidence in every row | Earlier single-condition form beside preview |
| C · Workspace | Queue and evidence side by side | Same current creation as A |
| D · Focus | One alert with plain-language explanation and next-alert navigation | Earlier intent-led form |
| E · Timeline | Overlapping occurrences and acknowledgement segments | Earlier paired-window illustration and form |

## Selected renders

| View | Light | Dark / narrow |
| --- | --- | --- |
| List | [Desktop](png/a-compact-light-1440.png) | [Dark](png/a-compact-dark-1440.png), [mobile](png/a-compact-dark-360.png) |
| Group by SLO | [Desktop](png/a-compact-grouped-light-1440.png) | [Mobile](png/a-compact-grouped-dark-360.png) |
| Alert detail | [Desktop](png/a-detail-light-1440.png) | [Dark](png/a-detail-dark-1440.png), [mobile](png/a-detail-dark-360.png) |
| Calculation | [Expanded](png/a-calculation-light-1440.png) | [Mobile](png/a-calculation-dark-360.png) |
| Creation | [Desktop](png/a-duration-light-1440.png) | [Dark](png/a-duration-dark-1440.png), [mobile](png/a-duration-dark-360.png) |
| Inline override | [Threshold](png/a-duration-edit-light-1440.png) | [Duration](png/a-duration-window-edit-dark-1440.png) |

## Fixture meaning and limits

The snapshot is 12 November 2026, 10:42 UTC. SLOs use a 99.9% target and a
30-day rolling period. Fast burn is 5 min/1 h at 14.4×; sustained burn is
30 min/6 h at 6×. Both windows must reach their threshold. The creation flow
combines conditions with OR and fixes severity at policy level.

Checkout fast burn uses 228 bad / 10,000 events (22.8×) and 1,116 / 60,000
(18.6×). Checkout sustained burn uses 375 / 30,000 (12.5×) and 2,664 /
360,000 (7.4×). Historical chart lines are illustrative samples, not calculated
from an underlying measurement stream. Search's last usable evidence is 10:36
and remains visibly stale. Linked SLO previews are separate fictional 10:00
snapshots, not synchronized production values.

Existing occurrence fixtures each represent a separate single-condition policy.
Multi-condition creation works, but the list/detail fixtures do not yet depict
several matching conditions in one occurrence. SPEC-0022 records the additional
review states needed before production acceptance: multi-condition handoff,
non-30-day presets, cold-start readiness, administrative closure, evaluation gaps
and real concurrency/authorization failures. Notes, assignment, destinations and
acknowledgement timeout are deferred.

The mock reuses the retained shell/form/chart styling. Production must use shared
controls/charts from the [design system](../../../../web/DESIGN_SYSTEM.md), real
canonical evidence and backend validation. The mock's 1,000× threshold limit is
specific to its 99.9% fixtures; it is not a general product rule.

## Focused verification

Run in the repository development container:

```sh
make shell CMD='node docs/design/mocks/alert-options/verify.cjs'
```

The checked-in scripts exercise list filters/grouping/acknowledgement, detail
links/chart inspection/calculation values, and creation/inline overrides in both
themes at 1440/1024/360 px. Generated review screenshots go to the ignored
`.local/alert-mock-review/` directory. Retained PNGs above are selected reference
artifacts, not an assertion that every production state is implemented.

Publication verification and durations are recorded in [F4-T01](../../../tasks/F4-T01.md).
Application suites and engine benchmarks are outside this static mock check.
