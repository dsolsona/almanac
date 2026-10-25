# Delivery status

## Current state

The first visual foundation task is under review. Shared controls, theme tokens,
the local heading font, catalogue and web verification harness are implemented.
No Go module, application binary, database schema, CLI or provider is implemented.
The scope of the design pass is the product described in SPEC-0001; further product
requirements may legitimately create additional specifications and ADRs.

| Records | Status | Evidence |
| --- | --- | --- |
| SPEC-0001, ADR-0001 | Accepted | Completed maintainer reviews recorded in document headers |
| SPEC-0002, ADR-0002 | Accepted | Completed maintainer reviews recorded in document headers |
| SPEC-0003, ADR-0003 | Accepted | Crit review `b7b16f696fed` approved after all comments resolved |
| SPEC-0004–0015 | Accepted | Crit review `7fd22977b6c0` approved with all comments resolved, 2026-10-24 |
| ADR-0004–0013 | Accepted | Same completed review; deferred encryption-library choice remains explicit |
| SPEC-0016–0017, ADR-0014–0015 | Accepted | Crit review `0de0c0e42db6` approved with no changes requested, 2026-10-24 |
| Product implementation | F1-T01 under review | Shared catalogue implemented; coordinator verification and review underway |

## Coverage

| Requirement | Primary specs |
| --- | --- |
| Single tenant, Go, preserved Almanac design | 0001, 0012, 0013 |
| Agent-owned collection, direct API ingestion | 0002, 0003, 0009, 0015 |
| Datasource host/auth/frequency, assignments and capability differences | 0005, 0009, 0015 |
| Prompt remote tests, versioned config and heartbeats | 0002, 0009 |
| Bounded live ring, isolated historical work and best-effort healing | 0003, 0008, 0009 |
| Periodic finite retries, visible gaps, manual repair | 0003, 0008, 0010, 0012 |
| PostgreSQL and measurement history | 0006, 0007 |
| Local/Google login, first-admin onboarding and scoped tokens | 0004, 0010, 0013 |
| Automation CLI with UI parity | 0010, 0012 |
| OpenSLO import and explicit execution compatibility | 0005, 0006 |
| SLOs, budgets, burn, reports and alerts | 0006, 0011 |
| Combined/separate runtime components and operational recovery | 0013 |
| Delivery sequence and meaningful verification | 0014 |
| Maintenance exclusions and SLO graph annotations | 0016, 0017 |

## Documentation checks

Checks cover local link targets, index/status consistency, unique numbered criteria,
review-note coverage, whitespace and publication constraints. Cross-domain review
reconciled permissions, snapshot precision, initial historical validity, lease timing,
restore credentials and live/historical resource limits. Numerical examples received
independent arithmetic checks where stated. These checks validate the design package,
not product correctness or scalability.

See [review guide](review-guide.md) for the highest-impact decisions, reading order,
and decision register. The completed review clarified mandatory initial-password
rotation, local users before Google SSO, calculation examples, composite sequencing,
React/API packaging and measurable UI performance. The encryption library remains a
candidate selection before encryption implementation. Web foundation checks now run;
no Go/API/database or full product-journey tests are claimed.

## Next work and session handoff

- The initial accepted design is committed as `09cfdfd`.
- The maintainer selected a primary coordinator and `gpt-6-luna` implementers.
  Follow AGENTS.md and [ways of working](ways-of-working.md) on every new session.
- [Testing guidance](testing.md) requires focused iteration, independent coordinator
  review, a real-stack smoke gate and full boundary/release evidence. Web foundation
  timings are measured below; real-stack and later boundary gates remain future work.
- The [visual foundation plan](plans/0001-visual-foundation.md) and
  [F1-T01](tasks/F1-T01.md) are active on `feat/f1-visual` in `.worktrees/f1-visual`.
  A `gpt-6-luna` implementer builds the catalogue; the coordinator owns acceptance.
- Resolve explicitly deferred choices before their affected task, including encryption
  library/key format; verify tool/dependency versions when scaffolding.
- Documentation validation uses `python3 scripts/check-docs.py`. Web build, unit and
  catalogue browser entry points exist; API/database/agent integration remains F1 work.

## Visual foundation verification

The coordinator independently ran `make verify`: component tests, typecheck,
production build, emitted-bundle exclusion and production routing smoke passed.
The initial component suite had 8 tests and took 0.97 seconds; the reviewed correction
adds a ninth regression test (focused suite: 1.16 seconds). The browser matrix
passed 36 checks in 15.3 seconds with two Chromium workers, at 1440/1024/360 px in
both themes. Production JavaScript measured 68.92 kB gzip. These are foundation
measurements, not the SLO workload benchmark or real-stack smoke acceptance.

Desktop and mobile screenshots in both themes received visual inspection. A missing
spacing-token mapping was corrected and guarded by computed layout assertions.
Review found a duplicate dialog title ID; negative verification found that an
unmatched test-file selector incorrectly ran all tests. Both were corrected in
`fd7a12a`, with a failing-then-passing dialog regression and a nonzero exit for an
unmatched selector. Manual screen-reader testing and Firefox/Safari coverage remain
outstanding; browser semantics, keyboard and computed contrast checks are partial
accessibility evidence only.

## Workflow preparation evidence

The coordinator reviewed the workflow and a fresh `gpt-6-luna` session performed an
advisory read-only review. Its CI wording finding was corrected: CI gates are required
future work, not running infrastructure. The documentation checker passed and rejected
isolated deliberate broken-link, duplicate-criterion and index-status fixtures, then
passed again after repair. These checks establish documentation tooling behavior only.
