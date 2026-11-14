"""Recorded-run analyzer coverage checks; no application or database."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('analyze.py').resolve()

class Coverage(unittest.TestCase):
    def analyze(self, mutate):
        rows = [dict(ordinal=n, prepared=True, good='1.00', total='2', observed=1,
                     known_empty=0, zero_traffic=0, first_observation=None,
                     last_observation=None, latest_receipt=None, raw_rows=1, dirty_hours=0)
                for n in range(1, 5)]
        batch = dict(candidate=rows, reference=copy.deepcopy(rows), baseline=rows,
                     sli='fixture', at='2026-11-13T00:00:01Z', baseline_ms=1, candidate_ms=1)
        queries = [copy.deepcopy(batch) for _ in range(800)]
        mutate(queries[0])
        result = dict(profile={'sourceHashes': {'perf/alert-calculation.perf.ts':
                     'b85ea3e9238cf41742c7fb1c2f642dcf0d6f2efe92e6704b1ed4d0d12bdc35bf'}},
                     before={'at':'2026-11-13T00:00:00Z'}, after={'at':'2026-11-13T00:00:02Z'},
                     timings={}, live={}, ingestedDuringLoad=0, outcomes=[])
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)/'.local/alert-calculation'; root.mkdir(parents=True)
            (root/'results.json').write_text(json.dumps(result))
            for worker in range(2):
                (root/f'queries-{worker}.jsonl').write_text('\n'.join(map(json.dumps, queries[worker*400:(worker+1)*400])))
            return subprocess.run([sys.executable,str(SCRIPT)],cwd=temp,capture_output=True,text=True,timeout=5)

    def test_missing_and_duplicate_windows_cannot_claim_complete_verification(self):
        for side in ('candidate','reference'):
            for kind in ('missing','duplicate'):
                with self.subTest(side=side,kind=kind):
                    def mutate(q):
                        if kind == 'missing': q[side].pop()
                        else: q[side][-1] = copy.deepcopy(q[side][0])
                    self.assertNotEqual(self.analyze(mutate).returncode,0)

    def test_complete_reordered_windows_are_matched_by_ordinal(self):
        def mutate(q):
            for side in ('candidate','reference'):
                for row in q[side]: row['good']=str(row['ordinal'])
            q['reference'].reverse()
        result=self.analyze(mutate)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(json.loads(result.stdout)['independentlyVerifiedWindows'],3200)

if __name__ == '__main__': unittest.main()
