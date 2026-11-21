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

func planBackfillGaps(ctx context.Context, tx pgx.Tx, id string, revision int64, start, end, expectedStart, recoveryStart time.Time, cadenceSeconds, retentionDays int) (BackfillPlan, []backfillGap, error) {
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
WITH missing AS (
	SELECT point FROM generate_series($3::timestamptz,$4::timestamptz-make_interval(secs=>$5),make_interval(secs=>$5)) point
	LEFT JOIN public.almanac_sli_observations o
	  ON o.sli_id=$1 AND o.measurement_revision=$2 AND o.interval_start=point AND o.outcome IN ('observed','complete_empty')
	WHERE o.sli_id IS NULL
), classified AS (
	SELECT point, CASE WHEN point < $6::timestamptz THEN 'outside-retention'
		ELSE coalesce((SELECT CASE
			WHEN c.state IN ('queued','leased','retry-wait') THEN CASE WHEN j.requested_by IS NULL THEN 'automatic' ELSE 'manual' END
			ELSE 'previous-failure' END
		FROM public.almanac_backfill_chunks c JOIN public.almanac_backfill_jobs j ON j.id=c.job_id
		WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.range_start<=point AND point<c.range_end
		  AND c.state IN ('queued','leased','retry-wait','unavailable','failed')
		ORDER BY (c.state IN ('queued','leased','retry-wait')) DESC, j.created_at DESC, c.id
		LIMIT 1), 'eligible') END state
	FROM missing
), islands AS (
	SELECT point,state,point-row_number() OVER (PARTITION BY state ORDER BY point)*make_interval(secs=>$5) AS key FROM classified
)
SELECT min(point),max(point)+make_interval(secs=>$5),count(*),state
FROM islands GROUP BY state,key ORDER BY min(point) DESC`, id, revision, start, end, cadenceSeconds, recoveryStart)
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
