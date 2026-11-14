"""Fault checks for the diagnostic observer; no Docker daemon or app is used."""
import contextlib
import io
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPT = Path(os.environ.get('OBSERVER_UNDER_TEST', Path(__file__).with_name('observe-control.py'))).resolve()
REAL_POPEN = subprocess.Popen
REAL_SLEEP = time.sleep

class ObserverFailures(unittest.TestCase):
    def check_failure(self, blocked):
        processes = []
        def launch(command, **options):
            self.assertEqual(command[0], 'docker')
            self.assertIn(command[1], ['exec', 'stats'])
            if not blocked:
                raise OSError('injected sampler creation failure')
            child = REAL_POPEN([sys.executable, '-c', 'import time; time.sleep(60)'], **options)
            processes.append(child)
            return child
        started = time.monotonic()
        old_mask = os.umask(0o077)
        try:
            with tempfile.TemporaryDirectory() as directory, contextlib.chdir(directory), \
                 patch.object(sys, 'argv', [str(SCRIPT), '--smoke']), \
                 patch.object(subprocess, 'check_output', return_value='fixture-container\n'), \
                 patch.object(subprocess, 'Popen', side_effect=launch), \
                 patch.object(time, 'sleep', side_effect=lambda _: REAL_SLEEP(0.05)), \
                 contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as outcome:
                    runpy.run_path(str(SCRIPT), run_name='__main__')
                self.assertNotEqual(outcome.exception.code, 0)
                reports = list(Path('.local/runtime-observer').glob('*/result.json'))
                self.assertEqual(len(reports), 1)
                result = json.loads(reports[0].read_text())
                self.assertTrue(result['errors'])
                self.assertTrue(all(child.poll() is not None for child in processes))
                self.assertEqual(result.get('samplers_completed'), {'database': True, 'resources': True})
                for name in ['database', 'resources']:
                    self.assertTrue(any(error['stream'] == name for error in result['errors']))
        finally:
            os.umask(old_mask)
            for child in processes:
                if child.poll() is None:
                    child.kill()
                    child.wait(timeout=2)
        self.assertLess(time.monotonic() - started, 10)

    def test_startup_failure_is_recorded(self):
        self.check_failure(False)

    def test_blocked_output_has_bounded_cleanup(self):
        self.check_failure(True)

if __name__ == '__main__':
    unittest.main()
