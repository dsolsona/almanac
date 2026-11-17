# Alert UI correction evidence, 2026-11-17

These are browser captures of the actual application routes with fictional API
fixtures, not mockup HTML. Before captures use product source at `9f54acd`; first-pass captures use the hashes in [source.json](source.json). Final refined
captures use [refined/source.json](refined/source.json). Desktop is 1440 px,
mobile is 360 px. Both themes are included. The isolated deployed save journey is
recorded separately below.

## Policies table

The name header now aligns with its linked row name; secondary columns are centred,
status uses the shared chip, and the pager is inside the resource panel. Mobile
uses labelled stacked rows instead of a desktop table requiring sideways scrolling.

| Before | After |
| --- | --- |
| ![Policies, desktop light, before](before/policies-desktop-light.png) | ![Policies, desktop light, after](refined/policies-desktop-light.png) |
| ![Policies, mobile dark, before](before/policies-mobile-dark.png) | ![Policies, mobile dark, after](refined/policies-mobile-dark.png) |

The existing Services page is the reference, captured with fictional fixtures:
[desktop light](after/services-reference-desktop-light.png),
[desktop dark](after/services-reference-desktop-dark.png),
[mobile light](after/services-reference-mobile-light.png),
[mobile dark](after/services-reference-mobile-dark.png).
A browser comparison checks equal font size, line height and first-cell inset in
all six viewport/theme combinations, including 1024 px intermediate layouts.

## Alert navigation

Active/History is now a toolbar choice below the title. Policies is beside the
page actions. Browser checks exercise arrow-key selection and Back navigation.

| Before | After |
| --- | --- |
| ![Alerts, desktop light, before](before/alerts-desktop-light.png) | ![Alerts, desktop light, after](refined/alerts-desktop-light.png) |

## Alert details

The compact status line shows lifecycle and duration. Burn values and the chart
carry the explanation without a duplicate paragraph. One expansion shows calculations
and original evidence; only technical revisions remain further collapsed. History
and supporting sections use quiet dividers and begin collapsed.
Acknowledgement shows its actor/time and does not claim assignment. Sampling limits
are explained beside the chart without a page-level technical warning.

| Before | After |
| --- | --- |
| ![Detail, desktop dark, before](before/detail-desktop-dark.png) | ![Detail, desktop dark, after](refined/detail-desktop-dark.png) |
| ![Detail, mobile light, before](before/detail-mobile-light.png) | ![Detail, mobile light, after](refined/detail-mobile-light.png) |

[Expanded calculations, light](refined/details-expanded-desktop-light.png) ·
[Expanded calculations, dark](refined/details-expanded-desktop-dark.png) ·
[Acknowledged alert with reduced chart detail](refined/sampled-acknowledged.png).

The paired captures also include each page in desktop light/dark and mobile
light/dark in the `before/` and `after/` directories. They preserve all screenshots
without image editing. First-pass images remain in `after/`; final images are in `refined/`. Fixtures contain no real operational data or credentials.

## Verification

Results and exact commands are recorded in [F4-T52](../../../tasks/F4-T52.md).
The route checks verify successful save navigation, uncertain-response reconciliation,
conflicting updates, viewer access, navigation history, responsive table geometry,
reduced chart detail and acknowledgement. Catalogue checks cover exact original/latest
values, disclosures, missing observations, keyboard/touch inspection and focus.
The normal Docker journey passes both onboarding and policy management (7.1 s
Playwright duration; about 3 min 40 s including fresh image builds, provider pulls,
startup and cleanup). Create and edit both return to the list; persisted values,
metadata-only revision behavior and disable/enable survive subsequent reads/reload.
The isolated stack is removed after the run. The deployed application image is
`sha256:be9aec3602ff1a039606633be81a6eeef21bb4ec6487e49905098964163bfa4c`.

![Saved policy in the normal isolated deployment](after/deployed-policy-created.png)

The deployed screenshot contains disposable test records. Detail, acknowledgement
and theme/mobile captures above use fictional API fixtures; this focused deployed
journey verifies policy management, not a new live firing/recovery run. Capacity,
performance and broad application suites were not run for this UI correction.

## Refined save feedback

The shared toast leaves the list toolbar in exactly the same position before and
after saving. It can be dismissed by keyboard; hover or focus pauses its five-second
timer. A controlled regression verifies that a later toast still dismisses after
keyboard dismissal of the previous one.

![Save feedback outside the page layout](refined/save-toast.png)


The refined normal-deployment journey also passes: both onboarding and policy
management complete in 6.6 s (about 1 min 45 s including rebuild/startup/cleanup).
This verifies the toast on an actual persisted save, alongside create/edit return
to list and disable/enable/reload.

![Refined save feedback in the isolated normal deployment](refined/deployed-policy-created.png)

Refinement verification: 19 focused unit tests (3.62 s), 21 route/design browser
cases (1.0 min), 11 detail interaction cases (37.3 s), and the six-theme Services
comparison plus fixed-toolbar check (seven cases, 19.2 s) pass. Strict TypeScript,
production build (719 ms), production exclusions and documentation checks pass.
All six additional toast reading-time browser cases complete in 16.4 s, but their
host make process exits 143; this is preserved as a host-command caveat rather than
reported as a successful command. The controlled dismissal/timing unit cases and
normal deployed flow exit zero; fresh PR CI owns the complete browser gate.
