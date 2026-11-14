"""Fixed, opt-in CPU-placement diagnostic. Execute through make shell SOCKET=1.

Only the explicitly staged artifact directory is suitable for CI upload. Existing
fixture/report files and command logs remain private, including on failure.
"""
import argparse
from datetime import datetime, timedelta, timezone
import gzip
import hashlib
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
from typing import NamedTuple
import urllib.parse
import urllib.request


class Result(NamedTuple):
    code: int
    stdout: str = ''


class Failure(Exception):
    def __init__(self, code):
        self.code = code


def allocate(available, placement):
    ids = sorted(available)
    if len(ids) < 4 or placement not in ('shared', 'split'):
        raise Failure('requires_four_available_cpus')
    return ids[:2], ids[:2] if placement == 'shared' else ids[2:4]


def read_optional(path):
    try:
        return Path(path).read_text().strip()
    except OSError:
        return None


def host_profile(placement):
    if sys.platform != 'linux' or not hasattr(os, 'sched_getaffinity'):
        raise Failure('requires_linux_affinity')
    available = sorted(os.sched_getaffinity(0))
    server, client = allocate(available, placement)
    model = next((line.split(':', 1)[1].strip() for line in (read_optional('/proc/cpuinfo') or '').splitlines()
                  if line.startswith('model name')), 'unknown')
    memory = next((int(line.split()[1]) * 1024 for line in (read_optional('/proc/meminfo') or '').splitlines()
                   if line.startswith('MemTotal:')), None)
    topology = {}
    for cpu in available:
        root = Path(f'/sys/devices/system/cpu/cpu{cpu}/topology')
        topology[str(cpu)] = {key: read_optional(root / key) for key in
                              ('physical_package_id', 'core_id', 'thread_siblings_list')}
    return {'placement': placement, 'availableCPUs': available, 'serverCPUs': server, 'clientCPUs': client,
            'cpuModel': model, 'hostMemoryBytes': memory, 'topology': topology,
            'cgroupCPUMax': read_optional('/sys/fs/cgroup/cpu.max'),
            'cgroupMemoryMax': read_optional('/sys/fs/cgroup/memory.max'),
            'limitations': ['Same host memory, I/O and kernel; not a separate-machine acceptance run.',
                           'Logical CPU selection does not guarantee distinct physical cores; inspect topology.',
                           'Host daemons and unrelated containers are not pinned.',
                           'Synthetic historical writes bypass historical ingestion admission.',
                           'Observer and Docker clients share client CPU placement.'],
            'capacityAcceptance': False}


class Commands:
    """One private, bounded subprocess boundary; never return raw errors publicly."""
    def __init__(self, root, private):
        self.root, self.private, self.sequence = root, private, 0

    def __call__(self, argv, timeout, cwd='.', env=None):
        self.sequence += 1
        path = self.private / f'command-{self.sequence:03}.log'
        with path.open('wb') as output:
            child = subprocess.Popen(argv, cwd=self.root / cwd, env=env, stdout=output,
                                     stderr=subprocess.STDOUT, start_new_session=True)
            try:
                code = child.wait(timeout=timeout)
            except BaseException as error:
                # Only this command's process group; never terminate the operator stack.
                os.killpg(child.pid, signal.SIGTERM)
                try:
                    child.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    pass
                finally:
                    # A shell/observer can exit before its TERM-ignoring child.
                    # Reap the whole owned group even if the leader already exited.
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    child.wait(timeout=2)
                if not isinstance(error, subprocess.TimeoutExpired):
                    raise
                code = 124
        with path.open('rb') as saved:
            text = saved.read(8 * 1024 * 1024).decode('utf-8', errors='replace')
        return Result(code, text)


def allowed(container):
    return container.get('project') == 'almanac-e2e' or container.get('owned') == 'owned'


INSPECT = ('{"id":{{json .Id}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},'
           '"service":{{json (index .Config.Labels "com.docker.compose.service")}},'
           '"owned":{{json (index .Config.Labels "almanac.e2e")}},"cpuset":{{json .HostConfig.CpusetCpus}},'
           '"running":{{json .State.Running}},"image":{{json .Image}}}')
COMPOSE = ['docker', 'compose', '-p', 'almanac-e2e', '-f', 'compose.yaml', '-f', 'compose.e2e.yaml', '-f', 'compose.e2e.disk.yaml']


def discover(command, env):
    ids = set()
    for label in ('com.docker.compose.project=almanac-e2e', 'almanac.e2e=owned'):
        result = command(['docker', 'ps', '--quiet', '--no-trunc', '--filter', 'label=' + label], 10, env=env)
        if result.code:
            raise Failure('container_discovery_failed')
        ids.update(result.stdout.split())
    if not ids or len(ids) > 64 or any(not re.fullmatch('[a-f0-9]{64}', value) for value in ids):
        raise Failure('invalid_container_set')
    result = command(['docker', 'inspect', '--format', INSPECT, *sorted(ids)], 10, env=env)
    if result.code:
        raise Failure('container_inspection_failed')
    try:
        containers = [json.loads(line) for line in result.stdout.splitlines()]
        if {c['id'] for c in containers} != ids or any(not allowed(c) or c.get('running') is not True for c in containers):
            raise ValueError()
    except (KeyError, TypeError, ValueError):
        raise Failure('unowned_or_changed_container') from None
    return containers


def cpus(value):
    found = set()
    if not isinstance(value, str) or not re.fullmatch(r'\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*', value):
        return found
    for part in value.split(','):
        pair = [int(v) for v in part.split('-')]
        if max(pair) > 65535 or pair[-1] < pair[0]:
            return set()
        found.update(range(pair[0], pair[-1] + 1))
    return found


def pin_and_verify(command, env, assigned):
    containers = discover(command, env)
    setting = ','.join(map(str, assigned))
    result = command(['docker', 'update', '--cpuset-cpus', setting, *sorted(c['id'] for c in containers)], 30, env=env)
    if result.code:
        raise Failure('container_pin_failed')
    return verify(command, env, assigned)


def verify(command, env, assigned):
    containers = discover(command, env)
    if any(cpus(c.get('cpuset')) != set(assigned) for c in containers):
        raise Failure('container_cpuset_mismatch')
    services = {'app', 'postgres', 'victoriametrics', 'victoriatraces', 'loki', 'tempo', 'agent',
                'init', 'migrate', 'sources-init', 'sources-ready', 'provider-ready'}
    return [{'id': c['id'], 'role': c.get('service') if c.get('project') == 'almanac-e2e' and c.get('service') in services else 'owned-agent',
             'cpus': sorted(cpus(c['cpuset'])), 'image': c.get('image') if re.fullmatch(r'sha256:[a-f0-9]{64}', c.get('image') or '') else None}
            for c in containers]


def now():
    return datetime.now(timezone.utc).isoformat()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, allow_nan=False) + '\n')


def load(path, limit=32 * 1024 * 1024):
    if path.stat().st_size > limit:
        raise Failure('artifact_size_bound')
    return json.loads(path.read_text())


def number(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def numeric_fields(value, names):
    return {key: value[key] for key in names if isinstance(value, dict) and key in value and (number(value[key]) or value[key] is None)}


def timestamp(value):
    if not isinstance(value, str) or len(value) > 40:
        return None
    try:
        return datetime.fromisoformat(value.replace('Z', '+00:00')).isoformat()
    except ValueError:
        return None


def safe_timings(value, names):
    return {name: [v for v in value.get(name, []) if number(v)] for name in names
            if isinstance(value, dict) and isinstance(value.get(name), list)}


def safe_span(row):
    """Explicit fields only: no _msg/_stream, SQL, events, headers or exception text."""
    output = {}
    for key, length in [('trace_id', 32), ('span_id', 16), ('parent_span_id', 16)]:
        if re.fullmatch('[a-f0-9]{' + str(length) + '}', str(row.get(key, ''))):
            output[key] = row[key]
    if not {'trace_id', 'span_id'} <= output.keys():
        return None
    for key in ('duration', 'start_time_unix_nano', 'end_time_unix_nano', 'kind', 'status_code',
                'span_attr:http.response.status_code', 'span_attr:db.postgresql.backend_pid',
                'span_attr:db.response.returned_rows', 'span_attr:almanac.duration_ms'):
        if re.fullmatch(r'[0-9]{1,25}(?:\.[0-9]{1,12})?', str(row.get(key, ''))):
            output[key] = row[key]
    for key in ('name', 'resource_attr:service.name', 'span_attr:http.route', 'span_attr:db.query.summary',
                'span_attr:db.response.status_code', 'span_attr:almanac.traffic.class', 'span_attr:almanac.result',
                'span_attr:almanac.action', 'span_attr:almanac.journey'):
        value = row.get(key)
        if isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9_. /{}:-]{1,180}', value):
            output[key] = value
    return output


def safe_database(row):
    output = {'at': timestamp(row.get('at')), 'database': numeric_fields(row.get('database'),
        ('numbackends', 'xact_commit', 'xact_rollback', 'blks_read', 'blks_hit', 'tup_returned', 'tup_fetched',
         'tup_inserted', 'tup_updated', 'tup_deleted', 'temp_files', 'temp_bytes', 'deadlocks', 'blk_read_time', 'blk_write_time')),
        'backends': []}
    for backend in row.get('backends', [])[:64]:
        item = numeric_fields(backend, ('pid',))
        for key in ('backend_type', 'state', 'wait_event_type', 'wait_event', 'operation'):
            value = backend.get(key)
            if value is None or isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9_ ()-]{1,100}', value):
                item[key] = value
        item['blockers'] = [pid for pid in backend.get('blockers', [])[:64] if isinstance(pid, int)]
        output['backends'].append(item)
    return output


def safe_resource(row):
    output = {'at': timestamp(row.get('at')), 'resource': {}}
    source = row.get('resource', {})
    if re.fullmatch('[a-f0-9]{12,64}', str(source.get('ID', ''))):
        output['resource']['ID'] = source['ID']
    for key in ('CPUPerc', 'MemUsage', 'MemPerc', 'NetIO', 'BlockIO', 'PIDs'):
        value = source.get(key)
        if isinstance(value, str) and re.fullmatch(r'[0-9. %/kMGTPEiB]+', value) and len(value) < 80:
            output['resource'][key] = value
    return output


def backfill_settings(mode):
    if mode not in ('enabled', 'omitted-diagnostic'):
        raise Failure('invalid_backfill_mode')
    return {'backfill': mode, 'backfillBatches': 1400 if mode == 'enabled' else 0,
            'fullWorkload': mode == 'enabled', 'capacityAcceptance': False}


def stage_backfill(calculation, target, actual, mode):
    settings = backfill_settings(mode)
    planned = settings['backfillBatches']
    errors, completed, times = [], [], []
    safe = {'mode': mode, 'plannedBatches': planned, 'completedBatches': 0,
            'fullWorkload': settings['fullWorkload'], 'capacityAcceptance': False,
            'firstCompletedBatchAt': None, 'lastCompletedBatchAt': None,
            'process': {'status': 'unavailable', 'startedAt': None, 'finishedAt': None}}
    if (not isinstance(actual, dict) or actual.get('mode') != mode or
            type(actual.get('plannedBatches')) is not int or actual['plannedBatches'] != planned or
            actual.get('fullWorkload') is not settings['fullWorkload']):
        errors.append('backfill_profile_mismatch')
    output = calculation / 'backfill.jsonl'
    process_path = calculation / 'backfill.process.json'
    try:
        if output.exists():
            if output.stat().st_size > 16 * 1024 * 1024:
                raise Failure('backfill_size_bound')
            with output.open() as source:
                for line in source:
                    if len(line) > 65536:
                        raise Failure('backfill_line_bound')
                    if line.startswith('{'):
                        entry = json.loads(line)
                        batch = entry.get('completedBatch')
                        if type(batch) is not int or not 0 <= batch < 1400:
                            errors.append('invalid_backfill_batch')
                            continue
                        completed.append(batch)
                        times.append(timestamp(entry.get('at')))
        if mode == 'omitted-diagnostic':
            # Even an empty SQL output file contradicts the no-client contract.
            if output.exists():
                errors.append('unexpected_backfill_output')
            process = load(process_path)
            if (process.get('state') != mode or process.get('plannedBatches') != 0 or
                    process.get('startedAt') is not None or process.get('finishedAt') is not None or
                    any(key in process for key in ('code', 'signal', 'killed', 'inputError'))):
                errors.append('unexpected_backfill_process')
            else:
                safe['process']['status'] = mode
        else:
            if len(completed) != planned or len(set(completed)) != planned:
                errors.append('incomplete_backfill')
            if process_path.exists():
                process = load(process_path)
                started, finished = timestamp(process.get('startedAt')), timestamp(process.get('finishedAt'))
                failed = any(process.get(key) for key in ('code', 'signal', 'killed', 'inputError'))
                safe['process'] = {'startedAt': started, 'finishedAt': finished,
                                   'status': 'failed' if failed else 'finished' if started and finished else 'incomplete'}
                if failed:
                    errors.append('backfill_process_failed')
    except (OSError, ValueError, TypeError, AttributeError, Failure):
        errors.append('backfill_artifact_unavailable')
    safe['completedBatches'] = len(set(completed))
    # JSONL order follows the single SQL client's completed-batch statements.
    safe['firstCompletedBatchAt'] = times[0] if times else None
    safe['lastCompletedBatchAt'] = times[-1] if times else None
    write_json(target / 'backfill.json', safe)
    return errors


def stage_evidence(root, target, old_observers, backfill='enabled'):
    errors = []
    calculation = root / '.local/alert-calculation'
    raw, actual_backfill = {}, None
    try:
        raw = load(calculation / 'results.json')
        actual_backfill = raw.get('profile', {}).get('backfill')
    except (OSError, ValueError, TypeError, AttributeError, Failure):
        errors.append('workload_artifact_unavailable')
    # Partial SQL/process evidence remains useful if the workload never wrote its
    # final result. Missing profile/outcome still prevents complete evidence.
    errors.extend(stage_backfill(calculation, target, actual_backfill, backfill))
    try:
        safe = {'before': timestamp(raw.get('before', {}).get('at')), 'after': timestamp(raw.get('after', {}).get('at'))}
        baseline = load(calculation / 'ingestion-baseline.json')
        safe['baselineStart'] = timestamp(baseline.get('baselineStart', {}).get('at'))
        safe['baselineEnd'] = timestamp(baseline.get('baselineEnd', {}).get('at'))
        for key in ('ingestionOnly', 'ingestionUnderLoad'):
            safe[key] = numeric_fields(raw.get(key), ('accepted', 'duplicate', 'unsuccessful', 'p95LowerSeconds', 'p95UpperSeconds', 'elapsedSeconds'))
        safe['due'] = numeric_fields(raw.get('due'), ('intervals', 'missing', 'slisWithMissing', 'nonObserved'))
        safe['live'] = numeric_fields(raw.get('live'), ('slis', 'intervals', 'maxReceiptDelaySeconds'))
        required = {
            'ingestionOnly': ('accepted', 'duplicate', 'unsuccessful', 'p95LowerSeconds', 'p95UpperSeconds', 'elapsedSeconds'),
            'ingestionUnderLoad': ('accepted', 'duplicate', 'unsuccessful', 'p95LowerSeconds', 'p95UpperSeconds', 'elapsedSeconds'),
            'due': ('intervals', 'missing', 'slisWithMissing', 'nonObserved'),
            'live': ('slis', 'intervals', 'maxReceiptDelaySeconds'),
        }
        for section, fields in required.items():
            # Null histogram bounds / delay explicitly represent unavailable data;
            # omitted fields and nonnumeric counts never establish completeness.
            nullable = {'p95LowerSeconds', 'p95UpperSeconds', 'maxReceiptDelaySeconds'}
            if any(field not in safe[section] or
                   not number(safe[section][field]) and not (field in nullable and safe[section][field] is None)
                   for field in fields):
                errors.append('incomplete_measurements_' + section)
        safe['completedQueryCalls'] = numeric_fields(raw.get('completedQueryCalls'), ('baseline', 'candidate', 'reference'))
        safe['timings'] = safe_timings(raw.get('timings'), ('list-cold', 'list-warm', 'detail-cold', 'detail-warm'))
        safe['apiTimings'] = safe_timings(raw.get('apiTimings'), ('overview', 'status', 'history'))
        safe['apiSamples'] = [numeric_fields(sample, ('beganAt', 'finishedAt', 'milliseconds', 'status')) | {'name': sample['name']}
                              | ({'currentState': sample.get('currentState') if sample.get('currentState') in ('ready','stale','failed','unavailable') else None,
                                  'evaluatedThrough': timestamp(sample.get('evaluatedThrough')),
                                  'calculationLagSeconds': sample.get('calculationLagSeconds') if number(sample.get('calculationLagSeconds')) and sample['calculationLagSeconds'] >= 0 else None}
                                 if sample['name'] == 'status' else {})
                              for sample in raw.get('apiSamples', [])[:10000] if sample.get('name') in ('overview', 'status', 'history')]
        safe['outcomes'] = [{'status': entry['status']} if entry.get('status') == 'fulfilled' else {'status': 'rejected', 'code': 'lane_failed'}
                            for entry in raw.get('outcomes', [])[:32]]
        write_json(target / 'workload.json', safe)
        if (not safe['before'] or not safe['after'] or not safe['baselineStart'] or not safe['baselineEnd'] or len(safe['timings']) != 4 or
                any(len(values) != 20 for values in safe['timings'].values()) or
                len(safe['apiTimings']) != 3 or any(len(values) < 100 for values in safe['apiTimings'].values()) or
                len(safe['outcomes']) != 14 or any(v['status'] != 'fulfilled' for v in safe['outcomes']) or
                safe['completedQueryCalls'] != {'baseline': 800, 'candidate': 0, 'reference': 0}):
            errors.append('incomplete_workload_result')
        summary = load(calculation / 'summary.json')
        write_json(target / 'queries.json', numeric_fields(summary, ('queryCount', 'pendingBaseline')) |
                   {'baselineMS': [v for v in summary.get('baselineMs', []) if number(v)]})
    except (OSError, ValueError, TypeError, AttributeError, Failure):
        errors.append('workload_artifact_unavailable')
    observers = set((root / '.local/runtime-observer').glob('*')) - old_observers
    if len(observers) != 1:
        errors.append('observer_artifacts_ambiguous_or_missing')
    else:
        observer = observers.pop()
        try:
            result = load(observer / 'result.json')
            safe = {'workload_exit_code': result.get('workload_exit_code') if isinstance(result.get('workload_exit_code'), int) else None,
                    'finished_at': timestamp(result.get('finished_at')),
                    'samples': numeric_fields(result.get('samples'), ('database', 'resources')),
                    'samplers_completed': {name: result.get('samplers_completed', {}).get(name) is True for name in ('database', 'resources')},
                    'processes_terminated': result.get('processes_terminated') is True,
                    'workload_cleanup': {key: result.get('workload_cleanup', {}).get(key) is True for key in ('started','terminated','forced')},
                    'errors': [{'code': 'observer_error'} for _ in result.get('errors', [])[:100]]}
            write_json(target / 'observer-result.json', safe)
            if safe['workload_exit_code'] is None or safe['errors'] or not safe['processes_terminated'] or not all(safe['samplers_completed'].values()):
                errors.append('observer_incomplete')
            for name, sanitize in [('database', safe_database), ('resources', safe_resource)]:
                source = observer / (name + '.jsonl')
                if source.stat().st_size > 17 * 1024 * 1024:
                    raise Failure('observer_size_bound')
                count = 0
                with source.open() as incoming, (target / (name + '.jsonl')).open('w') as outgoing:
                    for line in incoming:
                        outgoing.write(json.dumps(sanitize(json.loads(line)), allow_nan=False) + '\n')
                        count += 1
                if count == 0 or count != safe['samples'].get(name):
                    errors.append('observer_sample_count_mismatch')
        except (OSError, ValueError, TypeError, AttributeError, Failure):
            errors.append('observer_artifact_unavailable')
    write_json(target / 'evidence-result.json', {'complete': not errors, 'errors': errors})
    return not errors


def export_traces(root, target):
    result = {'complete': False, 'rows': 0, 'rawRows': 0, 'requests': 0, 'errors': [],
              'exportStartedAt': now(), 'completenessScope': 'query-pagination-only',
              'ingestionLagNotEstablished': True,
              'limitations': ['No exporter flush/drain barrier; spans may arrive after this export.',
                              'Complete means uncapped queried pages, not that every emitted span arrived.']}
    began = time.monotonic()
    seen, byte_count = set(), 0
    try:
        baseline = load(root / '.local/alert-calculation/ingestion-baseline.json')
        workload = load(root / '.local/alert-calculation/results.json')
        start = datetime.fromisoformat(baseline['baselineStart']['at'].replace('Z', '+00:00')) - timedelta(seconds=2)
        finish = min(datetime.now(timezone.utc), datetime.fromisoformat(workload['after']['at'].replace('Z', '+00:00')) + timedelta(seconds=10))
        if not 0 < (finish - start).total_seconds() <= 600:
            raise Failure('trace_time_bound')
        result.update({'from': start.isoformat(), 'through': finish.isoformat()})

        def read_range(first, last):
            if time.monotonic() - began >= 120 or result['requests'] >= 256:
                raise Failure('trace_export_bound')
            result['requests'] += 1
            query = urllib.parse.urlencode({'query': f'_time:[{first.isoformat()},{last.isoformat()}]', 'limit': 10000})
            with urllib.request.urlopen('http://localhost:19528/select/logsql/query?' + query,
                                        timeout=min(10, max(.1, 120 - (time.monotonic() - began)))) as response:
                chunks, size = [], 0
                while True:
                    if time.monotonic() - began >= 120:
                        raise Failure('trace_export_bound')
                    # read1 returns available bytes instead of waiting to fill the whole response.
                    chunk = response.read1(65536)
                    if not chunk:
                        break
                    chunks.append(chunk)
                    size += len(chunk)
                    if size > 16 * 1024 * 1024:
                        raise Failure('trace_response_bound')
                body = b''.join(chunks)
            if len(body) > 16 * 1024 * 1024:
                raise Failure('trace_response_bound')
            lines = body.splitlines()
            if len(lines) >= 10000:
                if (last - first).total_seconds() <= .001:
                    raise Failure('trace_cap_unresolved')
                middle = first + (last - first) / 2
                yield from read_range(first, middle)
                yield from read_range(middle, last)
            else:
                yield from lines

        with gzip.open(target / 'traces.jsonl.gz', 'wt') as outgoing:
            while start < finish:
                end = min(start + timedelta(seconds=10), finish)
                for line in read_range(start, end):
                    result['rawRows'] += 1
                    span = safe_span(json.loads(line))
                    if span is None:
                        continue
                    key = (span['trace_id'], span['span_id'])
                    if key in seen:
                        continue
                    seen.add(key)
                    encoded = json.dumps(span) + '\n'
                    byte_count += len(encoded)
                    if byte_count > 128 * 1024 * 1024 or len(seen) > 500000:
                        raise Failure('trace_output_bound')
                    outgoing.write(encoded)
                    result['rows'] += 1
                start = end
        result['complete'] = result['rows'] > 0
        if not result['complete']:
            result['errors'].append('empty_trace_export')
    except (OSError, ValueError, KeyError, TypeError, AttributeError, Failure) as error:
        result['errors'].append(error.code if isinstance(error, Failure) else 'trace_export_failed')
    result['seconds'] = time.monotonic() - began
    result['exportFinishedAt'] = now()
    write_json(target / 'trace-result.json', result)
    return result


def run(placement, root, command=None, backfill='enabled'):
    # All preconditions before Docker, affinity mutation or even artifact creation.
    try:
        profile = host_profile(placement)
        profile.update(backfill_settings(backfill))
    except Failure:
        return 2
    target = root / '.local/alert-cpu-control-artifacts' / placement
    private = root / '.local/alert-cpu-control-private' / placement
    if target.exists() or private.exists() or (root / '.local/alert-calculation').exists():
        return 2
    os.umask(0o077)
    target.mkdir(parents=True)
    private.mkdir(parents=True)
    execute = command or Commands(root, private)
    command_records = []

    def command(argv, timeout, cwd='.', env=None):
        started = time.monotonic()
        record = {'index': len(command_records) + 1, 'phase': phase, 'deadlineSeconds': timeout, 'exitCode': None}
        command_records.append(record)
        try:
            result = execute(argv, timeout, cwd=cwd, env=env)
            record['exitCode'] = result.code
            return result
        finally:
            record['elapsedSeconds'] = time.monotonic() - started
    env = os.environ | {'E2E_POSTGRES_STORAGE': 'disk', 'ALERT_CALCULATION_MODE': 'baseline-only',
                        'ALERT_BACKFILL_MODE': backfill, 'COMPOSE_PROJECT_NAME': 'almanac-e2e',
                        'E2E_BASE_URL': 'https://localhost:18188', 'E2E_PORT': '18188',
                        'E2E_METRICS_PORT': '18528', 'E2E_TRACES_PORT': '19528',
                        'E2E_METRICS_URL': 'http://localhost:18528', 'E2E_TRACES_URL': 'http://localhost:19528',
                        'ALMANAC_PORT': '18188', 'ALMANAC_METRICS_PORT': '18528', 'ALMANAC_TRACES_PORT': '19528',
                        'ALMANAC_LOKI_PORT': '13101', 'ALMANAC_TEMPO_PORT': '13201', 'ALMANAC_TEMPO_OTLP_PORT': '14319'}
    old_observers = set((root / '.local/runtime-observer').glob('*'))
    profile.update({'startedAt': now(), 'sourceHashes': {}, 'commands': command_records,
                    'workload': {'mode': 'baseline-only', **backfill_settings(backfill), 'SLIs': 200,
                                 'baselineCalls': 800, 'browserJourneys': 80,
                                 'APIUsers': 10, 'minimumCyclesPerAPIUser': 10, 'APICadenceSeconds': 10,
                                 'seedTestTimeoutSeconds': 1800, 'calculationTestTimeoutSeconds': 300}})
    for name in ('scripts/alert-cpu-control.py', 'scripts/e2e.sh', 'docs/evidence/alert-performance/observe-control.py',
                 'web/perf/alert-calculation.perf.ts', 'web/perf/alert-calculation.seed.ts',
                 'web/playwright.alert-calculation.config.ts', 'compose.yaml', 'compose.e2e.yaml', 'compose.e2e.disk.yaml'):
        path = root / name
        if path.is_file():
            profile['sourceHashes'][name] = hashlib.sha256(path.read_bytes()).hexdigest()
    code, cleanup_code, phase, failure = 1, None, 'affinity', None
    touched = False
    try:
        os.sched_setaffinity(0, profile['clientCPUs'])
        profile['clientEffectiveCPUs'] = sorted(os.sched_getaffinity(0))
        if profile['clientEffectiveCPUs'] != profile['clientCPUs']:
            raise Failure('client_affinity_mismatch')
        revision = command(['git', 'rev-parse', 'HEAD'], 10, env=env)
        profile['revision'] = revision.stdout.strip() if re.fullmatch('[a-f0-9]{40}', revision.stdout.strip()) else None
        phase = 'setup'
        touched = True
        setup = command(['sh', 'scripts/e2e.sh', 'up'], 1800, env=env)
        if setup.code:
            code = setup.code
            raise Failure('setup_failed')
        phase = 'pin_before_restart'
        profile['initialAssignments'] = pin_and_verify(command, env, profile['serverCPUs'])
        apps = [c for c in profile['initialAssignments'] if c['role'] == 'app']
        if len(apps) != 1:
            raise Failure('app_container_missing')
        for argv, timeout in [(COMPOSE + ['restart', 'app'], 120),
                              (COMPOSE + ['up', '--detach', '--wait', '--wait-timeout', '90', 'app'], 120)]:
            result = command(argv, timeout, env=env)
            if result.code:
                code = result.code
                raise Failure('app_readiness_failed')
        # Compose must not have recreated/reconfigured the app onto operator ports
        # or lost the CPU setting while establishing readiness.
        profile['preSeedAssignments'] = verify(command, env, profile['serverCPUs'])
        phase = 'seed'
        seeded = command(['./node_modules/.bin/playwright', 'test', '--config', 'playwright.alert-calculation.config.ts', '--project=seed'],
                         1860, cwd='web', env=env)
        if seeded.code:
            code = seeded.code
            raise Failure('seed_failed')
        phase = 'pin_after_seed'
        profile['measuredAssignments'] = pin_and_verify(command, env, profile['serverCPUs'])
        phase = 'measurement'
        write_json(target / 'profile.json', profile)
        measured = command(['python3', 'docs/evidence/alert-performance/observe-control.py', '--run-baseline'], 390, env=env)
        code = measured.code
        phase = 'verify_after_measurement'
        profile['finalAssignments'] = verify(command, env, profile['serverCPUs'])
    except Failure as error:
        failure = error.code
        code = code or 1
    except BaseException:
        failure, code = 'orchestration_failed', code or 1
    finally:
        try:
            if not stage_evidence(root, target, old_observers, backfill):
                code = code or 1
            if not export_traces(root, target)['complete']:
                code = code or 1
        except BaseException:
            failure, code = 'evidence_staging_failed', code or 1
        finally:
            profile['lastWorkPhase'] = phase
            if touched:
                phase = 'cleanup'
                try:
                    cleanup_code = command(['sh', 'scripts/e2e.sh', 'down'], 120, env=env).code
                    checks = []
                    # e2e.sh intentionally tolerates Compose down failure; establish
                    # removal independently, including stopped owned containers.
                    for label in ('com.docker.compose.project=almanac-e2e', 'almanac.e2e=owned'):
                        remaining = command(['docker', 'ps', '--all', '--quiet', '--no-trunc', '--filter', 'label=' + label], 10, env=env)
                        checks.append({'exitCode': remaining.code, 'remainingCount': len(remaining.stdout.split()) if not remaining.code else None})
                        cleanup_code = cleanup_code or remaining.code or (1 if remaining.stdout.strip() else 0)
                    profile['cleanupChecks'] = checks
                except BaseException:
                    cleanup_code = cleanup_code or 1
                code = code or cleanup_code
            try:
                os.sched_setaffinity(0, profile['availableCPUs'])
            except OSError:
                failure, code = failure or 'affinity_restore_failed', code or 1
            profile.update({'finishedAt': now(), 'lastPhase': phase, 'failure': failure, 'exitCode': code, 'cleanupExitCode': cleanup_code})
            write_json(target / 'profile.json', profile)
    return code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--placement', required=True, choices=('shared', 'split'))
    parser.add_argument('--backfill', choices=('enabled', 'omitted-diagnostic'), default='enabled')
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--check-host', action='store_true')
    mode.add_argument('--run', action='store_true')
    args = parser.parse_args()
    if args.check_host:
        try:
            print(json.dumps(host_profile(args.placement) | backfill_settings(args.backfill)))
            return 0
        except Failure as error:
            print(json.dumps({'supported': False, 'error': error.code}))
            return 2
    return run(args.placement, Path(__file__).resolve().parent.parent, backfill=args.backfill)


if __name__ == '__main__':
    raise SystemExit(main())
