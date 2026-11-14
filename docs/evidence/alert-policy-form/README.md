# Alert policy form component evidence

[F4-T09](../../tasks/F4-T09.md) implements the selected compact creation form as
shared React controls and a development catalogue example. It has no production
route or persistence API. The [approved mocks](../../design/mocks/alert-options/README.md)
remain the composition reference; this is not full alert UI or engine acceptance.

The focused component selection passes 45 cases in 5.82 seconds, TypeScript
checking passes, and nine browser cases pass in 37.6 seconds. Four review findings
were reproduced before correction: zero-target validation, menu toggle, selected
SLO error focus and save-error summary focus. Earlier failures and the test host's
memory-pressure investigation remain recorded in the task.

| Render | Light | Dark |
| --- | --- | --- |
| Desktop, selected SLO and overridden condition | [1440 px](png/alert-policy-form-desktop-light.png) | [1440 px](png/alert-policy-form-desktop-dark.png) |
| Narrow form | [360 px](png/alert-policy-form-mobile-light.png) | [360 px](png/alert-policy-form-mobile-dark.png) |
| Touch, long SLO name, non-30-day confirmation, pending/error examples | [360 px](png/alert-policy-touch-light.png) | [360 px](png/alert-policy-touch-dark.png) |

The primary agent inspected desktop and narrow renders in both themes and the
touch error/pending examples. Component geometry and interactions run at
1440×1000, 1024×900 and 360×800. Only after those checks, screenshot capture expands
the viewport height at the same width to include the full form; this avoids an
implicit screenshot resize shifting earlier catalogue sections into the clip.
The eight capture-bearing cases pass again in 35.6 seconds after that correction.
Images contain local fictional drafts and the catalogue's explicit unsaved
feedback. They do not depict saved policies or real alert occurrences.

Keyboard and touch checks cover unchanged picker/editor height, editing units
without dropdowns, invalid correction, preset confirmation, menu interaction,
pending controls, no-match search and long selected labels. Real API integration,
production-shell navigation, multi-user authorization and engine evidence remain
later acceptance work. Manual screen-reader and non-Chromium device testing are
not claimed.
