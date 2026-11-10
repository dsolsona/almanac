package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	errRetentionBlocked = errors.New("storage: retention waiting for admitted work")
	errRetentionScope   = errors.New("storage: retention preview scope changed")
)

// Discovery continues past known blocked scopes so one active old job cannot
// prevent previewing other months. Applying a plan is a separate transaction.
func (r *Runtime) discoverRetention(ctx context.Context, now time.Time) error {
	impact, err := r.previewRetention(ctx, now)
	if err != nil || len(impact.Partitions) == 0 {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	scopes, err := q.ListRetentionScopes(bounded)
	if err != nil {
		return err
	}
	if len(scopes) >= 1000 {
		return ErrOperationalResource
	}
	known := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		known[string(scope)] = true
	}
	var digest []byte
	for first := 0; first < len(impact.Partitions); {
		last := first + 1
		for last < len(impact.Partitions) && impact.Partitions[last].End.Equal(impact.Partitions[first].End) {
			last++
		}
		digest, err = retentionScopeDigest(impact.Partitions[first:last])
		if err != nil {
			return err
		}
		if !known[string(digest)] {
			impact.Partitions = impact.Partitions[first:last]
			break
		}
		first = last
		if first == len(impact.Partitions) {
			return nil
		}
	}
	impact.Bytes = 0
	for _, part := range impact.Partitions {
		impact.Bytes += part.Bytes
	}
	counts, err := q.PreviewCalculatedRetention(bounded, dbgen.PreviewCalculatedRetentionParams{
		RangeStart: timestamp(impact.Partitions[0].Start), RangeEnd: timestamp(impact.Partitions[0].End)})
	if err != nil {
		return err
	}
	impact.PreviewRowLimit = 1000
	impact.CalculatedRowsAtLeast = map[string]int64{"extrema": counts.Extrema, "source_hours": counts.SourceHours}
	payload, err := json.Marshal(impact)
	if err != nil {
		return err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	inserted, err := q.InsertRetentionPreview(bounded, dbgen.InsertRetentionPreviewParams{ID: uuidToPG(id), ScopeDigest: digest,
		RangeStart: timestamp(impact.Partitions[0].Start), RangeEnd: timestamp(impact.Partitions[0].End), Cutoff: timestamp(impact.Cutoff), Preview: payload, PreviewedAt: timestamp(now)})
	if err != nil {
		return err
	}
	if inserted != 0 {
		if err := q.InsertAgentAudit(bounded, dbgen.InsertAgentAuditParams{Actor: "system:retention", SubjectID: uuidToPG(id),
			Action: "retention.preview", OccurredAt: timestamp(now), RequestID: "retention-preview:" + id.String()}); err != nil {
			return err
		}
	}
	return tx.Commit(bounded)
}

// The scope identity excludes changing size/statistics and viewer clock values.
// It pins actual relations and UTC bounds, so replacements require a new preview.
func retentionScopeDigest(parts []retentionPartition) ([]byte, error) {
	type identity struct {
		RelationID   int64
		Parent, Name string
		Start, End   time.Time
	}
	identities := make([]identity, len(parts))
	for index, part := range parts {
		identities[index] = identity{part.RelationID, part.Parent, part.Name, part.Start, part.End}
	}
	payload, err := json.Marshal(identities)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	return digest[:], nil
}

func (r *Runtime) applyRetentionPreview(ctx context.Context, now time.Time) (worked bool, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "retention.apply")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "retention") }()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	q := dbgen.New(conn)
	if err := q.ExhaustRetentionWork(bounded, timestamp(now)); err != nil {
		return false, err
	}
	work, err := q.ClaimRetentionWork(bounded, dbgen.ClaimRetentionWorkParams{Now: timestamp(now), LeaseUntil: timestamp(now.Add(30 * time.Second))})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if conflict, ok := errors.AsType[*pgconn.PgError](resultErr); errors.Is(resultErr, errRetentionBlocked) || ok && conflict.Code == "40001" {
			err := q.DeferRetentionWork(cleanup, dbgen.DeferRetentionWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch, Column3: timestamp(now)})
			resultErr = errors.Join(resultErr, err)
			return
		}
		err := q.FailRetentionWork(cleanup, dbgen.FailRetentionWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch,
			Obsolete: errors.Is(resultErr, errRetentionScope), Now: timestamp(now), LastError: "retention_cleanup_failed"})
		resultErr = errors.Join(resultErr, err)
	}()
	var preview retentionImpact
	if err := json.Unmarshal(work.Preview, &preview); err != nil || len(preview.Partitions) == 0 || len(preview.Partitions) > 2 || !preview.Cutoff.Equal(work.Cutoff.Time) {
		return true, errRetentionScope
	}
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{})
	if err != nil {
		return true, err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return true, err
	}
	locked := dbgen.New(tx)
	if _, err := locked.LockRetentionWork(bounded, dbgen.LockRetentionWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch}); err != nil {
		return true, err
	}
	counts := make(map[string]int64)
	if !work.CanonicalAppliedAt.Valid {
		counts["expired_source_work"], err = locked.ExpireRetentionSourceWork(bounded, dbgen.ExpireRetentionSourceWorkParams{RangeStart: work.RangeStart, RangeEnd: work.RangeEnd, Cutoff: work.Cutoff, Now: timestamp(now)})
		if err != nil {
			return true, err
		}
		if counts["expired_source_work"] > 0 {
			if err := retentionProgress(bounded, locked, work, now, counts, false); err != nil {
				return true, err
			}
			return true, tx.Commit(bounded)
		}
	} else {
		return true, finishRetentionCleanup(bounded, tx, locked, work, now)
	}
	// Match ingestion's coverage-before-values order. ONLY avoids locking all
	// child partitions while dependencies and catalog identities are rechecked.
	if _, err := tx.Exec(bounded, "LOCK TABLE ONLY public.almanac_sli_coverage, ONLY public.almanac_sli_observation_values IN ACCESS EXCLUSIVE MODE"); err != nil {
		return true, err
	}
	rows, err := locked.ListCanonicalRetentionPartitions(bounded)
	if err != nil {
		return true, err
	}
	current, err := canonicalRetentionImpact(rows, now)
	if err != nil {
		return true, err
	}
	for _, expected := range preview.Partitions {
		found := false
		for _, actual := range current.Partitions {
			if actual.RelationID == expected.RelationID && actual.Parent == expected.Parent && actual.Name == expected.Name &&
				actual.Start.Equal(expected.Start) && actual.End.Equal(expected.End) && !actual.End.After(preview.Cutoff) {
				found = true
				break
			}
		}
		if !found || !expected.Start.Equal(work.RangeStart.Time) || !expected.End.Equal(work.RangeEnd.Time) {
			return true, errRetentionScope
		}
	}
	digest, err := retentionScopeDigest(preview.Partitions)
	if err != nil || !equalDigest(digest, work.ScopeDigest) {
		return true, errRetentionScope
	}
	blocked, err := locked.RetentionHasDependencies(bounded, dbgen.RetentionHasDependenciesParams{
		RangeStart: work.RangeStart, RangeEnd: work.RangeEnd, Now: timestamp(now)})
	if err != nil {
		return true, err
	}
	if blocked {
		return true, errRetentionBlocked
	}
	for _, part := range preview.Partitions {
		// Detach removes inherited FK dependencies from still-retained values partitions.
		if part.Parent == "almanac_sli_coverage" {
			if _, err := tx.Exec(bounded, "ALTER TABLE public.almanac_sli_coverage DETACH PARTITION "+pgx.Identifier{"public", part.Name}.Sanitize()); err != nil {
				return true, err
			}
		}
		if _, err := tx.Exec(bounded, "DROP TABLE "+pgx.Identifier{"public", part.Name}.Sanitize()); err != nil {
			return true, err
		}
	}
	if err := retentionProgress(bounded, locked, work, now, counts, true); err != nil {
		return true, err
	}
	return true, tx.Commit(bounded)
}

func retentionProgress(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacRetentionWork, now time.Time, added map[string]int64, canonicalApplied bool) error {
	var counts map[string]int64
	if err := json.Unmarshal(work.CleanupCounts, &counts); err != nil || counts == nil {
		return errRetentionScope
	}
	for key, count := range added {
		if count < 0 || counts[key] < 0 || counts[key] > math.MaxInt64-count {
			return ErrOperationalResource
		}
		counts[key] += count
	}
	payload, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	changed, err := q.AdvanceRetentionCleanup(ctx, dbgen.AdvanceRetentionCleanupParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch,
		CanonicalApplied: canonicalApplied, Now: timestamp(now), CleanupCounts: payload})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errRetentionScope
	}
	return q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: "system:retention", SubjectID: work.ID,
		Action: "retention.progress", OccurredAt: timestamp(now), RequestID: "retention-progress:" + work.ID.String() + ":" + strconv.FormatInt(work.LeaseEpoch, 10)})
}

func finishRetentionCleanup(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, work dbgen.AlmanacRetentionWork, now time.Time) error {
	counts := make(map[string]int64)
	blocked, err := q.RetentionHasDependencies(ctx, dbgen.RetentionHasDependenciesParams{RangeStart: work.RangeStart, RangeEnd: work.RangeEnd, Now: timestamp(now)})
	if err != nil {
		return err
	}
	if blocked {
		return errRetentionBlocked
	}
	counts["extrema"], err = q.DeleteExpiredRetentionExtrema(ctx, dbgen.DeleteExpiredRetentionExtremaParams{RangeStart: work.RangeStart, RangeEnd: work.RangeEnd})
	if err != nil {
		return err
	}
	counts["source_hours"], err = q.DeleteExpiredRetainedSourceHours(ctx, dbgen.DeleteExpiredRetainedSourceHoursParams{RangeStart: work.RangeStart, RangeEnd: work.RangeEnd})
	if err != nil {
		return err
	}
	counts["source_work"], err = q.DeleteExpiredRetainedSourceWork(ctx, dbgen.DeleteExpiredRetainedSourceWorkParams{RangeStart: work.RangeStart, RangeEnd: work.RangeEnd, Now: timestamp(now)})
	if err != nil {
		return err
	}
	progress := int64(0)
	for _, count := range counts {
		progress += count
	}
	remaining, err := q.HasCalculatedRetentionRows(ctx, dbgen.HasCalculatedRetentionRowsParams{RangeStart: work.RangeStart, RangeEnd: work.RangeEnd})
	if err != nil {
		return err
	}
	if remaining && progress == 0 {
		return errRetentionBlocked
	}
	if err := retentionProgress(ctx, q, work, now, counts, true); err != nil {
		return err
	}
	if !remaining {
		changed, err := q.CompleteRetentionWork(ctx, dbgen.CompleteRetentionWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch, AppliedAt: timestamp(now)})
		if err != nil {
			return err
		}
		if changed != 1 {
			return errRetentionScope
		}
		if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: "system:retention", SubjectID: work.ID,
			Action: "retention.apply", OccurredAt: timestamp(now), RequestID: "retention-apply:" + work.ID.String()}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
