# Alerting performance investigation

- State: in progress
- Task: [F4-T14](../tasks/F4-T14.md)
- Base: `35a6318`, the corrected fixture and baseline-only control in PR #107
- Contracts: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md),
  [SPEC-0020](../specs/0020-cumulative-slo-calculation.md),
  [SPEC-0004](../specs/0004-identity-and-access.md),
  [ADR-0025](../adr/0025-fast-operational-journeys-and-tracing.md)
- Authorization: the maintainer explicitly requested a complete, prioritized
  performance investigation on 2026-11-13 and authorized continued implementation.

## Outcome

Explain and correct the failures that prevent normal navigation and live ingestion
from retaining their accepted budgets during the 200-SLO workload. The
[ranked inventory](../evidence/alert-performance/README.md) is the working record:
it separates observed failures, proven mechanisms, hypotheses, corrected defects
and limitations in the experiment. It must be updated when evidence changes.

The corrected baseline fails with no proposed fallback or raw oracle. Therefore
the next action is investigation of existing workload interference, not adoption
of alert calculation or another undifferentiated full load run. The failed gate,
all earlier evidence and the accepted budgets remain intact.

## Order and method

1. Reproduce the auth-context 503 with real PostgreSQL shared-reader locks;
   identify the blocked lock and SQLSTATE. Preserve authority, revocation, expiry
   and no-touch semantics before considering a narrower read lock.
2. Decompose overview cost into admission, authorization, count/page execution,
   result transfer, decode/encode and browser rendering. Use identical output and
   snapshots for query alternatives. An isolated mean improvement cannot establish
   a tail-latency or browser fix.
3. Attribute ingestion slowdown to its actual operations, separating pool/lock
   waits, server work and database execution. Correlate source-generation writes
   with background activity; do not call span duration CPU use or lock blocking.
4. Classify repeated current evaluations that return pending: outstanding source
   ranges, eligibility, retry scheduling and publication races. Reproduce redundant
   attempts before changing selection; preserve healthy-work fairness and recovery.
5. Validate each proven correction with the smallest regression and security/
   correctness boundary checks. Then run the affected integrated journey and one
   frozen corrected control to assess the combined result. Only after that revisit
   candidate-only/paired load and the calculation adoption decision.

The primary owns the inventory, prioritization, integration and runtime lane.
One agent investigates session contention in an isolated worktree; a second
performed the read-only historical evidence audit. No concurrent load runs,
fixture replacement or source changes during a timed candidate measurement.

## Evidence requirements

Every issue records its observed user impact, source revision/fixture, measurements,
trace/code location, proven and unproved causes, next discriminating check, and
exit criteria. Keep failed and incomplete runs; neither retries, raised budgets,
reduced authentication nor omitted public data can manufacture acceptance.

Use existing spans first. Where needed, add bounded, credential-safe diagnostics:
database error codes, backend/wait relationships and per-container resource
samples. Query text, parameters, credentials and complete user payloads are not
diagnostic fields. Capture a blocker while the wait exists; a post-run snapshot
cannot identify a historical blocker. Instrumentation must be tested before use.

Matched controls retain hardware/storage, source fingerprints, workload identity,
history validity and exact expected data. Distinguish synthetic SQL backfill from
the real historical ingestion protocol, and cohosted browser timings from a
capable remote-client acceptance run. A control that removes work narrows attribution;
it does not itself prove the removed work harmless under production admission.

## Initial focused commands

```sh
make shell CMD='go test -tags=integration ./internal/storage -run "^TestIntegrationInspectSessionConcurrentReadAuthority\z" -count=1 -timeout=60s'
make shell CMD='python3 scripts/check-docs.py'
```

The first is an isolated database reproduction, not permission to alter production
session locks. Record additional focused commands and expected results in F4-T14
before running them. Full application suites belong to CI; broad local acceptance
needs an identified changed workload and frozen inputs.
