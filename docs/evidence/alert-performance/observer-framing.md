# Multi-backend observer framing correction

This diagnostic correction belongs to [F4-T14](../../tasks/F4-T14.md). It changes
no application code, workload, sampling limits or performance acceptance budget.
Base: `925d156`.

The instrumented baseline observation at `20261113T222406Z` completed its workload
with performance failures, but its database sampler stopped after 16 samples with
`Expecting value: line 2 column 1 (char 276)`. Its 8,604 resource samples remain
available. That incomplete database stream cannot establish a whole-run wait
distribution; this correction does not retroactively repair it.

The focused regression holds two real read-only PostgreSQL transactions open,
extracts the actual observer SQL, and executes two samples. Before correction it
fails in **0.499 s** with a JSON decoding error on the first line. Preserved raw
output confirms that `json_agg(record)` separates backend records with literal
newlines inside the outer `json_build_object`. Consequently, `readline()` receives
only part of a sample once multiple backend rows are present. Original query and
raw output are retained privately under `.local/observer-framing/`.

The correction changes the outer constructor to `jsonb_build_object`, which
normalizes the embedded JSON into one output line. Fields, query limits, statement
timeouts, sampling interval, secrecy restrictions and cleanup remain unchanged.

```sh
make shell SOCKET=1 CMD='python3 docs/evidence/alert-performance/test_observer_postgres.py && python3 docs/evidence/alert-performance/test_observer.py'
```

The corrected PostgreSQL check and actual smoke pass in **3.800 s**. Both owned
backend PIDs occur in each of two directly decoded records and every smoke database
sample. The smoke records **30 database / 54 resource samples**, zero errors,
completed samplers and terminated observer clients. The two read-only transactions
are rolled back and their clients closed in bounded cleanup. Smoke artifacts are
`.local/runtime-observer/20261113T223050Z` in the isolated correction worktree.
The unchanged two sampler-startup/blocked-output fault tests pass in **0.681 s**.

No fixture reset, seed or load run was performed. These checks verify observer
framing and cleanup only; the baseline performance failures remain open.
