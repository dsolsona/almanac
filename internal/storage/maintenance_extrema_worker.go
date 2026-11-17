package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type maintenanceExtremaRead struct {
	session *maintenanceConsumerSession
	parent  dbgen.AlmanacSloExtremaCursor
	payload []byte
	pending bool
}

// Execute one immutable hour target. Read/calculation releases its RR connection
// before RC resource capture and publication; the outer worker owns the ACK.
func (r *Runtime) executeMaintenanceExtremaHour(ctx context.Context, job dbgen.AlmanacMaintenancePreparation, o maintenancePreparationTurnOptions, clock func() time.Time) (string, error) {
	if r == nil || clock == nil || !o.valid() || job.Kind != "extrema_hour" || !job.SloID.Valid || !job.SliID.Valid || !job.BuildID.Valid || !job.DefinitionRevision.Valid || job.DefinitionRevision.Int64 < 1 || !job.MeasurementRevision.Valid || job.MeasurementRevision.Int64 < 1 || !job.SourceGeneration.Valid || job.SourceGeneration.Int64 < 0 || !job.HourStart.Valid || !validMaintenanceTime(job.HourStart.Time) || !job.HourStart.Time.Equal(job.HourStart.Time.Truncate(time.Hour)) || !validMaintenanceTime(job.HourStart.Time.Add(time.Hour)) || job.SecondBuild.Valid {
		return "", ErrMaintenanceInvalid
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() || !job.LeaseUntil.Valid || !now.Before(job.LeaseUntil.Time) {
		return "", ErrMaintenancePreparationStale
	}
	if r.maintenanceConsumers.Load() == nil {
		return "pending", nil
	}
	if job.HourStart.Time.Add(time.Hour).After(now.Truncate(time.Hour)) {
		return "pending", nil
	}
	bounded, cancel := context.WithTimeout(ctx, min(o.OperationBudget, job.LeaseUntil.Time.Sub(now)))
	defer cancel()
	read, err := r.readMaintenanceExtremaHour(bounded, job, o, now)
	if err != nil {
		return "", err
	}
	if read.pending && len(read.session.preparation) == 0 {
		return "pending", nil
	}
	phaseNow := normalizedAgentNow(clock)
	if phaseNow.IsZero() || !phaseNow.Before(job.LeaseUntil.Time) {
		return "", ErrMaintenancePreparationStale
	}
	err = r.maintenancePreparationTransaction(bounded, min(o.OperationBudget, job.LeaseUntil.Time.Sub(phaseNow)), func(ctx context.Context, tx pgx.Tx) error {
		var prepared maintenancePreparedEnqueue
		if read.pending {
			var err error
			prepared, err = prepareMaintenanceEnqueue(ctx, tx, read.session.scope, read.session.preparation, read.session.preparationLimits, phaseNow)
			if errors.Is(err, ErrMaintenanceSnapshotChanged) {
				// This transaction is already RC; the resource capture's mismatch
				// therefore means the immutable scope/authority is obsolete.
				return errors.Join(ErrMaintenanceHourStale, err)
			}
			if err != nil {
				return err
			}
		}
		if err := fenceMaintenanceConsumerSnapshot(ctx, tx, read.session); err != nil {
			if errors.Is(err, errReadSerialization) {
				identity, e := dbgen.New(tx).ReadMaintenanceEvaluationIdentity(ctx, job.SloID)
				if e != nil {
					return e
				}
				if identity.SliID != job.SliID || identity.DefinitionRevision != job.DefinitionRevision.Int64 || identity.MeasurementRevision != job.MeasurementRevision.Int64 || identity.ExclusionRevision != job.TargetRevision {
					return ErrMaintenanceHourStale
				}
				return errors.Join(ErrDerivedPending, err)
			}
			return err
		}
		q := dbgen.New(tx)
		parent, err := q.LockMaintenanceExtremaParent(ctx, read.parent.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDerivedPending
		}
		if err != nil {
			return err
		}
		if parent.ID != read.parent.ID || parent.Version != read.parent.Version {
			return ErrDerivedPending
		}
		live, err := q.LockMaintenancePreparation(ctx, job.ID)
		if err != nil {
			return err
		}
		finished := normalizedAgentNow(clock)
		if !maintenanceExtremaClaimMatches(live, job, finished) {
			return ErrMaintenancePreparationStale
		}
		if read.pending {
			err = enqueueMaintenancePreparation(ctx, tx, prepared, maintenancePreparationPriority(live.Priority), finished)
		} else {
			var n int64
			n, err = q.PublishMaintenanceSloExtrema(ctx, dbgen.PublishMaintenanceSloExtremaParams{ID: read.parent.ID, Version: read.parent.Version, SloID: job.SloID, DefinitionRevision: job.DefinitionRevision.Int64, MeasurementRevision: job.MeasurementRevision.Int64, CalculationVersion: operationalCalculationVersion, ExclusionRevision: job.TargetRevision, RangeStart: job.HourStart, RangeEnd: timestamp(job.HourStart.Time.Add(time.Hour)), SourceGeneration: read.session.sourceGeneration, RetentionStart: timestamp(o.RetentionStart), PublishedAt: timestamp(finished), Lanes: read.payload})
			if err == nil && n != 1 {
				return ErrDerivedPending
			}
		}
		if err != nil {
			return err
		}
		if !maintenanceExtremaClaimMatches(live, job, normalizedAgentNow(clock)) {
			return ErrMaintenancePreparationStale
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if read.pending {
		return "pending", nil
	}
	return "complete", nil
}

func maintenanceExtremaClaimMatches(live, job dbgen.AlmanacMaintenancePreparation, now time.Time) bool {
	return !now.IsZero() && live.State == "pending" && live.ID == job.ID && live.LeaseToken.Valid && live.LeaseToken == job.LeaseToken && live.LeaseEpoch == job.LeaseEpoch && live.LeaseUntil.Valid && live.LeaseUntil.Time.Equal(job.LeaseUntil.Time) && now.Before(live.LeaseUntil.Time) && live.Kind == job.Kind && live.SloID == job.SloID && live.SliID == job.SliID && live.TargetRevision == job.TargetRevision && live.BuildID == job.BuildID && live.DefinitionRevision == job.DefinitionRevision && live.MeasurementRevision == job.MeasurementRevision && live.SourceGeneration == job.SourceGeneration && live.HourStart.Time.Equal(job.HourStart.Time)
}

func (r *Runtime) readMaintenanceExtremaHour(ctx context.Context, job dbgen.AlmanacMaintenancePreparation, o maintenancePreparationTurnOptions, now time.Time) (maintenanceExtremaRead, error) {
	var out maintenanceExtremaRead
	conn, err := r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		return out, err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return out, err
	}
	defer rollbackTransaction(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return out, err
	}
	q := dbgen.New(tx)
	live, err := q.ReadMaintenanceExtremaClaim(ctx, job.ID)
	if err != nil {
		return out, err
	}
	if !maintenanceExtremaClaimMatches(live, job, now) {
		return out, ErrMaintenancePreparationStale
	}
	row, err := q.GetSlo(ctx, job.SloID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrMaintenanceHourStale
	}
	if err != nil {
		return out, err
	}
	if row.SliID != job.SliID || row.DefinitionRevision != job.DefinitionRevision.Int64 || row.MeasurementRevision != job.MeasurementRevision.Int64 || row.ExclusionRevision != job.TargetRevision {
		return out, ErrMaintenanceHourStale
	}
	build, err := q.ReadMaintenanceEvaluationBuild(ctx, dbgen.ReadMaintenanceEvaluationBuildParams{SloID: job.SloID, TargetRevision: job.TargetRevision})
	if err != nil {
		return out, err
	}
	if build.ID != job.BuildID {
		return out, ErrMaintenanceHourStale
	}
	out.parent, err = q.ReadMaintenanceExtremaParent(ctx, dbgen.ReadMaintenanceExtremaParentParams{SloID: job.SloID, DefinitionRevision: job.DefinitionRevision.Int64, MeasurementRevision: job.MeasurementRevision.Int64, CalculationVersion: operationalCalculationVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDerivedPending
	}
	if err != nil {
		return out, err
	}
	generation, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: job.SliID, MeasurementRevision: job.MeasurementRevision.Int64})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		return out, err
	}
	if generation < job.SourceGeneration.Int64 {
		return out, ErrDerivedPending
	}
	out.session, err = captureMaintenanceConsumer(ctx, tx, row, r)
	if err != nil {
		return out, err
	}
	if out.session == nil {
		return out, ErrDerivedPending
	}
	out.session.sourceGeneration = generation
	out.session.sourceCaptured = true
	source, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return out, err
	}
	base := extremaWindowInput(row, source, now, o.RetentionStart)
	lanes, err := buildMaintenanceExtremaHour(ctx, out.session, row, base, job.HourStart.Time.UTC())
	if errors.Is(err, ErrDerivedPending) {
		out.pending = true
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.payload, err = marshalReceiptMetadata(lanes)
	if err != nil {
		return out, err
	}
	if len(out.payload) > 20<<20 {
		return out, ErrOperationalResource
	}
	return out, nil
}
