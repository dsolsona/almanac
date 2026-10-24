# Delivery status

## Current state

Documentation only. No Go module, application binary, database schema, UI, CLI,
provider executable, build/test commands, or deployment artifacts are implemented.
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
| Product implementation | Not started | No runtime verification or passing product tests claimed |

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
candidate selection before encryption implementation. No product tests have run.
