package storage

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrMaintenanceHourPending     = errors.Join(ErrDerivedPending, errors.New("storage: scoped hour preparation pending"))
	ErrMaintenanceHourUnavailable = errors.New("storage: scoped hour source population unavailable")
	ErrMaintenanceHourStale       = errors.New("storage: scoped hour source identity changed")
)

type maintenanceHourKey struct {
	SliID, SloID, BuildID                                     pgtype.UUID
	DefinitionRevision, MeasurementRevision, SourceGeneration int64
	HourStart                                                 time.Time
}
type maintenanceHourInput struct {
	maintenanceHourKey
	RetentionStart, Deadline time.Time
}
type maintenanceHourPopulation struct {
	Good, Total                                      *big.Rat
	Expected, Observed, KnownEmpty, ZeroTraffic      int64
	FirstObservation, LastObservation, LatestReceipt *time.Time
}
type maintenanceHourRecord struct {
	Key                maintenanceHourKey
	ExclusionRevision  int64
	PreparedAt         time.Time
	Eligible, Excluded maintenanceHourPopulation
}

func validMaintenanceHourKey(k maintenanceHourKey) bool {
	return k.SliID.Valid && k.SloID.Valid && k.BuildID.Valid && k.DefinitionRevision > 0 && k.MeasurementRevision > 0 && k.SourceGeneration >= 0 && validMaintenanceTime(k.HourStart) && k.HourStart.Equal(k.HourStart.Truncate(time.Hour)) && validMaintenanceTime(k.HourStart.Add(time.Hour))
}
func maintenanceHourClock(clock func() time.Time, deadline time.Time) (time.Time, error) {
	if clock == nil || deadline.IsZero() {
		return time.Time{}, ErrMaintenanceInvalid
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return time.Time{}, ErrMaintenanceInvalid
	}
	if !now.Before(deadline) {
		return time.Time{}, ErrMaintenanceHourPending
	}
	return now, nil
}

// Preparation is intentionally READ COMMITTED. SLI SHARE excludes accepted
// ingestion; subsequent statements see writers that completed during that lock
// wait. RR could retain a pre-wait dirty-row phantom. The caller owns admission,
// statement bounds, cancellation and immediate commit/rollback. No worker calls it.
func prepareMaintenanceHour(ctx context.Context, tx pgx.Tx, in maintenanceHourInput, clock func() time.Time) (maintenanceHourRecord, error) {
	var empty maintenanceHourRecord
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !validMaintenanceHourKey(in.maintenanceHourKey) || !validMaintenanceTime(in.RetentionStart) {
		return empty, ErrMaintenanceInvalid
	}
	if _, err := maintenanceHourClock(clock, in.Deadline); err != nil {
		return empty, err
	}
	q := dbgen.New(tx)
	isolation, err := q.ReadMaintenanceHourIsolation(ctx)
	if err != nil {
		return empty, err
	}
	if isolation != "read committed" {
		return empty, ErrMaintenanceInvalid
	}
	if _, err = q.LockMaintenanceHourSli(ctx, in.SliID); err != nil {
		return empty, err
	}
	if _, err = q.LockMaintenanceHourSlo(ctx, in.SloID); err != nil {
		return empty, err
	}
	if _, err = q.FenceMaintenanceState(ctx, in.SloID); err != nil {
		return empty, err
	}
	build, err := q.LockMaintenanceHourBuild(ctx, in.BuildID)
	if err != nil {
		return empty, err
	}
	if err = maintenanceHourBuild(build, in.maintenanceHourKey); err != nil {
		return empty, err
	}
	definition, err := q.ReadMaintenanceHourDefinition(ctx, dbgen.ReadMaintenanceHourDefinitionParams{SloID: in.SloID, Revision: in.DefinitionRevision})
	if err != nil {
		return empty, err
	}
	if err = maintenanceHourDefinition(definition, in.maintenanceHourKey); err != nil {
		return empty, err
	}
	source, err := q.LockMaintenanceHourSource(ctx, dbgen.LockMaintenanceHourSourceParams{SliID: in.SliID, MeasurementRevision: in.MeasurementRevision, HourStart: timestamp(in.HourStart)})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	now, err := maintenanceHourClock(clock, in.Deadline)
	if err != nil {
		return empty, err
	}
	if err = maintenanceHourReady(ctx, q, in.maintenanceHourKey, now); err != nil {
		return empty, err
	}
	existing, err := readStoredMaintenanceHour(ctx, q, in.maintenanceHourKey, build.TargetRevision)
	if err == nil {
		if _, err = maintenanceHourClock(clock, in.Deadline); err != nil {
			return empty, err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if in.HourStart.Before(in.RetentionStart) {
		return empty, ErrMaintenanceHourUnavailable
	}
	raw, err := q.ReadMaintenanceHourRaw(ctx, dbgen.ReadMaintenanceHourRawParams{SliID: in.SliID, MeasurementRevision: in.MeasurementRevision, HourStart: timestamp(in.HourStart)})
	if err != nil {
		return empty, err
	}
	fragments, err := q.ReadMaintenanceHourFragments(ctx, dbgen.ReadMaintenanceHourFragmentsParams{BuildID: in.BuildID, HourStart: timestamp(in.HourStart)})
	if err != nil {
		return empty, err
	}
	result, err := partitionMaintenanceHour(in.maintenanceHourKey, definition.CadenceSeconds, raw, fragments, source)
	if err != nil {
		return empty, err
	}
	result.ExclusionRevision = build.TargetRevision
	result.PreparedAt, err = maintenanceHourClock(clock, in.Deadline)
	if err != nil {
		return empty, err
	}
	result, err = storeMaintenanceHour(ctx, q, result)
	if err != nil {
		return empty, err
	}
	if _, err = maintenanceHourClock(clock, in.Deadline); err != nil {
		return empty, err
	}
	return result, nil
}

func maintenanceHourBuild(b dbgen.AlmanacMaintenanceBuild, k maintenanceHourKey) error {
	if b.SloID != k.SloID {
		return ErrMaintenanceInvalid
	}
	if b.Phase == "unavailable" || b.Phase == "cancelled" {
		return ErrMaintenanceCompileUnavailable
	}
	if b.Phase != "complete" {
		return ErrMaintenanceCompilePending
	}
	return nil
}
func maintenanceHourDefinition(d dbgen.ReadMaintenanceHourDefinitionRow, k maintenanceHourKey) error {
	if d.SliID != k.SliID || d.MeasurementRevision != k.MeasurementRevision || d.CadenceSeconds < 1 || d.MeasurementKind != "event_counts" || d.ComponentMode != "interval_count" {
		return ErrMaintenanceInvalid
	}
	return nil
}
func maintenanceHourReady(ctx context.Context, q *dbgen.Queries, k maintenanceHourKey, now time.Time) error {
	state, err := q.ReadMaintenanceHourState(ctx, dbgen.ReadMaintenanceHourStateParams{SliID: k.SliID, MeasurementRevision: k.MeasurementRevision, HourStart: timestamp(k.HourStart), Now: timestamp(now)})
	if err != nil {
		return err
	}
	if state.Exhausted {
		return ErrDerivedSourceFailed
	}
	if state.Pending {
		return ErrMaintenanceHourPending
	}
	if state.SourceGeneration != k.SourceGeneration {
		return ErrMaintenanceHourStale
	}
	if k.SourceGeneration == 0 && state.RawPresent {
		return ErrMaintenanceHourPending
	}
	return nil
}

// Reads share the caller's repeatable-read source snapshot. A generation-zero
// entry is an explicit absent-hour certificate, never a wildcard generation.
func readMaintenanceHour(ctx context.Context, q *dbgen.Queries, k maintenanceHourKey, now time.Time) (maintenanceHourRecord, error) {
	var empty maintenanceHourRecord
	if !validMaintenanceHourKey(k) || now.IsZero() {
		return empty, ErrMaintenanceInvalid
	}
	b, err := q.ReadMaintenanceBuild(ctx, k.BuildID)
	if err != nil {
		return empty, err
	}
	if err = maintenanceHourBuild(b, k); err != nil {
		return empty, err
	}
	d, err := q.ReadMaintenanceHourDefinition(ctx, dbgen.ReadMaintenanceHourDefinitionParams{SloID: k.SloID, Revision: k.DefinitionRevision})
	if err != nil {
		return empty, err
	}
	if err = maintenanceHourDefinition(d, k); err != nil {
		return empty, err
	}
	if err = maintenanceHourReady(ctx, q, k, now); err != nil {
		return empty, err
	}
	result, err := readStoredMaintenanceHour(ctx, q, k, b.TargetRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, ErrMaintenanceHourPending
	}
	return result, err
}
