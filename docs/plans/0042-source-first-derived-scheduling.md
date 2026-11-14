# Source-first derived scheduling

- State: review; focused implementation checks pass, integrated performance pending
- Task: [F4-T11](../tasks/F4-T11.md)
- Governing contracts: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md),
  [SPEC-0020](../specs/0020-cumulative-slo-calculation.md),
  [ADR-0034](../adr/0034-self-healing-derived-data.md)
- Base: `291f0fe`; implementation authorized inside existing scheduling contracts.

## Outcome

When source and current work are due together, rebuild one bounded source batch
before evaluating current projections. A current result whose source becomes ready
in that batch can publish without first acquiring an unnecessary temporary deferral.
Historical backlog, source failures and leases must not skip healthy current work.
The existing 10-second currentness target remains unchanged.

## Implementation

Only `internal/storage/derived_worker.go` and focused scheduler tests change.
Extract one scheduler turn with an explicit clock and existing metrics deadline,
preserving behavior first so a real PostgreSQL regression can demonstrate the
current-before-source failure. The public `RunDerived` interface stays unchanged.
Then move one existing batch of at most 100 newest dirty keys before current work,
read due state again with a fresh clock, and process current/metrics before the
full-batch immediate continuation. Leave source errors observable and still run
current selection. Connections are released before calculations and sleeping.

No new persisted state, dependency cache, public contract, migration, raw fallback,
admission limit, retry budget or authority fence. Source claim/acknowledgment and
current publication transactions remain unchanged. Deferral still lasts two
seconds; worker sleep remains capped at one second. A bounded source operation
can itself take up to five seconds; verification must not conceal that tradeoff.

## Cases and verification

- [x] Reproduce one-turn failure: source becomes acknowledged but current remains
  unpublished with a waiting row; after the correction exact current publishes.
- [x] More than 100 dirty keys still permits a healthy current publication after
  only one batch, with historical backlog and metrics work remaining serviced.
- [x] Source failures, leased keys and zero claims do not suppress healthy current;
  blocked values retain truthful pending/failed state and temporary attempts.
- [x] A cadence or validity deadline crossed during source work is selected using
  the fresh clock, even when initial current work was not due.
- [x] Source repair, existing deferred retry and new revision publication recover
  without requiring another observation; publication fences remain covered.
- [x] Unrelated ranges and revisions do not globally block current; preserve the
  existing blocked-objective/healthy-tail fairness regression.
- [x] Empty/backed-off work sleeps and cancels without holding a connection.

Read the complete Go 1.27.3 guidelines before Go edits. Commands use the Docker
development environment, with isolated PostgreSQL databases owned by each test:

```sh
make shell CMD='make generate GENERATE_MODE=go-guidelines'
make shell CMD='go test -tags=integration ./internal/storage -run TestIntegrationDerivedScheduler -count=1 -timeout=2m -v'
make shell CMD='make generate GENERATE_MODE=format'
```

The task records the exact dependency-selected recovery/due expression and results.
No broad suite, E2E fixture mutation or performance rerun ran in this slice.
The integrated 200-SLI measurement remains a separate acceptance gate; fewer
waiting attempts alone cannot establish ingestion or navigation latency success.
