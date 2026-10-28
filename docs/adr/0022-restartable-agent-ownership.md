# ADR-0022: Replace agent processes without an ownership-expiry wait

- Status: accepted
- Created: 2026-10-28
- Accepted: 2026-10-28
- Approval: The maintainer explicitly required immediate replacement after agent crashes or restarts, rejecting the existing one-hour wait.
- Related specs: [SPEC-0002](../specs/0002-agent-behavior.md), [SPEC-0004](../specs/0004-identity-and-access.md), [SPEC-0009](../specs/0009-agent-protocol-and-ownership.md)
- Supersedes / superseded by: supersedes the process-replacement expiry/administrator-takeover requirement in [ADR-0012](0012-agent-leases-and-protocol.md) and [ADR-0004](0004-identity-and-access.md); offline expiry and datasource reassignment policy remain unchanged

## Context

A stopped or crashed process left an execution lease valid for up to one hour.
Its replacement had a different in-memory resume proof and could not connect.
This made ordinary agent upgrades and crash recovery impractical. A transport
disconnect cannot prove that a remote process has stopped querying.

## Decision

A compatible cold start authenticated by a currently valid agent credential and
a fresh process resume proof immediately acquires a new ownership generation.
It replaces the previous process even if the old execution lease has not expired.
The same ordinary start command handles initial start, clean restart and crash recovery.
There is no manual database repair, credential rotation, or administrator action.

Acquisition is serialized per identity in PostgreSQL. A replay with the current
proof returns the same generation while valid. A proof belonging to any superseded
generation is terminally rejected, including a delayed initial hello whose welcome
was lost. Concurrent fresh starts serialize; only the latest committed generation
can renew, resume, upload diagnostics or save observations. Generation allocation
and retained ownership records provide durable replacement evidence.

The backend closes superseded connected sessions with a terminal replacement reason.
Session publication and routing are monotonic by generation, including out-of-order
handshake completion. A replaced agent cancels its work and exits; it must never
reset its generation or create a fresh proof to reclaim ownership automatically.
Both handshake and established-session readers classify this terminal condition.
The executable reports intentional replacement and exits successfully, rather than
triggering restart-on-failure policies. Invalid credentials and protocol failures
remain unsuccessful starts. Always-restart supervisors still require one desired replica.

The one-hour offline lease continues to support collection during backend outages.
An unreachable superseded process cannot be stopped remotely and may still query
until that lease expires. Its results are fenced immediately at replacement commit,
including buffered observations and repeated receipts. Previously accepted history
is preserved, and its persisted attempt cursor prevents deliberate re-collection.
Possession of a valid shared agent credential now authorizes replacing its process;
deploy only one desired replica per agent identity. This is not proof that both
processes are the same instance.

Existing protocol envelopes and generation/resume fields carry this server policy;
unknown protocol versions cannot acquire or replace ownership. Revoked, expired or
disabled credentials remain denied. Datasource handoff, revision validity, canonical
windows, source delay, and historical grant retention are unchanged. No graceful
release extension or administrator takeover UI is needed for ordinary restart.

## Alternatives considered

- Wait for lease expiry: rejected because it blocks routine crash recovery.
- Release only on graceful stop: insufficient for crashes and unconfirmed shutdown.
- Persist resume proof: unsafe under copied disks or overlapping replacement pods.
- Shorten all offline leases: reduces outage tolerance and still leaves a restart wait.
- Require a manual takeover flag or administrator action: unnecessary friction for
  starting the replacement the operator has already chosen to run.

## Consequences

Restarts recover without waiting for the former process. Accepted data remains
single-owner, but exclusive external query execution cannot be guaranteed across a
partition. A mistakenly duplicated deployment can replace its peer; fenced processes
exit rather than repeatedly taking ownership back. Credential protection remains
essential. Old clients still stop after their fenced resume is rejected; updated
clients report replacement directly on the connected session.

## Revisit conditions

Revisit automatic replacement if multiple desired replicas per identity or active
backend replicas are introduced. Do not infer remote process death from reachability.
