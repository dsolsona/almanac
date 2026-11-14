package storage

import (
	"context"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// pinAlertCutoff makes canonical eligibility a durable property of the current
// scheduled head. New source facts may change counts, but never this boundary.
// The authority transaction reads only the bounded ready tail, not any windows.
func (r *Runtime) pinAlertCutoff(ctx context.Context, claim alertClaim, clock func() time.Time) (time.Time, error) {
	if clock == nil {
		return time.Time{}, ErrAlertInvalid
	}
	var cutoff time.Time
	err := r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		cursor, err := lockAlertRangeHead(ctx, q, claim, normalizedAgentNow(clock))
		if err != nil {
			return err
		}
		if cursor.PendingCutoff.Valid {
			cutoff = cursor.PendingCutoff.Time.UTC()
		} else {
			binding, err := q.ReadAlertBinding(ctx, cursor.PolicyID)
			if err != nil {
				return err
			}
			row, err := q.GetSlo(ctx, binding.SloID)
			if err != nil {
				return err
			}
			source, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
			if err != nil {
				return err
			}
			cutoff, err = liveSloEvaluationEnd(ctx, q, row, claim.ScheduledAt, source.SettlingDelaySeconds)
			if err != nil {
				return err
			}
			n, err := q.PinAlertCutoff(ctx, dbgen.PinAlertCutoffParams{PolicyID: cursor.PolicyID, Revision: cursor.Revision, PendingCutoff: timestamp(cutoff), Epoch: claim.Epoch, ClaimToken: claim.Token, Version: claim.Version, NextScheduledAt: timestamp(claim.ScheduledAt)})
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrAlertStale
			}
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() || !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return cutoff, nil
}
