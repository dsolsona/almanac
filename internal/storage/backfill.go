package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/jackc/pgx/v5"
)

var (
	ErrBackfillRange   = errors.New("backfill requires an aligned, settled range within retained measurement history")
	ErrBackfillOverlap = errors.New("a historical job already owns part of the requested range")
)

type BackfillRequest struct {
	Proof      DatasourceProof
	SLIID      string
	Start, End time.Time
	RequestKey string
	Preview    bool
	Now        func() time.Time
}

type BackfillAdmission struct {
	ID         string    `json:"id,omitempty"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	ChunkCount int       `json:"chunk_count"`
}

// RequestBackfill admits historical work under the locked SLI definition and user session.
func RequestBackfill(ctx context.Context, databaseURL string, input BackfillRequest) (BackfillAdmission, error) {
	sliID, valid := canonicalUUID(input.SLIID)
	if !valid || input.Now == nil || !validPrintableASCII(input.RequestKey, 1, 128) {
		return BackfillAdmission{}, ErrSliInvalid
	}
	conn, _, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor")
	if err != nil {
		return BackfillAdmission{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)

	var revision int64
	var validFrom time.Time
	var cadenceSeconds, delaySeconds int
	var agentID, provider string
	err = tx.QueryRow(ctx, `
SELECT m.revision, m.effective_from, d.polling_interval_seconds, d.settling_delay_seconds,
       d.agent_id, d.provider
FROM public.almanac_slis s
JOIN public.almanac_sli_measurements m ON m.sli_id=s.id AND m.revision=s.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id
WHERE s.id=$1
FOR NO KEY UPDATE OF m
FOR UPDATE OF s, d`, sliID).Scan(&revision, &validFrom, &cadenceSeconds, &delaySeconds, &agentID, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackfillAdmission{}, ErrSliNotFound
	}
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: lock historical measurement", err)
	}

	var admission BackfillAdmission
	err = tx.QueryRow(ctx, `
SELECT j.id, j.requested_start, j.requested_end, (SELECT count(*) FROM public.almanac_backfill_chunks c WHERE c.job_id=j.id)
FROM public.almanac_backfill_jobs j
WHERE j.sli_id=$1 AND j.requested_by=$2 AND j.request_key=$3`, sliID, user.ID, input.RequestKey).
		Scan(&admission.ID, &admission.Start, &admission.End, &admission.ChunkCount)
	if err == nil {
		if !admission.Start.Equal(input.Start) || !admission.End.Equal(input.End) {
			return BackfillAdmission{}, ErrSliConflict
		}
		admission.Start, admission.End = admission.Start.UTC(), admission.End.UTC()
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return BackfillAdmission{}, err
		}
		return admission, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BackfillAdmission{}, operationError(ctx, "storage: read historical request receipt", err)
	}
	if provider != "prometheus" {
		return BackfillAdmission{}, ErrSliUnsupported
	}

	now := normalizedAgentNow(input.Now)
	start, end := input.Start.UTC(), input.End.UTC()
	cadence := time.Duration(cadenceSeconds) * time.Second
	latestSettled := floorSliBoundary(now.Add(-time.Duration(delaySeconds)*time.Second), cadence)
	if !start.Before(end) || start.Before(validFrom) || start.Before(now.Add(-sli.ObservationRetention)) ||
		end.After(latestSettled) || !start.Equal(floorSliBoundary(start, cadence)) || !end.Equal(floorSliBoundary(end, cadence)) {
		return BackfillAdmission{}, ErrBackfillRange
	}
	stepSeconds := historicalChunkSeconds(cadenceSeconds)
	if input.Preview {
		step := time.Duration(stepSeconds) * time.Second
		count := int((end.Sub(start) + step - 1) / step)
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return BackfillAdmission{}, err
		}
		return BackfillAdmission{Start: start, End: end, ChunkCount: count}, nil
	}
	var overlaps, exhausted bool
	err = tx.QueryRow(ctx, `
SELECT coalesce(bool_or(c.state IN ('queued','leased','retry-wait')),false),
       coalesce(bool_or(j.requested_by IS NULL AND c.state IN ('failed','unavailable')),false)
FROM public.almanac_backfill_chunks c
JOIN public.almanac_backfill_jobs j ON j.id=c.job_id
WHERE c.sli_id=$1 AND c.range_start<$3 AND c.range_end>$2`, sliID, start, end).
		Scan(&overlaps, &exhausted)
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: check active historical range", err)
	}
	if overlaps {
		return BackfillAdmission{}, ErrBackfillOverlap
	}
	if exhausted && user.Role != "administrator" {
		return BackfillAdmission{}, ErrAgentForbidden
	}

	admission, err = insertBackfillJob(ctx, tx, sliID.String(), revision, agentID, start, end, now, cadenceSeconds, input.RequestKey, user.ID)
	if err != nil {
		return BackfillAdmission{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return BackfillAdmission{}, err
	}
	return admission, nil
}

func historicalChunkSeconds(cadenceSeconds int) int {
	return min(86400/cadenceSeconds, 256) * cadenceSeconds
}

func insertBackfillJob(ctx context.Context, tx pgx.Tx, sliID string, revision int64, agentID string, start, end, now time.Time, cadenceSeconds int, key string, actor any) (BackfillAdmission, error) {
	admission := BackfillAdmission{Start: start, End: end}
	err := tx.QueryRow(ctx, `
INSERT INTO public.almanac_backfill_jobs (sli_id,requested_start,requested_end,request_key,requested_by,created_at)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id`, sliID, start, end, key, actor, now).Scan(&admission.ID)
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: record historical request", err)
	}

	tag, err := tx.Exec(ctx, `
INSERT INTO public.almanac_backfill_chunks
    (job_id,sli_id,measurement_revision,agent_id,range_start,range_end,cadence_seconds,next_attempt_at,source_version)
SELECT $1,$2,$3,$4,point,LEAST(point + make_interval(secs=>$8),$6),$7,$9,d.version
FROM public.almanac_sli_measurements m
JOIN public.almanac_datasources d ON d.id=m.datasource_id
CROSS JOIN LATERAL generate_series($5::timestamptz,$6::timestamptz - make_interval(secs=>$7),make_interval(secs=>$8)) point
WHERE m.sli_id=$2 AND m.revision=$3`,
		admission.ID, sliID, revision, agentID, start, end, cadenceSeconds, historicalChunkSeconds(cadenceSeconds), now)
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: plan historical chunks", err)
	}
	admission.ChunkCount = int(tag.RowsAffected())
	return admission, nil
}
