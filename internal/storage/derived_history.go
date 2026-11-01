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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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

func resolveOperationalBounds(input OperationalDetailInput, cadence time.Duration) (operationalBounds, error) {
	if cadence <= 0 || input.Now == nil || (input.Start == nil) != (input.End == nil) {
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
		bound.start = floorSliBoundary(bound.end.Add(-7*24*time.Hour), cadence)
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
		worked := false
		err := r.prepareHistory(ctx, time.Now, &worked)
		if err != nil && !errors.Is(err, ErrDerivedPending) && ctx.Err() == nil {
			r.metrics.preparationErrors[1].Add(1)
			logger.ErrorContext(ctx, "SLO history preparation failed")
		}
		if worked && err == nil && ctx.Err() == nil {
			r.metrics.historyJobs.Add(1)
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
		_, cleanupErr := q.DeleteExpiredDerivedGenerations(ctx, timestamp(clock().Add(-5*time.Minute)))
		if cleanupErr == nil {
			_, cleanupErr = q.DeleteExpiredSloDisplayBlocks(ctx, timestamp(clock().Add(-5*time.Minute)))
		}
		conn.Release()
		return cleanupErr
	}
	conn.Release()
	if err != nil {
		return err
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
	value, err := buildSloOperationalDetail(bounded, q, row, contextRow, OperationalDetailInput{displayCache: cache, overviewOnly: job.Kind == "overview", Now: func() time.Time { return now }, At: &at, Start: &start, End: &end, BurnWindow: int(job.BurnWindowSeconds)})
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
	return tx.Commit(bounded)
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

// loadOrQueueSloHistory never evaluates observations. The caller commits admitted
// preparation before returning pending, preserving per-request authorization.
func loadOrQueueSloHistory(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, input OperationalDetailInput) (api.SloOperationalDetail, bool, error) {
	kind := "detail"
	if input.overviewOnly {
		kind = "overview"
	}
	bound, err := resolveOperationalBounds(input, time.Duration(row.CadenceSeconds)*time.Second)
	if err != nil {
		return api.SloOperationalDetail{}, false, err
	}
	sourceGeneration, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		sourceGeneration = 0
	} else if err != nil {
		return api.SloOperationalDetail{}, false, err
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
			return result, true, nil
		}
		if _, err := q.DeleteCorruptPreparedSloHistory(ctx, cached.GenerationID); err != nil {
			return api.SloOperationalDetail{}, false, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalDetail{}, false, err
	}
	input.Runtime.metrics.historyMisses.Add(1)
	failed, err := hasFailedSourceAt(ctx, q, row, normalizedAgentNow(input.Now), bound.effectiveAt)
	if err != nil {
		return api.SloOperationalDetail{}, false, err
	}
	if failed {
		return api.SloOperationalDetail{}, false, ErrDerivedSourceFailed
	}
	if err := q.LockHistoryAdmission(ctx); err != nil {
		return api.SloOperationalDetail{}, false, err
	}
	_, err = q.AdmitSloHistoryJob(ctx, dbgen.AdmitSloHistoryJobParams{RetentionStart: retention, CalculationVersion: operationalCalculationVersion, Kind: kind, SloID: row.ID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration,
		EvaluatedThrough: timestamp(bound.effectiveAt), RangeStart: timestamp(bound.start), RangeEnd: timestamp(bound.end), BurnWindowSeconds: int32(input.BurnWindow), NextAttemptAt: timestamp(normalizedAgentNow(input.Now))})
	if err != nil {
		return api.SloOperationalDetail{}, false, err
	}
	state, err := q.ReadSloHistoryPreparationState(ctx, dbgen.ReadSloHistoryPreparationStateParams{RetentionStart: retention, CalculationVersion: operationalCalculationVersion, Kind: kind, SloID: row.ID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		SourceGeneration: sourceGeneration, EvaluatedThrough: key.EvaluatedThrough, RangeStart: key.RangeStart, RangeEnd: key.RangeEnd, BurnWindowSeconds: key.BurnWindowSeconds})
	if err != nil {
		return api.SloOperationalDetail{}, false, err
	}
	if state.Exhausted {
		return api.SloOperationalDetail{}, false, ErrDerivedFailed
	}
	if !state.Queued {
		return api.SloOperationalDetail{}, false, ErrOperationalResource
	}
	return api.SloOperationalDetail{}, false, nil
}
