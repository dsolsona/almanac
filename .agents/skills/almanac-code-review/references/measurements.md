# Measurement semantics

Read [calculations](../../../../docs/specs/0006-slo-calculation-and-reporting.md),
[resource revisions](../../../../docs/specs/0005-openslo-and-resource-model.md),
and relevant [maintenance](../../../../docs/specs/0016-maintenance-exclusions.md)
or [annotation](../../../../docs/specs/0017-slo-timeline-annotations.md) contracts.

- Derive expected results independently using the specified aggregation, weighting
  and budgeting method. Check whether combining inputs preserves their denominators
  and meaning rather than substituting a convenient formula.
- Check units, precision/range, intermediate arithmetic, rounding and comparisons
  against the numerical contract, especially at classification boundaries.
- Verify explicit evaluation time, interval inclusion, alignment and revision
  boundaries. Exercise daylight-saving and civil-calendar transitions where relevant.
- Trace missing, partial, stale, empty, zero-traffic and expired inputs through the
  defined quality and freshness rules. Check that unavailable evidence cannot be
  mistaken for a successful measurement or alert recovery.
- Check the defined effects of exclusions, silences and annotations independently;
  inspect population, interval and revision handling where they affect evaluation.
- Compare windows, historical evaluations, snapshots and exports at equivalent
  effective bounds. Check serialization and export safety against their contracts.

Give a small numerical or time-boundary counterexample and trace its effect through
the evaluator to the displayed result, export or alert decision.
