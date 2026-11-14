# Review follow-up: complete and bounded diagnostic evidence

Reviewed base: `29035f22b12a2515427658ee820636602ba7e02c`. These changes address
four tooling findings without changing application behavior, workload sizes,
latency gates, calculation policy, or performance acceptance.

- [Analyzer coverage](https://github.com/dsolsona/almanac/pull/120#discussion_r4574690005):
  both candidate and reference must contain exactly four unique ordinals. Rows
  match by ordinal and the verified count comes from completed comparisons.
  The original recorded-run source fingerprint restriction remains intact.
- [Observer cleanup](https://github.com/dsolsona/almanac/pull/120#discussion_r4574690006):
  the workload owns a process group. Timeout, SIGTERM, interruption and ordinary
  leader exit all clean remaining group members. The existing 370-second workload
  deadline remains. Cleanup sends TERM with one second of grace, then KILL if
  necessary, bounds leader reaping and checks for remaining executing members.
  Linux zombies are nonexecuting; the check does not claim to reap grandchildren.
  Results include workload cleanup in `processes_terminated`; forced cleanup is
  recorded as an observer error. SIGTERM and timeout retain exit codes 143 and 124.
- [Status evidence](https://github.com/dsolsona/almanac/pull/120#discussion_r4574690023):
  both API lanes retain allowlisted calculation state, evaluated-through timestamp
  and calculation lag. Sanitized CI artifacts preserve them. Missing optional
  state remains null, not inferred ready. Compatible stale/failed responses and
  unavailable responses remain accepted HTTP response states under
  [SPEC-0019](../../specs/0019-backend-performance-and-derived-data.md).
  Every sample remains in the existing latency calculation; this adds no ready-only
  gate and does not establish the separate commit-to-visible currentness target.
- [Terminal output](https://github.com/dsolsona/almanac/pull/120#discussion_r4574690028):
  cancellation still starts at 295 seconds, terminal reporting begins by 299
  seconds, and forced termination remains at 300 seconds. Completed writes are
  acknowledged before voluntary exit; the hard timer remains active while stdout
  is pending. A permanently blocked reader can still lose output at the hard
  deadline. Complete output and unconditional bounded termination cannot both be
  guaranteed against such a reader; termination takes precedence.

## Focused verification

All checks ran through the Docker command catalogue. Test timings below are
suite-reported durations, not total container startup time. No application,
benchmark, browser workload, remote client or retained fixture was exercised.

The analyzer first failed four assertions in 1.019 seconds: missing/duplicate
coverage was accepted and reordered references were compared positionally.
Observer process regressions first failed twice in 0.310 seconds: a descendant
survived leader exit, and SIGTERM left no final result. CI staging initially
failed because it discarded the new state field (25 tests, 0.712 seconds).

New TypeScript checks initially failed to import the proposed evidence helper
(1.39 seconds). The first real-pipe test had two test setup defects: its exit
listener was attached after delaying reads, and the jsdom URL constructor
produced an HTTP asset URL for a Node import. The timeout run (6.45 seconds) and
subsequent explicit URL diagnostic (0.847 seconds) were preserved. The corrected
test registers listeners immediately and uses a Node file URL; no timeout was
increased.

Final commands and results:

```sh
make shell CMD='python3 docs/evidence/alert-calculation/test_analyze.py && python3 docs/evidence/alert-performance/test_observer.py && python3 docs/evidence/alert-performance/test_workload_cleanup.py && python3 scripts/alert-cpu-control-test.py && python3 -m py_compile docs/evidence/alert-calculation/analyze.py docs/evidence/alert-performance/observe-control.py scripts/alert-cpu-control.py'
make shell CMD='cd web && npm test -- tests/alert-evidence.test.tsx tests/alert-navigation.test.tsx && npm run typecheck'
```

Python: analyzer 2 passed in 0.620 seconds; existing observer 2 passed in 0.671
seconds; workload cleanup 3 passed in 2.396 seconds; CPU-control tooling 25 passed
in 0.750 seconds; syntax compilation passed. Process tests use disposable owned
children, fake Docker discovery, and an injected wait expiration for the timeout
case; they do not invoke Docker or Playwright workloads.

TypeScript: 24 passed in 2.12 seconds and typecheck passed. Real Node pipe tests
cover a delayed reader receiving a complete 1-MiB record and a permanently blocked
reader forcing termination. Fake-clock checks preserve the exact production
295/299/300-second schedule. These are protocol/process correctness checks, not
capacity or full five-minute workload acceptance. Independent source/test review
reported no actionable findings before commit.
