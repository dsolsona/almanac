package storage

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
)

func (r *Runtime) buildSloExtrema(ctx context.Context, clock func() time.Time) (worked bool, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.extrema.build")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "extrema") }()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	// Normalize the server clock once before constructing shared window inputs.
	// Publication still samples its own post-wait lease clock.
	now := normalizedAgentNow(clock)
	closed := now.Truncate(time.Hour)
	work, err := dbgen.New(conn).ClaimSloExtremaCursor(bounded, dbgen.ClaimSloExtremaCursorParams{
		CalculationVersion: operationalCalculationVersion, Now: timestamp(now), ClosedThrough: timestamp(closed), LeaseUntil: timestamp(now.Add(30 * time.Second))})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	span.SetAttributes(attribute.String("almanac.work.key", work.ID.String()))
	var facts derivedFactsRange
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		q := dbgen.New(conn)
		resultErr = errors.Join(resultErr, facts.rebuildIfInconsistent(cleanup, q, resultErr, clock()))
		if derivedTemporary(ctx, resultErr) {
			var reason pgtype.Text
			if errors.Is(resultErr, ErrDerivedSourceFailed) {
				reason = pgtype.Text{String: "source_aggregate_failed", Valid: true}
			}
			now := clock()
			resultErr = errors.Join(resultErr, q.DeferSloExtremaCursor(cleanup, dbgen.DeferSloExtremaCursorParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch, PriorAttempts: work.PriorAttempts, Now: timestamp(now), NextAttemptAt: timestamp(now.Add(time.Second)), Reason: reason}))
		} else {
			resultErr = errors.Join(resultErr, q.FailSloExtremaCursor(cleanup, dbgen.FailSloExtremaCursorParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch, Now: timestamp(clock()), Reason: derivedFailureReason(resultErr, "extrema_build_failed")}))
		}
	}()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return true, err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return true, err
	}
	q := dbgen.New(tx)
	row, err := q.GetSlo(bounded, work.SloID)
	if err != nil {
		return true, err
	}
	if row.DefinitionRevision != work.DefinitionRevision || row.MeasurementRevision != work.MeasurementRevision {
		return true, ErrDerivedPending
	}
	maintenance, err := captureMaintenanceConsumer(bounded, tx, row, r)
	if err != nil {
		return true, err
	}
	// The cursor tracks source progress, not a coalesced maintenance dirty range.
	// New artifacts use this read's authority; unchanged old buckets are proven
	// compatible or repaired individually by the existing preparation queue.
	work.ExclusionRevision = row.ExclusionRevision
	source, err := q.GetSloOperationalContext(bounded, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return true, err
	}
	generation, err := q.GetDerivedSourceGeneration(bounded, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		return true, err
	}
	retention := ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)
	built := maxTime(work.BuiltThrough.Time, retention.Truncate(time.Hour))
	start := built
	if work.DirtyFrom.Valid && work.DirtyFrom.Time.Before(start) {
		start = maxTime(work.DirtyFrom.Time, retention.Truncate(time.Hour))
	}
	end := extremaBuildEnd(start, closed, time.Duration(row.RollingWindowSeconds)*time.Second, time.Duration(row.CadenceSeconds)*time.Second)
	if work.DirtyFrom.Valid && !start.Before(work.DirtyFrom.Time) && work.DirtyTo.Time.Before(end) {
		end = ceilSliBoundary(work.DirtyTo.Time, time.Hour)
	}
	facts = derivedFactsRange{sli: row.SliID, revision: row.MeasurementRevision, from: start.Add(-max(time.Duration(row.RollingWindowSeconds)*time.Second, 6*time.Hour)), to: end}
	base := extremaWindowInput(row, source, now, retention)
	// Admitted maintenance turns finish every calculation before acquiring
	// resource/publication locks. A pending input commits only prerequisites.
	if maintenance != nil {
		maintenance.sourceGeneration, maintenance.sourceCaptured = generation, true
	}
	enterPublication := func() error {
		phaseNow := normalizedAgentNow(clock)
		if phaseNow.IsZero() || !work.LeaseUntil.Valid || !phaseNow.Before(work.LeaseUntil.Time) {
			return ErrDerivedPending
		}
		if err := tx.Rollback(bounded); err != nil {
			return err
		}
		nextTx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		tx = nextTx
		if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
			return err
		}
		var prepared maintenancePreparedEnqueue
		if len(maintenance.preparation) > 0 {
			prepared, err = prepareMaintenanceEnqueue(bounded, tx, maintenance.scope, maintenance.preparation, maintenance.preparationLimits, phaseNow)
			if err != nil {
				if errors.Is(err, ErrMaintenanceSnapshotChanged) {
					return errors.Join(ErrDerivedPending, err)
				}
				return err
			}
		}
		if err := fenceMaintenanceConsumerSnapshot(bounded, tx, maintenance); err != nil {
			return errors.Join(ErrDerivedPending, err)
		}
		q = dbgen.New(tx)
		parent, err := q.LockMaintenanceExtremaParent(bounded, work.ID)
		if err != nil {
			return err
		}
		finished := normalizedAgentNow(clock)
		if parent.Version != work.Version || parent.LeaseEpoch != work.LeaseEpoch || !parent.LeaseUntil.Valid || !parent.LeaseUntil.Time.Equal(work.LeaseUntil.Time) || finished.IsZero() || !finished.Before(parent.LeaseUntil.Time) {
			return ErrDerivedPending
		}
		if len(maintenance.preparation) > 0 {
			return enqueueMaintenancePreparation(bounded, tx, prepared, maintenancePreparationHistorical, finished)
		}
		return nil
	}
	defer func() { rollbackTransaction(tx) }()
	type hourArtifact struct {
		start time.Time
		lanes extremaLanes
	}
	var artifacts []hourArtifact
	for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
		var lanes extremaLanes
		var err error
		if maintenance != nil {
			lanes, err = buildMaintenanceExtremaHour(bounded, maintenance, row, base, hour)
		} else {
			lanes, err = buildExtremaHour(bounded, q, row, base, hour)
		}
		if err != nil {
			if maintenance != nil && errors.Is(err, ErrDerivedPending) && len(maintenance.preparation) > 0 {
				if err := enterPublication(); err != nil {
					return true, err
				}
				if err := tx.Commit(bounded); err != nil {
					return true, err
				}
			}
			return true, err
		}
		if maintenance != nil {
			artifacts = append(artifacts, hourArtifact{start: hour, lanes: lanes})
		} else if err := publishExtrema(bounded, q, work, lanes, hour, hour.Add(time.Hour), generation, retention, now); err != nil {
			return true, err
		}
	}
	dayEnd := utcDayStart(start).Add(24 * time.Hour)
	if !dayEnd.After(closed) && end.After(start) && (!built.Before(dayEnd) || !end.Before(dayEnd)) {
		if maintenance != nil {
			maintenance.preparation = append(maintenance.preparation, maintenanceEvaluationPreparation{Kind: "extrema_day", RequestedRevision: row.ExclusionRevision, Hour: maintenanceHourKey{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: generation, HourStart: dayEnd.Add(-24 * time.Hour)}})
		} else if err := buildExtremaDay(bounded, q, work, row, base, dayEnd, generation, now); err != nil {
			return true, err
		}
	}
	if maintenance != nil {
		if err := enterPublication(); err != nil {
			return true, err
		}
		for _, artifact := range artifacts {
			if err := publishExtrema(bounded, q, work, artifact.lanes, artifact.start, artifact.start.Add(time.Hour), generation, retention, normalizedAgentNow(clock)); err != nil {
				return true, err
			}
		}
		finished := normalizedAgentNow(clock)
		if finished.IsZero() || !finished.Before(work.LeaseUntil.Time) {
			return true, ErrDerivedPending
		}
	}
	if !start.After(built) {
		built = maxTime(built, end)
	}
	dirtyFrom, dirtyTo := work.DirtyFrom, work.DirtyTo
	if dirtyFrom.Valid && !start.After(dirtyFrom.Time) && end.After(dirtyFrom.Time) || dirtyFrom.Valid && dirtyFrom.Time.Before(retention.Truncate(time.Hour)) {
		if !end.Before(dirtyTo.Time) {
			dirtyFrom, dirtyTo = pgtype.Timestamptz{}, pgtype.Timestamptz{}
		} else {
			dirtyFrom = timestamp(end)
		}
	}
	next := now
	if !built.Before(closed) && (!dirtyFrom.Valid || !dirtyFrom.Time.Before(closed)) {
		next = closed.Add(time.Hour)
	}
	advanced, err := q.AdvanceSloExtremaCursor(bounded, dbgen.AdvanceSloExtremaCursorParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch,
		ExclusionRevision: row.ExclusionRevision, BuiltThrough: timestamp(built), DirtyFrom: dirtyFrom, DirtyTo: dirtyTo, NextAttemptAt: timestamp(next)})
	if err != nil {
		return true, err
	}
	if advanced != 1 {
		return true, ErrDerivedPending
	}
	return true, tx.Commit(bounded)
}

func extremaBuildEnd(start, closed time.Time, window, cadence time.Duration) time.Time {
	// Whole hours share the existing exact-input budget within the five-second turn.
	span := max(time.Hour, retainedRepairSpan(window, cadence).Truncate(time.Hour))
	end := start.Add(span)
	if dayEnd := utcDayStart(start).Add(24 * time.Hour); dayEnd.Before(end) {
		end = dayEnd
	}
	if closed.Before(end) {
		end = closed
	}
	return end
}

func buildExtremaHour(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, base slo.WindowInput, start time.Time) (extremaLanes, error) {
	end := start.Add(time.Hour)
	if !end.After(maxTime(base.ValidFrom, base.SliValidFrom, base.RetentionStart)) {
		reader := retainedSeriesReader{row: row}
		tile, err := reader.beforeValidity(ctx, base, retainedReadPart{start: start, end: end, resolution: 3600})
		if err != nil {
			return extremaLanes{}, err
		}
		return makeExtremaLanes([4][]api.SloOperationalSample{tile.Budget, tile.Attainment, tile.Burn1h, tile.Burn6h}, start, end, base.Target, base.Cadence)
	}
	durations := retainedRepairDurations(base.Duration, base.Cadence)
	var inputs []slo.WindowInput
	for at := floorSliBoundary(start, base.Cadence).Add(base.Cadence); !at.After(end); at = at.Add(base.Cadence) {
		for _, duration := range durations {
			input := base
			input.At, input.Duration = at, duration
			inputs = append(inputs, input)
		}
	}
	values, err := evaluateDerivedWindowsUsing(ctx, q, row.SliID, row.MeasurementRevision, inputs, slo.EvaluateOperationalTotalsSummary)
	if err != nil {
		return extremaLanes{}, err
	}
	var samples [4][]slo.WindowResult
	for index, value := range values {
		lane := index % len(durations)
		if lane >= 2 && inputs[index].Duration == 6*time.Hour {
			lane = 3
		}
		// Retain the complete receipt watermark; the API attainment projection
		// deliberately hides it when there are only known-empty intervals.
		samples[lane] = append(samples[lane], value)
	}
	var lanes [4]*extremaLane
	for index, samples := range samples {
		if index == 2 && time.Hour%base.Cadence != 0 || index == 3 && 6*time.Hour%base.Cadence != 0 {
			continue
		}
		lane, err := makeExtremaSummaryLane(samples, start, end, base.Target, [4]string{"budget", "attainment", "burn", "burn"}[index], strconv.FormatInt(row.DefinitionRevision, 10))
		if err != nil {
			return extremaLanes{}, err
		}
		lanes[index] = &lane
	}
	return extremaLanes{Window: lanes[0], Attainment: lanes[1], Burn1h: lanes[2], Burn6h: lanes[3]}, nil
}

func publishExtrema(ctx context.Context, q *dbgen.Queries, work dbgen.ClaimSloExtremaCursorRow, lanes extremaLanes, start, end time.Time, generation int64, retention, now time.Time) error {
	payload, err := marshalReceiptMetadata(lanes)
	if err != nil {
		return err
	}
	if len(payload) > 20<<20 {
		return ErrOperationalResource
	}
	count, err := q.PublishSloExtrema(ctx, dbgen.PublishSloExtremaParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch,
		ExclusionRevision: work.ExclusionRevision, ResolutionSeconds: int32(end.Sub(start) / time.Second), RangeStart: timestamp(start), RangeEnd: timestamp(end), SourceGeneration: generation,
		RetentionStart: timestamp(retention), PublishedAt: timestamp(now), Lanes: payload})
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrDerivedPending
	}
	return nil
}

func buildExtremaDay(ctx context.Context, q *dbgen.Queries, work dbgen.ClaimSloExtremaCursorRow, row dbgen.GetSloRow, base slo.WindowInput, end time.Time, generation int64, now time.Time) error {
	start := end.Add(-24 * time.Hour)
	rows, err := q.ReadSloExtremaHours(ctx, dbgen.ReadSloExtremaHoursParams{SloID: row.ID, DefinitionRevision: row.DefinitionRevision,
		MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, RangeStart: timestamp(start), RangeEnd: timestamp(end)})
	if err != nil {
		return err
	}
	known := make(map[time.Time]dbgen.AlmanacSloExtrema, len(rows))
	for _, hour := range rows {
		known[hour.RangeStart.Time.UTC()] = hour
	}
	var samples [4][]api.SloOperationalSample
	var originals [4]map[time.Time]extremaMark
	for index := range originals {
		originals[index] = make(map[time.Time]extremaMark)
	}
	var last *extremaMark
	for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
		stored, ok := known[hour]
		if !ok {
			if hour.Add(time.Hour).After(maxTime(base.ValidFrom, base.SliValidFrom, base.RetentionStart)) {
				return ErrDerivedPending
			}
			lanes, err := buildExtremaHour(ctx, q, row, base, hour)
			if err != nil {
				return err
			}
			if err := publishExtrema(ctx, q, work, lanes, hour, hour.Add(time.Hour), generation, base.RetentionStart, now); err != nil {
				return err
			}
			stored.RetentionStart = timestamp(base.RetentionStart)
			stored.Lanes, err = marshalReceiptMetadata(lanes)
			if err != nil {
				return err
			}
		}
		var lanes extremaLanes
		if err := unmarshalReceiptMetadata(stored.Lanes, &lanes); err != nil {
			return ErrDerivedFailed
		}
		input := base
		input.RetentionStart = stored.RetentionStart.Time
		tile, err := lanes.tile(ctx, input, hour, hour.Add(time.Hour), strconv.FormatInt(row.DefinitionRevision, 10))
		if err != nil {
			return err
		}
		for index, values := range [4][]api.SloOperationalSample{tile.Budget, tile.Attainment, tile.Burn1h, tile.Burn6h} {
			samples[index] = append(samples[index], values...)
			if lane := lanes.list()[index]; lane != nil {
				for _, mark := range lane.Marks {
					retention := mark.RetentionStart
					if retention.IsZero() {
						retention = input.RetentionStart
					}
					mark.RetentionStart = retention
					originals[index][mark.At] = mark
					if index == 0 && (last == nil || mark.At.After(last.At)) {
						last = new(mark)
					}
				}
			}
		}
	}
	lanes, err := makeExtremaLanes(samples, start, end, base.Target, base.Cadence)
	if err != nil {
		return err
	}
	for index, lane := range lanes.list() {
		if lane != nil {
			for i, selected := range lane.Marks {
				mark := originals[index][selected.At]
				mark.Labels = selected.Labels
				lane.Marks[i] = mark
			}
		}
	}
	if last == nil || !last.At.Equal(floorSliBoundary(end, base.Cadence)) {
		return ErrDerivedPending
	}
	lanes.Compliance = &extremaCompliance{DayEnd: end, Mark: *last}
	return publishExtrema(ctx, q, work, lanes, start, end, generation, base.RetentionStart, now)
}
