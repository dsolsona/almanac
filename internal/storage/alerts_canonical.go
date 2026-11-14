package storage

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
)

var ErrAlertExclusionsPending = errors.Join(ErrDerivedPending, errors.New("storage: alert exclusion calculator not integrated"))

type alertPreparedEvidence struct {
	Cutoff time.Time
	Input  alert.Input
}

// readAlertEvidence is an unwired canonical adapter. Persist the first selected
// cutoff before calculation, then retain that pin through every pending retry.
// Returning a cutoff on error is preparation metadata, never partial evidence.
func (r *Runtime) readAlertEvidence(ctx context.Context, claim alertClaim, cutoff time.Time, limits alert.Limits, clock func() time.Time) (alertPreparedEvidence, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "alert.evidence.read")
	defer span.End()
	if !validAlertClaim(claim) || clock == nil {
		return alertPreparedEvidence{}, ErrAlertInvalid
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() || now.Before(claim.ScheduledAt) {
		return alertPreparedEvidence{}, ErrAlertInvalid
	}
	pinned, err := r.pinAlertCutoff(ctx, claim, clock)
	if err != nil {
		return alertPreparedEvidence{}, err
	}
	if !cutoff.IsZero() && !cutoff.Equal(pinned) {
		return alertPreparedEvidence{}, ErrAlertStale
	}
	cutoff = pinned
	now = normalizedAgentNow(clock)
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return alertPreparedEvidence{}, err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return alertPreparedEvidence{}, err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return alertPreparedEvidence{}, err
	}
	prepared, err := prepareAlertEvidence(bounded, dbgen.New(tx), claim, cutoff, limits, now)
	if err != nil {
		return prepared, err
	}
	if err := tx.Commit(bounded); err != nil {
		return alertPreparedEvidence{Cutoff: prepared.Cutoff}, err
	}
	return prepared, nil
}

func prepareAlertEvidence(ctx context.Context, q *dbgen.Queries, claim alertClaim, cutoff time.Time, limits alert.Limits, now time.Time) (alertPreparedEvidence, error) {
	policyID, _ := canonicalUUID(claim.PolicyID)
	binding, err := q.ReadAlertBinding(ctx, policyID)
	if err != nil {
		return alertPreparedEvidence{}, err
	}
	row, err := q.GetSlo(ctx, binding.SloID)
	if err != nil {
		return alertPreparedEvidence{}, err
	}
	if binding.EvaluationRevision != claim.Revision || binding.DefinitionRevision != row.DefinitionRevision || binding.MeasurementRevision != row.MeasurementRevision || binding.CalculationVersion != operationalCalculationVersion {
		return alertPreparedEvidence{}, ErrAlertStale
	}
	var conditions []alert.Condition
	if err := unmarshalAlertData(binding.Conditions, &conditions); err != nil {
		return alertPreparedEvidence{}, err
	}
	input := alert.Input{Policy: alert.PolicyRevision{ID: claim.PolicyID, Revision: claim.Revision, Conditions: conditions}, Objective: alertObjective(row, now)}
	if err := alert.Validate(input.Policy, input.Objective, limits); err != nil {
		return alertPreparedEvidence{}, err
	}
	source, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return alertPreparedEvidence{}, err
	}
	if cutoff.IsZero() {
		cutoff, err = liveSloEvaluationEnd(ctx, q, row, claim.ScheduledAt, source.SettlingDelaySeconds)
		if err != nil {
			return alertPreparedEvidence{}, err
		}
	}
	result := alertPreparedEvidence{Cutoff: cutoff}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	if cutoff.IsZero() || cutoff.After(claim.ScheduledAt) || !floorSliBoundary(cutoff, cadence).Equal(cutoff) {
		return result, ErrAlertInvalid
	}
	exclusion, err := q.ReadMaintenanceState(ctx, row.ID)
	if err != nil {
		return result, err
	}
	if exclusion != 0 {
		// The shared nonzero-mask calculator is a separate integration gate.
		// Even a future or metadata-only declaration cannot be relabeled zero.
		return result, ErrAlertExclusionsPending
	}
	generation, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		return result, err
	}
	input.Snapshot = alert.SnapshotIdentity{SLOID: row.ID.String(), SLORevision: row.DefinitionRevision, SLIRevision: row.MeasurementRevision, SourceGeneration: generation, ExclusionRevision: exclusion, CalculationVersion: binding.CalculationVersion, Cutoff: cutoff}
	unique := make(map[time.Duration]bool)
	for _, condition := range conditions {
		unique[condition.Short], unique[condition.Long] = true, true
	}
	durations := make([]time.Duration, 0, len(unique))
	for duration := range unique {
		durations = append(durations, duration)
	}
	slices.Sort(durations)
	grace := cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))
	windows := make([]slo.WindowInput, 0, len(durations))
	for _, duration := range durations {
		windows = append(windows, slo.WindowInput{At: cutoff, Now: now, Duration: duration, Cadence: cadence, FreshnessGrace: grace, Target: input.Objective.Target, ValidFrom: input.Objective.DefinitionValidFrom, SliValidFrom: input.Objective.MeasurementValidFrom, RetentionStart: input.Objective.RetentionStart})
	}
	input.Windows, err = readAlertCanonicalWindows(ctx, q, row.SliID, row.MeasurementRevision, windows, input.Snapshot, claim.ScheduledAt)
	if err != nil {
		return result, err
	}
	if _, err := alert.Evaluate(input, limits); err != nil {
		return result, err
	}
	result.Input = input
	return result, nil
}

func canonicalAlertWindow(identity alert.SnapshotIdentity, scheduled time.Time, grace, duration time.Duration, value slo.WindowResult) (alert.WindowEvidence, error) {
	window := alert.WindowEvidence{Snapshot: identity, Start: identity.Cutoff.Add(-duration), End: identity.Cutoff, Reason: value.Reason}
	if !value.End.Equal(window.End) || !value.RequestedStart.Equal(window.Start) {
		return window, alert.ErrEvidence
	}
	switch value.State {
	case "complete":
		window.Quality = alert.Complete
		if value.KnownEmpty > 0 {
			window.Quality = alert.Partial
			if value.Observed == 0 {
				window.Quality = alert.Missing
			}
		} else if value.Freshness == "stale" || value.LastObservation != nil && scheduled.After(value.LastObservation.Add(grace)) {
			window.Quality, window.Reason = alert.Stale, "stale_measurements"
		} else if value.Freshness != "fresh" {
			return window, alert.ErrEvidence
		}
	case "partial":
		window.Quality = alert.Partial
	case "no_data":
		window.Quality = alert.Missing
	case "zero_traffic":
		window.Quality = alert.ZeroTraffic
	case "expired_history":
		window.Quality = alert.Expired
	case "not_applicable":
		window.Quality = alert.Warmup
	case "excluded":
		window.Quality = alert.Excluded
	default:
		return window, alert.ErrEvidence
	}
	if value.Good != nil {
		window.Good = sli.NormalizeDecimal(*value.Good)
	}
	if value.Total != nil {
		window.Total = sli.NormalizeDecimal(*value.Total)
	}
	return window, nil
}
