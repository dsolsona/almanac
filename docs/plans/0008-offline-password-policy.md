# Offline password-policy implementation plan

> Use one Luna implementer in the isolated worktree. Coordinator owns review,
> acceptance and stacked PR publication under subagent-driven development.

Goal: provide offline common-password screening before onboarding/password creation.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md), password-policy AC-03.
Task: [F1-T08](../tasks/F1-T08.md).
Architecture: immutable embedded, pinned source data with retained MIT license;
one shared read-only lookup; safe validation errors and no runtime network calls.

## Global constraints

Passwords remain unchanged, with15–128 Unicode code points and max512bytes.
Screen entire values, not substrings. No normalization, trimming or composition rule.
Existing verifier operations remain independent so policy updates cannot lock out
existing passwords. No login/startup/storage changes or new dependencies. No claim
of exhaustive compromised-password detection; no PR merge or new command approvals.

## Review focus

- Source/checksum/license must match the pinned data actually embedded.
- Invalid or unavailable policy data must fail closed, never become an empty list.
- Concurrent calls must not mutate shared lookup state.
- Existing-password verification must not begin applying creation policy.
- Error strings must not disclose submitted values or dataset contents.

## Task 1: Offline screening

- [x] Vendor pinned source/license and provenance with independent checksum evidence.
- [x] Add failing policy/boundary/concurrency/data-integrity cases from F1-T08.
- [x] Implement `ValidateNewPassword` with immutable embedded lookup.
- [x] Run focused/race gates, review diff and verify the integrated candidate.
- [x] Record acceptance; ready to publish against the initial-owner PR.
