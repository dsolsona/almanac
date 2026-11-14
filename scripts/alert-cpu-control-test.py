"""Focused orchestration checks: no Docker, network or workload."""
import importlib.util
import gzip
import io
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('cpu_control', Path(__file__).with_name('alert-cpu-control.py'))
cpu = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cpu)


def container(identifier, service=None, owned=False, cpuset=''):
    return {'id': identifier * 64, 'project': 'almanac-e2e' if service else '',
            'service': service or '', 'owned': 'owned' if owned else '',
            'cpuset': cpuset, 'running': True}


class Commands:
    def __init__(self, root, fail=None, mismatch=False):
        self.root, self.fail, self.mismatch = root, fail, mismatch
        self.calls = []
        self.containers = [container('a', 'app'), container('b', 'postgres')]

    def __call__(self, argv, timeout, cwd='.', env=None):
        self.calls.append((argv, timeout, cwd, env))
        if argv[:2] == ['git', 'rev-parse']:
            return cpu.Result(0, 'a' * 40)
        if argv[:2] == ['docker', 'ps']:
            return cpu.Result(0, '\n'.join(c['id'] for c in self.containers))
        if argv[:2] == ['docker', 'inspect']:
            return cpu.Result(0, '\n'.join(json.dumps(c) for c in self.containers if c['id'] in argv))
        if argv[:2] == ['docker', 'update']:
            for c in self.containers:
                if c['id'] in argv and not self.mismatch:
                    c['cpuset'] = argv[3]
        if '--wait-timeout' in argv and self.fail == 'recreated_app':
            self.containers[0]['cpuset'] = ''
        if '--project=seed' in argv:
            self.containers.append(container('c', owned=True))
            if self.fail == 'seed':
                return cpu.Result(7, 'PRIVATE_SEED_TOKEN')
        if '--run-baseline' in argv:
            if self.fail == 'measurement':
                return cpu.Result(9, 'PRIVATE_EXCEPTION')
        if argv == ['sh', 'scripts/e2e.sh', 'down'] and self.fail != 'cleanup_remains':
            self.containers = []
        return cpu.Result(0, '')


class ControlTests(unittest.TestCase):
    def test_two_cpu_refusal_precedes_commands_or_affinity_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            commands = Commands(Path(directory))
            with patch.object(cpu.os, 'sched_getaffinity', return_value={1, 3}), patch.object(cpu.os, 'sched_setaffinity') as affinity:
                self.assertNotEqual(cpu.run('shared', Path(directory), commands), 0)
                self.assertEqual(commands.calls, [])
                affinity.assert_not_called()
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_noncontiguous_cpu_allocation(self):
        self.assertEqual(cpu.allocate({2, 5, 8, 11, 14}, 'shared'), ([2, 5], [2, 5]))
        self.assertEqual(cpu.allocate({2, 5, 8, 11}, 'split'), ([2, 5], [8, 11]))

    def test_only_owned_container_labels(self):
        self.assertTrue(cpu.allowed(container('a', 'app')))
        self.assertTrue(cpu.allowed(container('b', owned=True)))
        alien = container('d', 'app'); alien['project'] = 'almanac-local'
        self.assertFalse(cpu.allowed(alien))
        self.assertFalse(cpu.allowed(container('e')))

    def exercise(self, fail=None, mismatch=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); commands = Commands(root, fail, mismatch)
            with patch.object(cpu.os, 'sched_getaffinity', side_effect=[{2, 5, 8, 11}, {8, 11}]), patch.object(cpu.os, 'sched_setaffinity'), \
                    patch.object(cpu, 'stage_evidence', return_value=True), patch.object(cpu, 'export_traces', return_value={'complete': True}):
                code = cpu.run('split', root, commands)
            return code, commands.calls

    def test_new_seed_agent_pinned_and_original_workload_invoked(self):
        code, calls = self.exercise()
        self.assertEqual(code, 0)
        updates = [argv for argv, *_ in calls if argv[:2] == ['docker', 'update']]
        self.assertTrue(any('c' * 64 in argv and argv[3] == '2,5' for argv in updates))
        seed = next(call for call in calls if '--project=seed' in call[0])
        self.assertEqual(seed[0], ['./node_modules/.bin/playwright', 'test', '--config', 'playwright.alert-calculation.config.ts', '--project=seed'])
        self.assertEqual(seed[2], 'web')
        measure = next(call for call in calls if '--run-baseline' in call[0])
        self.assertEqual(measure[0], ['python3', 'docs/evidence/alert-performance/observe-control.py', '--run-baseline'])
        self.assertEqual(measure[3]['ALERT_CALCULATION_MODE'], 'baseline-only')
        self.assertEqual(measure[3]['ALERT_BACKFILL_MODE'], 'enabled')
        self.assertEqual(measure[3]['E2E_POSTGRES_STORAGE'], 'disk')
        for argv, _, _, env in calls:
            if argv[:2] == ['docker', 'compose']:
                self.assertEqual({key: env[key] for key in ('ALMANAC_PORT', 'ALMANAC_METRICS_PORT', 'ALMANAC_TRACES_PORT',
                    'ALMANAC_LOKI_PORT', 'ALMANAC_TEMPO_PORT', 'ALMANAC_TEMPO_OTLP_PORT')},
                    dict(ALMANAC_PORT='18188', ALMANAC_METRICS_PORT='18528', ALMANAC_TRACES_PORT='19528',
                         ALMANAC_LOKI_PORT='13101', ALMANAC_TEMPO_PORT='13201', ALMANAC_TEMPO_OTLP_PORT='14319'))

    def test_readiness_recreation_losing_cpuset_prevents_seed(self):
        code, calls = self.exercise(fail='recreated_app')
        self.assertNotEqual(code, 0)
        self.assertFalse(any('--project=seed' in argv for argv, *_ in calls))

    def test_bad_cpuset_prevents_workload(self):
        code, calls = self.exercise(mismatch=True)
        self.assertNotEqual(code, 0)
        self.assertFalse(any('--run-baseline' in argv for argv, *_ in calls))
        self.assertIn(['sh', 'scripts/e2e.sh', 'down'], [argv for argv, *_ in calls])

    def test_seed_and_measurement_failure_survive_cleanup(self):
        for stage, expected in [('seed', 7), ('measurement', 9)]:
            code, calls = self.exercise(fail=stage)
            self.assertEqual(code, expected)
            self.assertIn(['sh', 'scripts/e2e.sh', 'down'], [argv for argv, *_ in calls])

    def test_safe_trace_drops_payloads_errors_headers_and_sql(self):
        raw = {'trace_id': 'a' * 32, 'span_id': 'b' * 16, 'name': 'db.ReadDerivedDue', 'duration': '123',
               'span_attr:db.query.summary': 'ReadDerivedDue', 'span_attr:db.statement': 'PRIVATE_SQL',
               'span_attr:http.request.header.cookie': 'PRIVATE_SESSION', '_msg': 'PRIVATE_BODY', 'exception': 'PRIVATE_ERROR'}
        safe = cpu.safe_span(raw)
        self.assertNotIn('PRIVATE', json.dumps(safe))
        self.assertEqual(safe['span_attr:db.query.summary'], 'ReadDerivedDue')

    def test_missing_outcome_cannot_stage_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); target = root / 'safe'; target.mkdir()
            self.assertFalse(cpu.stage_evidence(root, target, set()))
            self.assertNotIn('PRIVATE', ''.join(p.read_text() for p in target.glob('*.json')))

    def test_staged_artifacts_drop_raw_secrets_and_reject_incomplete_counts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); target = root / 'safe'; target.mkdir()
            calc = root / '.local/alert-calculation'; calc.mkdir(parents=True)
            observer = root / '.local/runtime-observer/new'; observer.mkdir(parents=True)
            raw = {'before': {'at': '2026-11-14T10:00:00Z'}, 'after': {'at': '2026-11-14T10:01:00Z'},
                   'timings': {key: [] for key in ('list-cold', 'list-warm', 'detail-cold', 'detail-warm')},
                   'outcomes': [{'status': 'rejected', 'reason': 'PRIVATE_EXCEPTION'}], 'password': 'PRIVATE_PASSWORD',
                   'apiSamples': [{'name': 'overview', 'status': 503, 'body': 'PRIVATE_BODY', 'headers': {'cookie': 'PRIVATE_COOKIE'}}]}
            (calc / 'results.json').write_text(json.dumps(raw))
            (calc / 'ingestion-baseline.json').write_text(json.dumps({'baselineStart': {'at': '2026-11-14T09:59:00Z'},
                'baselineEnd': {'at': '2026-11-14T10:00:00Z'}, 'secret': 'PRIVATE_BASELINE'}))
            (calc / 'backfill.jsonl').write_text('{"completedBatch":0,"password":"PRIVATE_TOKEN"}\n')
            (calc / 'summary.json').write_text('{"queryCount":1,"baselineMs":[1],"sql":"PRIVATE_SQL"}')
            (observer / 'result.json').write_text(json.dumps({'workload_exit_code': 0, 'samples': {'database': 1, 'resources': 1},
                'samplers_completed': {'database': True, 'resources': True}, 'processes_terminated': True,
                'errors': [{'error': 'PRIVATE_STDERR'}]}))
            (observer / 'database.jsonl').write_text('{"at":"2026-11-14T10:00:00Z","backends":[],"query":"PRIVATE_SQL"}\n')
            (observer / 'resources.jsonl').write_text('{"at":"2026-11-14T10:00:00Z","resource":{"CPUPerc":"12.0%","Names":"PRIVATE_NAME"}}\n')
            self.assertFalse(cpu.stage_evidence(root, target, set()))
            staged = ''.join(p.read_text() for p in target.iterdir())
            self.assertNotIn('PRIVATE', staged)
            self.assertIn('incomplete_workload_result', staged)
            self.assertIn('503', staged)
            self.assertIn('2026-11-14T09:59:00+00:00', staged)

    def test_cleanup_failure_and_missing_evidence_prevent_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); commands = Commands(root)
            def fail_cleanup(argv, *args, **kwargs):
                result = commands(argv, *args, **kwargs)
                return cpu.Result(12) if argv == ['sh', 'scripts/e2e.sh', 'down'] else result
            with patch.object(cpu.os, 'sched_getaffinity', side_effect=[{2, 5, 8, 11}, {8, 11}]), patch.object(cpu.os, 'sched_setaffinity'), \
                    patch.object(cpu, 'stage_evidence', return_value=True), patch.object(cpu, 'export_traces', return_value={'complete': True}):
                self.assertEqual(cpu.run('split', root, fail_cleanup), 12)

    def test_discovery_refuses_unrelated_container_returned_by_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            commands = Commands(Path(directory))
            commands.containers.append(container('d'))
            with self.assertRaises(cpu.Failure):
                cpu.pin_and_verify(commands, {}, [2, 5])
            self.assertFalse(any(argv[:2] == ['docker', 'update'] for argv, *_ in commands.calls))

    def test_trace_export_splits_capped_pages_deduplicates_and_labels_arrival_gap(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); target = root / 'safe'; target.mkdir()
            calc = root / '.local/alert-calculation'; calc.mkdir(parents=True)
            at = datetime.now(timezone.utc)
            (calc / 'ingestion-baseline.json').write_text(json.dumps({'baselineStart': {'at': (at - timedelta(seconds=3)).isoformat()}}))
            (calc / 'results.json').write_text(json.dumps({'after': {'at': (at - timedelta(seconds=1)).isoformat()}}))
            span = json.dumps({'trace_id': 'a' * 32, 'span_id': 'b' * 16, 'name': 'ui.slo.list',
                               'duration': '100', '_msg': 'PRIVATE_BODY', 'span_attr:db.statement': 'PRIVATE_SQL'}).encode() + b'\n'
            responses = [io.BytesIO(b'{}\n' * 10000), io.BytesIO(span), io.BytesIO(span)]
            with patch.object(cpu.urllib.request, 'urlopen', side_effect=responses) as request:
                result = cpu.export_traces(root, target)
            self.assertTrue(result['complete']); self.assertEqual(result['rows'], 1)
            self.assertEqual(request.call_count, 3)
            self.assertEqual(result['completenessScope'], 'query-pagination-only')
            self.assertTrue(result['ingestionLagNotEstablished'])
            with gzip.open(target / 'traces.jsonl.gz', 'rt') as spans:
                self.assertNotIn('PRIVATE', spans.read())

    def test_cleanup_failure_does_not_replace_original_seed_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); commands = Commands(root, fail='seed')
            def fail_cleanup(argv, *args, **kwargs):
                result = commands(argv, *args, **kwargs)
                return cpu.Result(12) if argv == ['sh', 'scripts/e2e.sh', 'down'] else result
            with patch.object(cpu.os, 'sched_getaffinity', side_effect=[{2, 5, 8, 11}, {8, 11}]), patch.object(cpu.os, 'sched_setaffinity'), \
                    patch.object(cpu, 'stage_evidence', return_value=True), patch.object(cpu, 'export_traces', return_value={'complete': True}):
                self.assertEqual(cpu.run('split', root, fail_cleanup), 7)

    def test_swallowed_cleanup_failure_is_detected(self):
        code, calls = self.exercise(fail='cleanup_remains')
        self.assertNotEqual(code, 0)
        down_index = next(i for i, call in enumerate(calls) if call[0] == ['sh', 'scripts/e2e.sh', 'down'])
        self.assertTrue(any(argv[:2] == ['docker', 'ps'] and '--all' in argv for argv, *_ in calls[down_index+1:]))

    def test_cleanup_inspection_error_retains_original_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); commands = Commands(root, fail='seed'); cleaning = False
            def broken_inspection(argv, *args, **kwargs):
                nonlocal cleaning
                if argv == ['sh', 'scripts/e2e.sh', 'down']:
                    cleaning = True
                if cleaning and argv[:2] == ['docker', 'ps']:
                    return cpu.Result(17, 'PRIVATE_DOCKER_ERROR')
                return commands(argv, *args, **kwargs)
            with patch.object(cpu.os, 'sched_getaffinity', side_effect=[{2, 5, 8, 11}, {8, 11}]), patch.object(cpu.os, 'sched_setaffinity'), \
                    patch.object(cpu, 'stage_evidence', return_value=True), patch.object(cpu, 'export_traces', return_value={'complete': True}):
                self.assertEqual(cpu.run('split', root, broken_inspection), 7)
            profile = json.loads((root / '.local/alert-cpu-control-artifacts/split/profile.json').read_text())
            self.assertEqual(profile['cleanupExitCode'], 17)


if __name__ == '__main__':
    unittest.main()
