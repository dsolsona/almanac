# Browser authentication and onboarding implementation plan

> Use subagent-driven development with one isolated Luna implementer at a time.
> Coordinator owns interfaces, the real browser/database fixture, reviews and Git.

Goal: sign in, complete initial administrator password rotation and sign out through
the real combined HTTPS application, with truthful handling of uncertain responses.
Architecture: a typed same-origin browser client feeds a root-page state machine;
shared controls and design tokens supply the interface. HttpOnly cookies remain
browser-managed and CSRF remains in memory. No new framework or dependency.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md),
[SPEC-0010](../specs/0010-api-and-cli.md), ADR-0004/0009/0011/0016.

## Global constraints

- A restricted session cannot read application data or perform management work.
- Preserve the Almanac shared controls, typography, light/dark themes and focus.
- Never persist or log passwords, session secrets or CSRF tokens in browser storage.
- Lost authentication responses never trigger automatic mutation replay.
- Preserve catalogue production exclusion and unknown-path behavior.
- Real browser journeys use the actual HTTPS backend and disposable PostgreSQL;
  mock-based client/component tests are only focused feedback.
- Existing approved command families only; no new approval requests or broad wrappers.

## Review focus

- Partial/malformed success must not become an authenticated or completed state.
- Page reload with a restricted session must still allow deliberate password setup.
- Network failure after mutation must preserve uncertainty even if context is valid.
- Development catalogue and production authentication must remain separate builds.
- Browser evidence must avoid secret-bearing logs, traces and persistent profiles.

## Task 1: Browser authentication client

Exact interfaces, files and checks: [F1-T17](../tasks/F1-T17.md).

- [x] Write focused request/shape/fault/deadline tests, preserving first failures.
- [x] Implement bounded same-origin operations using generated types.
- [x] Verify focused and integrated checks and independently review the client.

## Task 2: Root-page sign-in, restricted setup and real browser acceptance

Finalize [F1-T18](../tasks/F1-T18.md) after the client interface is reviewed,
before dispatch. Reuse shared Button/TextField/Panel/ThemeToggle/LoadingIndicator.
The root shows loading, unavailable, sign-in, restricted password setup, or a minimal
signed-in identity view with logout. No fake management actions, Google SSO control,
new route library or styling system. The restricted form explicitly asks for current
password plus new password/confirmation so reload never depends on retained login
credentials. Existing catalogue and unknown paths remain separate.

- [x] Finalize the screen and real-browser fixture brief against the accepted specs.
- [x] Implement accessible states and uncertainty recovery with component tests.
- [x] Exercise the production binary/API/database in Chromium across themes and
  desktop/mobile layouts; inspect actual pages and preserve sanitized first failures.
- [x] Run integrated checks, independently review tasks and reconcile docs.
- [x] Complete whole-branch review and publish the browser increment stacked on
  the combined-runtime PR.

Published as [PR #14](https://github.com/dsolsona/almanac/pull/14), based on
[PR #13](https://github.com/dsolsona/almanac/pull/13). Independent whole-branch and
scoped editorial reviews approve the delivered increment.

Google SSO, recovery, user management, ordinary account settings, persisted CLI
profiles and secure standalone UI proxy integration remain separate increments.
