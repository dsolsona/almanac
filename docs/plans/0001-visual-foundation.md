# Visual foundation implementation plan

> **For agentic workers:** Use the subagent-driven-development workflow task by task.

**Goal:** Deliver the first runnable Almanac component catalogue with exact shared
visual tokens, interactive controls, and fast component/browser verification.

**Architecture:** React renders shared UI components; Vite serves a local-only
catalogue. Production output includes the shared foundations and a minimal neutral
Almanac shell, but no catalogue route, examples or fixtures. No backend behavior is
simulated as working product functionality.

**Tech stack:** React, TypeScript, Vite; Vitest for component behavior and Playwright
for real browser checks. Dependency versions are registry-verified and pinned in the
lockfile. These are build/test adapters under ADR-0011, not production services.

**Spec:** [SPEC-0012](../specs/0012-user-interface.md) AC-01/02/07 and catalogue portion
of AC-09; [ADR-0011](../adr/0011-ui-design-and-tooling.md).

## Global constraints

- Catalogue uses real shared exports and is excluded from production builds.
- No feature screens before shared foundations. No fabricated API/DB/auth behavior.
- Paper #f8f6ee; ink #183440; vermilion #bd4426; teal #246469. Both themes.
- Bevan Regular only, locally bundled with SIL OFL; no synthetic bold or remote font.
- Shared UI in web/src/components/ui/; charts later in web/src/components/charts/.
- No private project names, paths or investigation narratives in repository content.
- Use make setup/build/verify/preview for execution; do not create generic shell wrappers.
- One Luna implementer, isolated worktree; coordinator integrates and verifies.

## Review focus

- A production build must contain no catalogue code, route or fixture, including lazy chunks.
- Theme survives reload; unavailable browser storage cannot crash rendering.
- Disabled controls cannot activate; field error text must be associated with its input.
- Dialog keyboard focus is contained, Escape closes, and focus returns to its trigger.
- At 360 px neither theme causes page-wide overflow; keyboard focus remains visible.

## Task 1: Runnable shared foundations (F1-T01)

Read [the complete task brief](../tasks/F1-T01.md), including exact palette, interfaces,
allowed files and tests. That file is the implementation requirements; keep its
acceptance mapping and verification evidence there instead of duplicating it here.

- [x] Implement meaningful red/green component tests and the specified shared controls.
- [x] Run fixed Make targets for install, production build, component and browser checks.
- [x] Inspect rendered light/dark/mobile pages and generated production output.
- [x] Coordinator reviews, integrates and records evidence; no claim that F1 is complete.

## Subsequent F1 work

After this task, prepare a separate bounded task for chart wrappers and exact tables,
then Go runtime/API/PostgreSQL/bootstrap flows and their integration harness. These
are not part of this plan; they need their own interfaces and readiness checks.
