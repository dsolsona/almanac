import json, math, hashlib
from pathlib import Path
from decimal import Decimal
from datetime import datetime

root = Path('.local/alert-calculation')
results = json.loads((root / 'results.json').read_text())
assert results['profile']['sourceHashes']['perf/alert-calculation.perf.ts'] == 'b85ea3e9238cf41742c7fb1c2f642dcf0d6f2efe92e6704b1ed4d0d12bdc35bf', 'This analyzer is only for the original recorded run; refusing to overwrite it with a different workload.'
queries = [json.loads(line) for i in range(2) for line in (root / f'queries-{i}.jsonl').read_text().splitlines() if line.startswith('{')]
assert len(queries) == 800
fields = ['observed', 'known_empty', 'zero_traffic', 'first_observation', 'last_observation', 'latest_receipt']
verified_windows = 0
for q in queries:
    for side in ('candidate', 'reference'):
        rows = q[side]
        assert len(rows) == 4 and {r['ordinal'] for r in rows} == {1, 2, 3, 4}, (q['sli'], side, 'incomplete window coverage')
    reference = {row['ordinal']: row for row in q['reference']}
    for row in q['candidate']:
        assert row['prepared']
        raw = reference[row['ordinal']]
        for key in ['good', 'total']:
            assert Decimal(row[key]) == Decimal(raw[key]), (q['sli'], row['ordinal'], key)
        for key in fields:
            assert row[key] == raw[key], (q['sli'], row['ordinal'], key)
        assert row['raw_rows'] <= 7200
        verified_windows += 1

def stats(values):
    values = sorted(values)
    return dict(samples=len(values), p50=values[math.ceil(len(values)*.5)-1], p95=values[math.ceil(len(values)*.95)-1], maximum=values[-1]) if values else dict(samples=0)

elapsed = (datetime.fromisoformat(results['after']['at']) - datetime.fromisoformat(results['before']['at'])).total_seconds()
report = dict(profile=results['profile'], result='not accepted: contention failed',
    elapsedSeconds=elapsed, pairedBatches=len(queries), independentlyVerifiedWindows=verified_windows,
    pendingBaseline=sum(any(not r['prepared'] for r in q['baseline']) for q in queries),
    pendingCandidate=0, fallbackBatches=sum(any(r['dirty_hours'] > 0 for r in q['candidate']) for q in queries),
    maximumEmittedRawRows=max(r['raw_rows'] for q in queries for r in q['candidate']),
    baselineMilliseconds=stats([q['baseline_ms'] for q in queries]),
    candidateMilliseconds=stats([q['candidate_ms'] for q in queries]),
    completedQuerySpanSeconds=(max(datetime.fromisoformat(q['at']) for q in queries)-min(datetime.fromisoformat(q['at']) for q in queries)).total_seconds(),
    completedQueriesPerMeasuredSecond=len(queries)/elapsed,
    browserMilliseconds={k: stats(v) for k,v in results['timings'].items()},
    live=results['live'], persistedObservationsDuringLoad=results['ingestedDuringLoad'],
    failedWorkers=sum(r['status']=='rejected' for r in results['outcomes']),
    artifactHashes={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in root.iterdir() if p.is_file()},
    caveats=['Query timing excludes client admission; closed-loop throughput is not an open-loop capacity result.',
      'Backfill exceeded its 180-second process deadline; exact completed batches were not recorded in this measured harness.',
      'Nine first-list successes were followed by navigation errors/timeouts; all ten browser workers failed. There are no completed detail samples.',
      'Live counts measure continuity, not ingestion handler latency. They do not establish data loss.',
      'The run combines backfill, baseline, candidate, raw reference and browser load; it cannot attribute interference to fallback alone.',
      'Emitted raw rows are not physical visits; separate EXPLAIN fixtures are reported in the task.'])
destination=Path('docs/evidence/alert-calculation')
destination.mkdir(parents=True,exist_ok=True)
(destination/'2026-11-13-contention.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps({k:v for k,v in report.items() if k not in ['profile','artifactHashes','caveats']},indent=2))
