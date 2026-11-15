package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// Resource capture, queue publication and parent deferral share one RC commit.
// A rejected/expired parent rolls back its newly captured pins and shared jobs.
func (r *Runtime) deferAlertMaintenanceWork(ctx context.Context, claim alertClaim, evidence alertPreparedEvidence, limits maintenancePreparationLimits, clock func() time.Time, retry time.Duration) error {
	if !validAlertClaim(claim) || clock == nil || retry <= 0 || retry%time.Microsecond != 0 || evidence.Cutoff.IsZero() || !evidence.Snapshot.Cutoff.Equal(evidence.Cutoff) || len(evidence.Preparation) == 0 {
		return ErrAlertInvalid
	}
	in := evidence.MaintenanceInput
	if !in.SloID.Valid || !in.SliID.Valid || evidence.Snapshot.SLOID != in.SloID.String() || evidence.Snapshot.SLORevision != in.DefinitionRevision || evidence.Snapshot.SLIRevision != in.MeasurementRevision {
		return ErrAlertInvalid
	}
	return r.alertTransaction(ctx, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries) error {
		prepared, err := prepareMaintenanceEnqueue(ctx, tx, maintenancePreparationScope{SloID: in.SloID, SliID: in.SliID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision}, evidence.Preparation, limits, normalizedAgentNow(clock))
		if err != nil {
			if errors.Is(err, ErrMaintenanceSnapshotChanged) {
				return ErrAlertStale
			}
			return err
		}
		// The preparation phase owns SLI SHARE and SLO NO KEY UPDATE before any
		// policy/cursor/queue lock. Re-read authorities after those lock waits.
		exclusion, err := q.ReadMaintenanceState(ctx, in.SloID)
		if err != nil {
			return err
		}
		generation, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: in.SliID, MeasurementRevision: in.MeasurementRevision})
		if errors.Is(err, pgx.ErrNoRows) {
			generation = 0
		} else if err != nil {
			return err
		}
		if exclusion != evidence.Snapshot.ExclusionRevision || generation != evidence.Snapshot.SourceGeneration {
			return ErrAlertStale
		}
		id, _ := canonicalUUID(claim.PolicyID)
		binding, err := q.ReadAlertBinding(ctx, id)
		if err != nil {
			return err
		}
		// Reject a rebound policy before range-head locking could acquire its
		// different SLI/SLO after the resource scope already held here.
		if binding.EvaluationRevision != claim.Revision || binding.SloID != in.SloID || binding.SliID != in.SliID || binding.DefinitionRevision != in.DefinitionRevision || binding.MeasurementRevision != in.MeasurementRevision || binding.CalculationVersion != evidence.Snapshot.CalculationVersion {
			return ErrAlertStale
		}
		cursor, err := lockAlertRangeHead(ctx, q, claim, normalizedAgentNow(clock))
		if err != nil {
			return err
		}
		if !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(evidence.Cutoff) {
			return ErrAlertStale
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() || !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		if err := enqueueMaintenancePreparation(ctx, tx, prepared, maintenancePreparationLive, now); err != nil {
			return err
		}
		for _, job := range prepared.jobs {
			state, err := q.ReadAlertMaintenancePreparationState(ctx, dbgen.ReadAlertMaintenancePreparationStateParams{Kind: job.Kind, SloID: job.SloID, TargetRevision: job.TargetRevision, BuildID: job.BuildID, SecondBuild: job.SecondBuild, SliID: job.SliID, DefinitionRevision: job.DefinitionRevision, MeasurementRevision: job.MeasurementRevision, HourStart: job.HourStart, SourceGeneration: job.SourceGeneration})
			if err != nil {
				return err
			}
			switch state {
			case "pending", "complete":
			case "unavailable", "exhausted", "obsolete":
				if job.Kind == "comparison" {
					// This optional reuse proof never blocks independently queued
					// exact scoped-hour preparation.
					continue
				}
				// A job may stop while this transaction waits for its row. Read
				// unavailable evidence in a fresh RR attempt at the same cutoff.
				return ErrAlertStale
			default:
				return ErrAlertInvalid
			}
		}
		// Queue upserts may wait. Neither retry time nor lease validity may use
		// the clock sampled before that wait.
		now = normalizedAgentNow(clock)
		if now.IsZero() || !cursor.ClaimUntil.Time.After(now) || !now.Add(retry).After(claim.ScheduledAt) {
			return ErrAlertStale
		}
		n, err := q.DeferAlertCursor(ctx, dbgen.DeferAlertCursorParams{PolicyID: cursor.PolicyID, Revision: claim.Revision, AvailableAt: timestamp(now.Add(retry)), WaitingReason: "maintenance_preparation_pending", Epoch: claim.Epoch, ClaimToken: claim.Token, Version: claim.Version, NextScheduledAt: timestamp(claim.ScheduledAt), ClaimUntil: timestamp(now)})
		if err != nil {
			return err
		}
		if n != 1 || !cursor.ClaimUntil.Time.After(normalizedAgentNow(clock)) {
			return ErrAlertStale
		}
		return nil
	})
}
