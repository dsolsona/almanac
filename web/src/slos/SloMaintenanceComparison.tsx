import type { components } from '../api/generated.ts';
import { combineMaintenanceCounts } from './maintenance.ts';
import { countExact, percentExact } from './detailFormat.ts';

type Current = components['schemas']['SloOperationalValue'];

export function SloMaintenanceComparison({ current }: { current: Current }) {
  const { quality, performance } = current;
  const excluded = quality.excluded_intervals ?? 0;
  if (!excluded) return null;

  const fullyExcluded = quality.state === 'excluded';
  const eligible = fullyExcluded ? { good: '0', total: '0', bad: '0', attainment: null } : performance;
  const comparison = combineMaintenanceCounts(eligible, { good: quality.excluded_good, total: quality.excluded_total });

  return <details className="slo-detail-disclosure">
    <summary>Maintenance calculation comparison <span>{excluded.toLocaleString()} excluded {excluded === 1 ? 'interval' : 'intervals'}</span></summary>
    <div>
      <p className="small">Without maintenance is the reported SLO result. Including maintenance adds those events back for comparison.</p>
      {performance.provisional && <p className="small">The comparison uses observed events. Coverage is incomplete.</p>}
      {!comparison && <p className="small">{quality.excluded_total === null
        ? 'The full excluded event population is unknown.'
        : 'Comparable event counts are unavailable for this evaluation.'}</p>}
      <table className="slo-population-comparison">
        <thead><tr><th scope="col">Measure</th><th scope="col">Without maintenance</th><th scope="col">Including maintenance</th></tr></thead>
        <tbody>
          <tr><th scope="row">Total events</th><td>{countExact(eligible.total)}</td><td>{countExact(comparison?.total)}</td></tr>
          <tr><th scope="row">Successful</th><td>{countExact(eligible.good)}</td><td>{countExact(comparison?.good)}</td></tr>
          <tr><th scope="row">Unsuccessful</th><td>{countExact(eligible.bad)}</td><td>{countExact(comparison?.bad)}</td></tr>
          <tr><th scope="row">Attainment</th><td>{percentExact(eligible.attainment)}</td><td>{percentExact(comparison?.attainment)}</td></tr>
        </tbody>
      </table>
    </div>
  </details>;
}
