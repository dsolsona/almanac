package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
)

var ErrDerivedFailed = errors.New("storage: derived preparation exhausted")

// Treat a syntactically valid but incomplete or wrongly addressed cache entry as
// corrupt too. None of these provenance fields can be inferred from empty JSON.
func preparedHistoryMatches(value api.SloOperationalDetail, key dbgen.FindPreparedSloHistoryParams, generation pgtype.UUID) bool {
	stamp := value.Generation
	return stamp != nil && generation.Valid && stamp.GenerationId == generation.Bytes &&
		value.Objective.Id == key.SloID.Bytes &&
		value.Objective.DefinitionRevision == strconv.FormatInt(key.DefinitionRevision, 10) &&
		stamp.DefinitionRevision == strconv.FormatInt(key.DefinitionRevision, 10) &&
		stamp.MeasurementRevision == strconv.FormatInt(key.MeasurementRevision, 10) &&
		stamp.SourceGeneration == strconv.FormatInt(key.SourceGeneration, 10) &&
		stamp.CalculationVersion == operationalCalculationVersion &&
		value.CalculationVersion == operationalCalculationVersion &&
		stamp.EvaluatedThrough.Equal(key.EvaluatedThrough.Time) &&
		value.EffectiveAt.Equal(key.EvaluatedThrough.Time) &&
		value.Range.RequestedStart.Equal(key.RangeStart.Time) &&
		value.Range.RequestedEnd.Equal(key.RangeEnd.Time) &&
		(key.Kind == "overview" || (value.BurnSeries.BurnWindowSeconds != nil && *value.BurnSeries.BurnWindowSeconds == api.SloOperationalSeriesBurnWindowSeconds(key.BurnWindowSeconds))) &&
		value.Urgency.Valid()
}

type operationalBounds struct{ requestedAt, effectiveAt, start, end time.Time }

func resolveOperationalBounds(input OperationalDetailInput, cadence time.Duration, windowSeconds int64) (operationalBounds, error) {
	if cadence <= 0 || input.Now == nil || (input.Start == nil) != (input.End == nil) || input.Period != "" && input.Start != nil {
		return operationalBounds{}, ErrOperationalRange
	}
	now := normalizedAgentNow(input.Now)
	at := now
	if input.At != nil {
		_, offset := input.At.Zone()
		at = input.At.UTC()
		if offset != 0 || at.IsZero() || at.Nanosecond()%1000 != 0 || at.After(now) {
			return operationalBounds{}, ErrObjectiveEvaluationTime
		}
	}
	bound := operationalBounds{requestedAt: at, effectiveAt: floorSliBoundary(at, cadence)}
	if input.Start == nil {
		bound.end = bound.effectiveAt
		days := int64(7)
		switch input.Period {
		case "", "7d":
		case "90d":
			days = 90
		case "1y":
			days = 365
		case "window":
			if windowSeconds <= 0 {
				return operationalBounds{}, ErrOperationalRange
			}
			days = min(365, max(1, windowSeconds/86400+(windowSeconds%86400+43200)/86400))
		default:
			return operationalBounds{}, ErrOperationalRange
		}
		bound.start = bound.end.Add(-time.Duration(days) * 24 * time.Hour)
		if input.Period == "" {
			bound.start = floorSliBoundary(bound.start, cadence)
		} else {
			bound.start = ceilSliBoundary(bound.start, cadence)
		}
	} else {
		_, startOffset := input.Start.Zone()
		_, endOffset := input.End.Zone()
		bound.start, bound.end = input.Start.UTC(), input.End.UTC()
		if startOffset != 0 || endOffset != 0 || bound.start.IsZero() || bound.end.IsZero() || bound.start.Nanosecond()%1000 != 0 || bound.end.Nanosecond()%1000 != 0 ||
			!floorSliBoundary(bound.start, cadence).Equal(bound.start) || !floorSliBoundary(bound.end, cadence).Equal(bound.end) || bound.end.After(bound.effectiveAt) || bound.end.Sub(bound.start) > 365*24*time.Hour {
			return operationalBounds{}, ErrOperationalRange
		}
	}
	if !bound.end.After(bound.start) {
		return operationalBounds{}, ErrOperationalRange
	}
	return bound, nil
}

func (r *Runtime) RunHistory(ctx context.Context, logger *slog.Logger) {
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	for {
		repaired, repairErr := r.repairRetainedSeries(ctx, time.Now)
		if repaired && repairErr == nil && ctx.Err() == nil {
			r.metrics.historyJobs.Add(1)
		}
		if repairErr != nil && !errors.Is(repairErr, ErrDerivedPending) && !errors.Is(repairErr, ErrDerivedSourceFailed) && ctx.Err() == nil {
			r.metrics.preparationErrors[1].Add(1)
			logger.ErrorContext(ctx, "retained SLO series repair failed")
		}
		seriesWorked, seriesErr := r.prepareRetainedTile(ctx, time.Now)
		if seriesErr != nil && ctx.Err() == nil {
			r.metrics.preparationErrors[1].Add(1)
			logger.ErrorContext(ctx, "retained SLO tile preparation failed")
		}
		if seriesWorked && seriesErr == nil && ctx.Err() == nil {
			r.metrics.historyJobs.Add(1)
		}
		if err := r.queueDueBudgetHistory(ctx, time.Now()); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "SLO overview history admission failed")
		}
		worked := false
		err := r.prepareHistory(ctx, time.Now, &worked)
		if err != nil && !errors.Is(err, ErrDerivedPending) && ctx.Err() == nil {
			r.metrics.preparationErrors[1].Add(1)
			logger.ErrorContext(ctx, "SLO history preparation failed")
		}
		if worked && err == nil && ctx.Err() == nil {
			r.metrics.historyJobs.Add(1)
		}
		if (worked && err == nil || repaired && repairErr == nil || seriesWorked && seriesErr == nil) && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks.C:
		}
	}
}

func (r *Runtime) prepareNextHistory(ctx context.Context, clock func() time.Time) (resultErr error) {
	return r.prepareHistory(ctx, clock, nil)
}

func (r *Runtime) prepareHistory(ctx context.Context, clock func() time.Time, worked *bool) (resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "derived.history.prepare")
	outcome := "idle"
	defer func() { finishDerivedTrace(ctx, span, resultErr, outcome) }()
	conn, err := r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		return err
	}
	q := dbgen.New(conn)
	if _, err := q.ExhaustExpiredSloHistoryJobs(ctx, timestamp(clock())); err != nil {
		conn.Release()
		return err
	}
	job, err := q.ClaimSloHistoryJob(ctx, dbgen.ClaimSloHistoryJobParams{Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(45 * time.Second))})
	if errors.Is(err, pgx.ErrNoRows) {
		conn.Release()
		return nil
	}
	conn.Release()
	if err != nil {
		return err
	}
	outcome = "executing"
	if span.IsRecording() {
		span.SetAttributes(attribute.String("almanac.work.key", historyTraceKey(dbgen.FindPreparedSloHistoryParams{SloID: job.SloID, DefinitionRevision: job.DefinitionRevision, MeasurementRevision: job.MeasurementRevision,
			SourceGeneration: job.SourceGeneration, EvaluatedThrough: job.EvaluatedThrough, RangeStart: job.RangeStart, RangeEnd: job.RangeEnd, BurnWindowSeconds: job.BurnWindowSeconds, Kind: job.Kind, CalculationVersion: job.CalculationVersion, RetentionStart: job.RetentionStart})), attribute.Int("almanac.work.attempt", int(job.Attempts)))
		if wait := clock().Sub(job.NextAttemptAt.Time).Milliseconds(); wait >= 0 {
			span.SetAttributes(attribute.Int64("almanac.queue.wait_ms", wait))
		} else {
			span.SetAttributes(attribute.Bool("almanac.queue.clock_skew", true))
		}
		if age := clock().Sub(job.CreatedAt.Time).Milliseconds(); age >= 0 {
			span.SetAttributes(attribute.Int64("almanac.queue.age_ms", age))
		}
	}
	if worked != nil {
		*worked = true
	}
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		failureConn, err := r.Acquire(cleanup, DerivedTraffic)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			return
		}
		defer failureConn.Release()
		if errors.Is(resultErr, ErrDerivedPending) || errors.Is(resultErr, ErrDerivedSourceFailed) {
			_, err = dbgen.New(failureConn).ReleaseSloHistoryForDependencies(cleanup, dbgen.ReleaseSloHistoryForDependenciesParams{ID: job.ID, LeaseEpoch: job.LeaseEpoch, NextAttemptAt: timestamp(clock().Add(2 * time.Second))})
			resultErr = errors.Join(resultErr, err)
			return
		}
		_, err = dbgen.New(failureConn).FailSloHistoryJob(cleanup, dbgen.FailSloHistoryJobParams{ID: job.ID, LeaseEpoch: job.LeaseEpoch,
			NextAttemptAt: timestamp(clock().Add(time.Duration(1<<min(job.Attempts, int32(5))) * time.Second)), LastError: pgtype.Text{String: derivedFailureCode(resultErr), Valid: true}})
		resultErr = errors.Join(resultErr, err)
	}()
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err = r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return err
	}
	q = dbgen.New(tx)
	row, err := q.GetSlo(bounded, job.SloID)
	if err != nil {
		return err
	}
	sourceGeneration, err := q.GetDerivedSourceGeneration(bounded, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		sourceGeneration = 0
	} else if err != nil {
		return err
	}
	if row.DefinitionRevision != job.DefinitionRevision || row.MeasurementRevision != job.MeasurementRevision || sourceGeneration != job.SourceGeneration || job.CalculationVersion != operationalCalculationVersion {
		outcome = "obsolete"
		if _, err := q.CompleteSloHistoryJob(bounded, dbgen.CompleteSloHistoryJobParams{ID: job.ID, LeaseEpoch: job.LeaseEpoch}); err != nil {
			return err
		}
		return tx.Commit(bounded)
	}
	contextRow, err := q.GetSloOperationalContext(bounded, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return err
	}
	at, start, end := job.EvaluatedThrough.Time.UTC(), job.RangeStart.Time.UTC(), job.RangeEnd.Time.UTC()
	now := clock().UTC()
	cache := &sloDisplayCache{q: q, row: row, sourceGeneration: sourceGeneration, now: now}
	input := OperationalDetailInput{displayCache: cache, overviewOnly: job.Kind == "overview", Now: func() time.Time { return now }, At: &at, Start: &start, End: &end, BurnWindow: int(job.BurnWindowSeconds)}
	if input.overviewOnly {
		input.displayCache = nil
		input.retainedReader = &retainedSeriesReader{q: q, row: row, source: contextRow, now: now}
	}
	value, err := buildSloOperationalDetail(bounded, q, row, contextRow, input)
	if err != nil {
		return err
	}
	generationID, err := q.InsertDerivedGeneration(bounded, dbgen.InsertDerivedGenerationParams{SloID: job.SloID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		SourceGeneration: sourceGeneration, CalculationVersion: value.CalculationVersion, EvaluatedThrough: job.EvaluatedThrough, RetentionStart: timestamp(ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)), PublishedAt: timestamp(clock())})
	if err != nil {
		return err
	}
	stamp := generationStamp(generationID, sourceGeneration, row.DefinitionRevision, row.MeasurementRevision, value.CalculationVersion, at, clock(), ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
	value.Generation = &stamp
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > 20<<20 {
		return ErrOperationalResource
	}
	var sparkline []byte
	if job.Kind == "overview" {
		sparkline, err = json.Marshal(value.BudgetSeries.Samples)
		if err != nil {
			return err
		}
	}
	if err := q.InsertPreparedSloHistory(bounded, dbgen.InsertPreparedSloHistoryParams{GenerationID: generationID, RangeStart: job.RangeStart, RangeEnd: job.RangeEnd, BurnWindowSeconds: job.BurnWindowSeconds, Payload: payload, Kind: job.Kind, BudgetSparkline: sparkline}); err != nil {
		return err
	}
	if sparkline != nil {
		stamp := generationStamp(generationID, sourceGeneration, row.DefinitionRevision, row.MeasurementRevision, value.CalculationVersion, at, clock(), ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
		metadata, err := json.Marshal(stamp)
		if err != nil {
			return err
		}
		if _, err := q.AttachPreparedBudgetSparkline(bounded, dbgen.AttachPreparedBudgetSparklineParams{RetentionStart: timestamp(ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)), SloID: row.ID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration, At: job.EvaluatedThrough, Samples: sparkline, HistoryGeneration: metadata}); err != nil {
			return err
		}
	}
	completed, err := q.CompleteSloHistoryJob(bounded, dbgen.CompleteSloHistoryJobParams{ID: job.ID, LeaseEpoch: job.LeaseEpoch})
	if err != nil {
		return err
	}
	if completed != 1 {
		return errors.New("storage: history publication lost its lease")
	}
	if _, err := q.DeleteExpiredDerivedGenerations(bounded, timestamp(clock().Add(-5*time.Minute))); err != nil {
		return err
	}
	if _, err := q.DeleteExpiredSloDisplayBlocks(bounded, timestamp(clock().Add(-5*time.Minute))); err != nil {
		return err
	}
	resultErr = tx.Commit(bounded)
	if resultErr == nil {
		outcome = "published"
	}
	return resultErr
}

func derivedFailureCode(err error) string {
	if failure, ok := errors.AsType[*pgconn.PgError](err); ok && failure.Code == "54000" {
		return "derived_cache_limit"
	}
	if errors.Is(err, ErrOperationalResource) {
		return "derived_resource_limit"
	}
	if errors.Is(err, ErrOperationalRangeDense) {
		return "history_range_too_dense"
	}
	return "history_preparation_failed"
}

type retainedHistoryPublication struct {
	generation        dbgen.InsertDerivedGenerationParams
	history           dbgen.InsertPreparedSloHistoryParams
	corruptGeneration pgtype.UUID
}

// Response generations pin exact tables. A cache miss composes maintained series
// and bounded edges synchronously; normal navigation never admits graph jobs.
func loadRetainedSloHistory(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, input OperationalDetailInput) (result api.SloOperationalDetail, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.history.read")
	found := false
	defer func() {
		state := "not_cached"
		if found {
			state = "cache_hit"
		}
		span.SetAttributes(attribute.Bool("almanac.cache.hit", found))
		finishDerivedTrace(ctx, span, resultErr, state)
	}()
	kind := "retained_detail"
	if input.overviewOnly {
		kind = "overview"
	}
	bound, err := resolveOperationalBounds(input, time.Duration(row.CadenceSeconds)*time.Second, row.RollingWindowSeconds)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	span.SetAttributes(attribute.Int64("almanac.range.seconds", int64(bound.end.Sub(bound.start)/time.Second)), attribute.Int("almanac.burn.window_seconds", input.BurnWindow))
	sourceGeneration, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		sourceGeneration = 0
	} else if err != nil {
		return api.SloOperationalDetail{}, err
	}
	retention := timestamp(ceilSliBoundary(normalizedAgentNow(input.Now).Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
	key := dbgen.FindPreparedSloHistoryParams{Now: timestamp(normalizedAgentNow(input.Now)), RetentionStart: retention, CalculationVersion: operationalCalculationVersion, Kind: kind, SloID: row.ID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration,
		EvaluatedThrough: timestamp(bound.effectiveAt), RangeStart: timestamp(bound.start), RangeEnd: timestamp(bound.end), BurnWindowSeconds: int32(input.BurnWindow)}
	cached, err := q.FindPreparedSloHistory(ctx, key)
	if err == nil {
		var result api.SloOperationalDetail
		if json.Unmarshal(cached.Payload, &result) == nil && preparedHistoryMatches(result, key, cached.GenerationID) {
			input.Runtime.metrics.historyHits.Add(1)
			// Display metadata is mutable independently of calculation revisions.
			result.Objective = sloRecordDTO(sloRecord(row))
			result.RequestedAt = bound.requestedAt
			result.Current.Window.RequestedStart = bound.requestedAt.Add(-time.Duration(row.RollingWindowSeconds) * time.Second)
			found = true
			return result, nil
		}
		input.historyPublication.corruptGeneration = cached.GenerationID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalDetail{}, err
	}
	input.Runtime.metrics.historyMisses.Add(1)
	now := normalizedAgentNow(input.Now)
	input.retainedReader = &retainedSeriesReader{q: q, row: row, source: source, now: now}
	result, err = buildSloOperationalDetail(ctx, q, row, source, input)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	input.historyPublication.generation = dbgen.InsertDerivedGenerationParams{SloID: row.ID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration,
		CalculationVersion: operationalCalculationVersion, EvaluatedThrough: timestamp(bound.effectiveAt), RetentionStart: retention, PublishedAt: timestamp(now)}
	input.historyPublication.history = dbgen.InsertPreparedSloHistoryParams{RangeStart: timestamp(bound.start), RangeEnd: timestamp(bound.end), BurnWindowSeconds: int32(input.BurnWindow), Kind: kind}
	return result, nil
}

// Cache accounting shares a budget row. Publish after the protected calculation
// snapshot commits so unrelated cache writers cannot invalidate that snapshot.
func publishRetainedHistory(ctx context.Context, conn *readConnection, result api.SloOperationalDetail, publication retainedHistoryPublication) (api.SloOperationalDetail, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return api.SloOperationalDetail{}, err
	}
	q := dbgen.New(tx)
	if publication.corruptGeneration.Valid {
		if _, err := q.DeleteCorruptPreparedSloHistory(ctx, publication.corruptGeneration); err != nil {
			return api.SloOperationalDetail{}, err
		}
	}
	generationID, err := q.InsertDerivedGeneration(ctx, publication.generation)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	g := publication.generation
	stamp := generationStamp(generationID, g.SourceGeneration, g.DefinitionRevision, g.MeasurementRevision, g.CalculationVersion,
		g.EvaluatedThrough.Time, g.PublishedAt.Time, g.RetentionStart.Time)
	result.Generation = &stamp
	payload, err := json.Marshal(result)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	if len(payload) > 20<<20 {
		return api.SloOperationalDetail{}, ErrOperationalResource
	}
	publication.history.GenerationID, publication.history.Payload = generationID, payload
	if err := q.InsertPreparedSloHistory(ctx, publication.history); err != nil {
		return api.SloOperationalDetail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return api.SloOperationalDetail{}, err
	}
	return result, nil
}
