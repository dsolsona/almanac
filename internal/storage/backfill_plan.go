package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type BackfillGapView struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Intervals int       `json:"intervals"`
	State     string    `json:"state"`
}

type BackfillPlan struct {
	RecoveryStart    time.Time         `json:"recovery_start"`
	RetentionDays    int               `json:"datasource_retention_days"`
	CadenceSeconds   int               `json:"cadence_seconds"`
	Covered          int               `json:"covered_intervals"`
	Eligible         int               `json:"eligible_intervals"`
	Automatic        int               `json:"automatic_intervals"`
	Manual           int               `json:"manual_intervals"`
	OutsideRetention int               `json:"outside_retention_intervals"`
	PreviousFailure  int               `json:"previous_failure_intervals"`
	Missing          int               `json:"missing_intervals"`
	GapCount         int               `json:"gap_count"`
	Chunks           int               `json:"chunk_count"`
	Gaps             []BackfillGapView `json:"gaps"`
}

func planBackfillGaps(ctx context.Context, tx pgx.Tx, id string, revision int64, start, end, expectedStart, recoveryStart time.Time, cadenceSeconds, retentionDays int, retryExhausted bool) (BackfillPlan, []backfillGap, error) {
	plan := BackfillPlan{RecoveryStart: recoveryStart, RetentionDays: retentionDays, CadenceSeconds: cadenceSeconds, Gaps: make([]BackfillGapView, 0)}
	if expectedStart.After(plan.RecoveryStart) {
		plan.RecoveryStart = expectedStart
	}
	if expectedStart.After(start) {
		start = expectedStart
	}
	if !start.Before(end) {
		return plan, nil, nil
	}
	rows, err := tx.Query(ctx, `
WITH source AS (
	SELECT d.historical_configuration_version AS version
	FROM public.almanac_sli_measurements m
	JOIN public.almanac_datasources d ON d.id=m.datasource_id
	WHERE m.sli_id=$1 AND m.revision=$2
), covered AS (
	SELECT coalesce(range_agg(tstzrange(o.interval_start,o.interval_start+make_interval(secs=>$5))), '{}'::tstzmultirange) AS spans
	FROM public.almanac_sli_observations o
	WHERE o.sli_id=$1 AND o.measurement_revision=$2 AND o.interval_start >= $3 AND o.interval_start < $4
	  AND o.outcome IN ('observed','complete_empty')
), chunk_ranges AS (
	SELECT tstzrange(greatest(c.range_start,$3),least(c.range_end,$4)) AS span,
	       c.state, j.requested_by, c.source_version
	FROM public.almanac_backfill_chunks c
	JOIN public.almanac_backfill_jobs j ON j.id=c.job_id
	WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.range_start < $4 AND c.range_end > $3
), queued AS (
	SELECT coalesce(range_agg(span) FILTER (WHERE state IN ('queued','leased','retry-wait') AND requested_by IS NULL), '{}'::tstzmultirange) AS automatic,
	       coalesce(range_agg(span) FILTER (WHERE state IN ('queued','leased','retry-wait') AND requested_by IS NOT NULL), '{}'::tstzmultirange) AS manual,
	       coalesce(range_agg(span) FILTER (WHERE NOT $7::boolean AND state IN ('unavailable','failed') AND source_version=(SELECT version FROM source)), '{}'::tstzmultirange) AS exhausted
	FROM chunk_ranges
), missing AS (
	SELECT tstzmultirange(tstzrange($3,$4))-covered.spans AS all_gaps,
	       tstzmultirange(tstzrange($3,least($4,greatest($3,$6)))) AS expired
	FROM covered
), recoverable AS (
	SELECT all_gaps-expired AS spans, all_gaps*expired AS outside_retention
	FROM missing
)
SELECT lower(gap),upper(gap),(extract(epoch FROM upper(gap)-lower(gap))/$5)::bigint,state
FROM recoverable r CROSS JOIN queued q
CROSS JOIN LATERAL (VALUES
	('outside-retention',r.outside_retention),
	('automatic',r.spans*q.automatic),
	('manual',(r.spans-q.automatic)*q.manual),
	('previous-failure',(r.spans-q.automatic-q.manual)*q.exhausted),
	('eligible',r.spans-q.automatic-q.manual-q.exhausted)
) AS classified(state,spans)
CROSS JOIN LATERAL unnest(classified.spans) AS gap
ORDER BY lower(gap) DESC`, id, revision, start, end, cadenceSeconds, recoveryStart, retryExhausted)
	if err != nil {
		return BackfillPlan{}, nil, err
	}
	defer rows.Close()
	eligible := make([]backfillGap, 0)
	for rows.Next() {
		var gap BackfillGapView
		if err := rows.Scan(&gap.Start, &gap.End, &gap.Intervals, &gap.State); err != nil {
			return BackfillPlan{}, nil, err
		}
		switch gap.State {
		case "eligible":
			plan.Eligible += gap.Intervals
			step := historicalChunkSeconds(cadenceSeconds) / cadenceSeconds
			plan.Chunks += (gap.Intervals + step - 1) / step
			eligible = append(eligible, backfillGap{start: gap.Start, end: gap.End})
		case "automatic":
			plan.Automatic += gap.Intervals
		case "manual":
			plan.Manual += gap.Intervals
		case "outside-retention":
			plan.OutsideRetention += gap.Intervals
		case "previous-failure":
			plan.PreviousFailure += gap.Intervals
		}
		plan.Missing += gap.Intervals
		plan.GapCount++
		if len(plan.Gaps) < 200 {
			plan.Gaps = append(plan.Gaps, gap)
		}
	}
	plan.Covered = int(end.Sub(start)/(time.Duration(cadenceSeconds)*time.Second)) - plan.Missing
	return plan, eligible, rows.Err()
}
