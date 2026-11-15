package storage

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// claimReceiptRepair bounds optional metadata work even while source batches
// keep the derived scheduler busy. It stores time.Time, avoiding epoch overflow.
func (r *Runtime) claimReceiptRepair(now time.Time) bool {
	for {
		previous := r.receiptRepairAfter.Load()
		if previous != nil && now.Before(*previous) {
			return false
		}
		if r.receiptRepairAfter.CompareAndSwap(previous, new(now.Add(time.Second))) {
			return true
		}
	}
}

// repairCanonicalReceipt repairs at most one hour and never rebuilds counts.
// Candidates and raw input both have fixed limits. Unknown metadata is a
// completed result, so expired history cannot permanently occupy the queue head.
func (r *Runtime) repairCanonicalReceipt(ctx context.Context) (repaired bool, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "derived.receipt.repair")
	defer func() {
		state := "idle"
		if repaired {
			state = "repaired"
		}
		finishDerivedTrace(ctx, span, resultErr, state)
	}()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	jobs, err := dbgen.New(conn).ListLegacyReceiptHours(bounded, 8)
	if err != nil {
		return false, err
	}
	for _, job := range jobs {
		changed, err := repairCanonicalReceiptHour(bounded, conn, job)
		if err != nil || changed {
			return changed, err
		}
	}
	return false, nil
}

func repairCanonicalReceiptHour(ctx context.Context, conn *pgxpool.Conn, job dbgen.ListLegacyReceiptHoursRow) (bool, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return false, err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return false, err
	}
	q := dbgen.New(tx)
	// Serialize with source mutation/retention before taking an aggregate lock.
	if _, err := q.LockCanonicalReceiptSli(ctx, job.SliID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	stored, err := q.LockLegacyReceiptHour(ctx, dbgen.LockLegacyReceiptHourParams{
		SliID: job.SliID, MeasurementRevision: job.MeasurementRevision, HourStart: job.HourStart,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receipt pgtype.Timestamptz
	if stored.PopulationReady && (stored.ObservedIntervals > 0 || stored.KnownEmptyIntervals > 0) {
		raw, err := q.ReadCanonicalReceiptPopulation(ctx, dbgen.ReadCanonicalReceiptPopulationParams{
			SliID: job.SliID, MeasurementRevision: job.MeasurementRevision, HourStart: job.HourStart,
		})
		if err != nil {
			return false, err
		}
		if receiptPopulationMatches(stored, raw) {
			receipt = raw.LatestReceipt
		}
	}
	count, err := q.UpdateCanonicalReceiptMetadata(ctx, dbgen.UpdateCanonicalReceiptMetadataParams{
		SliID: job.SliID, MeasurementRevision: job.MeasurementRevision, HourStart: job.HourStart,
		SourceGeneration: stored.SourceGeneration, LatestReceiptAt: receipt,
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return count == 1, nil
}

func receiptPopulationMatches(stored dbgen.LockLegacyReceiptHourRow, raw dbgen.ReadCanonicalReceiptPopulationRow) bool {
	if raw.RawRows > 3600 || stored.ObservedIntervals != raw.Observed ||
		stored.KnownEmptyIntervals != raw.KnownEmpty || stored.ZeroTrafficIntervals != raw.ZeroTraffic ||
		!equalReceiptTime(stored.FirstObservationAt, raw.FirstObservation) ||
		!equalReceiptTime(stored.LastObservationAt, raw.LastObservation) {
		return false
	}
	for _, pair := range [][2]string{{stored.Good, raw.Good}, {stored.Total, raw.Total}} {
		a, aOK := new(big.Rat).SetString(pair[0])
		b, bOK := new(big.Rat).SetString(pair[1])
		if !aOK || !bOK || a.Cmp(b) != 0 {
			return false
		}
	}
	return true
}

func equalReceiptTime(a, b pgtype.Timestamptz) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time))
}
