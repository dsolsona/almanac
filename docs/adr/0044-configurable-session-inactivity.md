# ADR-0044: Configurable session inactivity

- Status: accepted
- Created: 2026-11-10
- Accepted: 2026-11-10
- Approval: The maintainer requested an in-app setting with an eight-hour default.
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md)
- Supersedes / superseded by: amends the idle timeout in [ADR-0004](0004-identity-and-access.md)

## Context

The fixed 30-minute idle timeout interrupts work between visits to Almanac.

## Decision

Expose the installation's session inactivity timeout under Settings → Sessions.
Only an unrestricted administrator can read or change it. Store the policy
in PostgreSQL, default to eight hours, and accept whole minutes from 1 to 720.
Use version-checked writes with an atomic audit event.

Copy the current timeout into each newly issued or rotated human session.
Existing sessions keep their timeout, including the original 30 minutes for
sessions carried through the schema upgrade. The absolute lifetime is 12 hours.

## Alternatives considered

Applying a longer timeout retroactively could restore access to a session
that had already expired under its original policy.

## Consequences

Signing in again applies the current policy. Browser and CLI sessions use the
same rules. Access revocation and the browser cookie's absolute expiry continue
to follow the existing identity contract.
