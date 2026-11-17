package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Explicit owner-supplied limits; this consumer is not registered at startup.
// RetentionStart comes from canonical retention authority, never a fresh guess.
type maintenanceAuthorityOptions struct {
	MaxClaims, MaxChanges, MaxBuckets, MaxScanHours    int
	MaxAttempts                                        int32
	Lease, OperationBudget, PendingRetry, FailureRetry time.Duration
	RetentionStart                                     time.Time
}
type maintenanceAuthorityResult struct{ Claimed, Advanced, Stale, Failed int }

func (o maintenanceAuthorityOptions) valid() bool {
	return o.MaxClaims > 0 && o.MaxClaims <= 1000 && o.MaxChanges > 0 && o.MaxChanges <= 1000 && o.MaxBuckets > 0 && o.MaxBuckets <= (derivedWindowBatchLimit-2)/2 && o.MaxScanHours > 0 && o.MaxScanHours <= 10000 && o.MaxAttempts > 0 && o.Lease > 0 && o.Lease <= 5*time.Minute && o.Lease%time.Microsecond == 0 && o.OperationBudget > 0 && o.OperationBudget <= 5*time.Second && o.OperationBudget <= o.Lease && o.PendingRetry > 0 && o.PendingRetry <= 24*time.Hour && o.FailureRetry > 0 && o.FailureRetry <= 24*time.Hour && validMaintenanceTime(o.RetentionStart)
}

func claimMaintenanceAuthority(ctx context.Context, tx pgx.Tx, tick time.Time, o maintenanceAuthorityOptions, clock func() time.Time) ([]dbgen.AlmanacSloExclusionWork, error) {
	if tx == nil || clock == nil || tick.IsZero() || !o.valid() {
		return nil, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return nil, ErrMaintenanceInvalid
	}
	rows, err := q.LockMaintenanceAuthorityDue(ctx, dbgen.LockMaintenanceAuthorityDueParams{Now: timestamp(now), Tick: timestamp(tick), MaxAttempts: o.MaxAttempts, RowLimit: 1})
	if err != nil {
		return nil, err
	}
	now = normalizedAgentNow(clock)
	if now.IsZero() {
		return nil, ErrMaintenanceInvalid
	}
	for i := range rows {
		rows[i], err = q.LeaseMaintenanceAuthority(ctx, dbgen.LeaseMaintenanceAuthorityParams{SloID: rows[i].SloID, LeaseToken: maintenanceCompilerID(), LeaseUntil: timestamp(now.Add(o.Lease)), LastTick: timestamp(tick)})
		if err != nil {
			return nil, err
		}
	}
	if finished := normalizedAgentNow(clock); finished.IsZero() || !finished.Before(now.Add(o.Lease)) {
		return nil, ErrMaintenancePreparationStale
	}
	return rows, nil
}

func maintenanceAuthorityClaimMatches(row, claim dbgen.AlmanacSloExclusionWork, now time.Time) bool {
	return !now.IsZero() && row.SloID == claim.SloID && row.WorkVersion == claim.WorkVersion && row.DesiredRevision == claim.DesiredRevision && row.LeaseToken == claim.LeaseToken && row.LeaseEpoch == claim.LeaseEpoch && row.LeaseUntil.Valid && row.LeaseUntil.Time.Equal(claim.LeaseUntil.Time) && now.Before(row.LeaseUntil.Time)
}

func (r *Runtime) runMaintenanceAuthorityTurn(ctx context.Context, tick time.Time, o maintenanceAuthorityOptions, clock func() time.Time) (maintenanceAuthorityResult, error) {
	var result maintenanceAuthorityResult
	if r == nil || clock == nil || tick.IsZero() || !o.valid() {
		return result, ErrMaintenanceInvalid
	}
	var failures []error
	for range o.MaxClaims {
		var claims []dbgen.AlmanacSloExclusionWork
		err := r.maintenancePreparationTransaction(ctx, o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			claims, err = claimMaintenanceAuthority(ctx, tx, tick, o, clock)
			return err
		})
		if err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		if len(claims) == 0 {
			break
		}
		claim := claims[0]
		result.Claimed++
		err = r.processMaintenanceAuthority(ctx, claim, o, clock)
		switch {
		case err == nil:
			result.Advanced++
		case errors.Is(err, ErrMaintenancePreparationStale), errors.Is(err, ErrMaintenanceSnapshotChanged):
			result.Stale++
			// Source/definition changes may leave the same token/version owned.
			// Release only that owner; newer edits and expired leases cannot match.
			ack := r.maintenancePreparationTransaction(ctx, o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
				return finishMaintenanceAuthorityFailure(ctx, tx, claim, o, false, clock)
			})
			if ack != nil && !errors.Is(ack, ErrMaintenancePreparationStale) {
				failures = append(failures, ack)
			}
		default:
			if ctx.Err() != nil {
				return result, errors.Join(append(failures, err)...)
			}
			failed := !errors.Is(err, ErrDerivedPending) && !alertBackpressure(err)
			ack := r.maintenancePreparationTransaction(ctx, o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
				return finishMaintenanceAuthorityFailure(ctx, tx, claim, o, failed, clock)
			})
			if failed {
				result.Failed++
				failures = append(failures, err)
			}
			if ack != nil && !errors.Is(ack, ErrMaintenancePreparationStale) {
				failures = append(failures, ack)
			}
		}
	}
	return result, errors.Join(failures...)
}

func (r *Runtime) processMaintenanceAuthority(ctx context.Context, claim dbgen.AlmanacSloExclusionWork, o maintenanceAuthorityOptions, clock func() time.Time) error {
	now := normalizedAgentNow(clock)
	if !maintenanceAuthorityClaimMatches(claim, claim, now) {
		return ErrMaintenancePreparationStale
	}
	bounded, cancel := context.WithTimeout(ctx, min(o.OperationBudget, claim.LeaseUntil.Time.Sub(now)))
	defer cancel()
	ctx = bounded
	// Release this read connection before current's own RR/RC phases. Current's
	// publication retains its existing source fences and failure budgets.
	var boundary pgtype.Timestamptz
	err := r.maintenancePreparationTransaction(ctx, min(o.OperationBudget, claim.LeaseUntil.Time.Sub(now)), func(ctx context.Context, tx pgx.Tx) error {
		after := claim.BoundaryThrough
		if !after.Valid {
			after = timestamp(now)
		}
		var err error
		boundary, err = dbgen.New(tx).NextMaintenanceAuthorityBoundary(ctx, dbgen.NextMaintenanceAuthorityBoundaryParams{SloID: claim.SloID, AfterAt: after, Revision: claim.DesiredRevision})
		return err
	})
	if err != nil {
		return err
	}
	refreshStarted := now
	var refreshedThrough time.Time
	refreshed := claim.CurrentRevision != claim.DesiredRevision || (boundary.Valid && !now.Before(boundary.Time))
	if refreshed {
		if err = r.refreshCurrentProjection(ctx, claim.SloID, clock); err != nil {
			if !errors.Is(err, ErrDerivedPending) && !alertBackpressure(err) {
				return err
			}
			refreshed = false
		}
		if refreshed {
			refreshedThrough = refreshStarted
		}
	}
	now = normalizedAgentNow(clock)
	if !maintenanceAuthorityClaimMatches(claim, claim, now) {
		return ErrMaintenancePreparationStale
	}
	return r.maintenancePreparationTransaction(ctx, min(o.OperationBudget, claim.LeaseUntil.Time.Sub(now)), func(ctx context.Context, tx pgx.Tx) error {
		return advanceMaintenanceAuthority(ctx, tx, claim, o, refreshedThrough, clock)
	})
}

// On failure only the original live owner can release/reschedule the claim.
// A newer authority work version starts its own budget without losing checkpoints.
func finishMaintenanceAuthorityFailure(ctx context.Context, tx pgx.Tx, claim dbgen.AlmanacSloExclusionWork, o maintenanceAuthorityOptions, failed bool, clock func() time.Time) error {
	q := dbgen.New(tx)
	row, err := q.LockMaintenanceAuthorityWork(ctx, claim.SloID)
	if err != nil {
		return err
	}
	now := normalizedAgentNow(clock)
	if !maintenanceAuthorityClaimMatches(row, claim, now) {
		return ErrMaintenancePreparationStale
	}
	retry := o.PendingRetry
	if failed {
		retry = o.FailureRetry
	}
	// Pending releases no counters. This is a queue-only transaction.
	var n int64
	if failed {
		n, err = q.FailMaintenanceAuthorityWork(ctx, dbgen.FailMaintenanceAuthorityWorkParams{SloID: claim.SloID, DesiredRevision: claim.DesiredRevision, WorkVersion: claim.WorkVersion, LeaseToken: claim.LeaseToken, LeaseEpoch: claim.LeaseEpoch, NextAttemptAt: timestamp(now.Add(retry)), Now: timestamp(now)})
	} else {
		n, err = q.ReleaseMaintenanceAuthorityWork(ctx, dbgen.ReleaseMaintenanceAuthorityWorkParams{SloID: claim.SloID, DesiredRevision: claim.DesiredRevision, WorkVersion: claim.WorkVersion, LeaseToken: claim.LeaseToken, LeaseEpoch: claim.LeaseEpoch, NextAttemptAt: timestamp(now.Add(retry)), Now: timestamp(now)})
	}
	if err == nil && n != 1 {
		return ErrMaintenancePreparationStale
	}
	return err
}

func advanceMaintenanceAuthority(ctx context.Context, tx pgx.Tx, claim dbgen.AlmanacSloExclusionWork, o maintenanceAuthorityOptions, refreshedThrough time.Time, clock func() time.Time) error {
	q := dbgen.New(tx)
	now := normalizedAgentNow(clock)
	if !maintenanceAuthorityClaimMatches(claim, claim, now) {
		return ErrMaintenancePreparationStale
	}
	row, err := q.GetSlo(ctx, claim.SloID)
	if err != nil {
		return err
	}
	identity, err := q.ReadMaintenanceEvaluationIdentity(ctx, claim.SloID)
	if err != nil {
		return err
	}
	if row.ExclusionRevision != claim.DesiredRevision {
		return ErrMaintenanceSnapshotChanged
	}
	scope := maintenancePreparationScope{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision}
	progress := claim
	horizon := now.Truncate(time.Hour)
	if !progress.ScheduledThrough.Valid {
		progress.ScheduledThrough = timestamp(horizon)
	}
	requests := []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: claim.DesiredRevision}, {Kind: "compatibility", RequestedRevision: claim.DesiredRevision}}
	// Transfer unfinished earlier targets exactly once. The marker lives on the
	// original queue row; its target, lease, attempts and evidence stay unchanged.
	retargets, err := q.ReadMaintenanceAuthorityRetargets(ctx, dbgen.ReadMaintenanceAuthorityRetargetsParams{SloID: claim.SloID, TargetRevision: claim.DesiredRevision, RowLimit: int32(o.MaxBuckets)})
	if err != nil {
		return err
	}
	buckets := 0
	for _, old := range retargets {
		if old.SliID == row.SliID && old.DefinitionRevision.Int64 == row.DefinitionRevision && old.MeasurementRevision.Int64 == row.MeasurementRevision {
			requests = append(requests, maintenanceAuthorityBucket(row, identity.SourceGeneration, old.Kind, old.HourStart.Time))
		}
		buckets++
	}
	window := max(time.Duration(row.RollingWindowSeconds)*time.Second, 6*time.Hour)
	floor := maxTime(o.RetentionStart, row.EvaluationValidFrom.Time, row.SliValidFrom.Time).Truncate(time.Hour)
	changes := 0
	for progress.ProcessedRevision < claim.DesiredRevision && changes < o.MaxChanges && buckets < o.MaxBuckets {
		revision := progress.ProcessedRevision + 1
		count, err := q.ReadMaintenanceAuthorityChange(ctx, dbgen.ReadMaintenanceAuthorityChangeParams{SloID: row.ID, Revision: revision})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		} // shared compatibility job catches up
		if err != nil {
			return err
		}
		ranges, err := q.ReadMaintenanceAuthorityRanges(ctx, dbgen.ReadMaintenanceAuthorityRangesParams{SloID: row.ID, Revision: revision})
		if err != nil {
			return err
		}
		if len(ranges) != int(count) {
			return ErrMaintenanceInvalid
		}
		if !progress.ProgressUntil.Valid {
			progress.ProgressUntil = timestamp(horizon)
		}
		for int(progress.ProgressPart) < len(ranges) && buckets < o.MaxBuckets {
			part := ranges[progress.ProgressPart]
			start := maxTime(part.StartsAt.Time.Truncate(time.Hour), floor)
			end := maintenanceAuthorityEarlier(part.EndsAt.Time.Add(window).Truncate(time.Hour).Add(time.Hour), progress.ProgressUntil.Time)
			if progress.ProgressHour.Valid {
				start = maxTime(start, progress.ProgressHour.Time)
			}
			for h := start; h.Before(end) && buckets < o.MaxBuckets; h = h.Add(time.Hour) {
				requests = append(requests, maintenanceAuthorityBucket(row, identity.SourceGeneration, "extrema_hour", h))
				day := h.Truncate(24 * time.Hour)
				if !day.Add(24 * time.Hour).After(now) {
					requests = append(requests, maintenanceAuthorityBucket(row, identity.SourceGeneration, "extrema_day", day))
				}
				buckets++
				progress.ProgressHour = timestamp(h.Add(time.Hour))
			}
			if !start.Before(end) || (progress.ProgressHour.Valid && !progress.ProgressHour.Time.Before(end)) {
				progress.ProgressPart++
				progress.ProgressHour = pgtype.Timestamptz{}
			} else {
				break
			}
		}
		if int(progress.ProgressPart) < len(ranges) {
			break
		}
		progress.ProcessedRevision = revision
		progress.ProgressPart = 0
		progress.ProgressHour = pgtype.Timestamptz{}
		progress.ProgressUntil = pgtype.Timestamptz{}
		changes++
	}
	// Time advances independently of change revisions: a far-future declaration
	// never blocks a later historical edit. Probe each closed hour at most once.
	compatibility, err := q.ReadMaintenanceCompatibilityAuthority(ctx, row.ID)
	if err != nil {
		return err
	}
	if compatibility.Unavailable {
		return ErrMaintenanceCompileUnavailable
	}
	scanned := 0
	for progress.ProcessedRevision == claim.DesiredRevision && progress.ScheduledThrough.Time.Before(horizon) && scanned < o.MaxScanHours && buckets < o.MaxBuckets {
		h := maxTime(progress.ScheduledThrough.Time, floor)
		if !h.Before(horizon) {
			progress.ScheduledThrough = timestamp(horizon)
			break
		}
		affected, err := q.HasMaintenanceAuthorityInput(ctx, dbgen.HasMaintenanceAuthorityInputParams{SloID: row.ID, ScopeOrdinal: compatibility.ScopeOrdinal, Revision: claim.DesiredRevision, StartAt: timestamp(h.Add(-window)), EndAt: timestamp(h.Add(time.Hour))})
		if err != nil {
			return err
		}
		if affected {
			requests = append(requests, maintenanceAuthorityBucket(row, identity.SourceGeneration, "extrema_hour", h))
		}
		// Daily composition and its compliance endpoint must also wake at midnight,
		// even when the final hour itself has no maintenance intersection.
		if h.Add(time.Hour).Equal(h.Truncate(24 * time.Hour).Add(24 * time.Hour)) {
			day := h.Truncate(24 * time.Hour)
			affected, err = q.HasMaintenanceAuthorityInput(ctx, dbgen.HasMaintenanceAuthorityInputParams{SloID: row.ID, ScopeOrdinal: compatibility.ScopeOrdinal, Revision: claim.DesiredRevision, StartAt: timestamp(day.Add(-window)), EndAt: timestamp(day.Add(24 * time.Hour))})
			if err != nil {
				return err
			}
			if affected {
				requests = append(requests, maintenanceAuthorityBucket(row, identity.SourceGeneration, "extrema_day", day))
			}
		}
		buckets++
		scanned++
		progress.ScheduledThrough = timestamp(h.Add(time.Hour))
	}
	// Acquire source authority before build/compatibility locks.
	if _, err = q.LockMaintenanceHourSli(ctx, scope.SliID); err != nil {
		return err
	}
	if _, err = q.LockMaintenanceSlo(ctx, scope.SloID); err != nil {
		return err
	}
	if revision, e := q.FenceMaintenanceState(ctx, scope.SloID); e != nil {
		return e
	} else if revision != claim.DesiredRevision {
		return ErrMaintenanceSnapshotChanged
	}
	if generation, e := q.FenceAlertSourceGeneration(ctx, dbgen.FenceAlertSourceGenerationParams{SliID: scope.SliID, MeasurementRevision: scope.MeasurementRevision}); e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	} else if generation != identity.SourceGeneration {
		return ErrMaintenanceSnapshotChanged
	}
	prepared, err := prepareMaintenanceEnqueue(ctx, tx, scope, requests, maintenancePreparationLimits{MaxRequests: len(requests), MaxSourceHours: 1}, now)
	if err != nil {
		return err
	}
	// Resources, actual exclusion authority, and actual source generation all
	// precede the authority queue and shared preparation queue locks.
	session := &maintenanceConsumerSession{scope: scope, requestedRevision: claim.DesiredRevision, sourceGeneration: identity.SourceGeneration, sourceCaptured: true}
	if err = checkMaintenanceConsumerSnapshot(ctx, tx, session); err != nil {
		return errors.Join(ErrMaintenanceSnapshotChanged, err)
	}
	live, err := q.LockMaintenanceAuthorityWork(ctx, claim.SloID)
	if err != nil {
		return err
	}
	finished := normalizedAgentNow(clock)
	if !maintenanceAuthorityClaimMatches(live, claim, finished) {
		return ErrMaintenancePreparationStale
	}
	if !refreshedThrough.IsZero() {
		progress.CurrentRevision = claim.DesiredRevision
		// A boundary crossed while current was being evaluated remains due.
		progress.BoundaryThrough = timestamp(refreshedThrough)
	} else if !progress.BoundaryThrough.Valid {
		progress.BoundaryThrough = timestamp(now)
	}
	if err = enqueueMaintenancePreparation(ctx, tx, prepared, maintenancePreparationHistorical, finished); err != nil {
		return err
	}
	for _, old := range retargets {
		if _, err = q.MarkMaintenanceAuthorityRetarget(ctx, dbgen.MarkMaintenanceAuthorityRetargetParams{ID: old.ID, AuthorityRetargetedRevision: claim.DesiredRevision}); err != nil {
			return err
		}
	}
	next := now.Add(o.PendingRetry)
	if progress.CurrentRevision == claim.DesiredRevision && progress.ProcessedRevision == claim.DesiredRevision && !progress.ScheduledThrough.Time.Before(horizon) && len(retargets) < o.MaxBuckets {
		next = horizon.Add(time.Hour)
		boundary, err := q.NextMaintenanceAuthorityBoundary(ctx, dbgen.NextMaintenanceAuthorityBoundaryParams{SloID: row.ID, AfterAt: progress.BoundaryThrough, Revision: claim.DesiredRevision})
		if err != nil {
			return err
		}
		if boundary.Valid {
			next = maintenanceAuthorityEarlier(next, boundary.Time)
		}
	}
	finished = normalizedAgentNow(clock)
	if !maintenanceAuthorityClaimMatches(live, claim, finished) {
		return ErrMaintenancePreparationStale
	}
	n, err := q.AdvanceMaintenanceAuthorityWork(ctx, dbgen.AdvanceMaintenanceAuthorityWorkParams{SloID: row.ID, DesiredRevision: claim.DesiredRevision, WorkVersion: claim.WorkVersion, LeaseToken: claim.LeaseToken, LeaseEpoch: claim.LeaseEpoch, Now: timestamp(finished), ProcessedRevision: progress.ProcessedRevision, ProgressPart: progress.ProgressPart, ProgressHour: progress.ProgressHour, ProgressUntil: progress.ProgressUntil, ScheduledThrough: progress.ScheduledThrough, CurrentRevision: progress.CurrentRevision, BoundaryThrough: progress.BoundaryThrough, NextAttemptAt: timestamp(next)})
	if err == nil && n != 1 {
		return ErrMaintenancePreparationStale
	}
	return err
}

func maintenanceAuthorityBucket(row dbgen.GetSloRow, generation int64, kind string, start time.Time) maintenanceEvaluationPreparation {
	return maintenanceEvaluationPreparation{Kind: kind, RequestedRevision: row.ExclusionRevision, Hour: maintenanceHourKey{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: generation, HourStart: start}}
}

func maintenanceAuthorityEarlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
