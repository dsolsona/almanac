"""Bounded diagnostics for one explicitly selected, isolated baseline control.

Run only inside the development container with Docker socket access. This does
not reset/seed a fixture or change application budgets. The existing harness
rejects previously measured fixtures. Smoke mode observes only, without workload.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import threading
import time

parser = argparse.ArgumentParser()
mode = parser.add_mutually_exclusive_group(required=True)
mode.add_argument('--smoke', action='store_true')
mode.add_argument('--run-baseline', action='store_true')
args = parser.parse_args()
os.umask(0o077)
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
root = Path('.local/runtime-observer') / stamp
root.mkdir(parents=True)
stop = threading.Event()
errors = []
counts = {'database': 0, 'resources': 0}
completed = {'database': False, 'resources': False}
children = {}
children_lock = threading.Lock()
def launch(name, command, **options):
    child = subprocess.Popen(command, **options)
    with children_lock:
        children[name] = child
    if stop.is_set() and child.poll() is None:
        child.terminate()
    return child

def guarded_sampler(name, function):
    try:
        function()
    except Exception as exc:
        errors.append({'stream': name, 'error': str(exc)})
    finally:
        completed[name] = True

def now():
    return datetime.now(timezone.utc).isoformat()
def docker(*values):
    return subprocess.check_output(['docker', *values], text=True, timeout=10).strip()
postgres = docker('ps', '--quiet', '--filter', 'label=com.docker.compose.project=almanac-e2e',
                  '--filter', 'label=com.docker.compose.service=postgres').splitlines()
if len(postgres) != 1:
    raise SystemExit('expected one running isolated PostgreSQL container')
containers = set(docker('ps', '--quiet', '--filter', 'label=com.docker.compose.project=almanac-e2e').splitlines())
containers.update(docker('ps', '--quiet', '--filter', 'label=almanac.e2e=owned').splitlines())
containers.update(docker('ps', '--quiet', '--filter', 'name=almanac-dev-dev-run').splitlines())
containers.discard('')
query = """SELECT json_build_object('at',clock_timestamp(),'observer_pid',pg_backend_pid(),
'track_io_timing',current_setting('track_io_timing'),'backends',(
 SELECT coalesce(json_agg(sample),'[]'::json) FROM (
  SELECT pid,backend_type,state,wait_event_type,wait_event,
   CASE WHEN wait_event_type='Lock' THEN pg_blocking_pids(pid) ELSE '{}'::integer[] END AS blockers,
   coalesce((regexp_match(query,'^-- name: ([A-Za-z0-9_]{1,80}) :[a-z]+'))[1],'other') AS operation
  FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
   AND (state='active' OR state LIKE 'idle in transaction%') ORDER BY pid LIMIT 64
 ) sample),'database',(
 SELECT row_to_json(sample) FROM (
  SELECT numbackends,xact_commit,xact_rollback,blks_read,blks_hit,tup_returned,tup_fetched,
   tup_inserted,tup_updated,tup_deleted,temp_files,temp_bytes,deadlocks,blk_read_time,blk_write_time
  FROM pg_stat_database WHERE datname=current_database()
 ) sample));
"""
profile = {'started_at': now(), 'mode': 'smoke' if args.smoke else 'baseline-only',
           'sample_interval_seconds': 0.1, 'database_statement_timeout_ms': 250,
           'database_lock_timeout_ms': 100, 'max_output_bytes_per_stream': 16 * 1024 * 1024,
           'containers': sorted(containers), 'postgres': postgres[0],
           'limitations': ['Observer adds one read-only database connection and resource sampling.',
                          'Use identical observation in ingestion-only and loaded phases.',
                          'A sampled wait distribution can miss short waits; absence is not proof of no contention.',
                          'Database statement spans include driver/network/scheduling time.']}
(root / 'profile.json').write_text(json.dumps(profile, indent=2) + '\n')

def database_sampler():
    with (root / 'database.jsonl').open('w') as output, (root / 'database.stderr').open('w') as stderr:
        child = launch('database', ['docker', 'exec', '-i', postgres[0], 'psql', '--no-psqlrc',
            '--quiet', '--tuples-only', '--no-align', '-v', 'ON_ERROR_STOP=1', '-U', 'almanac', '-d', 'almanac', '-f', '-'],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr, text=True, bufsize=1)
        try:
            child.stdin.write("SET application_name='almanac-performance-diagnostic'; SET statement_timeout='250ms'; SET lock_timeout='100ms';\n")
            while not stop.is_set():
                began = time.monotonic()
                child.stdin.write(query)
                child.stdin.flush()
                line = child.stdout.readline()
                if not line:
                    raise RuntimeError('database sampler ended before a complete sample')
                row = json.loads(line)
                if not isinstance(row.get('backends'), list):
                    raise RuntimeError('invalid database sample')
                output.write(json.dumps(row) + '\n')
                output.flush()
                counts['database'] += 1
                if output.tell() > profile['max_output_bytes_per_stream']:
                    raise RuntimeError('database diagnostic size bound reached')
                stop.wait(max(0, 0.1 - (time.monotonic() - began)))
        except Exception as exc:
            errors.append({'stream': 'database', 'error': str(exc)})
        finally:
            try:
                child.stdin.close()
                code = child.wait(timeout=5)
                if code:
                    errors.append({'stream': 'database', 'exit_code': code})
            except Exception as exc:
                errors.append({'stream': 'database_cleanup', 'error': str(exc)})

def resource_sampler():
    with (root / 'resources.jsonl').open('w') as output, (root / 'resources.stderr').open('w') as stderr, (root / 'resources.raw').open('w') as raw:
        child = launch('resources', ['docker', 'stats', '--no-trunc', '--format', '{{json .}}', *sorted(containers)],
                                 stdout=subprocess.PIPE, stderr=stderr, text=True, bufsize=1)
        def terminate():
            stop.wait()
            if child.poll() is None:
                child.terminate()
        terminator = threading.Thread(target=terminate, daemon=True)
        terminator.start()
        try:
            for line in child.stdout:
                raw.write(line)
                raw.flush()
                # Streaming stats emits cursor-home/erase controls even to a pipe.
                # JSON encodes control characters; these literal terminal bytes
                # are framing, not resource values. Preserve the raw frames too.
                line = re.sub(r'\x1b\[[0-9;]*[HJK]', '', line)
                if not line.strip():
                    continue
                output.write(json.dumps({'at': now(), 'resource': json.loads(line)}) + '\n')
                output.flush()
                counts['resources'] += 1
                if output.tell() > profile['max_output_bytes_per_stream']:
                    raise RuntimeError('resource diagnostic size bound reached')
        except Exception as exc:
            errors.append({'stream': 'resources', 'error': str(exc)})
            if child.poll() is None:
                child.terminate()
        finally:
            try:
                code = child.wait(timeout=5)
                if not stop.is_set():
                    errors.append({'stream': 'resources', 'exit_code': code, 'error': 'resource stream ended before workload'})
            except subprocess.TimeoutExpired:
                errors.append({'stream': 'resources_cleanup', 'error': 'stats client did not terminate'})
                child.kill()
                child.wait(timeout=5)

threads = [threading.Thread(target=guarded_sampler, args=(name, function), daemon=True)
           for name, function in [('database', database_sampler), ('resources', resource_sampler)]]
for thread in threads:
    thread.start()
exit_code = None
try:
    if args.smoke:
        time.sleep(3)
        exit_code = 0
    else:
        with (root / 'workload.log').open('w') as log:
            env = os.environ | {'ALERT_CALCULATION_MODE': 'baseline-only', 'E2E_POSTGRES_STORAGE': 'disk'}
            run = subprocess.run(['./node_modules/.bin/playwright', 'test', '--config', 'playwright.alert-calculation.config.ts',
                                  '--project=calculation', '--no-deps'], cwd='web', env=env,
                                 stdout=log, stderr=subprocess.STDOUT, timeout=370)
            exit_code = run.returncode
except subprocess.TimeoutExpired:
    exit_code = 124
    errors.append({'stream': 'workload', 'error': 'wrapper deadline reached; inspect child processes before reusing runtime lane'})
except BaseException:
    exit_code = 1
    raise
finally:
    stop.set()
    for thread in threads:
        thread.join(timeout=0.5)
    # A PostgreSQL statement timeout does not bound Docker's transport. Supervise
    # owned clients even when a reader thread cannot reach its own finally block.
    with children_lock:
        owned = list(children.items())
    for name, child in owned:
        if child.poll() is None:
            errors.append({'stream': name, 'error': 'forced sampler shutdown; inspect remote session if transport failed'})
            try:
                child.terminate()
                try:
                    child.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=1)
            except Exception as exc:
                errors.append({'stream': name, 'cleanup_error': str(exc)})
    for thread in threads:
        thread.join(timeout=1)
        if thread.is_alive():
            errors.append({'stream': 'cleanup', 'error': 'sampler thread did not terminate'})
    for name in completed:
        if not completed[name] or counts[name] == 0:
            errors.append({'stream': name, 'error': 'sampler did not complete with samples'})
    result = {'finished_at': now(), 'workload_exit_code': exit_code, 'samples': counts,
              'samplers_completed': completed, 'processes_terminated': all(child.poll() is not None for _, child in owned),
              'errors': errors}
    (root / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps({'artifacts': str(root), **result}))
if args.smoke and (counts['database'] < 2 or counts['resources'] < 1):
    raise SystemExit('observer smoke lacks complete samples')
raise SystemExit(exit_code or (1 if errors else 0))
