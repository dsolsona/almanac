import './sli-recent-strip.css';

export type SliRecentSample = {
  interval_start: string;
  interval_end: string;
  state: 'observed' | 'zero_traffic' | 'known_empty' | 'missing' | 'partial' | 'failed';
  good_count: string | null;
  total_count: string | null;
};

const stateLabels: Record<SliRecentSample['state'], string> = {
  observed: 'Observed',
  zero_traffic: 'Zero traffic',
  known_empty: 'Known empty',
  missing: 'Missing',
  partial: 'Partial',
  failed: 'Failed',
};

function scaledCount(value: string): bigint {
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole!) * 1_000_000_000_000n + BigInt((fraction + '0'.repeat(12)).slice(0, 12));
}

function observedFraction(sample: SliRecentSample): number {
  if (sample.state !== 'observed' || sample.good_count === null || sample.total_count === null) return 0;
  const total = scaledCount(sample.total_count);
  if (total === 0n) return 0;
  return Number(scaledCount(sample.good_count) * 10_000n / total) / 10_000;
}

function stripLabel(samples: SliRecentSample[], cadenceSeconds: number): string {
  const counts = new Map<string, number>();
  for (const sample of samples) counts.set(stateLabels[sample.state], (counts.get(stateLabels[sample.state]) ?? 0) + 1);
  const summary = [...counts].map(([label, count]) => `${count} ${label.toLowerCase()}`).join(', ');
  return `Recent good-to-total measurements at ${cadenceSeconds}-second cadence: ${summary || 'no eligible intervals'}. Exact values are available in the table.`;
}

export function SliRecentStrip({ samples, cadenceSeconds }: { samples: SliRecentSample[]; cadenceSeconds: number }) {
  return <div className="sli-recent-strip">
    <svg className="sli-recent-strip__plot" viewBox="0 0 140 24" role="img" aria-label={stripLabel(samples, cadenceSeconds)}>
      <line className="sli-recent-strip__baseline" x1="0" y1="21" x2="140" y2="21" />
      {samples.map((sample, index) => {
        const x = 3 + index * 11;
        if (sample.state === 'observed') {
          const height = Math.max(1, observedFraction(sample) * 18);
          return <rect key={`${sample.interval_start}-${sample.state}`} className="sli-recent-strip__observed" x={x} y={21 - height} width="7" height={height} />;
        }
        if (sample.state === 'zero_traffic') return <circle key={`${sample.interval_start}-${sample.state}`} className="sli-recent-strip__zero" cx={x + 3.5} cy="17" r="3" />;
        if (sample.state === 'known_empty') return <rect key={`${sample.interval_start}-${sample.state}`} className="sli-recent-strip__empty" x={x + 1} y="15" width="5" height="5" />;
        if (sample.state === 'partial') return <line key={`${sample.interval_start}-${sample.state}`} className="sli-recent-strip__partial" x1={x} y1="17" x2={x + 7} y2="17" />;
        if (sample.state === 'failed') return <path key={`${sample.interval_start}-${sample.state}`} className="sli-recent-strip__failed" d={`M${x + 1},14 L${x + 6},20 M${x + 6},14 L${x + 1},20`} />;
        return <line key={`${sample.interval_start}-${sample.state}`} className="sli-recent-strip__missing" x1={x} y1="18" x2={x + 7} y2="18" />;
      })}
    </svg>
    <details className="sli-recent-strip__details">
      <summary>Exact values · {samples.length}</summary>
      <div className="sli-recent-strip__table-scroll" role="region" aria-label="Recent SLI exact count values" tabIndex={0}>
        <table>
          <caption>Exact counts for recent {cadenceSeconds}-second intervals</caption>
          <thead><tr><th scope="col">Interval start (UTC)</th><th scope="col">Interval end (UTC)</th><th scope="col">State</th><th scope="col">Good</th><th scope="col">Total</th></tr></thead>
          <tbody>{samples.map((sample) => <tr key={`${sample.interval_start}-${sample.state}`}>
            <td className="sli-recent-strip__timestamp">{new Date(sample.interval_start).toISOString()}</td>
            <td className="sli-recent-strip__timestamp">{new Date(sample.interval_end).toISOString()}</td>
            <td>{stateLabels[sample.state]}</td><td>{sample.good_count ?? '—'}</td><td>{sample.total_count ?? '—'}</td>
          </tr>)}</tbody>
        </table>
      </div>
    </details>
  </div>;
}
