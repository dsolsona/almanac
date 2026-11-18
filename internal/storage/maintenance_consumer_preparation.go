package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// The caller owns authenticated RC and its final completion-time authority
// check. Resource capture precedes queue writes; mismatch rolls back every pin.
func publishMaintenanceConsumerPreparation(ctx context.Context, tx pgx.Tx, session *maintenanceConsumerSession, priority maintenancePreparationPriority, clock func() time.Time) error {
	if session == nil || !session.sourceCaptured || clock == nil || len(session.preparation) == 0 {
		return ErrMaintenanceInvalid
	}
	prepared, err := prepareMaintenanceEnqueue(ctx, tx, session.scope, session.preparation, session.preparationLimits, normalizedAgentNow(clock))
	if err != nil {
		return err
	}
	if err := checkMaintenanceConsumerSnapshot(ctx, tx, session); err != nil {
		return err
	}
	return enqueueMaintenancePreparation(ctx, tx, prepared, priority, normalizedAgentNow(clock))
}

func fenceMaintenanceConsumerSnapshot(ctx context.Context, tx pgx.Tx, session *maintenanceConsumerSession) error {
	if session == nil || !session.sourceCaptured {
		return ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	if _, err := q.LockMaintenanceHourSli(ctx, session.scope.SliID); err != nil {
		return err
	}
	if _, err := q.LockMaintenanceSlo(ctx, session.scope.SloID); err != nil {
		return err
	}
	return checkMaintenanceConsumerSnapshot(ctx, tx, session)
}

func checkMaintenanceConsumerSnapshot(ctx context.Context, tx pgx.Tx, session *maintenanceConsumerSession) error {
	q := dbgen.New(tx)
	// The SLO root may be unchanged while its separate exclusion-state row
	// advances. Lock that actual authority so an old RR snapshot cannot pass.
	revision, err := q.FenceMaintenanceState(ctx, session.scope.SloID)
	if err != nil {
		return err
	}
	if revision != session.requestedRevision {
		return errReadSerialization
	}
	identity, err := q.ReadMaintenanceEvaluationIdentity(ctx, session.scope.SloID)
	if err != nil {
		return err
	}
	if identity.SliID != session.scope.SliID || identity.DefinitionRevision != session.scope.DefinitionRevision || identity.MeasurementRevision != session.scope.MeasurementRevision || identity.ExclusionRevision != session.requestedRevision {
		return errReadSerialization
	}
	generation, err := q.FenceAlertSourceGeneration(ctx, dbgen.FenceAlertSourceGenerationParams{SliID: session.scope.SliID, MeasurementRevision: session.scope.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		return err
	}
	if generation != session.sourceGeneration {
		return errReadSerialization
	}
	if !session.serviceRangeStart.IsZero() {
		if _, err := q.FenceServiceMaintenanceAuthority(ctx); err != nil {
			return err
		}
		err := requireServiceMaintenanceReady(ctx, q, session.scope.SloID, session.serviceRangeStart, session.serviceRangeEnd)
		if errors.Is(err, ErrDerivedPending) {
			return errReadSerialization
		}
		return err
	}
	return nil
}
