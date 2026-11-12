# ADR-0050: A dedicated Firecracker Tern client key

- Status: accepted
- Created: 2026-11-12
- Accepted: 2026-11-12
- Approval: maintainer requested an automatically created, unencrypted SSH key used only for Firecracker/Tern, without unlocking a personal key or using ssh-agent.
- Related specs: optional development workflow in the [Firecracker guide](../firecracker.md)
- Supersedes / superseded by: supersedes the externally provisioned laptop client-credential requirement of [ADR-0048](0048-tern-firecracker-agent-terminals.md); its transport, host verification and guest lifecycle decisions remain unchanged.

## Context

A worker can start Codex successfully while laptop attachment fails because its
personal SSH key is locked. Failing earlier identifies that dependency but does
not deliver the requested single-command workflow. Tern 0.9.16 supports host
IdentityFile settings in the user's SSH configuration.

## Decision

On first `vm-run` or native `vm-codex`, create one unencrypted Ed25519 key in
`~/.ssh/almanac-firecracker/`, shared by this laptop's workers. Reuse it without
rotation, keep its directory private and its private file mode 0600, and refuse
to replace an unreadable or symlinked key. Only its public identity is authorized
by guest Tern listeners. Never copy the private key into a guest or a base image.

Install an owned SSH include for the reserved VM address range, selected before
general user rules, with IdentityFile pointing to the dedicated key and
IdentityAgent disabled. Preserve the user's existing SSH configuration and keep
a backup before adding the include. Serialize simultaneous first launches and
publish configuration atomically. Other host destinations retain their settings.

Existing native workers receive the public key during normal reconnect, without
replaying prompts or restarting Codex. Keep the shared identity and configuration
when deleting an individual worker; other workers still depend on them.

## Alternatives considered

- Unlock a personal SSH key: requires manual session setup unrelated to these VMs.
- Add worker keys to ssh-agent: keeps a desktop agent dependency and requires cleanup.

## Consequences

Ordinary launches and reconnects need neither a personal SSH key nor ssh-agent.
The laptop stores an unencrypted credential whose intended scope is these private
development workers. Initial setup modifies its SSH configuration once.

## Revisit conditions

Tern exposes a per-connection private-key option, or workers need separate laptop
client credentials.
