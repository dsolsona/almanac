package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// A consumer session belongs to one caller-owned repeatable-read transaction.
// Callers supply admitted limits; constructing one does not enable any worker.
type maintenanceConsumerSession struct {
	tx                pgx.Tx
	requestedRevision int64
	limits            maintenanceWindowLimits
	preparationLimits maintenancePreparationLimits
	maxRetainedProofs int
	scope             maintenancePreparationScope
	sourceGeneration  int64
	sourceCaptured    bool
	preparation       []maintenanceEvaluationPreparation
	serviceRangeStart time.Time
	serviceRangeEnd   time.Time
}

// No production profile is selected here. Internal acceptance callers supply
// all work budgets explicitly before enabling shared consumer adoption.
type maintenanceConsumerOptions struct {
	Windows           maintenanceWindowLimits
	Preparation       maintenancePreparationLimits
	MaxRetainedProofs int
	// Only the tagged acceptance adapter supplies this isolated fixture control.
	pauseSource func() bool
}

func captureMaintenanceConsumer(ctx context.Context, tx pgx.Tx, row dbgen.GetSloRow, runtime *Runtime) (*maintenanceConsumerSession, error) {
	if runtime == nil {
		runtime = boundRuntime(ctx)
	}
	var options *maintenanceConsumerOptions
	if runtime != nil {
		options = runtime.maintenanceConsumers.Load()
	}
	if options == nil {
		if row.ExclusionRevision != 0 {
			return nil, ErrDerivedPending
		}
		return nil, nil
	}
	if tx == nil || row.ExclusionRevision < 0 {
		return nil, ErrMaintenanceInvalid
	}
	return &maintenanceConsumerSession{tx: tx, requestedRevision: row.ExclusionRevision, limits: options.Windows,
		preparationLimits: options.Preparation, maxRetainedProofs: options.MaxRetainedProofs,
		scope: maintenancePreparationScope{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision}}, nil
}

type maintenanceConsumerBatch struct {
	input  maintenanceEvaluationInput
	result maintenanceEvaluationResult
	values []slo.WindowResult
}

func (session *maintenanceConsumerSession) evaluate(ctx context.Context, row dbgen.GetSloRow, windows []slo.WindowInput, summary, retain bool) (maintenanceConsumerBatch, error) {
	out := maintenanceConsumerBatch{input: maintenanceEvaluationInput{
		SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		Summary: summary, RetainFacts: retain, Limits: session.limits,
		Requests: []maintenanceMaskWindows{{RequestedRevision: session.requestedRevision, Windows: windows}},
	}}
	if len(windows) > 0 {
		start, end := serviceMaintenanceWindowRange(windows)
		session.trackServiceMaintenanceRange(start, end)
	}
	var err error
	out.result, err = evaluateMaintenanceWindows(ctx, session.tx, out.input)
	if err == nil || errors.Is(err, ErrDerivedPending) && len(out.result.Preparation) > 0 {
		scope := maintenancePreparationScope{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision}
		if session.scope.SloID.Valid && session.scope != scope || session.sourceCaptured && session.sourceGeneration != out.result.SourceGeneration {
			return maintenanceConsumerBatch{}, ErrMaintenanceSnapshotChanged
		}
		session.scope, session.sourceGeneration, session.sourceCaptured = scope, out.result.SourceGeneration, true
		if errors.Is(err, ErrDerivedPending) {
			session.preparation = append(session.preparation, out.result.Preparation...)
		}
	}
	if err != nil {
		return out, err
	}
	if len(out.result.Masks) != 1 || len(out.result.Masks[0].Windows) != len(windows) || len(out.result.Masks[0].Unavailable) != len(windows) {
		return out, ErrDerivedFailed
	}
	mask := out.result.Masks[0]
	out.values = make([]slo.WindowResult, len(windows))
	for index, value := range mask.Windows {
		if reason := mask.Unavailable[index]; reason != "" {
			value, err = unavailableMaintenanceWindow(windows[index], reason)
			if err != nil {
				return maintenanceConsumerBatch{}, err
			}
		}
		out.values[index] = value
	}
	return out, nil
}

func (session *maintenanceConsumerSession) trackServiceMaintenanceRange(start, end time.Time) {
	if session.serviceRangeStart.IsZero() || start.Before(session.serviceRangeStart) {
		session.serviceRangeStart = start
	}
	session.serviceRangeEnd = maxTime(session.serviceRangeEnd, end)
}
