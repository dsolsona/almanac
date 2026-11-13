export type CalculationMode = 'paired' | 'baseline-only' | 'candidate-only';
export const calculationFixtureVersion = 'eligible-published-history-v3';
export const calculationBackfillDays = 3;

export function calculationMode(value: string | undefined): CalculationMode {
  if (value === undefined) return 'paired';
  if (value === 'paired' || value === 'baseline-only' || value === 'candidate-only') return value;
  throw new Error('ALERT_CALCULATION_MODE must be paired, baseline-only or candidate-only');
}

export function plannedCalculationCalls(mode: CalculationMode, batches: number) {
  return { baseline: mode === 'candidate-only' ? 0 : batches,
    candidate: mode === 'baseline-only' ? 0 : batches, reference: mode === 'paired' ? batches : 0 };
}

type EquivalenceWindow = { ordinal: number; prepared?: boolean; [field: string]: unknown };
type EquivalenceBatch = { mode: string; sli: string; cutoff: string;
  candidate: EquivalenceWindow[] | null; reference: EquivalenceWindow[] | null };

function decimalScale(value: unknown): string | null {
  if (typeof value !== 'string' || !/^-?\d+(?:\.\d+)?$/.test(value)) return null;
  const [integer, fraction = ''] = value.split('.');
  const significantFraction = fraction.replace(/0+$/, '');
  return significantFraction ? `${integer}.${significantFraction}` : integer!;
}

// Evidence is calculated before any workload/latency assertion can throw. Count
// comparisons stay in PostgreSQL's decimal-text domain; only scale is normalized.
export function verifyQueryEquivalence(mode: CalculationMode, batches: EquivalenceBatch[], expectedBatches: number) {
  const result: { state: 'matched' | 'mismatched' | 'incomplete' | 'not_checked'; windows: number;
    expectedWindows: number; missingWindows: number;
    mismatches: { sli: string; cutoff: string; ordinal: number; field: string; actual: unknown; expected: unknown }[] } = {
      state: mode === 'paired' ? 'incomplete' : 'not_checked', windows: 0,
      expectedWindows: mode === 'paired' ? expectedBatches * 4 : 0, missingWindows: 0, mismatches: [],
    };
  if (mode !== 'paired') return result;
  let complete = batches.length === expectedBatches;
  for (const batch of batches) {
    if (batch.mode !== 'paired' || !Array.isArray(batch.candidate) || !Array.isArray(batch.reference)) { complete = false; continue; }
    if (batch.candidate.length !== 4 || batch.reference.length !== 4) complete = false;
    for (let index = 0; index < 4; index++) {
      const row = batch.candidate[index], raw = batch.reference[index];
      if (!row?.prepared || row.ordinal !== index + 1 || raw?.ordinal !== index + 1) { complete = false; continue; }
      result.windows++;
      for (const field of ['good', 'total', 'observed', 'known_empty', 'zero_traffic', 'first_observation', 'last_observation', 'latest_receipt']) {
        const same = field === 'good' || field === 'total'
          ? decimalScale(row[field]) !== null && decimalScale(row[field]) === decimalScale(raw[field])
          : field in row && field in raw && row[field] === raw[field];
        if (!same) result.mismatches.push({ sli: batch.sli, cutoff: batch.cutoff, ordinal: index + 1, field, actual: row[field], expected: raw[field] });
      }
    }
  }
  result.missingWindows = Math.max(0, result.expectedWindows - result.windows);
  result.state = result.mismatches.length ? 'mismatched' : complete ? 'matched' : 'incomplete';
  return result;
}

export type BackfillRange = { from: string; to: string };
export type EligibilityBounds = {
  measurementEffectiveFrom: string;
  sloEvaluationValidFrom: string;
  cadenceSeconds: number;
};

export function assertEligibleBackfill(ranges: BackfillRange[], bounds: EligibilityBounds) {
  const measurement = Date.parse(bounds.measurementEffectiveFrom);
  const objective = Date.parse(bounds.sloEvaluationValidFrom);
  const cadence = bounds.cadenceSeconds * 1000;
  if (!Number.isFinite(measurement) || !Number.isFinite(objective) || !Number.isSafeInteger(cadence) || cadence <= 0) {
    throw new Error('invalid persisted eligibility bounds');
  }
  for (const range of ranges) {
    const from = Date.parse(range.from), to = Date.parse(range.to);
    if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to || from % cadence !== 0 || to % cadence !== 0) {
      throw new Error('backfill range must be ordered and cadence aligned');
    }
    if (from < measurement) throw new Error('backfill begins before measurement effective_from');
    if (from < objective) throw new Error('backfill begins before SLO evaluation_valid_from');
  }
}

// Only selected functions and calls enter the generated SQL. Single-query controls
// cannot accidentally execute the other calculation or the independent raw oracle.
export function experimentSQL(mode: CalculationMode, baselineSQL: string, candidateSQL: string) {
  const columns = 'ordinal integer,good text,total text,observed bigint,known_empty bigint,zero_traffic bigint,first_observation timestamptz,last_observation timestamptz,latest_receipt timestamptz,prepared boolean,exhausted boolean';
  const parameters = 'uuid,bigint,timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz';
  const baseline = `began=clock_timestamp();SELECT jsonb_agg(to_jsonb(r)) INTO base FROM public.alert_experiment_baseline(id,1,ends,starts,finishes,a,b,c,d,cut+interval '10 seconds') r;base_ms=extract(epoch FROM clock_timestamp()-began)*1000;`;
  const candidate = `began=clock_timestamp();SELECT jsonb_agg(to_jsonb(r)) INTO proposed FROM public.alert_experiment_candidate(id,1,ends,starts,finishes,a,b,c,d,cut+interval '10 seconds',8,7200) r;proposed_ms=extract(epoch FROM clock_timestamp()-began)*1000;`;
  const reference = `SELECT jsonb_agg(to_jsonb(r)) INTO reference FROM (
  SELECT n AS ordinal,coalesce(sum(o.good_count) FILTER(WHERE o.outcome='observed'),0)::text AS good,
   coalesce(sum(o.total_count) FILTER(WHERE o.outcome='observed'),0)::text AS total,
   count(*) FILTER(WHERE o.outcome='observed') AS observed,count(*) FILTER(WHERE o.outcome='complete_empty') AS known_empty,
   count(*) FILTER(WHERE o.outcome='observed' AND o.total_count=0) AS zero_traffic,
   min(o.interval_start) FILTER(WHERE o.outcome='observed') AS first_observation,
   max(o.interval_start+interval '1 minute') FILTER(WHERE o.outcome='observed') AS last_observation,max(o.recorded_at) AS latest_receipt
  FROM generate_subscripts(ends,1) n LEFT JOIN public.almanac_sli_observations o
   ON o.sli_id=id AND o.measurement_revision=1 AND o.interval_start>=a[n] AND o.interval_start<ends[n]
   AND o.interval_start+interval '1 minute'<=ends[n] GROUP BY n ORDER BY n
 ) r;`;
  const work = mode === 'paired'
    ? `IF candidate_first THEN ${candidate} END IF;\n${baseline}\nIF NOT candidate_first THEN ${candidate} END IF;\n${reference}`
    : mode === 'baseline-only' ? baseline : candidate;
  return `${mode === 'candidate-only' ? '' : `CREATE FUNCTION public.alert_experiment_baseline(${parameters}) RETURNS TABLE(${columns}) LANGUAGE sql STABLE AS $query$${baselineSQL}$query$;`}
${mode === 'baseline-only' ? '' : `CREATE FUNCTION public.alert_experiment_candidate(${parameters},integer,integer) RETURNS TABLE(${columns},dirty_hours bigint,raw_rows bigint) LANGUAGE sql STABLE AS $query$${candidateSQL}$query$;`}
CREATE FUNCTION public.alert_experiment_compare(id uuid,cut timestamptz,candidate_first boolean) RETURNS jsonb LANGUAGE plpgsql AS $fn$
DECLARE duration interval; start_at timestamptz; fs timestamptz; fe timestamptz; began timestamptz;
ends timestamptz[]='{}'; starts timestamptz[]='{}'; finishes timestamptz[]='{}'; a timestamptz[]='{}'; b timestamptz[]='{}'; c timestamptz[]='{}'; d timestamptz[]='{}';
base jsonb; proposed jsonb; reference jsonb; base_ms numeric; proposed_ms numeric;
BEGIN
 FOREACH duration IN ARRAY ARRAY[interval '5 minutes',interval '1 hour',interval '30 minutes',interval '6 hours'] LOOP
  start_at=cut-duration;fs=date_trunc('hour',start_at);IF fs<start_at THEN fs=fs+interval '1 hour';END IF;
  fe=date_trunc('hour',cut);IF fs>fe THEN fs=cut;fe=cut;END IF;
  ends=array_append(ends,cut);starts=array_append(starts,fs);finishes=array_append(finishes,fe);
  a=array_append(a,start_at);b=array_append(b,fs);c=array_append(c,fe);d=array_append(d,cut);
 END LOOP;
 ${work}
 RETURN jsonb_build_object('sli',id,'cutoff',cut,'mode','${mode}','baseline_ms',base_ms,'candidate_ms',proposed_ms,'baseline',base,'candidate',proposed,'reference',reference,'at',clock_timestamp());
END $fn$;`;
}
