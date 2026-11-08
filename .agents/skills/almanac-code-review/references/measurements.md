# Measurement semantics

Read [calculations](../../../../docs/specs/0006-slo-calculation-and-reporting.md),
[resource revisions](../../../../docs/specs/0005-openslo-and-resource-model.md),
and relevant [maintenance](../../../../docs/specs/0016-maintenance-exclusions.md)
or [annotation](../../../../docs/specs/0017-slo-timeline-annotations.md) contracts.

- Derive expected results from inputs independently. Distinguish occurrence sums,
  timeslice classification and ratio-timeslice means; do not substitute one method
  for another or pool composite children with incompatible denominators.
- Check units, input precision/range, exact intermediate comparisons and rounding
  only at the specified boundary. Display rounding cannot decide status. Preserve
  overspent and negative budgets instead of clamping them.
- Verify explicit evaluation time, half-open bounds, full-interval eligibility,
  cadence alignment and effective revisions. Check DST and civil-calendar boundaries
  for zoned reporting; a local day is not always 24 hours.
- Keep missing, partial, stale, known-empty, zero-traffic, excluded and expired data
  distinguishable. Absent or unevaluable input cannot become attainment of 100%,
  zero burn, a met objective or an alert recovery.
- Trace exclusions through both numerator and denominator, including unions and
  revision snapshots. Notification silences and timeline annotations do not change math.
- Verify independent short burn windows, historical evaluations, snapshots and
  exports against the same effective bounds. Check lossless numeric serialization
  and spreadsheet-safe text where exports change.

Give a small numerical or time-boundary counterexample and trace its effect through
the evaluator to the displayed result, export or alert decision.
