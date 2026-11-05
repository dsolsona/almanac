# Almanac design system

This guide records the shared visual foundation and the controls demonstrated in
the local `/design-system` development catalogue. The production root uses these
controls for local sign-in, restricted password setup and the signed-in application shell.

## Foundations

The bundled Bevan Regular font is used for the ALMANAC wordmark and display
headings. Its license is included at `public/fonts/Bevan-OFL.txt`. Body copy uses
the 14 px system sans stack; technical labels and controls use 14 px monospace,
with 12 px captions. Section headings are 24 px and the page title is capped at
44 px. Numeric measurements use tabular figures. No external font request is
required.

The source palette is warm paper `#f8f6ee`, navy ink `#183440`, vermilion
`#bd4426`, and teal `#246469`. Semantic tokens in `src/styles/tokens.css` map
these foundations to UI roles:

| Role | Token | Light value | Dark value |
| --- | --- | --- | --- |
| Paper source | `--almanac-paper` | `#f8f6ee` | unchanged |
| Ink source | `--almanac-ink` | `#183440` | unchanged |
| Vermilion source | `--almanac-vermilion` | `#bd4426` | unchanged |
| Teal source | `--almanac-teal` | `#246469` | unchanged |
| Page background | `--bg` | `#f8f6ee` | `#202725` |
| Main surface | `--surface` | `#fffdf6` | `#27302d` |
| Raised surface | `--surface-2` | `#eeeade` | `#323c37` |
| Highest surface | `--surface-3` | `#e0dacb` | `#455047` |
| Text | `--text` | `#183440` | `#f8f6ee` |
| Secondary text | `--text-2` | `#485353` | `#d0cdbf` |
| Muted text | `--text-3` | `#606761` | `#adb6a9` |
| Border | `--border` | `#c5cbc4` | `#465148` |
| Strong border | `--border-strong` | `#858e85` | `#778578` |
| Chart grid | `--grid` | `#e6e1d4` | `#3a443d` |
| Chart axis | `--axis` | `#a1a99e` | `#778578` |
| Chart crosshair | `--crosshair` | `#606761` | `#adb6a9` |
| Action | `--accent` | `#bd4426` | `#efab8f` |
| Action text | `--accent-ink` | `#99371f` | `#f5b59c` |
| Action wash | `--accent-wash` | `#f3e2d7` | `#4c3830` |
| Action foreground | `--primary-foreground` | `#ffffff` | `#202725` |
| Selected control | `--selection` | `#246469` | `#98c6bc` |
| Selected control text | `--selection-foreground` | `#ffffff` | `#202725` |
| Focus | `--focus-ring` | action color | action color |
| Healthy text | `--status-healthy` | `#31665e` | `#98c6bc` |
| Warning text | `--status-warning` | `#ae4b00` | `#e5c06e` |
| Breached text | `--status-breached` | `#a52b38` | `#f39da5` |
| No data / stale text | `--status-no-data`, `--status-stale` | `#606761` | `#b8bfb2` |
| Healthy line / wash | `--good`, `--good-wash` | `#416b68` / `#e5eee8` | `#98c6bc` / `#31473e` |
| Warning line / wash | `--warn`, `--warn-wash` | `#d66b00` / `#f5ecd1` | `#e5c06e` / `#4d442c` |
| Breached line / wash | `--crit`, `--crit-wash` | `#b53442` / `#f5e3e3` | `#f39da5` / `#503337` |
| Neutral line / wash | `--neutral`, `--neutral-wash` | `#7d847a` / `#e9e7dd` | `#b8bfb2` / `#3f473f` |
| Series 1 and 3 | `--s1`, `--s3` | `#246469` | `#98c6bc` |
| Series 2 | `--s2` | `#bd4426` | `#efab8f` |
| Elevation | `--shadow` | `0 4px 16px rgb(36 43 44 / 8%)` | `0 4px 16px rgb(0 0 0 / 25%)` |

Status text uses the `--status-*` foreground tokens with `--good-wash`,
`--warn-wash`, `--crit-wash` or `--neutral-wash`. Do not use the brand action
color to communicate a warning. Components consume tokens; feature styles own
composition only.

Spacing follows 4, 8, 12, 16, 20, 24, 32, 48 and 64 px (`--almanac-space-*` and
their `--space-*` semantic aliases; `--space-5` is the 20 px gap between form
section cards and below page heads, as in the approved mocks; `--space-12` and `--space-16` are 48 and
64 px). Controls are 38 px high on fine pointers and 44 px on coarse pointers.
Controls and panels use 3 px corners (`--radius-sm`); the general radius is 4
px. Focus is visible with a 3 px semantic outline. Motion uses `--motion` (120
ms, reduced to 0 ms by the preference rule). Dialogs use `--layer-dialog` (20)
and the shared `--dialog-scrim` token.

## Shared controls

Import controls from `src/components/ui/` and prefer native semantics:

- `Button` supports primary, selected, outline, ghost and danger variants. Use the
  teal selected variant with `aria-pressed` for active presets and view controls.
  It defaults to
  `type="button"`; pending actions are disabled and announced as busy.
- `TextField` and `SelectField` require an id and visible label; hints and errors
  are programmatically associated. Select options are native options for fixed
  small choices; pickers that need state context use the type-ahead `Combobox`
  below, under [ADR-0028](../docs/adr/0028-unified-operational-ui.md).
- `TextareaField` matches `TextField` for multi-line text, with an `optional` label suffix.
- `StatusBadge` pairs a readable status with a distinct mark for healthy, warning,
  breached, no-data and stale states.
- `Panel` is a semantic section with a heading and content, not a nested card.
- `Dialog` is controlled by `open` and `onClose`; native dialog behavior supports
  Escape and focus containment, with focus restoration through `triggerRef`.
- `ThemeToggle` honors a persisted choice, defaults to system preference, and
  tolerates unavailable storage.
- `Switch` uses a native checkbox with switch semantics, persistent label, hint,
  checked state text and visible thumb position.
- `DatePicker` validates `YYYY-MM-DD` calendar dates and opens a shared native
  dialog containing a Monday-first, keyboard-navigable month grid.
- `LoadingIndicator` announces an indeterminate status with a transform-only
  moving segment and a static reduced-motion state in the component catalogue.
  Product pages use stable content and accessible pending state instead of visible
  loading messages, bars or placeholder flashes. Keep existing records on same-view
  refreshes, prevent unsafe pending actions, and distinguish pending, empty and
  failed reads.
- `ApplicationShell` provides desktop navigation, a focus-contained mobile drawer,
  account controls and visibly unavailable planned destinations.
- `MeasurementChoiceCards` explains the supported event-count mode and keeps
  unsupported collection modes disabled with an availability reason.
- `QueryEditor` preserves a native labelled textarea with PromQL context and
  scroll-synchronized, screen-reader-hidden line numbers.
- `SliRecentStrip` plots the bounded interval states without assigning an SLO
  target classification. Its native details table exposes exact counts, interval
  timestamps and coverage labels from the same list projection.

Operational controls (ADR-0028), demonstrated in the catalogue's Operational section:

- `StatusMark` shows a shape, a label and an optional one-line mono reason. The
  shape carries the tone: filled circle healthy, triangle warning, square breached,
  open circle stale, dashed circle none. The label names the reason (Breached,
  Burning fast, Not reporting) and must never contradict the group containing it.
  Marks go on a status line under a heading, never inside it.
- `SegmentedControl` is a radiogroup of buttons for one choice among a few fixed
  options, with optional counts, disabled options carrying a title, roving tabindex
  and arrow keys.
- `ChoiceChips` shows presets as chips plus a dashed “Custom…” chip that reveals an
  input; the value applies as typed, the chip relabels itself with the formatted
  value, and the input collapses on Enter or blur. `validate` reports an error under
  the field; the owning form rejects invalid values on submit.
- `Combobox` is a labelled text input with a filtered listbox. Options carry a
  status mark, a secondary line and a disabled state; keyboard navigation uses
  arrows, Home/End, Enter and Escape; a no-match row can offer an action. The
  selection collapses to a `SelectionSummary` whose Change action returns focus to
  the input.
- `SelectionSummary` shows status, title, badge, detail, optional message and
  action; the warning variant uses the warning wash.
- `FormSection` and `FormActions` compose create and edit forms as numbered
  steps: a heading row with the step, the title, an optional control (such as the
  measurement kind) and a right-aligned mono hint, then a body that stacks fields
  and `ui-form-grid` two-column rows. `FormActions` closes the form with a mono
  note on the left and the buttons on the right. Forms use the full content width;
  a muted section marks a step that cannot be configured yet.
- `PageHeader`, `Toolbar`, `SearchField` and `Pager` compose every list: Bevan
  title with a mono summary and “Updated hh:mm:ss UTC” beside the actions; search
  with a `/` shortcut that does not fire while typing elsewhere; a pager that always
  shows position, per-page and previous/next.
- Hints, errors and sublines are 13 px minimum; 12 px is reserved for labels, axis
  ticks and captions. `--card-inset` (24 px, 14 px at or below 860 px) governs
  card headers, bodies, first/last table cells, pagers and lane labels.

## Catalogue and scope

Run `make setup` once to install locked dependencies and Chromium. `make preview`
serves the development catalogue at `http://127.0.0.1:5198/design-system`.
`make build` typechecks and creates production assets. `make verify` runs the
component suite, production build and production exclusion check. Set
`MODE=unit` to run focused component behavior checks, or
`MODE=browser` for Chromium checks at desktop, intermediate and 360 px
mobile widths in both themes. `TEST_FILE` can select one component test filename
with the unit mode.

The catalogue demonstrates actual shared control exports and local-only state,
including the signed-in application shell. It includes palette and type specimens,
disabled/pending/invalid/empty/loading/
error/long-content examples, switches, date selection, a working dialog, SLI
measurement/query controls, and a lazy charts section. Its local save example completes only when the explicit
finish control is pressed. It is imported only by the development build; examples
do not imply application data, authentication or backend operation.

## Timeline charts and exact values

Timeline charts use the shared wrappers in `src/components/charts/`. Each lane
shares the declared UTC range and step; missing samples and absent expected steps
break lines, while numeric partial samples use a distinct marker. Partial samples
with no numeric value remain gaps. Zero remains numeric.
Units, range, UTC timezone and coverage meaning are shown next to every plot.
Charts are SVG rendered, theme-aware, resize-aware and bounded to 1,500 plotted
points per series. Chart values are display inputs only; the browser does not
calculate SLOs, budgets or burn rates.

Operational lanes can display server-supplied target and burn reference lines and
share either UTC or local display time. Keep their controls and exact-value actions
available when chart loading, validation or rendering fails. Budget sparklines also
break at partial coverage; they must not connect across an unknown interval.

Pair every chart with `ExactValuesTable`, backed by the same sample objects. It
shows exact decimal strings and coverage in a keyboard-focusable scroll region,
with controlled pages of at most 50 rows. Keep missing, partial, zero-traffic,
known-empty, expired and not-applicable labels distinct in charts, tooltips and
tables. Known-empty, expired and not-applicable samples remain gaps, never numeric
zero. Synthetic catalogue fixtures must be explicitly labelled and
must never look like live service data.
