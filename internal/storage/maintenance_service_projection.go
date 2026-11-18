package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *Runtime) runServiceMaintenanceProjection(ctx context.Context, pageLimit int, budget time.Duration, clock func() time.Time) error {
	if pageLimit < 1 || pageLimit > 64 || budget <= 0 || clock == nil {
		return ErrMaintenanceInvalid
	}
	windowErr := r.maintenancePreparationTransaction(ctx, budget, func(ctx context.Context, tx pgx.Tx) error {
		return projectServiceMaintenancePage(ctx, tx, pageLimit, clock)
	})
	memberErr := r.maintenancePreparationTransaction(ctx, budget, func(ctx context.Context, tx pgx.Tx) error {
		return projectServiceMaintenanceMembershipPage(ctx, tx, pageLimit, clock)
	})
	return errors.Join(windowErr, memberErr)
}

func projectServiceMaintenancePage(ctx context.Context, tx pgx.Tx, pageLimit int, clock func() time.Time) error {
	q := dbgen.New(tx)
	claim, err := q.LockNextServiceMaintenanceProjection(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ids, err := q.ListServiceMaintenanceProjectionSlos(ctx, dbgen.ListServiceMaintenanceProjectionSlosParams{
		ExclusionID: claim.ExclusionID, Revision: claim.DesiredRevision, AfterSlo: claim.ThroughSlo, PageLimit: int32(pageLimit),
	})
	if err != nil {
		return err
	}
	// The expiry work and explicit writer take the same order. Holding the
	// root locks first also prevents a definition move during projection.
	for _, id := range ids {
		if _, err := q.LockMaintenanceSlo(ctx, id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := q.LockMaintenanceState(ctx, id); err != nil {
			return err
		}
	}
	if _, err := q.FenceServiceMaintenanceAuthority(ctx); err != nil {
		return err
	}
	_, err = q.LockObservedServiceMaintenanceProjection(ctx, dbgen.LockObservedServiceMaintenanceProjectionParams{
		ExclusionID: claim.ExclusionID, Revision: claim.DesiredRevision, ThroughSlo: claim.ThroughSlo,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return ErrMaintenanceInvalid
	}
	after := claim.ThroughSlo
	for _, id := range ids {
		if _, err := q.ApplyServiceMaintenanceProjectionMember(ctx, dbgen.ApplyServiceMaintenanceProjectionMemberParams{
			SloID: id, ExclusionID: claim.ExclusionID, Revision: claim.DesiredRevision, NowAt: timestamp(now),
		}); err != nil {
			return err
		}
		after = id
	}
	n, err := q.AdvanceServiceMaintenanceProjection(ctx, dbgen.AdvanceServiceMaintenanceProjectionParams{
		ExclusionID: claim.ExclusionID, Revision: claim.DesiredRevision, ThroughSlo: after, Complete: len(ids) < pageLimit,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrMaintenanceSnapshotChanged
	}
	if len(ids) < pageLimit {
		return q.CompleteServiceMaintenanceProjection(ctx, dbgen.CompleteServiceMaintenanceProjectionParams{
			ExclusionID: claim.ExclusionID, Revision: claim.DesiredRevision,
		})
	}
	return nil
}

func projectServiceMaintenanceMembershipPage(ctx context.Context, tx pgx.Tx, pageLimit int, clock func() time.Time) error {
	q := dbgen.New(tx)
	claim, err := q.LockServiceMaintenanceMembershipWork(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return projectServiceMaintenanceMembershipClaim(ctx, q, claim, pageLimit, clock)
}

func projectServiceMaintenanceMembershipClaim(ctx context.Context, q *dbgen.Queries, claim dbgen.AlmanacServiceMaintenanceMembershipWork, pageLimit int, clock func() time.Time) error {
	if _, err := q.LockMaintenanceState(ctx, claim.SloID); err != nil {
		return err
	}
	if _, err := q.FenceServiceMaintenanceAuthority(ctx); err != nil {
		return err
	}
	_, err := q.LockObservedServiceMaintenanceMembershipWork(ctx, dbgen.LockObservedServiceMaintenanceMembershipWorkParams{
		SloID: claim.SloID, ThroughExclusion: claim.ThroughExclusion, AffectedFrom: claim.AffectedFrom,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	targets, err := q.ListServiceMaintenanceMembershipTargets(ctx, dbgen.ListServiceMaintenanceMembershipTargetsParams{
		AfterExclusion: claim.ThroughExclusion, AffectedFrom: claim.AffectedFrom, PageLimit: int32(pageLimit),
	})
	if err != nil {
		return err
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return ErrMaintenanceInvalid
	}
	for _, target := range targets {
		if _, err := q.ApplyServiceMaintenanceProjectionMember(ctx, dbgen.ApplyServiceMaintenanceProjectionMemberParams{
			SloID: claim.SloID, ExclusionID: target.ExclusionID, Revision: target.DesiredRevision, NowAt: timestamp(now),
		}); err != nil {
			return err
		}
	}
	if len(targets) < pageLimit {
		return q.FinishServiceMaintenanceMembershipWork(ctx, claim.SloID)
	}
	return q.AdvanceServiceMaintenanceMembershipWork(ctx, dbgen.AdvanceServiceMaintenanceMembershipWorkParams{
		SloID: claim.SloID, ThroughExclusion: targets[len(targets)-1].ExclusionID,
	})
}

func serviceMaintenanceWindowRange(windows []slo.WindowInput) (time.Time, time.Time) {
	start, end := windows[0].At.Add(-windows[0].Duration), windows[0].At
	for _, window := range windows[1:] {
		if candidate := window.At.Add(-window.Duration); candidate.Before(start) {
			start = candidate
		}
		end = maxTime(end, window.At)
	}
	return start, end
}

func requireServiceMaintenanceReady(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, start, end time.Time) error {
	pending, err := q.HasPendingServiceMaintenance(ctx, dbgen.HasPendingServiceMaintenanceParams{
		SloID: id, StartAt: timestamp(start), EndAt: timestamp(end),
	})
	if err != nil {
		return err
	}
	if pending {
		return ErrDerivedPending
	}
	return nil
}
