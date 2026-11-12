# Trace and log datasource UI evidence

Captured by the real authenticated `trace-log-datasources.spec.ts` journeys on
2026-11-11, after successful remote connection tests. These are disposable test
records, not maintainer production data. Journey screenshots are written under
`.local/e2e/datasource-renders/` and packaged here for review; subsequent test runs
do not overwrite the committed evidence. Each source then previews and collects
9 good / 10 total events and feeds a 95% SLO: exact attainment 0.900000000000,
budget remaining −100%, burn 2.000000000000.

| Provider | Light desktop (1440 px) | Dark desktop (1440 px) | Dark mobile (360 px) |
| --- | --- | --- | --- |
| VictoriaTraces | [Render](victoriatraces-light-1440.png) | [Render](victoriatraces-dark-1440.png) | [Render](victoriatraces-dark-360.png) |
| Tempo | [Render](tempo-light-1440.png) | [Render](tempo-dark-1440.png) | [Render](tempo-dark-360.png) |
| Loki | [Render](loki-light-1440.png) | [Render](loki-dark-1440.png) | [Render](loki-dark-360.png) |

The three-step Identity → Connection → Agent composition and current left
navigation retain the [datasource reference](../../design/mocks/ds-create.html).
Four active choices use a two-column desktop grid and a single column on mobile.
Native radio controls support arrow-key selection and preserve a compatible agent.
The journeys exercise invalid-endpoint correction, real connection/preview results,
SLI query language labels, ordinary save/navigation and absence of horizontal overflow.

Verification on application checkpoint `ee0e22a`: `make check` passes in 560 observed
seconds (451 frontend tests, PostgreSQL storage 202.627 seconds, 82 browser cases
in 3.6 minutes). `make test-e2e` passes all 20 journeys in 5.6 minutes; its complete
build/start/cleanup command takes 408 seconds. Optional persistent source startup,
stop and restart pass; their durations were not captured. Independent real-source
nanosecond-boundary probes count exactly two included events for all three sources.
The probes run through the Docker development environment on their own fixtures.

The first full E2E run had an unrelated Settings timing miss. Preserve both the
[initial 1,098.965 ms trace](settings-initial-timing.json) and the
[final 941.745 ms trace](settings-final-timing.json). The <1,000 ms assertion is
unchanged; the initial cold-load variation is not claimed fixed. Source-test
fixture corrections and all earlier failures remain in [F2-T34](../../tasks/F2-T34.md).
No large-fleet/year-long performance, historical backfill, custom tenant-header
editor or sampled-population extrapolation is claimed.


Post-publication follow-up preserves storage read errors, improves bounded CLI
failure evidence, uses an interval-count metric as the Prometheus placeholder,
and moves future journey renders to ignored output. These leave the packaged
new-datasource forms unchanged. The follow-up's fast suite passes, but its whole
integration run preserves a CLI diagnostic wait timeout; a focused reproduction
passes once without establishing a fix. See the task and published-head CI for
this separate unresolved variation rather than inferring all follow-up checks pass.

The first CodeRabbit review is tracked in F2-T34. Its follow-up reserves LogsQL
query options for a later profile, makes Compose readiness health-based, clears
queries across provider changes and adds visible HTTP authentication guidance.
The exact source override reproduction is 2 canonical events versus 4 with the
global filter ignored, and 0 with a one-hour time shift. Focused UI checks pass
86 tests in 21.99 s. Review follow-up full verification and refreshed renders follow.


### Review follow-up evidence

The nine PNGs are refreshed from the final local real-container journeys, after
successful native connection tests. All three new journeys pass again (36.3 s
VictoriaTraces, 44.7 s Tempo, 34.8 s Loki). Review `make check` passes 453 frontend
cases and all 82 browser cases; all four CI application jobs pass on `a5030c9`.
The latest local whole E2E test phase is 5.8 min: 18 pass, one Settings cold-load
check fails at 1,049.912801 ms and its dependent restart case does not run. See
[the third sanitized timing artifact](settings-timing-review.json). No retry or
changed timeout is used. This is an unresolved timing variation, not a claimed fix.
The following Unicode guard and fresh-draft corrections pass focused Go/UI checks;
see F2-T34 for the published-head status and open HTTP policy discussion.

### Integrated application checkpoint

On `94fabcb`, integrated with main `850f2c0`, all four application CI jobs pass:
470 frontend tests, 89 browser cases, PostgreSQL integration and 21 real-container
journeys (5.6 min E2E test phase). All three new source journeys pass against the
accepted SLI detail layout. The nine datasource form renders remain representative;
the integration changes the SLI detail, not these forms.

The stable local browser suite passes all 89 cases in 216.203 s. A preceding
intermediate-dark Charts failure is traced to a Vite reload triggered by a source
comment edit during the suite; F2-T34 preserves the failure and exact evidence.
The [final CI Settings timing](settings-timing-ci.json) is 509.303 ms / users
137.197 ms. This independent CI result does not establish a fix for the retained
local cold-load variation or earlier CLI/PTY failures.
