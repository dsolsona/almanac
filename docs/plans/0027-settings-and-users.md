# Settings and user administration implementation plan

Implement inline with the primary agent under `superpowers:executing-plans`.

**Goal:** deliver real account settings and local-user lifecycle management with
pixel parity to the approved [HTML reference](../design/mocks/settings/README.md).
**Architecture:** existing authentication boundary, PostgreSQL transactions and
generated API types; shared React controls and current application shell.
**Tech stack:** repository-pinned Go/PostgreSQL/React/TypeScript/Docker, no new dependency.
**Spec:** SPEC-0004, SPEC-0010, SPEC-0012 and ADR-0035.

## Constraints and review focus

- Preserve existing username access, current records, credentials and operator preview.
- One primary agent; commit/push/PR publication authorized, merge excluded. Worktree `settings-users`,
  branch `feat/settings-users`, base `dfc6b3a`.
- Ordinary/restricted/expired/revoked sessions must retain their existing boundaries.
- Concurrent demotion/disable/delete/reset cannot remove the last usable local admin.
- Duplicate/colliding email, stale writes and uncertain creation cannot overwrite or
  expose another identity or replay one-time secrets.
- Setup/reset expiry, purpose, installation epoch and revision are checked at commit;
  successful password change revokes prior challenges.
- Verify actual pages at 1440/360 px in both themes against the reference, including
  name/email columns, pill geometry, account role, footer actions and theme placement.

## Task 1: Identity lifecycle and versioned API

Files: migration `0042_user_administration.sql`, `internal/storage/query/users.sql`,
`internal/storage/users.go`, auth query/service additions, `internal/userhttp/handler.go`,
`api/openapi.yaml`, generated Go/TS types and `internal/backendhttp/server.go`.

- [x] Write failing real-PostgreSQL lifecycle tests using `testpostgres.Database`:
  create pending user, issue setup once, activate, email login, update, disable/re-enable,
  reset/cancel/reset/redeem, archive; reject unauthorized/stale/replayed/expired cases.
- [x] Add name/email/version/last-sign-in/archive metadata, account challenges and
  bounded command receipts. Generate SQL via `make shell CMD='make generate GENERATE_MODE=sqlc'`.
- [x] Implement transactional actor/target authorization, last-admin checks and audit.
  Reuse password hashing, session proof and session-bound CSRF/origin/TLS admission.
- [x] Expose `/api/v1/account`, `/api/v1/users` and versioned user commands; public
  challenge redemption uses preauthentication CSRF plus transport/origin protections.
- [x] Generate API contracts with `make shell CMD='make generate GENERATE_MODE=api'`.
- [x] Run `make test` and `make test-integration`; investigate failures before reruns.

## Task 2: Production settings pages and CLI parity

Files: `web/src/settings/`, `web/src/auth/AuthPage.tsx`, `ApplicationShell.tsx`,
theme control, shared neutral role chip/catalogue, `internal/command/users.go` and help.

- [x] Write focused component/client tests for profile, list search/filter/paging,
  explicit role selection, validation, commands, one-time output and unknown outcomes.
- [x] Build the approved account/list/create/edit/password/redemption pages with real
  API responses; no fixture values or prototype review controls enter production.
- [x] Preserve non-secret drafts after errors; never persist passwords/challenge secrets.
  Use real name links and selected-text-safe row navigation. Move theme selection to
  the account tab while initializing the persisted preference on all application routes.
- [x] Add noninteractive CLI operations through the same API/session profile helpers,
  protected one-time output and explicit version/idempotency inputs.
- [x] Update the shared catalogue for actual new control states and regenerate contracts.

## Task 3: Integrated and visual acceptance

- [x] Extend `web/e2e/` with account/user journeys through real HTTP/PostgreSQL.
- [x] Run `make check` and `make test-e2e`; preserve original failures and actual durations.
- [x] Compare reference/production screenshots in both themes at desktop/mobile, then
  exercise normal entry, back/forward, keyboard/focus and failure correction.
- [x] Review the full diff and security boundaries. Update task/status with exact evidence
  and gaps; do not claim completion or pixel parity from screenshots alone.
