# Tests and verification quality

Read [testing](../../../../docs/testing.md) and
[suite boundaries](../../../../docs/adr/0033-simple-test-suite.md).

## Useful assertions and risk coverage

- Identify the regression each changed test claims to detect. Name a plausible
  production mutation that should fail it; examine whether assertions and setup
  actually distinguish that mutation from correct behavior.
- Assert observable results, persisted state, errors or required side effects.
  A test that only confirms its own mock's canned response proves little about
  Almanac. Call-count checks are useful when the count itself is a contract, such
  as preventing duplicate notification delivery or bounding query growth.
- Calculate numerical expectations independently. Avoid copying the evaluator's
  aggregation, rounding or null handling into its expected value.
- Check ordinary behavior and consequential boundaries: missing/zero data, invalid
  inputs, denied authority, partial failure, cancellation, replay and concurrent
  writes when affected. Select cases from the changed contract and failure risk.
  One focused test can cover an invariant; trivial edits need no artificial suite.
- Use coverage reports to locate unexercised risky paths, then inspect assertion
  strength. A high percentage cannot establish correctness. Request a missing test
  with its concrete unchecked behavior, rather than a blanket coverage target.

## Proportionate layers and execution cost

- Choose the dependency boundary that can prove the assertion: database behavior
  needs the real database semantics, protocol behavior needs its actual implementation,
  and UI journeys need meaningful user interaction. Follow the current test guide
  for harnesses and fixtures. Mocks cannot prove behavior they do not implement.
- Keep pure calculations, validation and component behavior in fast unit tests.
  Database setup or full browser startup needs an assertion about behavior that
  dependency provides. Keep representative critical journeys end to end; exercise
  detailed input matrices at the lowest layer that can prove the contract.
- Look for repeated scenarios across layers or files. Retain overlap when each
  test protects a distinct boundary; consolidate copies that add the same evidence
  and make one behavior change require many assertion rewrites.
- Inspect expensive setup inside loops, per-case process/container launches,
  repeated migrations and global serialization. Use the documented isolation and
  parallelism policy. Share immutable setup only where ownership
  and cleanup remain clear; never share mutable fixtures to buy speed.
- Use suite/test timings to substantiate slowness and compare the affected lane
  after a change. A shorter test file or fewer tests does not establish a faster
  suite. Distinguish functional timeout checks from hardware-sensitive speed
  thresholds; benchmark page speed through the defined performance suite.

## Reliable and maintainable checks

- Check fake fidelity, input-sensitive stubs, assertion specificity and helpers
  whose missing anchors or empty selections can make assertions pass spuriously.
- Assert stable public behavior rather than internal call order, private state,
  broad snapshots or incidental markup. Snapshot assertions need a clear contract
  and a reviewable difference. Keep test abstractions smaller than the duplication
  they remove, and test-only helpers out of application code.
- Verify explicit clocks, per-test databases/ports, synchronization, deterministic
  ordering and isolation from host environment, credentials and filesystem state.
  Separate legitimate condition polling from rerunning a failed test into green.
- Preserve focused reproduction, failure diagnostics and the required final gates.
  Do not recommend retries, relaxed assertions or larger timeouts to conceal failure.
- Check suite responsibilities against the current test guide; keep detailed state
  matrices in the appropriate lower layer and user journeys focused on visible outcomes.
  Test names and failure messages should reveal the broken behavior without tracing
  several helpers or depending on another test having run first.

Report a test weakness with the specific regression that survives it. Keep commands
actually executed, inherited evidence, unavailable checks and remaining acceptance
gaps separate. A cost or duplication finding names the avoidable work and evidence
lost or preserved by removing it. Never claim a test passed because its source looks correct.
