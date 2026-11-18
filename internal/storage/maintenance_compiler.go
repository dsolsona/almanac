package storage

import (
	"context"
	"errors"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrMaintenanceCompilePending     = errors.New("storage: maintenance snapshot not complete")
	ErrMaintenanceCompileUnavailable = errors.New("storage: required maintenance history unavailable")
	ErrMaintenanceCompileStale       = errors.New("storage: maintenance compiler ownership expired")
)

type maintenanceBuildClaim struct {
	ID, Token pgtype.UUID
	Deadline  time.Time
}
type maintenanceCompilerProgress struct {
	Phase     string
	Scanned   int
	RawCursor int64
}

func maintenanceCompilerID() pgtype.UUID {
	id := uuid.New()
	return pgtype.UUID{Bytes: id, Valid: true}
}

// These unwired primitives participate in the caller's transaction. On error the
// caller must roll back; on success commit immediately after the final clock
// fence. The caller owns admission/cancellation, not a production worker default.
func captureMaintenanceBuild(ctx context.Context, tx pgx.Tx, id pgtype.UUID, revision *int64, now time.Time) (dbgen.AlmanacMaintenanceBuild, error) {
	var empty dbgen.AlmanacMaintenanceBuild
	if !id.Valid || now.IsZero() || revision != nil && *revision < 0 {
		return empty, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	// Existing maintenance writers acquire SLO root then exclusion state. The
	// deletion guard follows both; replay never acquires these locks afterward.
	if _, err := q.LockMaintenanceSlo(ctx, id); err != nil {
		return empty, err
	}
	current, err := q.LockMaintenanceState(ctx, id)
	if err != nil {
		return empty, err
	}
	target := current
	if revision != nil {
		target = *revision
	}
	if target > current {
		return empty, ErrMaintenanceSnapshotChanged
	}
	if err := q.TouchMaintenanceInputGuard(ctx, id); err != nil {
		return empty, err
	}
	return q.CaptureMaintenanceBuild(ctx, dbgen.CaptureMaintenanceBuildParams{ID: maintenanceCompilerID(), SloID: id, TargetRevision: target, CreatedAt: timestamp(now)})
}

func claimMaintenanceBuild(ctx context.Context, tx pgx.Tx, id pgtype.UUID, lease time.Duration, clock func() time.Time) (maintenanceBuildClaim, error) {
	var empty maintenanceBuildClaim
	if !id.Valid || clock == nil || lease <= 0 || lease > 5*time.Minute || lease%time.Microsecond != 0 {
		return empty, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	b, err := q.LockMaintenanceBuild(ctx, id)
	if err != nil {
		return empty, err
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return empty, ErrMaintenanceInvalid
	}
	if b.Phase == "unavailable" {
		return empty, ErrMaintenanceCompileUnavailable
	}
	if b.Phase != "replay" && b.Phase != "union" || b.LeaseUntil.Valid && now.Before(b.LeaseUntil.Time) {
		return empty, ErrMaintenanceCompileStale
	}
	claim := maintenanceBuildClaim{ID: id, Token: maintenanceCompilerID(), Deadline: now.Add(lease)}
	if err := q.LeaseMaintenanceBuild(ctx, dbgen.LeaseMaintenanceBuildParams{ID: id, LeaseToken: claim.Token, LeaseUntil: timestamp(claim.Deadline)}); err != nil {
		return empty, err
	}
	if finished := normalizedAgentNow(clock); finished.IsZero() || !finished.Before(claim.Deadline) {
		return empty, ErrMaintenanceCompileStale
	}
	return claim, nil
}

func advanceMaintenanceBuild(ctx context.Context, tx pgx.Tx, claim maintenanceBuildClaim, limit int, clock func() time.Time) (maintenanceCompilerProgress, error) {
	var empty maintenanceCompilerProgress
	if limit < 1 || limit > 1000 || clock == nil || !claim.ID.Valid || !claim.Token.Valid {
		return empty, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	b, err := q.LockMaintenanceBuild(ctx, claim.ID)
	if err != nil {
		return empty, err
	}
	now := normalizedAgentNow(clock)
	if b.LeaseToken != claim.Token || !b.LeaseUntil.Valid || !b.LeaseUntil.Time.Equal(claim.Deadline) || now.IsZero() || !now.Before(claim.Deadline) {
		return empty, ErrMaintenanceCompileStale
	}
	var result maintenanceCompilerProgress
	switch b.Phase {
	case "replay":
		result, err = replayMaintenancePage(ctx, q, b, limit)
	case "union":
		result, err = unionMaintenancePage(ctx, q, b, limit, now)
	default:
		return empty, ErrMaintenanceCompileStale
	}
	if err != nil {
		return empty, err
	}
	if finished := normalizedAgentNow(clock); finished.IsZero() || !finished.Before(claim.Deadline) {
		return empty, ErrMaintenanceCompileStale
	}
	return result, nil
}

func replayMaintenancePage(ctx context.Context, q *dbgen.Queries, b dbgen.AlmanacMaintenanceBuild, limit int) (maintenanceCompilerProgress, error) {
	result := maintenanceCompilerProgress{Phase: "replay", RawCursor: b.RawCursor}
	rows, err := q.ReadMaintenanceCompilerEvents(ctx, dbgen.ReadMaintenanceCompilerEventsParams{SloID: b.SloID, AfterRevision: b.RawCursor, TargetRevision: b.TargetRevision, PageLimit: int32(limit)})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	// Validate the entire raw page before applying membership changes.
	expected := b.RawCursor
	for _, row := range rows {
		if row.Revision != expected+1 || !row.StartsAt.Valid || !row.EndsAt.Valid || !row.Tombstone.Valid || !validMaintenanceTime(row.StartsAt.Time.UTC()) || !validMaintenanceTime(row.EndsAt.Time.UTC()) || !row.StartsAt.Time.Before(row.EndsAt.Time) || row.Included && (!row.Scoped || row.Tombstone.Bool) {
			result.Phase = "unavailable"
			break
		}
		expected = row.Revision
	}
	if result.Phase != "unavailable" && len(rows) < limit && expected < b.TargetRevision {
		result.Phase = "unavailable"
	}
	if result.Phase == "unavailable" {
		return result, q.AdvanceMaintenanceReplay(ctx, dbgen.AdvanceMaintenanceReplayParams{ID: b.ID, RawCursor: b.RawCursor, Phase: result.Phase})
	}
	for _, row := range rows {
		if err := q.RemoveMaintenanceCompilerMember(ctx, dbgen.RemoveMaintenanceCompilerMemberParams{BuildID: b.ID, ExclusionID: row.ExclusionID}); err != nil {
			return result, err
		}
		if row.Included {
			if err := q.PutMaintenanceCompilerMember(ctx, dbgen.PutMaintenanceCompilerMemberParams{BuildID: b.ID, SloID: b.SloID, Revision: row.Revision}); err != nil {
				return result, err
			}
		}
	}
	result.RawCursor = expected
	if expected == b.TargetRevision {
		result.Phase = "union"
	}
	return result, q.AdvanceMaintenanceReplay(ctx, dbgen.AdvanceMaintenanceReplayParams{ID: b.ID, RawCursor: expected, Phase: result.Phase})
}

func unionMaintenancePage(ctx context.Context, q *dbgen.Queries, b dbgen.AlmanacMaintenanceBuild, limit int, now time.Time) (maintenanceCompilerProgress, error) {
	result := maintenanceCompilerProgress{Phase: "union", RawCursor: b.RawCursor}
	afterStart, afterID := b.MemberStart, b.MemberID
	if !afterStart.Valid {
		afterStart = timestamp(time.Unix(0, -1000))
		afterID = pgtype.UUID{Valid: true}
	}
	rows, err := q.ReadMaintenanceCompilerMembers(ctx, dbgen.ReadMaintenanceCompilerMembersParams{BuildID: b.ID, AfterStart: afterStart, AfterID: afterID, PageLimit: int32(limit)})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	flush := func() error {
		if !b.FrontierStart.Valid {
			return nil
		}
		// Supported UTC second bounds fit int64 microseconds even across 8030 years.
		b.CumulativeUs += b.FrontierEnd.Time.UnixMicro() - b.FrontierStart.Time.UnixMicro()
		return q.InsertMaintenanceCoverage(ctx, dbgen.InsertMaintenanceCoverageParams{BuildID: b.ID, StartsAt: b.FrontierStart, EndsAt: b.FrontierEnd, CumulativeUs: b.CumulativeUs})
	}
	for _, row := range rows {
		if !b.FrontierStart.Valid {
			b.FrontierStart, b.FrontierEnd = row.StartsAt, row.EndsAt
		} else if row.StartsAt.Time.After(b.FrontierEnd.Time) {
			if err := flush(); err != nil {
				return result, err
			}
			b.FrontierStart, b.FrontierEnd = row.StartsAt, row.EndsAt
		} else if row.EndsAt.Time.After(b.FrontierEnd.Time) {
			b.FrontierEnd = row.EndsAt
		}
		b.MemberStart, b.MemberID = row.StartsAt, row.ExclusionID
	}
	var completed pgtype.Timestamptz
	if len(rows) < limit {
		if err := flush(); err != nil {
			return result, err
		}
		b.FrontierStart, b.FrontierEnd = pgtype.Timestamptz{}, pgtype.Timestamptz{}
		result.Phase = "complete"
		completed = timestamp(now)
	}
	err = q.AdvanceMaintenanceUnion(ctx, dbgen.AdvanceMaintenanceUnionParams{ID: b.ID, MemberStart: b.MemberStart, MemberID: b.MemberID, FrontierStart: b.FrontierStart, FrontierEnd: b.FrontierEnd, CumulativeUs: b.CumulativeUs, Phase: result.Phase, CompletedAt: completed})
	return result, err
}
