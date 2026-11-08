# Performance and resource use

Read [performance contracts](../../../../docs/specs/0019-backend-performance-and-derived-data.md),
[operational journeys](../../../../docs/adr/0025-fast-operational-journeys-and-tracing.md),
and the current measurement method in
[testing](../../../../docs/testing.md) and
[ADR-0033](../../../../docs/adr/0033-simple-test-suite.md).

## Find the cost on the changed path

- Identify the request, collection tick, worker or interaction that executes the
  changed work. State what scales it: rows, observations, history length, SLOs,
  users, retries or concurrent requests. Separate naturally bounded work from work
  proportional to the installed fleet or retained history.
- Inspect query counts, unnecessary round trips, retention scans, join expansion,
  pagination and transaction/lock scope. Verify schema and index support with the
  Storage lens when needed. Use query plans for planner-dependent claims.
- Look for repeated calculations, parsing, sorting, copying, allocation and
  serialization in hot loops. Use CPU/allocation profiles to locate uncertain cost.
  Verify that optimizations preserve the numerical and data-quality guarantees
  required by the current contract.
- Check payload bytes, fetching unused history, duplicate requests, waterfall
  dependencies and repeated rendering work. Check display reduction against the
  current data-fidelity requirements. Smaller payloads alone do not prove faster
  rendered journeys.
- Trace pool/lock wait, fan-out, goroutine lifetime, buffers, queue backlog and retry
  amplification. Check cancellation, admission limits and workload isolation against
  the documented priorities and capacity requirements.
- Examine cache-miss, expiry, invalidation and simultaneous-miss behavior. Confirm
  coherent revisions and bounded fallback work. Verify foreground and background
  work against the current responsiveness and preparation policy.

## Establish the evidence

- Compare base and candidate on the same inputs, hardware and configuration. State
  fleet size, history, concurrency, cache state, runs and measured percentiles.
  Distinguish a demonstrated growth defect from measured latency; a nested loop or
  complex SQL statement alone does not establish a problem on a bounded workload.
- For operational page changes, measure authenticated navigation through usable
  content, including first visits, cold misses and relevant interactions. Read the
  current budgets, fixture settings, required scale and acceptance method from the
  linked documents. Report the workload actually measured and avoid extrapolating
  it to untested capacity or endurance.
- Use the available traces and profiles to attribute request, database, calculation,
  background and rendering costs. Report missing instrumentation as an evidence gap.
  Follow the current observability policy when deciding what evidence is required.
- Run relevant checks through documented commands and isolated fixtures. Retain
  slow and failed samples. Avoid claims based only on warmed API responses, raising
  budgets or repeatedly running the same workload until it passes.

A finding names the changed path, workload/growth mechanism, user or resource impact,
supporting measurement or structural proof, and the smallest justified correction.
Recommend profiles or measurements as open questions where evidence is missing;
do not prescribe caches, indexes, parallelism or new dependencies speculatively.
