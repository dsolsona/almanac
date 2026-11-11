# Configurable session inactivity

[ADR-0044](../adr/0044-configurable-session-inactivity.md) and
[SPEC-0004](../specs/0004-identity-and-access.md) govern [F1-T22](../tasks/F1-T22.md).

1. Reproduce the 30-minute cutoff with the deterministic session integration
   test. Add a forward-only policy migration and snapshot the current timeout
   through the shared session insertion query. Check exact idle and absolute
   expiry boundaries, existing sessions and password rotation.
2. Expose versioned, administrator-only reads and writes through the existing
   session request protections. Verify rejection of invalid and stale writes.
3. Add the Sessions tab with shared settings controls. Verify the default,
   saving, reloading, keyboard navigation and access restrictions in the real
   browser journey. Run `make check` and `make test-e2e`.
