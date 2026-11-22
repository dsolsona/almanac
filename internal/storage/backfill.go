package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrBackfillRange              = errors.New("backfill requires an aligned, settled range within retained measurement history")
	ErrBackfillOverlap            = errors.New("a historical job already owns part of the requested range")
	ErrBackfillNoGaps             = errors.New("the requested range contains no recoverable gaps")
	ErrBackfillEarlierUnavailable = errors.New("earlier history requires the original active measurement definition")
	ErrBackfillEarlierChanged     = errors.New("earlier history changed; preview the range again")
	ErrBackfillEarlierRange       = errors.New("choose an earlier range within retention that reaches the start of existing measurement history")
)

type BackfillRequest struct {
	Proof                 DatasourceProof
	SLIID                 string
	Start, End            time.Time
	RequestKey            string
	Preview               bool
	RetryExhausted        bool
	IncludeEarlierHistory bool
	EarlierHistoryContext string
	Now                   func() time.Time
}

type BackfillAdmission struct {
	ID                    string        `json:"id,omitempty"`
	Start                 time.Time     `json:"start"`
	End                   time.Time     `json:"end"`
	ChunkCount            int           `json:"chunk_count"`
	EarlierHistoryContext string        `json:"earlier_history_context,omitempty"`
	EarlierHistoryFrom    time.Time     `json:"earlier_history_from,omitzero"`
	EarlierHistoryUntil   time.Time     `json:"earlier_history_until,omitzero"`
	Plan                  *BackfillPlan `json:"plan,omitempty"`
}

// RequestBackfill admits historical work under the locked SLI definition and user session.
func RequestBackfill(ctx context.Context, databaseURL string, input BackfillRequest) (BackfillAdmission, error) {
	sliID, valid := canonicalUUID(input.SLIID)
	if !valid || input.Now == nil || !validPrintableASCII(input.RequestKey, 1, 128) {
		return BackfillAdmission{}, ErrSliInvalid
	}
	conn, _, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor", "viewer")
	if err != nil {
		return BackfillAdmission{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if (user.Role == "viewer" && (!input.Preview || input.IncludeEarlierHistory)) || (input.RetryExhausted && user.Role != "administrator") {
		return BackfillAdmission{}, ErrAgentForbidden
	}

	var revision, sourceVersion int64
	var validFrom time.Time
	var active, canBackfill bool
	var cadenceSeconds, delaySeconds, retentionDays int
	var agentID, provider, datasourceID string
	measurementQuery := `
SELECT m.revision, m.effective_from, m.activated_at IS NOT NULL, d.polling_interval_seconds, d.settling_delay_seconds,
       d.agent_id, d.provider, d.historical_retention_days, d.historical_configuration_version, d.id,
       EXISTS (SELECT 1 FROM public.almanac_agent_runtime_status runtime
               CROSS JOIN LATERAL jsonb_array_elements(runtime.capabilities) item
               WHERE runtime.agent_id=d.agent_id AND item->>'provider'=d.provider AND item->'operations' ? 'backfill')
FROM public.almanac_slis s
JOIN public.almanac_sli_measurements m ON m.sli_id=s.id AND m.revision=s.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id
WHERE s.id=$1`
	err = tx.QueryRow(ctx, measurementQuery, sliID).Scan(&revision, &validFrom, &active, &cadenceSeconds, &delaySeconds, &agentID, &provider, &retentionDays, &sourceVersion, &datasourceID, &canBackfill)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackfillAdmission{}, ErrSliNotFound
	}
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: read historical measurement", err)
	}

	if !input.Preview {
		admission, err := readBackfillReceipt(ctx, tx, sliID, user.ID, input)
		if err != nil {
			return BackfillAdmission{}, err
		}
		if admission.ID != "" {
			return admission, finishAgentRead(ctx, tx, input.Now, started, user, installation, session)
		}
	}
	if provider != "prometheus" {
		return BackfillAdmission{}, ErrSliUnsupported
	}

	now := normalizedAgentNow(input.Now)
	start, end := input.Start.UTC(), input.End.UTC()
	cadence := time.Duration(cadenceSeconds) * time.Second
	latestSettled := floorSliBoundary(now.Add(-time.Duration(delaySeconds)*time.Second), cadence)
	if !start.Before(end) || end.Sub(start) > 30*24*time.Hour ||
		end.After(latestSettled) || !start.Equal(floorSliBoundary(start, cadence)) || !end.Equal(floorSliBoundary(end, cadence)) {
		return BackfillAdmission{}, ErrBackfillRange
	}
	expectedStart := ceilSliBoundary(validFrom, cadence)
	recoveryStart := ceilSliBoundary(now.Add(-backfillRecoveryLookback(retentionDays)), cadence)
	var earlierFrom, earlierUntil time.Time
	var earlierContext string
	if input.IncludeEarlierHistory {
		if revision != 1 || !active || !canBackfill {
			return BackfillAdmission{}, ErrBackfillEarlierUnavailable
		}
		earlierFrom, earlierUntil = maxTime(start, recoveryStart), validFrom.UTC()
		if !earlierFrom.Before(expectedStart) || end.Before(expectedStart) {
			return BackfillAdmission{}, ErrBackfillEarlierRange
		}
		contextDigest := sha256.Sum256(fmt.Appendf(nil, "%s/%s/%d/%s/%s/%s/%s", sliID, datasourceID, sourceVersion,
			validFrom.UTC().Format(time.RFC3339Nano), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), earlierFrom.Format(time.RFC3339Nano)))
		earlierContext = hex.EncodeToString(contextDigest[:])
		if !input.Preview && input.EarlierHistoryContext != earlierContext {
			return BackfillAdmission{}, ErrBackfillEarlierChanged
		}
		expectedStart = start
	}
	plan, eligible, err := planBackfillGaps(ctx, tx, sliID.String(), revision, start, end, expectedStart, recoveryStart, cadenceSeconds, retentionDays, input.RetryExhausted)
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: plan historical gaps", err)
	}
	if input.Preview {
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return BackfillAdmission{}, err
		}
		return BackfillAdmission{Start: start, End: end, ChunkCount: plan.Chunks, Plan: &plan, EarlierHistoryFrom: earlierFrom, EarlierHistoryUntil: earlierUntil, EarlierHistoryContext: earlierContext}, nil
	}
	// The wide discovery scan finished before these live-ingestion locks.
	// Check the authority and selected ranges again before admitting work.
	var lockedRevision, lockedSource int64
	var lockedValidity time.Time
	err = tx.QueryRow(ctx, `
SELECT m.revision,m.effective_from,d.historical_configuration_version
FROM public.almanac_slis s
JOIN public.almanac_sli_measurements m ON m.sli_id=s.id AND m.revision=s.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id
WHERE s.id=$1
FOR NO KEY UPDATE OF m FOR UPDATE OF s,d`, sliID).Scan(&lockedRevision, &lockedValidity, &lockedSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackfillAdmission{}, ErrSliNotFound
	}
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: lock historical measurement", err)
	}
	admission, err := readBackfillReceipt(ctx, tx, sliID, user.ID, input)
	if err != nil {
		return BackfillAdmission{}, err
	}
	if admission.ID != "" {
		return admission, finishAgentRead(ctx, tx, input.Now, started, user, installation, session)
	}
	if lockedRevision != revision || lockedSource != sourceVersion || !lockedValidity.Equal(validFrom) {
		return BackfillAdmission{}, ErrBackfillOverlap
	}
	if len(eligible) == 0 {
		return BackfillAdmission{}, ErrBackfillNoGaps
	}
	if err := validateBackfillAdmission(ctx, tx, sliID, revision, sourceVersion, input.RetryExhausted, eligible); err != nil {
		return BackfillAdmission{}, err
	}
	admission, err = insertBackfillJob(ctx, tx, sliID.String(), revision, agentID, start, end, now, cadenceSeconds, input.RequestKey, user.ID, input.RetryExhausted, eligible...)
	if err != nil {
		return BackfillAdmission{}, err
	}
	if input.IncludeEarlierHistory {
		if _, err := tx.Exec(ctx, `
UPDATE public.almanac_sli_measurements
SET initial_live_from=coalesce(initial_live_from,effective_from), effective_from=$3
WHERE sli_id=$1 AND revision=$2`, sliID, revision, earlierFrom); err != nil {
			return BackfillAdmission{}, operationError(ctx, "storage: extend initial measurement history", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE public.almanac_backfill_jobs
SET earlier_history_from=$2, previous_valid_from=$3, earlier_history_context=$4
WHERE id=$1`, admission.ID, earlierFrom, validFrom, earlierContext); err != nil {
			return BackfillAdmission{}, operationError(ctx, "storage: record earlier measurement authorization", err)
		}
		admission.EarlierHistoryFrom = earlierFrom
		admission.EarlierHistoryUntil = earlierUntil
		admission.EarlierHistoryContext = earlierContext
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return BackfillAdmission{}, err
	}
	return admission, nil
}

func readBackfillReceipt(ctx context.Context, tx pgx.Tx, sliID, userID pgtype.UUID, input BackfillRequest) (BackfillAdmission, error) {
	var admission BackfillAdmission
	var retried bool
	var earlierFrom, earlierUntil pgtype.Timestamptz
	err := tx.QueryRow(ctx, `
SELECT j.id,j.requested_start,j.requested_end,j.retry_exhausted,j.earlier_history_from,j.previous_valid_from,COALESCE(j.earlier_history_context,''),
       (SELECT count(*) FROM public.almanac_backfill_chunks c WHERE c.job_id=j.id)
FROM public.almanac_backfill_jobs j
WHERE j.sli_id=$1 AND j.requested_by=$2 AND j.request_key=$3`, sliID, userID, input.RequestKey).
		Scan(&admission.ID, &admission.Start, &admission.End, &retried, &earlierFrom, &earlierUntil, &admission.EarlierHistoryContext, &admission.ChunkCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackfillAdmission{}, nil
	}
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: read historical request receipt", err)
	}
	if !admission.Start.Equal(input.Start) || !admission.End.Equal(input.End) || retried != input.RetryExhausted || earlierFrom.Valid != input.IncludeEarlierHistory ||
		admission.EarlierHistoryContext != input.EarlierHistoryContext {
		return BackfillAdmission{}, ErrSliConflict
	}
	admission.Start, admission.End = admission.Start.UTC(), admission.End.UTC()
	if earlierFrom.Valid {
		admission.EarlierHistoryFrom = earlierFrom.Time.UTC()
		admission.EarlierHistoryUntil = earlierUntil.Time.UTC()
	}
	return admission, nil
}

func validateBackfillAdmission(ctx context.Context, tx pgx.Tx, sliID pgtype.UUID, revision, sourceVersion int64, retry bool, ranges []backfillGap) error {
	starts, ends := backfillRangeBounds(ranges)
	var changed bool
	err := tx.QueryRow(ctx, `
WITH selected AS (SELECT * FROM unnest($3::timestamptz[],$4::timestamptz[]) span(start_at,end_at))
SELECT EXISTS (
  SELECT 1 FROM selected span JOIN public.almanac_sli_observations o
    ON o.interval_start >= span.start_at AND o.interval_start < span.end_at
  WHERE o.sli_id=$1 AND o.measurement_revision=$2 AND o.outcome IN ('observed','complete_empty')
) OR EXISTS (
  SELECT 1 FROM selected span JOIN public.almanac_backfill_chunks c
    ON c.range_start < span.end_at AND c.range_end > span.start_at
  WHERE c.sli_id=$1 AND c.measurement_revision=$2
    AND (c.state IN ('queued','leased','retry-wait') OR
         (NOT $6::boolean AND c.source_version=$5 AND c.state IN ('unavailable','failed')))
)`, sliID, revision, starts, ends, sourceVersion, retry).Scan(&changed)
	if err != nil {
		return operationError(ctx, "storage: revalidate historical admission", err)
	}
	if changed {
		return ErrBackfillOverlap
	}
	return nil
}

// backfillRecoveryLookback caps source work independently of local observation retention.
func backfillRecoveryLookback(configuredDays int) time.Duration {
	if configuredDays <= 0 {
		configuredDays = 30
	}
	return time.Duration(min(configuredDays, 30)) * 24 * time.Hour
}

func historicalChunkSeconds(cadenceSeconds int) int {
	return min(86400/cadenceSeconds, 64) * cadenceSeconds
}

func insertBackfillJob(ctx context.Context, tx pgx.Tx, sliID string, revision int64, agentID string, start, end, now time.Time, cadenceSeconds int, key string, actor any, retryExhausted bool, ranges ...backfillGap) (BackfillAdmission, error) {
	admission := BackfillAdmission{Start: start, End: end}
	err := tx.QueryRow(ctx, `
INSERT INTO public.almanac_backfill_jobs (sli_id,requested_start,requested_end,request_key,requested_by,created_at,retry_exhausted)
VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING id`, sliID, start, end, key, actor, now, retryExhausted).Scan(&admission.ID)
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: record historical request", err)
	}

	starts, ends := backfillRangeBounds(ranges)
	tag, err := tx.Exec(ctx, `
INSERT INTO public.almanac_backfill_chunks
    (job_id,sli_id,measurement_revision,agent_id,range_start,range_end,cadence_seconds,next_attempt_at,source_version)
SELECT $1,$2,$3,$4,point,LEAST(point + make_interval(secs=>$8),span.end_at),$7,$9,d.historical_configuration_version
FROM public.almanac_sli_measurements m
JOIN public.almanac_datasources d ON d.id=m.datasource_id
CROSS JOIN unnest($5::timestamptz[],$6::timestamptz[]) span(start_at,end_at)
CROSS JOIN LATERAL generate_series(span.start_at,span.end_at - make_interval(secs=>$7),make_interval(secs=>$8)) point
WHERE m.sli_id=$2 AND m.revision=$3`,
		admission.ID, sliID, revision, agentID, starts, ends, cadenceSeconds, historicalChunkSeconds(cadenceSeconds), now)
	if err != nil {
		return BackfillAdmission{}, operationError(ctx, "storage: plan historical chunks", err)
	}
	admission.ChunkCount = int(tag.RowsAffected())
	return admission, nil
}

func backfillRangeBounds(ranges []backfillGap) (starts, ends []time.Time) {
	starts, ends = make([]time.Time, 0, len(ranges)), make([]time.Time, 0, len(ranges))
	for _, gap := range ranges {
		starts, ends = append(starts, gap.start), append(ends, gap.end)
	}
	return starts, ends
}
