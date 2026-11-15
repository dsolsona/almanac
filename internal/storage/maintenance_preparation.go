package storage

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrMaintenancePreparationStale = errors.New("storage: maintenance preparation claim expired")

type maintenancePreparationScope struct {
	SloID, SliID                            pgtype.UUID
	DefinitionRevision, MeasurementRevision int64
}
type maintenancePreparationPriority string

const (
	maintenancePreparationLive       maintenancePreparationPriority = "live"
	maintenancePreparationHistorical maintenancePreparationPriority = "historical"
)

type maintenancePreparationLimits struct{ MaxRequests, MaxSourceHours int }
type maintenancePreparedEnqueue struct {
	tx    pgx.Tx
	scope maintenancePreparationScope
	jobs  []dbgen.EnqueueMaintenancePreparationParams
	hours []pgtype.Timestamptz
}

// The caller must roll back on any error. Resource capture precedes policy/cursor
// and queue locks; the prepared value may only be used in this transaction.
// RC makes reads after SLI/SLO lock waits see the authority that just committed.
func prepareMaintenanceEnqueue(ctx context.Context, tx pgx.Tx, scope maintenancePreparationScope, requests []maintenanceEvaluationPreparation, limits maintenancePreparationLimits, now time.Time) (maintenancePreparedEnqueue, error) {
	out := maintenancePreparedEnqueue{tx: tx, scope: scope}
	if tx == nil || !scope.SloID.Valid || !scope.SliID.Valid || scope.DefinitionRevision < 1 || scope.MeasurementRevision < 1 || now.IsZero() || limits.MaxRequests < 1 || limits.MaxRequests > derivedWindowBatchLimit || limits.MaxSourceHours < 1 || limits.MaxSourceHours > 10000 {
		return out, ErrMaintenanceInvalid
	}
	if len(requests) > limits.MaxRequests {
		return out, ErrOperationalResource
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// Validate and admit all source expansion before any write or resource lock.
	hours := map[int64]time.Time{}
	for _, r := range requests {
		if r.RequestedRevision < 0 {
			return out, ErrMaintenanceInvalid
		}
		switch r.Kind {
		case "mask", "compatibility":
		case "comparison":
			if r.Comparison == nil || !r.Comparison.FirstBuild.Valid || !r.Comparison.SecondBuild.Valid {
				return out, ErrMaintenanceInvalid
			}
		case "scoped_hour", "source_hour":
			k := r.Hour
			if k.SloID != scope.SloID || k.SliID != scope.SliID || k.DefinitionRevision != scope.DefinitionRevision || k.MeasurementRevision != scope.MeasurementRevision || !validMaintenanceHourKey(k) {
				return out, ErrMaintenanceInvalid
			}
			if r.Kind == "source_hour" {
				hours[k.HourStart.UnixMicro()] = k.HourStart.UTC()
			}
		case "source_window":
			if !validMaintenanceTime(r.Start) || !validMaintenanceTime(r.End) || !r.Start.Before(r.End) || !r.Start.Equal(r.Start.Truncate(time.Hour)) || !r.End.Equal(r.End.Truncate(time.Hour)) {
				return out, ErrMaintenanceInvalid
			}
			n := (r.End.UnixMicro() - r.Start.UnixMicro()) / time.Hour.Microseconds()
			if n > int64(limits.MaxSourceHours) {
				return out, ErrOperationalResource
			}
			for h := r.Start; h.Before(r.End); h = h.Add(time.Hour) {
				hours[h.UnixMicro()] = h.UTC()
			}
		default:
			return out, ErrMaintenanceInvalid
		}
		if len(hours) > limits.MaxSourceHours {
			return out, ErrOperationalResource
		}
	}
	q := dbgen.New(tx)
	isolation, err := q.ReadMaintenanceHourIsolation(ctx)
	if err != nil {
		return out, err
	}
	if isolation != "read committed" {
		return out, ErrMaintenanceSnapshotChanged
	}
	if _, err = q.LockMaintenanceHourSli(ctx, scope.SliID); err != nil {
		return out, err
	}
	if _, err = q.LockMaintenanceSlo(ctx, scope.SloID); err != nil {
		return out, err
	}
	identity, err := q.ReadMaintenanceEvaluationIdentity(ctx, scope.SloID)
	if err != nil {
		return out, err
	}
	if identity.SliID != scope.SliID || identity.DefinitionRevision != scope.DefinitionRevision || identity.MeasurementRevision != scope.MeasurementRevision {
		return out, ErrMaintenanceSnapshotChanged
	}
	if identity.MeasurementKind != "event_counts" || identity.ComponentMode != "interval_count" {
		return out, ErrMaintenanceInvalid
	}
	// Acquire the canonical queue FK's resource fence before any queue rows.
	if len(hours) > 0 {
		_, err = q.FenceAlertSourceGeneration(ctx, dbgen.FenceAlertSourceGenerationParams{SliID: scope.SliID, MeasurementRevision: scope.MeasurementRevision})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	// A SLO root serializes its capture sequence. Sort stable resource identities
	// anyway, so queue/source locks have a consistent order across callers.
	requests = slices.Clone(requests)
	slices.SortFunc(requests, func(a, b maintenanceEvaluationPreparation) int {
		return cmp.Or(cmp.Compare(a.RequestedRevision, b.RequestedRevision), cmp.Compare(a.Kind, b.Kind))
	})
	needsCompatibility := false
	for _, r := range requests {
		if r.RequestedRevision > identity.ExclusionRevision {
			return out, ErrMaintenanceSnapshotChanged
		}
		p := dbgen.EnqueueMaintenancePreparationParams{ID: maintenanceCompilerID(), Kind: r.Kind, SloID: scope.SloID, TargetRevision: r.RequestedRevision, Now: timestamp(now)}
		switch r.Kind {
		case "source_hour", "source_window":
			continue
		case "mask", "compatibility":
			// All builds must be acquired before the shared compatibility row:
			// catch-up workers lock their target build then that authority.
			needsCompatibility = needsCompatibility || r.Kind == "compatibility"
			b, err := captureMaintenanceBuild(ctx, tx, scope.SloID, &r.RequestedRevision, now)
			if err != nil {
				return out, err
			}
			p.BuildID = b.ID
		case "scoped_hour":
			b, e := q.LockMaintenanceHourBuild(ctx, r.Hour.BuildID)
			if e != nil {
				return out, e
			}
			if b.SloID != scope.SloID || b.TargetRevision != r.RequestedRevision || (r.BuildID.Valid && r.BuildID != b.ID) {
				return out, ErrMaintenanceInvalid
			}
			p.BuildID = b.ID
			p.SliID = scope.SliID
			p.DefinitionRevision = pgtype.Int8{Int64: scope.DefinitionRevision, Valid: true}
			p.MeasurementRevision = pgtype.Int8{Int64: scope.MeasurementRevision, Valid: true}
			p.HourStart = timestamp(r.Hour.HourStart)
			p.SourceGeneration = pgtype.Int8{Int64: r.Hour.SourceGeneration, Valid: true}
		case "comparison":
			a, e := q.LockMaintenanceHourBuild(ctx, r.Comparison.FirstBuild)
			if e != nil {
				return out, e
			}
			b, e := q.LockMaintenanceHourBuild(ctx, r.Comparison.SecondBuild)
			if e != nil {
				return out, e
			}
			if a.SloID != scope.SloID || b.SloID != scope.SloID || a.Phase != "complete" || b.Phase != "complete" {
				return out, ErrMaintenanceInvalid
			}
			if a.TargetRevision > b.TargetRevision {
				a, b = b, a
			}
			if b.TargetRevision != r.RequestedRevision {
				return out, ErrMaintenanceInvalid
			}
			p.BuildID = a.ID
			p.SecondBuild = b.ID
		}
		out.jobs = append(out.jobs, p)
	}
	if needsCompatibility {
		if err := q.EnsureMaintenanceCompatibility(ctx, scope.SloID); err != nil {
			return out, err
		}
		if _, err := q.LockMaintenanceCompatibility(ctx, scope.SloID); err != nil {
			return out, err
		}
	}
	for _, h := range hours {
		out.hours = append(out.hours, timestamp(h))
	}
	slices.SortFunc(out.hours, func(a, b pgtype.Timestamptz) int { return a.Time.Compare(b.Time) })
	slices.SortFunc(out.jobs, func(a, b dbgen.EnqueueMaintenancePreparationParams) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.BuildID.String(), b.BuildID.String()), cmp.Compare(a.SecondBuild.String(), b.SecondBuild.String()), a.HourStart.Time.Compare(b.HourStart.Time), cmp.Compare(a.SourceGeneration.Int64, b.SourceGeneration.Int64))
	})
	return out, nil
}

func prepareMaintenanceCompatibilityAt(ctx context.Context, tx pgx.Tx, id pgtype.UUID, revision int64, now time.Time) (dbgen.AlmanacMaintenanceBuild, error) {
	b, err := captureMaintenanceBuild(ctx, tx, id, &revision, now)
	if err != nil {
		return b, err
	}
	q := dbgen.New(tx)
	if err = q.EnsureMaintenanceCompatibility(ctx, id); err != nil {
		return b, err
	}
	_, err = q.LockMaintenanceCompatibility(ctx, id)
	return b, err
}

// Queue-only phase. All resource/policy authority must already be held; this
// helper never acquires a resource lock after touching a durable queue row.
func enqueueMaintenancePreparation(ctx context.Context, tx pgx.Tx, p maintenancePreparedEnqueue, priority maintenancePreparationPriority, now time.Time) error {
	if tx == nil || p.tx != tx || now.IsZero() || (priority != maintenancePreparationLive && priority != maintenancePreparationHistorical) {
		return ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	if len(p.hours) > 0 {
		if err := q.EnqueueMissingMaintenanceSourceHours(ctx, dbgen.EnqueueMissingMaintenanceSourceHoursParams{SliID: p.scope.SliID, MeasurementRevision: p.scope.MeasurementRevision, Hours: p.hours, Now: timestamp(now)}); err != nil {
			return err
		}
	}
	for _, job := range p.jobs {
		job.Priority = string(priority)
		job.Now = timestamp(now)
		if err := q.EnqueueMaintenancePreparation(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

func claimMaintenancePreparation(ctx context.Context, tx pgx.Tx, priority maintenancePreparationPriority, tick time.Time, lease time.Duration, limit int, clock func() time.Time) ([]dbgen.AlmanacMaintenancePreparation, error) {
	if tx == nil || clock == nil || tick.IsZero() || lease <= 0 || lease > 5*time.Minute || lease%time.Microsecond != 0 || limit < 1 || limit > 1000 || (priority != maintenancePreparationLive && priority != maintenancePreparationHistorical) {
		return nil, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return nil, ErrMaintenanceInvalid
	}
	rows, err := q.LockMaintenancePreparationDue(ctx, dbgen.LockMaintenancePreparationDueParams{Priority: string(priority), Now: timestamp(now), Tick: timestamp(tick), RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	now = normalizedAgentNow(clock)
	if now.IsZero() {
		return nil, ErrMaintenanceInvalid
	}
	for i := range rows {
		rows[i], err = q.LeaseMaintenancePreparation(ctx, dbgen.LeaseMaintenancePreparationParams{ID: rows[i].ID, LeaseToken: maintenanceCompilerID(), LeaseUntil: timestamp(now.Add(lease)), LastTick: timestamp(tick), UpdatedAt: timestamp(now)})
		if err != nil {
			return nil, err
		}
	}
	if finished := normalizedAgentNow(clock); finished.IsZero() || !finished.Before(now.Add(lease)) {
		return nil, ErrMaintenancePreparationStale
	}
	return rows, nil
}

func finishMaintenancePreparation(ctx context.Context, tx pgx.Tx, claim dbgen.AlmanacMaintenancePreparation, state, reason string, retry time.Duration, maxAttempts int32, clock func() time.Time) error {
	if tx == nil || clock == nil || !claim.ID.Valid || !claim.LeaseToken.Valid || !claim.LeaseUntil.Valid || maxAttempts < 1 || retry < 0 || retry > 24*time.Hour || retry%time.Microsecond != 0 {
		return ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	row, err := q.LockMaintenancePreparation(ctx, claim.ID)
	if err != nil {
		return err
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() || row.State != "pending" || row.LeaseToken != claim.LeaseToken || row.LeaseEpoch != claim.LeaseEpoch || !row.LeaseUntil.Valid || !row.LeaseUntil.Time.Equal(claim.LeaseUntil.Time) || !now.Before(row.LeaseUntil.Time) {
		return ErrMaintenancePreparationStale
	}
	attempts := row.Attempts
	if state == "failed" {
		if attempts >= maxAttempts-1 {
			attempts = maxAttempts
			state = "exhausted"
		} else {
			attempts++
			state = "pending"
		}
	}
	switch state {
	case "pending", "complete", "unavailable", "obsolete", "exhausted":
	default:
		return ErrMaintenanceInvalid
	}
	if state == "pending" && retry == 0 {
		return ErrMaintenanceInvalid
	}
	n, err := q.FinishMaintenancePreparation(ctx, dbgen.FinishMaintenancePreparationParams{ID: claim.ID, LeaseToken: claim.LeaseToken, LeaseEpoch: claim.LeaseEpoch, State: state, Reason: reason, Attempts: attempts, AvailableAt: timestamp(now.Add(retry)), Now: timestamp(now)})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrMaintenancePreparationStale
	}
	return nil
}
