# Maintenance management implementation plan

Deliver the scheduling, lifecycle and SLO evidence journeys reviewed in
[F4-T61](../tasks/F4-T61.md), building on the shared calculator and durable
per-SLO authority from [plan 0039](0039-maintenance-exclusion-authority.md).
[SPEC-0023](../specs/0023-maintenance-management.md) and
[ADR-0053](../adr/0053-service-maintenance-workflow.md) record the proposed
service scope and delivery behavior. The accepted exclusion arithmetic and
historical permissions come from [SPEC-0016](../specs/0016-maintenance-exclusions.md).

1. **Shared picker and visual reference.** Add the search-first Combobox
   presentation, retain the inline dependency presentation, and exercise both
   the catalogue and the 50-service prototype. Keep screenshot evidence with
   the design. This is the first PR.
2. **Schedule and inspect explicit SLO maintenance.** Extend the existing
   `internal/storage/maintenance.go` authority with bounded list/detail reads
   and the public command paths under the accepted explicit-SLO contract.
   Define the API in `api/openapi.yaml` and regenerate the clients. Exercise
   future scheduling, stale revisions, replay, cancellation, prospective end,
   administrator corrections and SLO overlay overlap queries against PostgreSQL.
   Connect the reviewed UI and CLI and verify the authenticated journey.
   Describe calculation exclusions and their evidence in stage 2. Reserve
   notification-pause copy and controls for stage 3, when delivery suppression
   is available.
3. **Service scope and notification suppression.** Settle membership timing,
   composite coverage, large-scope publication and alert resume semantics in
   the spec and ADR before implementing those paths. The current alert engine
   exposes in-Almanac alert occurrences. External delivery is deferred under
   SPEC-0021, so acceptance must identify the actual suppressed surface and
   verify that existing occurrences remain inspectable. Exercise overlaps,
   membership changes, retries, stale publication and end-of-window behavior.
4. **Verify the integrated workflow.** Seed independently calculated inputs
   and check raw and adjusted values, tooltip evidence and affected scope.
   Verify both themes, mobile, keyboard, authorization, real container
   deployment and the normal SLO performance gate. Use the screenshot evidence
   to compare hierarchy and interactions with the reviewed prototype.

Stages 2 and 3 need task records with the exact API, storage and verification
boundaries before rollout. Keep accepted explicit-SLO behavior distinct from
the pending service-membership and alert-delivery decisions.
