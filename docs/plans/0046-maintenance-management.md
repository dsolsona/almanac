# Maintenance management implementation plan

The explicit-SLO [API](../tasks/F4-T53.md), [CLI](../tasks/F4-T54.md),
[browser lifecycle](../tasks/F4-T57.md) and [timeline overlays](../tasks/F4-T56.md)
have merged.
The service-scoped increment merged on 2026-11-18 at `779c2e8`. It implements
[SPEC-0023](../specs/0023-maintenance-management.md) and
[ADR-0053](../adr/0053-service-maintenance-workflow.md), building on
[plan 0039](0039-maintenance-exclusion-authority.md) and the calculation and
permissions in [SPEC-0016](../specs/0016-maintenance-exclusions.md).

[F4-T62](../tasks/F4-T62.md) records implementation and verification.

## Dependencies

1. Define the selector and impact API, preserve the explicit-SLO compatibility
   contract, and agree on the versioned reads for effective membership,
   calculation readiness and alert suppression. Specify bounded publication,
   restart and the authority fence for cancellation and prospective early end.
2. Work in parallel once those interfaces are stable.
   - **Scope authority and calculations.** Persist the selector and authority
     revision, record prospective membership, and publish bounded pages to the
     calculation and timeline consumers. Exercise overlaps, moves, archival,
     cancellation, restart and stale publication.
   - **Alert suppression.** Implement and test the occurrence decision using
     the effective suppression read. Cover existing incidents, overlapping
     windows, stale authority, catch-up and the first eligible evaluation
     after maintenance. Connect the real authority at integration.
   - **Browser and CLI.** Implement the three scope choices and paginated
     impact reads against contract fixtures. Keep browser and CLI edits in
     separate worktrees if both are in progress, and integrate against the
     shared generated API.
3. Verify the combined workflow on the ordinary deployment. Check exact
   populations, scope and alert evidence, authorization and lifecycle races.
   Exercise both themes, mobile, keyboard use and the 50-service picker.
   Use focused local journeys and hosted fleet verification. Keep progress
   captures with the PR or CI artifacts.

The scope authority is the shared integration dependency. Keep occurrence
suppression and calculation publication behind the same effective authority,
and retain explicit-SLO compatibility in the integrated tests. External
notification delivery follows its own future implementation under SPEC-0021.
