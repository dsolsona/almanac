package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type BackfillJob struct {
	ID                 string    `json:"id"`
	Start              time.Time `json:"start"`
	End                time.Time `json:"end"`
	State              string    `json:"state"`
	Automatic          bool      `json:"automatic"`
	Chunks             int       `json:"chunks"`
	Committed          int       `json:"committed"`
	Gaps               int       `json:"gaps"`
	RequestedIntervals int       `json:"requested_intervals"`
	RecoveredIntervals int       `json:"recovered_intervals"`
	CanonicalConflict  bool      `json:"canonical_conflict,omitempty"`
}

func ListBackfills(ctx context.Context, databaseURL string, proof DatasourceProof, sliID string, clock func() time.Time) ([]BackfillJob, error) {
	conn, _, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, clock, "administrator", "editor", "viewer")
	if err != nil {
		return nil, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	rows, err := tx.Query(ctx, `
SELECT j.id, j.requested_start, j.requested_end, j.requested_by IS NULL, count(c.id),
       count(*) FILTER (WHERE c.state='committed' AND NOT c.canonical_conflict),
       count(*) FILTER (WHERE c.state IN ('failed','unavailable') OR (c.state='committed' AND c.canonical_conflict)),
       CASE WHEN j.cancelled_at IS NOT NULL THEN 'cancelled'
            WHEN bool_or(c.state='leased') THEN 'running'
            WHEN bool_or(c.state IN ('queued','retry-wait')) THEN
                CASE WHEN bool_or(c.attempts>0) THEN 'running' ELSE 'queued' END
            WHEN bool_or(c.state IN ('failed','unavailable') OR c.canonical_conflict) THEN 'completed-with-gaps'
            ELSE 'completed' END,
       sum(extract(epoch FROM c.range_end-c.range_start)::bigint / c.cadence_seconds),
       sum((SELECT count(*) FROM public.almanac_sli_observations o WHERE o.backfill_chunk_id=c.id)), bool_or(c.canonical_conflict)
FROM (SELECT * FROM public.almanac_backfill_jobs WHERE sli_id=$1 ORDER BY created_at DESC, id LIMIT 20) j
JOIN public.almanac_backfill_chunks c ON c.job_id=j.id
GROUP BY j.id, j.requested_start, j.requested_end, j.cancelled_at, j.requested_by, j.created_at
ORDER BY j.created_at DESC, j.id`, sliID)
	if err != nil {
		return nil, operationError(ctx, "storage: read historical jobs", err)
	}
	jobs := make([]BackfillJob, 0, 20)
	for rows.Next() {
		var job BackfillJob
		if err := rows.Scan(&job.ID, &job.Start, &job.End, &job.Automatic, &job.Chunks, &job.Committed, &job.Gaps, &job.State, &job.RequestedIntervals, &job.RecoveredIntervals, &job.CanonicalConflict); err != nil {
			rows.Close()
			return nil, operationError(ctx, "storage: scan historical jobs", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError(ctx, "storage: read historical jobs", err)
	}
	if err := finishAgentRead(ctx, tx, clock, started, user, installation, session); err != nil {
		return nil, err
	}
	return jobs, nil
}

func CancelBackfill(ctx context.Context, databaseURL string, proof DatasourceProof, sliID, jobID string, clock func() time.Time) error {
	conn, _, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, clock, "administrator", "editor")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM public.almanac_backfill_jobs WHERE sli_id=$1 AND id=$2`, sliID, jobID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSliNotFound
		}
		return operationError(ctx, "storage: read historical job", err)
	}
	if _, err := tx.Exec(ctx, `SELECT public.almanac_cancel_backfill_job($1,$2)`, id, normalizedAgentNow(clock)); err != nil {
		return operationError(ctx, "storage: cancel historical job", err)
	}
	return finishAgentRead(ctx, tx, clock, started, user, installation, session)
}
