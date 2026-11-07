# Service catalog implementation plan

Implement inline with the primary agent under `superpowers:executing-plans`.

**Goal:** deliver the approved service catalog with pixel parity and real persistence.
**Architecture:** extend existing service storage/API/CLI; shared React controls and SVG graphs.
**Tech stack:** pinned Go/PostgreSQL/React/TypeScript/Docker; no additional dependency.
**Spec:** SPEC-0005, SPEC-0010, SPEC-0012 and [ADR-0036](../adr/0036-service-catalog.md).

## Constraints and review focus

- Worktree `.worktrees/service-catalog`, branch `feat/service-catalog`, base `61a5e5c`.
- Implementation verified locally; maintainer subsequently authorized commit, push and PR publication. No merge or deployment.
- Preserve operator data, unrelated work and all existing UUID/SLO references.
- Protect concurrent archive/reference creation and stale mutation preconditions.
- Missing/stale/partial observations never produce an On track service.
- Cycles, long labels/names, empty services and failed reads remain usable.
- Compare actual screens directly to the retained HTML at matching sizes/themes.

## Task 1: Persist and expose service catalog data

Files: migration `0043_service_catalog.sql`, service queries/storage, existing SLO HTTP
handler/client and CLI, `api/openapi.yaml` and generated Go/TS contracts.

- [x] Write validation and real-PostgreSQL lifecycle tests: enriched create/replay,
  rename with stable references, partial patch retention, cycles, archive blockers,
  role denial, conflicts, invalid links/labels and concurrent reference creation.
- [x] Add preserving metadata/archive/edge storage and generate SQL.
- [x] Extend existing service routes with filtered paged reads, summaries and safe writes.
- [x] Extend noninteractive service commands and regenerate API types.
- [x] Run `make test` and `make test-integration`; retain and diagnose failures.

## Task 2: Copy the approved UI into production controls

Files: `web/src/services/`, shell/auth/routes, shared label/picker controls and catalogue,
SLI/SLO service-selection integration and component/client tests.

- [x] Write focused cases for status precedence, search/filter/paging, read failures,
  create/edit chips and links, conflict/uncertain-save drafts and delete safeguards.
- [x] Build catalog, new/edit, details, dependency graph modes and service selection.
- [x] Copy reference markup, spacing, tokens and typography; preserve native links,
  selected-text-safe rows, keyboard focus, both themes and mobile layouts.
- [x] Keep prototype controls and sample values outside production.

## Task 3: Integrated and visual acceptance

Files: `web/e2e/services.spec.ts`, `web/browser/services-visual.spec.ts` and task evidence.

- [x] Exercise real UI/API/PostgreSQL CRUD, restart, role denial, dependency navigation,
  stable UUID rename, SLO creation context and blocked/allowed archival.
- [x] Compare matching actual/reference fixtures at 1440/1024/390 px in both themes,
  including list, details, forms, labels, graphs and keyboard selection.
- [x] Run `make check` and `make test-e2e`; inspect exact values, accessibility and
  authenticated navigation timing without interfering with the operator preview.
- [x] Record actual checks, durations, failures and any remaining limitations.
