# ADR-0035: Settings and local user administration

- Status: accepted
- Created: 2026-11-06
- Accepted: 2026-11-06
- Approval: The maintainer approved the settings mocks and explicitly requested implementation in a new worktree with pixel parity. Email sign-in was explicitly selected during the design review.
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md), [SPEC-0010](../specs/0010-api-and-cli.md), [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: amends ADR-0004's human login identifier and ADR-0032's spare-width rule for the users table only

## Context

Local authentication and deployment-wide roles exist, but user lifecycle management
and account settings do not. The maintainer reviewed and approved
[the interactive reference](../design/mocks/settings/README.md).

## Decision

Implement the approved Settings tabs, account form, theme placement, user list,
full-name/email/role forms and account-access commands. Copy reference geometry,
tokens and typography using shared React controls. Users have separate Name/Email
columns and natural table sizing; operational tables retain ADR-0032. Use one
full-name field. API keys, Google SSO, preferred names and name-part parsing remain
outside this increment.

New users sign in by case-insensitive email. Preserve existing login identifiers
as compatibility aliases so upgrading cannot lock out the initial administrator.
Existing accounts receive their current login as the initial display name; no
email address is invented. An administrator can assign their real email in Users.
Reject email assignments that collide with another user's email or legacy login.
After assignment, changing email replaces the email sign-in identity; the original
legacy alias remains for pre-existing accounts only.

Deletion archives the identity, disables access and preserves audit attribution;
archived addresses remain reserved. Disablement, deletion, role/email changes and
password reset invalidate existing human sessions and outstanding account challenges
as applicable. Other machine identities retain their independent ownership.

Activation/reset links carry random opaque secrets in URL fragments, never queries,
logs or durable browser storage. Store only purpose-bound verifiers, installation
epoch, expiry and credential revision. Setup lasts 24 hours; reset lasts 15 minutes.
Issuance is one-time output after commit; an idempotent replay returns metadata only.
Reset suspends password sign-in until redemption or explicit cancellation. Password
change and redemption invalidate all prior challenges. No automatic email is sent.

Serialize user lifecycle writes before locking actors/targets, recheck current
session authority transactionally, and protect the last enabled administrator with
a usable local password. Pending/reset-suspended administrators do not count.
Versioned mutations reject stale writes. Every successful mutation has an atomic
audit entry. Last sign-in records successful authentication, never activity touches
or password rotation. Theme stays a local browser preference, independent of Save.

## Alternatives considered

- Keeping username-only sign-in would reject the maintainer's selected direction.
- Replacing existing usernames without a compatibility path would lock out existing
  operators whose email has never been collected.
- Hard-deleting users would lose identity references and audit attribution.
- First/middle/surname fields impose unnecessary structure on personal names.

## Consequences

The application adds versioned user/account contracts, a forward-only migration,
UI/CLI parity, activation/reset redemption and real-stack acceptance coverage.
Existing deployments keep their identities and credentials. Email delivery and
general API-key activity remain future work. The approved HTML remains the visual
reference; its sample-data behavior is not a production implementation.

## Revisit conditions

Revisit legacy aliases when a deliberate migration/removal policy is accepted,
or identity recovery, email verification, SSO or API keys add new contracts.
