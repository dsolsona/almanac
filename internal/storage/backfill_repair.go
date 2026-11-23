package storage

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type backfillGap struct {
	start time.Time
	end   time.Time
}

// SweepBackfill puts overdue canonical gaps on the same queue as manual work.
// A persisted cursor bounds each pass and continues from the last committed point.
func (r *Runtime) SweepBackfill(ctx context.Context, now time.Time) error {
	conn, err := r.Acquire(ctx, HistoricalTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT public.almanac_reap_backfill_chunks($1)`, now); err != nil {
		return operationError(ctx, "storage: reap historical leases", err)
	}
	for _, sweep := range []string{"recent", "retained"} {
		// Share a 1,000-interval budget across both scans. Limit empty pages
		// too, so a large fleet cannot keep a sweep or its connection busy.
		remaining := 500
		for pages := 0; remaining > 0 && pages < 32; pages++ {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			examined, wrapped, err := sweepBackfillPage(ctx, tx, now.UTC(), sweep, remaining)
			if err == nil {
				err = tx.Commit(ctx)
			}
			rollbackTransaction(tx)
			if err != nil {
				return operationError(ctx, "storage: discover historical gaps", err)
			}
			if wrapped {
				break
			}
			remaining -= examined
		}
	}
	return nil
}

func sweepBackfillPage(ctx context.Context, tx pgx.Tx, now time.Time, sweep string, budget int) (examined int, wrapped bool, err error) {
	var cursorID pgtype.UUID
	var next pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
SELECT sli_id,next_interval FROM public.almanac_backfill_scan_cursor
WHERE sweep=$1 FOR UPDATE`, sweep).Scan(&cursorID, &next); err != nil {
		return 0, false, err
	}
	var id, agentID string
	var revision int64
	var validFrom time.Time
	var cadenceSeconds, delaySeconds, retentionDays int
	err = tx.QueryRow(ctx, `
SELECT s.id,m.revision,coalesce(m.initial_live_from,m.effective_from),d.agent_id,
       d.polling_interval_seconds,d.settling_delay_seconds,d.historical_retention_days
FROM public.almanac_slis s
JOIN public.almanac_sli_measurements m ON m.sli_id=s.id AND m.revision=s.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id
JOIN public.almanac_agents a ON a.id=d.agent_id
JOIN public.almanac_agent_runtime_status runtime ON runtime.agent_id=a.id
WHERE d.provider='prometheus' AND a.enabled AND m.activated_at IS NOT NULL
  AND runtime.last_heartbeat_at > $3::timestamptz - interval '1 minute'
  AND runtime.applied_configuration_revision=runtime.desired_configuration_revision
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(runtime.capabilities) item
              WHERE item->>'provider'=d.provider AND item->'operations' ? 'backfill')
  AND ($1::uuid IS NULL OR s.id>$1 OR (s.id=$1 AND $2::timestamptz IS NOT NULL))
ORDER BY s.id LIMIT 1
FOR NO KEY UPDATE OF m
FOR UPDATE OF s,d`, cursorID, next, now).Scan(&id, &revision, &validFrom, &agentID, &cadenceSeconds, &delaySeconds, &retentionDays)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `UPDATE public.almanac_backfill_scan_cursor SET sli_id=NULL,next_interval=NULL WHERE sweep=$1`, sweep)
		return 0, true, err
	}
	if err != nil {
		return 0, false, err
	}
	cadence := time.Duration(cadenceSeconds) * time.Second
	since := now.Add(-24 * time.Hour)
	until := now.Add(-5 * time.Minute).Add(-time.Duration(delaySeconds) * time.Second)
	if sweep == "retained" {
		until = since
		since = now.Add(-backfillRecoveryLookback(retentionDays))
	}
	lower := ceilSliBoundary(since, cadence)
	if cutoff := ceilSliBoundary(now.Add(-backfillRecoveryLookback(retentionDays)), cadence); cutoff.After(lower) {
		lower = cutoff
	}
	if validFrom.After(lower) {
		lower = ceilSliBoundary(validFrom, cadence)
	}
	end := floorSliBoundary(until, cadence)
	if cursorID.String() == id && next.Valid && next.Time.Before(end) {
		end = next.Time
	}
	// Scan newest gaps first and carry unused interval budget to the next SLI.
	start := end.Add(-time.Duration(budget) * cadence)
	if start.Before(lower) {
		start = lower
	}
	if start.Before(end) {
		examined = int(end.Sub(start) / cadence)
		if err := enqueueDiscoveredGaps(ctx, tx, id, revision, agentID, start, end, now, cadenceSeconds); err != nil {
			return 0, false, err
		}
	}
	var continuation *time.Time
	if start.After(lower) {
		continuation = &start
	}
	_, err = tx.Exec(ctx, `UPDATE public.almanac_backfill_scan_cursor SET sli_id=$2,next_interval=$3 WHERE sweep=$1`, sweep, id, continuation)
	return examined, false, err
}

func enqueueDiscoveredGaps(ctx context.Context, tx pgx.Tx, id string, revision int64, agentID string, start, end, now time.Time, cadenceSeconds int) error {
	rows, err := tx.Query(ctx, `
WITH source AS (
    SELECT d.historical_configuration_version AS version FROM public.almanac_sli_measurements m
    JOIN public.almanac_datasources d ON d.id=m.datasource_id WHERE m.sli_id=$1 AND m.revision=$2
), points AS (
    SELECT point FROM generate_series($3::timestamptz,$4::timestamptz-make_interval(secs=>$5),make_interval(secs=>$5)) point
    WHERE NOT EXISTS (SELECT 1 FROM public.almanac_sli_observations o
                      WHERE o.sli_id=$1 AND o.measurement_revision=$2 AND o.interval_start=point AND o.outcome IN ('observed','complete_empty'))
      AND NOT EXISTS (SELECT 1 FROM public.almanac_backfill_chunks c
                      WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.range_start<=point AND c.range_end>point
                        AND (c.state IN ('queued','leased','retry-wait') OR
                             (c.source_version=(SELECT version FROM source) AND
                               (c.state IN ('failed','unavailable') OR (c.state IN ('cancelled','committed') AND EXISTS (
                                 SELECT 1 FROM public.almanac_backfill_jobs j WHERE j.id=c.job_id AND j.requested_by IS NULL))))))
), islands AS (
    SELECT point, point-row_number() OVER (ORDER BY point)*make_interval(secs=>$5) AS key FROM points
)
SELECT min(point),max(point)+make_interval(secs=>$5),(SELECT version FROM source) FROM islands GROUP BY key ORDER BY min(point)`,
		id, revision, start, end, cadenceSeconds)
	if err != nil {
		return err
	}
	gaps := make([]backfillGap, 0)
	var version int64
	for rows.Next() {
		var gap backfillGap
		if err := rows.Scan(&gap.start, &gap.end, &version); err != nil {
			rows.Close()
			return err
		}
		gaps = append(gaps, gap)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, gap := range gaps {
		key := strconv.FormatInt(revision, 10) + "/" + strconv.FormatInt(version, 10) + "/" + gap.start.UTC().Format(time.RFC3339Nano)
		if _, err := insertBackfillJob(ctx, tx, id, revision, agentID, gap.start, gap.end, now, cadenceSeconds, key, nil, false, gap); err != nil {
			return err
		}
	}
	return nil
}
