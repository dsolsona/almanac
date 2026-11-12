# ADR-0045: Prepared Firecracker development workers

- Status: accepted
- Created: 2026-11-10
- Accepted: 2026-11-10
- Approval: maintainer requested a rebuildable dependency base, per-worker CoW disks, authenticated Codex/GitHub, and named worker lifecycle commands.
- Related specs: none; optional development tooling under [ADR-0029](0029-platform-agnostic-development-and-verification.md)
- Supersedes / superseded by: none

## Context

Installing Docker, Codex, the development image and Go/npm dependencies for each
worker makes parallel development too slow. Workers need isolated code, Docker
daemons and writable disks, with terminal access and GitHub publication available.

## Decision

Keep the reproducible definition in `scripts/firecracker-setup.sh`, the development
Dockerfile, Compose file and dependency manifests. Build or refresh a private base
from a seed guest with `base NAME`; run `make setup` there, shut it down cleanly,
then copy its disk. Remove the seed checkout, home credentials, SSH host keys,
logs and machine identity from the copy. Preserve Docker images and dependency
caches. Never copy a running filesystem or boot the published base itself.

Publish immutable local generations through one atomic pointer. New workers use
filesystem reflinks of the raw ext4 base, then receive fresh SSH keys, networking,
machine identity, an independent checkout and task worktree. Require a reflink
filesystem such as Btrfs or reflink-enabled XFS; do not silently make full copies.
Match the base's build/dependency fingerprint before creating a worker. Rebuilding
the base does not modify existing workers.

Copy Codex's file login and the host GitHub CLI token privately over SSH into each
worker after cloning, outside the base. Configure HTTPS GitHub remotes and `gh` as
Git's credential helper. Inherited tokens retain their original permissions;
repository rules still govern whether an agent may commit, push or open a PR.

Expose base preparation, named worker creation, listing, terminal attachment,
stop, collection and explicit deletion through the script and fixed Make targets.
Preserve existing guests on ordinary setup/start/stop and unknown process state.
Deletion requires an explicit name and removes that worker's disk and credentials.

## Alternatives considered

- Install everything per guest: simpler initially, but repeats the reported delay.
- Share the host checkout or Docker socket: couples workers and exposes host state.
- Restore memory snapshots: adds clock/network recovery; disk CoW with ordinary
  guest boot meets the current requirement with fewer moving parts.

## Consequences

Only scripts and documentation enter Git. Disks, caches and tokens remain under
ignored private local state. The first base build and dependency updates remain
expensive; normal workers inherit their results. Bases are private development
artifacts, not distributable images or forensic credential erasure. Old generations
remain available for existing clones and manual cleanup. RAM, disk and network
address availability bound concurrency. Pane orchestration is deferred.

## Revisit conditions

Measured cold-boot latency requires memory restore, another host filesystem needs
a different CoW backend, or orchestration needs a machine-readable lifecycle API.
