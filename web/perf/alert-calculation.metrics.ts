import { compose } from '../e2e/support/stack.ts';

export function ingestionMetrics() {
  const source = compose('exec', '-T', 'app', 'curl', '--fail', '--silent', '--max-time', '3', 'http://127.0.0.1:9092/metrics');
  const samples: Record<string, number> = {};
  for (const line of source.split('\n')) {
    if (!line.startsWith('almanac_ingestion_batch_duration_seconds_')) continue;
    const split = line.lastIndexOf(' ');
    samples[line.slice(0, split)] = Number(line.slice(split + 1));
  }
  if (!Object.keys(samples).length) throw new Error('ingestion histogram unavailable');
  return { at: new Date().toISOString(), samples };
}

export function ingestionDelta(before: ReturnType<typeof ingestionMetrics>, after: ReturnType<typeof ingestionMetrics>) {
  const count = (result: string) => {
    const key = `almanac_ingestion_batch_duration_seconds_count{result="${result}"}`;
    return after.samples[key]! - before.samples[key]!;
  };
  const accepted = count('accepted');
  const rank = Math.ceil(accepted * 0.95);
  const buckets = Object.entries(after.samples).flatMap(([key, value]) => {
    const match = key.match(/^almanac_ingestion_batch_duration_seconds_bucket\{result="accepted",le="([^"]+)"\}$/);
    return match ? [{ upper: match[1] === '+Inf' ? Infinity : Number(match[1]), count: value - before.samples[key]! }] : [];
  }).sort((a,b) => a.upper - b.upper);
  let lower = 0;
  let upper = Infinity;
  for (const bucket of buckets) {
    if (bucket.count >= rank && rank > 0) { upper = bucket.upper; break; }
    lower = bucket.upper;
  }
  return { accepted, duplicate: count('duplicate'), unsuccessful: count('unsuccessful'),
    p95LowerSeconds: lower, p95UpperSeconds: Number.isFinite(upper) ? upper : null,
    elapsedSeconds: (Date.parse(after.at)-Date.parse(before.at))/1000 };
}
