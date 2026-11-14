"""Disposable process tests only; no app, Docker daemon, or database."""
import contextlib
import io
import json
import os
from pathlib import Path
import signal
import runpy
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('observe-control.py').resolve()
REAL_POPEN = subprocess.Popen

class WorkloadCleanup(unittest.TestCase):
    def check_observer(self, interrupt):
        child = None; descendant = None
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'web/node_modules/.bin').mkdir(parents=True)
            docker=root/'docker'; docker.write_text('#!/bin/sh\nif [ "$1" = ps ]; then echo fixture; fi\n'); docker.chmod(0o755)
            workload=root/'web/node_modules/.bin/playwright'
            workload.write_text('#!'+sys.executable+'\nimport os,subprocess,sys,time\n'
                'p=subprocess.Popen([sys.executable,"-c","import signal,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);time.sleep(60)"])\n'
                'open("../descendant","w").write(str(p.pid))\n'
                + ('time.sleep(60)\n' if interrupt else 'time.sleep(.1)\n'))
            workload.chmod(0o755)
            try:
                child=subprocess.Popen([sys.executable,str(SCRIPT),'--run-baseline'],cwd=root,
                    env=os.environ|{'PATH':str(root)+os.pathsep+os.environ['PATH']},stdout=subprocess.PIPE,stderr=subprocess.PIPE)
                deadline=time.monotonic()+5
                while not (root/'descendant').exists() and time.monotonic()<deadline: time.sleep(.01)
                self.assertTrue((root/'descendant').exists(),'owned workload did not start')
                descendant=int((root/'descendant').read_text())
                if interrupt: child.send_signal(signal.SIGTERM)
                child.communicate(timeout=8)
                report=list((root/'.local/runtime-observer').glob('*/result.json'))
                self.assertEqual(len(report),1)
                result=json.loads(report[0].read_text())
                state=Path(f'/proc/{descendant}/stat')
                live=state.exists() and state.read_text().rsplit(')',1)[1].split()[0]!='Z'
                self.assertFalse(live,'workload descendant survived observer')
                self.assertTrue(result['processes_terminated'])
                self.assertTrue(result['workload_cleanup']['terminated'])
                self.assertTrue(result['workload_cleanup']['forced'])
                if interrupt:
                    self.assertEqual(child.returncode,143)
                    self.assertEqual(result['workload_exit_code'],143)
            finally:
                if descendant:
                    try: os.kill(descendant,signal.SIGKILL)
                    except ProcessLookupError: pass
                if child and child.poll() is None:
                    child.kill();child.communicate(timeout=2)

    def test_exited_leader_does_not_leave_descendants(self): self.check_observer(False)
    def test_sigterm_cleans_workload_and_records_interruption(self): self.check_observer(True)

    def test_timeout_cleans_real_group_and_preserves_124(self):
        children = []
        old_mask = os.umask(0o077)
        with tempfile.TemporaryDirectory() as directory, contextlib.chdir(directory):
            Path('web').mkdir()
            marker = Path(directory)/'descendant'
            class DeadlineProcess(REAL_POPEN):
                def wait(self, timeout=None):
                    if timeout == 370:
                        deadline = time.monotonic()+3
                        while not marker.exists() and time.monotonic()<deadline: time.sleep(.01)
                        if not marker.exists(): raise AssertionError('descendant did not start')
                        raise subprocess.TimeoutExpired('owned-test-workload', timeout)
                    return super().wait(timeout=timeout)
            def launch(command, **options):
                if command[0] == 'docker': raise OSError('injected sampler startup failure')
                self.assertEqual(command, ['./node_modules/.bin/playwright','test','--config','playwright.alert-calculation.config.ts','--project=calculation','--no-deps'])
                code = ('import os,signal,time\npid=os.fork()\nif pid==0:\n'
                        ' signal.signal(signal.SIGTERM,signal.SIG_IGN)\n'
                        f' open({str(marker)!r},"w").write(str(os.getpid()))\n'
                        'time.sleep(60)\n')
                child = DeadlineProcess([sys.executable,'-c',code],**options)
                children.append(child)
                return child
            try:
                with patch.object(sys,'argv',[str(SCRIPT),'--run-baseline']), \
                     patch.object(subprocess,'check_output',return_value='fixture\n'), \
                     patch.object(subprocess,'Popen',side_effect=launch), contextlib.redirect_stdout(io.StringIO()):
                    with self.assertRaises(SystemExit) as exit:
                        runpy.run_path(str(SCRIPT),run_name='__main__')
                    self.assertEqual(exit.exception.code,124)
                result=json.loads(next(Path('.local/runtime-observer').glob('*/result.json')).read_text())
                self.assertEqual(result['workload_exit_code'],124)
                self.assertTrue(result['processes_terminated'])
                self.assertTrue(result['workload_cleanup']['forced'])
                state=Path(f'/proc/{int(marker.read_text())}/stat')
                self.assertFalse(state.exists() and state.read_text().rsplit(')',1)[1].split()[0]!='Z')
            finally:
                os.umask(old_mask)
                if marker.exists():
                    try: os.kill(int(marker.read_text()),signal.SIGKILL)
                    except ProcessLookupError: pass
                for child in children:
                    if child.poll() is None: child.kill();child.wait(timeout=2)

if __name__=='__main__': unittest.main()
