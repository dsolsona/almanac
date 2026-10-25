# Persistent local sessions implementation plan

> Use one isolated Luna implementer with coordinator review, acceptance and stacked
> PR publication. Follow subagent-driven development; never merge the PR.

Goal: persist session verifiers with current identity checks, bounded lifetimes and
atomic issuance/logout audit before application authentication is exposed.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md), AC-03/09/10.
Task: [F1-T09](../tasks/F1-T09.md).
Architecture: generated PostgreSQL queries, explicit immutable migration and
transactional installation→user→session locking; clock is an explicit input.

## Global constraints

Go1.27.3/PostgreSQL18.6. Only session digests persisted. Idle30minutes and
absolute12hours with exact cutoff expiry. Current user/revision/epoch checks;
old restricted sessions never gain privileges automatically. No hashing under
locks or public authentication routes; no new dependencies or approval requests.

## Review focus

- A password check racing revision/epoch changes must not mint a valid session.
- Idle touches cannot extend absolute lifetime or regress stored activity time.
- Lock ordering must remain compatible with later rotation and restore operations.
- Audit failure must roll back session issuance/deletion.
- Returned snapshots are not future mutation authority; routes remain unavailable.

## Task 1: Session persistence

- [x] Add schema3 and failing real-database acceptance cases in F1-T09.
- [x] Implement generated lookup/issue/authenticate/revoke operations and safe errors.
- [x] Verify expiry/revocation/concurrency/audit rollback and prior-schema upgrade.
- [x] Run focused/integration/race and combined gates; independently review.
- [ ] Record acceptance and open the next stacked PR.
