# ADR-0042: Keyset pagination for the SLO overview

- Status: proposed
- Created: 2026-11-09
- Accepted: pending
- Approval: pending; this changes list semantics the maintainer clarified on 2026-11-01 and needs an explicit decision
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0010](../specs/0010-api-and-cli.md), [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: would replace the overview snapshot admission rules of SPEC-0019 ("Browser behavior", 2026-11-01 clarification)

## Context

The SLO overview pins each list view to a per-session snapshot so that later
pages never mix in rows that changed urgency after page one. That guarantee costs
four tables, an installation-wide advisory lock, a fingerprint computed over every
SLO on each request (up to three times per page load), HMAC cursors, byte budgets
and a two-minute lifetime. A page shows 10 or 20 rows.

## Decision

Proposal. Read the overview directly from `almanac_slo_current_projections`
ordered by urgency rank, lowercase name and id, filtered by query text and
service, with an opaque keyset cursor holding the last row's ordering key. Group
counts are one aggregate over the same filtered set, computed per request. Each
row's `current_state` is classified inline as the materialization query does
today. The `snapshot_id` response field is removed or made nullable during the
API contract review.

Semantics change: rows may move between pages while the user is paging, so a row
can appear on two pages or on none, and group counts reflect the moment of each
request. This is the ordinary behaviour of paginated operational lists.

## Alternatives considered

- Keep snapshots but drop the fingerprint reuse: keeps the stable-page guarantee
  at lower cost; still four tables and a lock. Reasonable fallback if the guarantee
  is wanted.
- Snapshot only when the filtered set exceeds one page: already implemented; the
  cost remains for every installation above 20 SLOs.

## Consequences

- Removes `almanac_slo_overview_snapshots`, `almanac_slo_overview_snapshot_items`,
  `almanac_slo_projection_payloads`, `almanac_slo_history_admission`, the admission
  lock, the fingerprint query and about 600 lines of storage code.
- List reads become one page query plus one count query, with no write.
- The "pinned coherent generation" wording for lists in SPEC-0019 is replaced by
  per-row generation stamps; detail exact-value pages keep their pin.

## Revisit conditions

Revisit if operators report confusion from rows shifting between pages, in which
case the first alternative restores the guarantee without the fingerprint.
