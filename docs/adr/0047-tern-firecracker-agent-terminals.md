# ADR-0047: Tern terminals for Firecracker agents

- Status: accepted
- Created: 2026-11-11
- Accepted: 2026-11-11
- Approval: maintainer requested that make vm-run boot its worker, start Codex directly inside Stencil Tern and add the remote host to the laptop's Tern.
- Related specs: optional development workflow in the [Firecracker guide](../firecracker.md)
- Supersedes / superseded by: none; extends [ADR-0045](0045-prepared-firecracker-development-workers.md)

## Context

The worker launcher already owns boot, independent checkouts and Codex/GitHub
authentication. Its tmux terminal requires separate attachment. Stencil Tern
0.9.16 runs in the Ubuntu guest, and a live trial verified laptop attachment.
Local Tern Lua can add and select hosts without a model request. The maintainer
selected an integrated native Tern workflow after evaluating manual attachment.

## Decision

Make `vm-run` launch new Codex agents directly in a guest Tern session and connect
the local Tern window after boot. Preserve names, inline/stdin prompts, independent
CoW disks, authentication, Docker isolation and the refusal to overwrite an
existing agent's task. `vm-codex` reconnects without submitting another task.
Existing tmux workers remain attachable; do not terminate or silently migrate
their running processes.

The maintainer clarified that plain `make vm-run` must work without a state
environment variable or checkout argument. Resolve the default VM state through
Git's primary worktree and share its `.local/firecracker/` across linked worktrees.
Keep the invoking checkout as the source snapshot. Retain the existing state
override for deliberately separate pools, rather than requiring it for daily use.

Use the installed laptop Tern executable as the guest runtime source. Copy it
only when necessary, through authenticated SSH; keep executable data outside
the credential-bearing home. Existing prepared dependency bases can receive this
small runtime addition without repeating application dependency setup. Future
prepared bases may retain the executable, while removing user state and keys.

Run separate persistent systemd services as guest user `dev` for the Tern session
daemon and its remote listener. Bind the listener to the guest's private IPv4
address on UDP 8376, disable relays and authorize the laptop's public SSH keys.
Private SSH keys remain on the laptop. Retrieve the remote host fingerprint over
the launcher's authenticated SSH connection before configuring local Tern trust.
Do not blindly replace a previously recorded different key for an existing worker.

Use Tern's existing CLI/control APIs rather than a custom plugin or desktop
configuration. Prefer the invoking local Tern pane's window; otherwise use the
first existing local window. Open the installed local app if no window exists.
Bootstrap a local, automatically closing control session when the invoking
terminal lacks a Tern window context. Bound connection waits and check the actual
host result, not merely acceptance of a control command. Keep per-launch result
files private and separate so concurrent launches cannot consume each other's
results.

Connection failure preserves the VM and agent, returns an error and explains
how to reconnect with `make vm-codex VM=NAME`. Closing the laptop window leaves
guest processes running. Guest shutdown stops processes; restarting must never
resubmit the original task prompt automatically. Collection and base preparation
must refuse active Tern tasks and fail closed when inspection fails. Worker
deletion must remove only that worker's local registration and trusted identity;
ordinary stop preserves its disk, credentials and reconnect information.

## Alternatives considered

- Keep tmux and show SSH terminals in Tern: the trial preserves existing agents,
  but does not satisfy direct Codex execution in guest Tern for new workers.
- Install a dedicated Tern plugin: adds a user configuration dependency when
  existing local control APIs can add and select a host.
- Connect manually: verified feasible, but repeats host entry and trust setup
  for every worker.

## Consequences

The optional worker-run workflow now requires an installed Stencil Tern and usable
laptop SSH authentication. This beta executable remains a local prerequisite;
do not commit binaries or distribute an authenticated desktop configuration.
Native remote transport uses UDP rather than SSH TCP forwarding. The desktop
integration is tested against 0.9.16; later CLI changes may require adaptation.

## Revisit conditions

Tern provides a simpler direct connection CLI, its control API changes, or the
maintainer requests a combined view of agents across several guest hosts.
