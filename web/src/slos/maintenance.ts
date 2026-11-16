const record = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const revision = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9][0-9]{0,18})$/.test(value) && BigInt(value) <= 9223372036854775807n;
const uuid = (value: unknown) => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(value) && value !== '00000000-0000-0000-0000-000000000000';
function instant(value: unknown): bigint | undefined {
  if (typeof value !== 'string') return;
  const match = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,6}))?Z$/.exec(value);
  if (!match) return;
  const milliseconds = Date.parse(`${match[1]}Z`);
  if (!Number.isFinite(milliseconds) || new Date(milliseconds).toISOString().slice(0,19) !== match[1]) return;
  return BigInt(milliseconds) * 1000n + BigInt((match[2] ?? '').padEnd(6, '0'));
}

export function validCalculationProvenance(value: unknown, quality: unknown): boolean {
  if (value === undefined) return true; // Legacy omission is not fabricated proof.
  if (!record(quality) || quality.excluded_intervals === undefined || !record(value) || !revision(value.requested_revision) || !revision(value.computed_revision) || !revision(value.source_generation)) return false;
  const computed = BigInt(value.computed_revision), requested = BigInt(value.requested_revision);
  if (computed > requested || (value.build_id === undefined ? computed !== 0n : !uuid(value.build_id))) return false;
  const proof = value.compatibility;
  if (proof === undefined) return computed === requested;
  if (!record(proof) || proof.computed_revision !== value.computed_revision || proof.requested_revision !== value.requested_revision || !revision(proof.indexed_through)) return false;
  const start = instant(proof.start), end = instant(proof.end);
  if (start === undefined || end === undefined || start >= end) return false;
  if (proof.kind === 'same_revision') return computed === requested && proof.comparison_id === undefined;
  if (computed >= requested || BigInt(proof.indexed_through) < requested) return false;
  if (proof.kind === 'indexed_absence') return proof.comparison_id === undefined;
  return proof.kind === 'equal_union' && uuid(proof.comparison_id);
}

function decimal(value: unknown): value is string {
  return typeof value === 'string' && value.length <= 128 && /^\d+(?:\.\d+)?$/.test(value);
}
function atScale(value: string, scale: number): bigint {
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole + fraction.padEnd(scale, '0'));
}
export function validExcludedPopulation(value: Record<string, unknown>): boolean {
  const { excluded_intervals: intervals, excluded_good: good, excluded_total: total } = value;
  if (intervals === undefined && good === undefined && total === undefined) return true;
  if (typeof intervals !== 'number' || !Number.isSafeInteger(intervals) || intervals < 0) return false;
  if (good === null && total === null) return true;
  if (!decimal(good) || !decimal(total)) return false;
  const scale = Math.max(good.split('.')[1]?.length ?? 0, total.split('.')[1]?.length ?? 0);
  return atScale(good, scale) <= atScale(total, scale);
}
