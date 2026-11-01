package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
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

// Application uses previously committed previews. Discovery continues past known
// blocked scopes so one active old job cannot prevent previewing other months.
func (r *Runtime) maintainRetention(ctx context.Context, now time.Time) error {
	_, err := r.applyRetentionPreview(ctx, now)
	if err != nil && !errors.Is(err, errRetentionBlocked) {
		return err
	}
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
		if _, err := tx.Exec(bounded, "DROP TABLE "+pgx.Identifier{"public", part.Name}.Sanitize()); err != nil {
			return true, err
		}
	}
	completed, err := locked.CompleteRetentionWork(bounded, dbgen.CompleteRetentionWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch, AppliedAt: timestamp(now)})
	if err != nil || completed != 1 {
		if err == nil {
			err = errRetentionScope
		}
		return true, err
	}
	if err := locked.InsertAgentAudit(bounded, dbgen.InsertAgentAuditParams{Actor: "system:retention", SubjectID: work.ID,
		Action: "retention.apply", OccurredAt: timestamp(now), RequestID: "retention-apply:" + work.ID.String()}); err != nil {
		return true, err
	}
	return true, tx.Commit(bounded)
}
