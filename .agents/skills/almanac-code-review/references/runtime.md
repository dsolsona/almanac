# Migrations and runtime operations

Read [runtime](../../../../docs/specs/0013-runtime-and-operations.md),
[storage decisions](../../../../docs/adr/0007-postgresql-and-storage-boundaries.md),
[Docker workflow](../../../../docs/adr/0029-platform-agnostic-development-and-verification.md),
and current [commands](../../../../docs/commands.md).

- Read the supported database versions, migration ownership and compatibility policy.
  Check ordering, serialization and startup behavior against those requirements.
- Identify existing data and old readers/writers before assessing a schema change.
  Apply expand/contract sequencing when running-version compatibility requires it;
  establish the supported deployment topology and rollback guarantees first.
- Check actual DDL lock/rewrite behavior and migration transaction restrictions.
  Large data movement needs bounded work and a defined interruption/recovery path.
- Keep operational backfills from blocking live collection or ordinary reads.
  A manually run script still needs validation and protection against partial data loss.
- Verify startup configuration, schema readiness, TLS, protected credentials,
  signal handling and connection draining in the normal container deployment.
  Check whether a failed start preserves useful diagnostics and existing data.
- Review image architecture/pins, artifact packaging and host/container paths for
  the supported deployment environments. Test fixtures must not reuse operator volumes,
  credentials, preview ports or the local development stack.
- Trace changed notification or provider side effects after a timeout, process
  restart or retry. Code rollback cannot retract an already delivered webhook.

Name the supported starting installation, operation, failure point, persisted
state and recovery consequence. Separate required compatibility from hypothetical
deployment advice; do not introduce flags or resilience dependencies without evidence.
