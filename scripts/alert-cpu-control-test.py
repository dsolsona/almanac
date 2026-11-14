"""Focused orchestration checks: no Docker, network or workload."""
import importlib.util
import gzip
import io
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import tempfile
import os
import signal
import sys
import time
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
    def test_timeout_kills_descendant_after_direct_child_exits_on_term(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); pidfile = root / 'descendant.pid'
            child = "import os,signal,time,pathlib; signal.signal(signal.SIGTERM,signal.SIG_IGN); pathlib.Path('descendant.pid').write_text(str(os.getpid())); time.sleep(60)"
            parent = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c'," + repr(child) + "]); time.sleep(60)"
            pid = None
            try:
                result = cpu.Commands(root, root)([sys.executable, '-c', parent], .5)
                self.assertEqual(result.code, 124)
                pid = int(pidfile.read_text())
                def running():
                    try: return Path(f'/proc/{pid}/stat').read_text().split()[2] != 'Z'
                    except FileNotFoundError: return False
                deadline = time.monotonic() + 1
                while running() and time.monotonic() < deadline: time.sleep(.01)
                self.assertFalse(running(), 'timed-out command left a live descendant')
            finally:
                if pid is None and pidfile.exists(): pid = int(pidfile.read_text())
                if pid:
                    try: os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError: pass

    def test_missing_measurement_fields_cannot_be_complete_but_failed_values_remain(self):
        fields = {'ingestionOnly': ['accepted', 'duplicate', 'unsuccessful', 'p95LowerSeconds', 'p95UpperSeconds', 'elapsedSeconds'],
                  'ingestionUnderLoad': ['accepted', 'duplicate', 'unsuccessful', 'p95LowerSeconds', 'p95UpperSeconds', 'elapsedSeconds'],
                  'due': ['intervals', 'missing', 'slisWithMissing', 'nonObserved'],
                  'live': ['slis', 'intervals', 'maxReceiptDelaySeconds']}
        for section, names in fields.items():
            for field in names:
                with self.subTest(section=section, field=field), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory); target = root / 'safe'; target.mkdir()
                    calc, raw = self.staged_fixture(root, 'omitted-diagnostic')
                    del raw[section][field]
                    cpu.write_json(calc / 'results.json', raw)
                    self.assertFalse(cpu.stage_evidence(root, target, set(), 'omitted-diagnostic'))
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

    def exercise(self, fail=None, mismatch=False, backfill=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); commands = Commands(root, fail, mismatch)
            with patch.object(cpu.os, 'sched_getaffinity', side_effect=[{2, 5, 8, 11}, {8, 11}]), patch.object(cpu.os, 'sched_setaffinity'), \
                    patch.object(cpu, 'stage_evidence', return_value=True), patch.object(cpu, 'export_traces', return_value={'complete': True}):
                code = cpu.run('split', root, commands, **({'backfill': backfill} if backfill else {}))
            return code, commands.calls

    def test_omitted_mode_reaches_original_seed_and_measurement(self):
        code, calls = self.exercise(backfill='omitted-diagnostic')
        self.assertEqual(code, 0)
        for marker in ('--project=seed', '--run-baseline'):
            call = next(call for call in calls if marker in call[0])
            self.assertEqual(call[3]['ALERT_BACKFILL_MODE'], 'omitted-diagnostic')
            self.assertEqual(call[3]['ALERT_CALCULATION_MODE'], 'baseline-only')

    def test_cli_backfill_default_and_explicit_read_only_profile(self):
        for option, expected in (([], 'enabled'), (['--backfill', 'omitted-diagnostic'], 'omitted-diagnostic')):
            with patch.object(cpu.sys, 'argv', ['control', '--placement', 'shared', '--check-host'] + option), \
                    patch.object(cpu.os, 'sched_getaffinity', return_value={2, 5, 8, 11}), \
                    patch.object(cpu.os, 'sched_setaffinity') as affinity, patch.object(cpu, 'Commands') as commands, \
                    patch.object(cpu, 'write_json') as write, patch('sys.stdout', new_callable=io.StringIO) as output:
                self.assertEqual(cpu.main(), 0)
                profile = json.loads(output.getvalue())
                self.assertEqual(profile['backfill'], expected)
                self.assertEqual(profile['fullWorkload'], expected == 'enabled')
                self.assertFalse(profile['capacityAcceptance'])
                affinity.assert_not_called(); commands.assert_not_called(); write.assert_not_called()

    def staged_fixture(self, root, mode):
        """Complete bookkeeping input, not a simulated performance acceptance."""
        calc = root / '.local/alert-calculation'; calc.mkdir(parents=True)
        observer = root / '.local/runtime-observer/new'; observer.mkdir(parents=True)
        at = '2026-11-14T10:00:00Z'
        raw = {'before': {'at': at}, 'after': {'at': '2026-11-14T10:03:00Z'},
               'ingestionOnly': {'accepted': 20, 'duplicate': 0, 'unsuccessful': 0, 'p95LowerSeconds': .1, 'p95UpperSeconds': .2, 'elapsedSeconds': 30},
               'ingestionUnderLoad': {'accepted': 20, 'duplicate': 0, 'unsuccessful': 2, 'p95LowerSeconds': 1, 'p95UpperSeconds': None, 'elapsedSeconds': 180},
               'due': {'intervals': 200, 'missing': 1, 'slisWithMissing': 1, 'nonObserved': 1},
               'live': {'slis': 200, 'intervals': 200, 'maxReceiptDelaySeconds': 100},
               'profile': {'backfill': {'mode': mode, 'availableBatches': 1400,
                    'plannedBatches': 1400 if mode == 'enabled' else 0, 'fullWorkload': mode == 'enabled'}},
               'timings': {key: [1200] * 20 for key in ('list-cold', 'list-warm', 'detail-cold', 'detail-warm')},
               'apiTimings': {key: [1200] * 100 for key in ('overview', 'status', 'history')},
               'outcomes': [{'status': 'fulfilled'}] * 14,
               'completedQueryCalls': {'baseline': 800, 'candidate': 0, 'reference': 0}}
        cpu.write_json(calc / 'results.json', raw)
        cpu.write_json(calc / 'ingestion-baseline.json', {'baselineStart': {'at': at}, 'baselineEnd': {'at': at}})
        cpu.write_json(calc / 'summary.json', {'queryCount': 800, 'baselineMs': [1]})
        if mode == 'enabled':
            with (calc / 'backfill.jsonl').open('w') as output:
                for index in range(1400):
                    output.write(json.dumps({'completedBatch': index, 'at': at if index < 1399 else '2026-11-14T10:02:00Z',
                                             'secret': 'PRIVATE_BATCH'}) + '\n')
            cpu.write_json(calc / 'backfill.process.json', {'startedAt': at, 'finishedAt': '2026-11-14T10:02:01Z',
                                                         'private': 'PRIVATE_PROCESS'})
        else:
            cpu.write_json(calc / 'backfill.process.json', {'state': 'omitted-diagnostic', 'startedAt': None,
                                                         'finishedAt': None, 'plannedBatches': 0})
        cpu.write_json(observer / 'result.json', {'workload_exit_code': 1, 'samples': {'database': 1, 'resources': 1},
            'samplers_completed': {'database': True, 'resources': True}, 'processes_terminated': True, 'errors': []})
        (observer / 'database.jsonl').write_text(json.dumps({'at': at, 'backends': []}) + '\n')
        (observer / 'resources.jsonl').write_text(json.dumps({'at': at, 'resource': {'CPUPerc': '12.0%'}}) + '\n')
        return calc, raw

    def test_backfill_bookkeeping_and_timestamps_are_safe_in_both_modes(self):
        for mode in ('enabled', 'omitted-diagnostic'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); target = root / 'safe'; target.mkdir()
                self.staged_fixture(root, mode)
                self.assertTrue(cpu.stage_evidence(root, target, set(), mode))
                staged = json.loads((target / 'backfill.json').read_text())
                self.assertEqual(staged['plannedBatches'], 1400 if mode == 'enabled' else 0)
                self.assertEqual(staged['completedBatches'], staged['plannedBatches'])
                self.assertEqual(staged['fullWorkload'], mode == 'enabled')
                self.assertFalse(staged['capacityAcceptance'])
                if mode == 'enabled':
                    self.assertEqual(staged['firstCompletedBatchAt'], '2026-11-14T10:00:00+00:00')
                    self.assertEqual(staged['lastCompletedBatchAt'], '2026-11-14T10:02:00+00:00')
                    self.assertEqual(staged['process']['finishedAt'], '2026-11-14T10:02:01+00:00')
                else:
                    self.assertIsNone(staged['firstCompletedBatchAt'])
                    self.assertEqual(staged['process']['status'], 'omitted-diagnostic')
                self.assertNotIn('PRIVATE', ''.join(p.read_text() for p in target.iterdir()))

    def test_backfill_profile_mismatch_and_unexpected_writes_fail(self):
        for mutation in ('wrong_mode', 'wrong_count', 'wrong_full_workload', 'unexpected_batch', 'empty_output', 'started_process'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); target = root / 'safe'; target.mkdir()
                calc, raw = self.staged_fixture(root, 'omitted-diagnostic')
                if mutation == 'wrong_mode': raw['profile']['backfill']['mode'] = 'enabled'
                if mutation == 'wrong_count': raw['profile']['backfill']['plannedBatches'] = 1400
                if mutation == 'wrong_full_workload': raw['profile']['backfill']['fullWorkload'] = True
                if mutation == 'unexpected_batch': (calc / 'backfill.jsonl').write_text('{"completedBatch":0,"at":"2026-11-14T10:00:00Z"}\n')
                if mutation == 'empty_output': (calc / 'backfill.jsonl').write_text('')
                if mutation == 'started_process': cpu.write_json(calc / 'backfill.process.json', {'startedAt': '2026-11-14T10:00:00Z'})
                cpu.write_json(calc / 'results.json', raw)
                self.assertFalse(cpu.stage_evidence(root, target, set(), 'omitted-diagnostic'))
                if mutation == 'unexpected_batch':
                    staged = json.loads((target / 'backfill.json').read_text())
                    self.assertEqual(staged['completedBatches'], 1)

    def test_backfill_missing_boundary_timestamp_is_not_replaced_by_middle_batch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); target = root / 'safe'; target.mkdir()
            calc, _ = self.staged_fixture(root, 'enabled')
            path = calc / 'backfill.jsonl'
            rows = [json.loads(line) for line in path.read_text().splitlines()]
            rows[0].pop('at'); rows[-1].pop('at')
            path.write_text(''.join(json.dumps(row) + '\n' for row in rows))
            self.assertTrue(cpu.stage_evidence(root, target, set()))
            staged = json.loads((target / 'backfill.json').read_text())
            self.assertIsNone(staged['firstCompletedBatchAt'])
            self.assertIsNone(staged['lastCompletedBatchAt'])

    def test_backfill_timings_survive_missing_final_workload_result(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); target = root / 'safe'; target.mkdir()
            calc, _ = self.staged_fixture(root, 'enabled')
            (calc / 'results.json').unlink()
            self.assertFalse(cpu.stage_evidence(root, target, set()))
            staged = json.loads((target / 'backfill.json').read_text())
            self.assertEqual(staged['completedBatches'], 1400)
            self.assertEqual(staged['lastCompletedBatchAt'], '2026-11-14T10:02:00+00:00')

    def test_enabled_missing_or_extra_batch_fails(self):
        for mutation in ('missing', 'extra'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); target = root / 'safe'; target.mkdir()
                calc, _ = self.staged_fixture(root, 'enabled')
                path = calc / 'backfill.jsonl'
                lines = path.read_text().splitlines()
                path.write_text('\n'.join(lines[:-1] if mutation == 'missing' else lines + ['{"completedBatch":1400}']) + '\n')
                self.assertFalse(cpu.stage_evidence(root, target, set()))

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
