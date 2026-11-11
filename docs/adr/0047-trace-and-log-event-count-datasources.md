# ADR-0047: Trace and log event-count datasources

- Status: accepted
- Date: 2026-11-11
- Approval: The maintainer requested VictoriaTraces (Tempo) and Loki support,
  authorized assumptions and implementation decisions, and requested a PR with
  the decisions recorded for later discussion.
- Related: [SPEC-0015](../specs/0015-datasource-providers.md),
  [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)

## Decision

Expose separate VictoriaTraces, Grafana Tempo and Loki provider choices alongside
Prometheus. VictoriaTraces' Tempo search compatibility does not imply TraceQL
metrics compatibility. Use its native LogsQL statistics API for span counts,
Tempo's TraceQL metrics API for span counts, and Loki's LogQL metrics API for log
counts. Never count truncated trace searches or log search pages.

Keep the current event-count SLI and occurrence SLO model. Each component returns
one integral count for the datasource interval; good must be a subset of total.
Availability is a good-event filter; latency is a duration filter on individual
spans/log events. Percentiles are not counts and cannot be used as occurrence
inputs. Trace populations describe ingested spans, not automatically all requests:
users must select one server/root span per request and understand sampling and
source retention. Do not extrapolate sampled traces or infer ingestion completeness
from an empty response. Existing missing, partial and zero-traffic rules remain.

All execution stays on the assigned capable agent, using existing encrypted
credentials, TLS, bounded transport, lease and collection budgets. Capabilities
are checked for the selected provider and format at every admission boundary.
Keep providers immutable after creation. Query editors identify the language and
explain interval counts. Local fixtures are opt-in and keep persistent data;
existing application tracing remains available.

## Consequences and discussion points

- Three explicit source choices avoid suggesting Tempo and VictoriaTraces are
  interchangeable. This adds a card to the current create form.
- Native source aggregation avoids raw-event transfers and preserves current SLO
  arithmetic; no new SLO calculation kind or storage schema for trace payloads.
- Source sampling, dropped logs, duplicate events and delayed ingestion affect the
  measured population. No automatic population correction is claimed.
- Keep the existing 60-second settling delay initially; sources with longer ingest
  delays need an explicit future configurable-delay contract.
- Native queries remain an advanced editor rather than introducing a visual query
  builder. Provider examples and interval guidance make the required shape visible.
- Source history/backfill is not promised merely because live collection works.

## Review clarifications

- Reserve LogsQL query-level options for a later profile. Reject all unquoted
  `options` clauses, including nested clauses, rather than maintain a permissive
  subset of upstream options that could change interval or completeness semantics.
  This also excludes performance-only query options initially.
- Clear both draft SLI queries when switching providers; preserve same-provider
  drafts and their language/cadence while choosing the next source. Query syntax
  is not interchangeable. Fresh forms reset the previous selection.
- Retain the established HTTP endpoint contract for private/local deployments.
  Show an explicit warning when HTTP carries Basic/Bearer credentials and explain
  that TLS controls apply only to HTTPS. No new persisted transport opt-in is
  introduced; API/CLI users remain responsible for selecting HTTPS.

## References

- [VictoriaTraces querying](https://docs.victoriametrics.com/victoriatraces/querying/)
- [VictoriaLogs statistics API used by VictoriaTraces](https://docs.victoriametrics.com/victorialogs/querying/)
- [Tempo metrics API](https://grafana.com/docs/tempo/latest/api_docs/)
- [Loki metrics API](https://grafana.com/docs/loki/latest/reference/loki-http-api/)
