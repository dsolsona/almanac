# ADR-0017: Recoverable secret encryption

- Status: accepted
- Created: 2026-10-26
- Accepted: 2026-10-26
- Approval: Maintainer explicitly approved the Tink proposal in the working conversation.
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md),
  [SPEC-0013](../specs/0013-runtime-and-operations.md)
- Supersedes / superseded by: none; resolves REVIEW-I06 in
  [ADR-0004](0004-identity-and-access.md)

## Context

Datasource passwords must be recoverable for authorized execution but unavailable
through ordinary management reads. SPEC-0004 already requires authenticated
encryption, keys outside PostgreSQL, resource/field binding, retained old keys during
resumable re-encryption, and separately protected backup keys. Library selection and
ciphertext format were explicitly deferred. This decision resolves that choice;
it does not implement datasource management or change bearer-token verifiers.

## Decision

Use **Tink Go v2.8.0**, module `github.com/tink-crypto/tink-go/v2`, using its
high-level AEAD and AES256GCM template with TINK output prefixes. The official setup
guide identifies this release; its Go1.25 minimum is compatible with Almanac's pinned
Go1.27.3. Review the resolved dependency graph and licenses when adding the pin.
[Setup](https://developers.google.com/tink/setup/go),
[tagged module](https://raw.githubusercontent.com/tink-crypto/tink-go/v2.8.0/go.mod).

Use a small internal adapter with explicit encrypt/decrypt calls at application
boundaries. Tink keysets provide a primary encryption key and retained decryption
keys; Almanac still owns authorization, persistence, redaction and re-encryption.
Do not use low-level cryptographic primitives or add a cloud KMS dependency.
[Keyset design](https://developers.google.com/tink/design/keysets).

### Stored format and binding

Store format version1, keyset UUID, unsigned32-bit key ID and complete Tink ciphertext
bytes. Keep Tink's output intact; do not split out or generate the nonce. The output
prefix is a selection hint, not authenticated authority. Validate its consistency
with the envelope, then require successful authenticated decryption.
[Wire format](https://developers.google.com/tink/wire-format).

Associated data is a fixed-order JSON array containing domain `almanac.secret`,
format version, installation UUID, resource kind, resource UUID, field identifier,
secret revision, keyset UUID and key ID. Use canonical identifiers and dedicated
secret revisions, independent of display names and unrelated configuration edits.
Derive the expected resource/field/revision from trusted application state. Bind
header identifiers into associated data; never trust a copied envelope as the
source of its own authorization context. Initially bound each plaintext field to
16KiB and the envelope to20KiB; absent and explicitly empty remain distinct.

Re-encryption preserves secret revision; replacing a secret increments it. Do not
bind to credential_epoch: restore invalidates bearer authority without destroying
recoverable source passwords. Legitimate restore preserves installation identity;
moving secrets to another installation requires explicit authorized re-encryption.
This prevents ciphertext substitution, not rollback by a database administrator
who can also replace authoritative revision metadata.

### Keys and operations

Keep the complete keyset in a protected mounted deployment-secret file outside
PostgreSQL. Configuration supplies only its path. Load once at startup, bounded to
16KiB, with regular-file and owner-only permission checks. Require exactly one
enabled primary, unique IDs, at most16 keys, and AES256GCM/TINK keys only. Limit
Tink's cleartext-keyset import/export API to this loader and offline provisioning.
The file is plaintext key material protected by the deployment secret boundary;
encrypting it under an equally accessible colocated file is not required.
[Cleartext API](https://raw.githubusercontent.com/tink-crypto/tink-go/v2.8.0/insecurecleartextkeyset/insecurecleartextkeyset.go).

Provision explicitly once with exclusive creation and0600 permissions; never
regenerate or replace keys during ordinary startup. Never print keys or include
them in management responses, credential-display commands or exports. Agents
receive only assigned source credentials over authorized TLS, never the keyset.
Missing, malformed or incomplete keys fail secret-dependent operations closed;
there is no plaintext fallback. Auth-only deployments may leave this capability
unconfigured until they use recoverable secrets.

Rotation adds a new key while preserving old IDs/material, distributes the expanded
keyset, then promotes the new primary through explicit deployment restart. Retain
old enabled keys until a resumable bounded re-encryption job and verification finish.
Use compare-and-swap on secret revision and original envelope so concurrent secret
replacement cannot be overwritten. Record redacted operation audits and persist
progress. Do not remove old keys until live references and retained backup recovery
requirements have been checked. Back up keysets separately from database backups.
Restore remains isolated under ADR-0004 and promotes a fresh primary before new
encryption; restoring old accounting must not renew an old key's usage allowance.

Random nonces have a probabilistic collision bound, not an absolute uniqueness
guarantee. Adopt a conservative ceiling below2^32 encryption attempts per key,
including re-encryption and failed/retried writes, and rotate before reaching it.
This is a real cryptographic usage constraint. An arbitrary one-million limit and
a bespoke durable reservation-counter subsystem are **not** day-one requirements.
Document and validate the secret-write workload and operational rotation budget;
add stronger accounting if that workload cannot be conservatively bounded.
[Go AEAD usage guidance](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce).

## Alternatives considered

**Go1.27 AES plus NewGCMWithRandomNonce** supplies authenticated encryption and nonce
generation without another module. It is a credible smaller-dependency alternative,
but leaves keyring validation, identifiers, primary selection and rotation dispatch
to Almanac. Tink better matches the already-required multi-key lifecycle.

**Encrypted keysets backed by an external KMS** provide a distinct protection and
operations boundary but add infrastructure not required for the initial deployment.
They remain a later option without changing resource/field binding.

## Consequences

Add Tink/protobuf maintenance and a protected keyset lifecycle. Library choice does
not automatically encrypt database fields or authorize decryption. Ordinary reads
must never decrypt; assigned execution checks current authority before release.
Source credential replacement is still necessary after compromise: key rotation
cannot make an agent forget plaintext it already received.

First implement and test the adapter/loader, then actual datasource persistence and
authorized release with real PostgreSQL evidence. Test tampering and cross-context
substitution, missing keys, mixed generations, resumable concurrent-safe re-encryption,
redacted reads and backup/key restore. No source credential feature is complete
until those integration boundaries exist. Runtime readiness and operator commands
must be specified before exposure. Acceptance settles the encryption choice;
implementation still follows bounded tasks and the applicable verification gates.

## Revisit conditions

Reconsider for external secret-manager requirements, compliance constraints,
substantially higher encryption volume, multi-backend key rollout, dependency
security issues, or evidence that simpler standard-library key management has lower
operational risk for the supported deployment.
