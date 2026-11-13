"""Opt-in framing regression against the retained isolated PostgreSQL fixture.

Run through make shell SOCKET=1. Holds two read-only transactions briefly, then
rolls back and closes both. Does not seed/reset or write application data.
"""
import ast
import json
import os
from pathlib import Path
import selectors
import subprocess
import sys
import unittest

SCRIPT = Path(os.environ.get('OBSERVER_UNDER_TEST', Path(__file__).with_name('observe-control.py'))).resolve()


class ObserverPostgresFraming(unittest.TestCase):
    def test_multiple_backends_remain_one_json_record_per_sample(self):
        os.umask(0o077)
        root = Path('.local/observer-framing')
        root.mkdir(parents=True, exist_ok=True)
        containers = subprocess.check_output(['docker', 'ps', '--quiet', '--filter',
            'label=com.docker.compose.project=almanac-e2e', '--filter',
            'label=com.docker.compose.service=postgres'], text=True, timeout=5).splitlines()
        self.assertEqual(len(containers), 1)
        command = ['docker', 'exec', '-i', containers[0], 'psql', '--no-psqlrc', '--quiet',
            '--tuples-only', '--no-align', '-v', 'ON_ERROR_STOP=1', '-U', 'almanac', '-d', 'almanac', '-f', '-']
        # Extract the actual observer SQL without executing its CLI or duplicating
        # a reduced approximation of the query under test.
        module = ast.parse(SCRIPT.read_text())
        query = next(ast.literal_eval(node.value) for node in module.body
                     if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name)
                     and target.id == 'query' for target in node.targets))
        children, pids = [], []
        try:
            for _ in range(2):
                child = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE, text=True, bufsize=1)
                children.append(child)
                child.stdin.write("SET statement_timeout='250ms'; SET idle_in_transaction_session_timeout='15s'; BEGIN READ ONLY; SELECT pg_backend_pid();\n")
                child.stdin.flush()
                with selectors.DefaultSelector() as ready:
                    ready.register(child.stdout, selectors.EVENT_READ)
                    self.assertTrue(ready.select(timeout=5), 'read-only holder did not start')
                pids.append(int(child.stdout.readline().strip()))
            sample = subprocess.run(command, input="SET statement_timeout='250ms';\n" + query + query,
                text=True, capture_output=True, timeout=5, check=True)
            (root / 'database.raw').write_text(sample.stdout)
            (root / 'database.stderr').write_text(sample.stderr)
            lines = sample.stdout.splitlines()
            # readline() is the observer's protocol: each query must produce one
            # complete JSON record, including when json_agg has multiple rows.
            rows = [json.loads(line) for line in lines]
            self.assertEqual(len(rows), 2)
            for row in rows:
                self.assertTrue(set(pids).issubset({backend['pid'] for backend in row['backends']}))
            smoke = subprocess.run([sys.executable, str(SCRIPT), '--smoke'],
                text=True, capture_output=True, timeout=12)
            (root / 'smoke.stdout').write_text(smoke.stdout)
            (root / 'smoke.stderr').write_text(smoke.stderr)
            self.assertEqual(smoke.returncode, 0, smoke.stdout + smoke.stderr)
            result = json.loads(smoke.stdout.strip().splitlines()[-1])
            self.assertEqual(result['errors'], [])
            self.assertTrue(result['processes_terminated'])
            samples = [json.loads(line) for line in (Path(result['artifacts']) / 'database.jsonl').read_text().splitlines()]
            self.assertGreaterEqual(len(samples), 2)
            self.assertTrue(all(set(pids).issubset({backend['pid'] for backend in row['backends']}) for row in samples))
            print(json.dumps({'holders': pids, 'framing_records': len(rows), **result}))
        finally:
            for child in children:
                try:
                    child.communicate('ROLLBACK;\n', timeout=3)
                except (subprocess.TimeoutExpired, BrokenPipeError):
                    child.terminate()
                    try:
                        child.wait(timeout=1)
                    except subprocess.TimeoutExpired:
                        child.kill()
                        child.wait(timeout=1)


if __name__ == '__main__':
    unittest.main()
