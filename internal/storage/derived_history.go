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
		(value.BurnSeries.BurnWindowSeconds != nil && *value.BurnSeries.BurnWindowSeconds == api.SloOperationalSeriesBurnWindowSeconds(key.BurnWindowSeconds)) &&
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
	for ctx.Err() == nil {
		now := time.Now().UTC()
		conn, err := r.Acquire(ctx, DerivedTraffic)
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "SLO extrema scheduling failed", "error", derivedErrorDetail(err))
			}
			if !waitDerived(ctx, now.Add(time.Second)) {
				return
			}
			continue
		}
		due, err := dbgen.New(conn).ReadExtremaDue(ctx, dbgen.ReadExtremaDueParams{Now: timestamp(now), CalculationVersion: operationalCalculationVersion})
		conn.Release()
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "SLO extrema scheduling failed", "error", derivedErrorDetail(err))
			}
			if !waitDerived(ctx, now.Add(time.Second)) {
				return
			}
			continue
		}
		if due.EnsureCursorsDue {
			conn, err := r.Acquire(ctx, DerivedTraffic)
			if err == nil {
				_, err = dbgen.New(conn).EnsureSloExtremaCursors(ctx, dbgen.EnsureSloExtremaCursorsParams{Now: timestamp(now), CalculationVersion: operationalCalculationVersion})
				conn.Release()
			}
			if err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "SLO extrema cursor discovery failed", "error", derivedErrorDetail(err))
				if !waitDerived(ctx, now.Add(time.Second)) {
					return
				}
				continue
			}
		}
		if due.ExtremaDue {
			worked, err := r.buildSloExtrema(ctx, time.Now)
			if err != nil && ctx.Err() == nil && !derivedTemporary(ctx, err) {
				r.metrics.preparationErrors[1].Add(1)
				logger.ErrorContext(ctx, "SLO extrema preparation failed", "error", derivedErrorDetail(err))
			}
			if worked && err == nil {
				r.metrics.extremaBatches.Add(1)
				continue
			}
			if !worked && err == nil {
				if !waitDerived(ctx, time.Now().Add(time.Second)) {
					return
				}
				continue
			}
		}
		if !waitDerived(ctx, maxTime(due.NextDue.Time, now.Add(10*time.Millisecond))) {
			return
		}
	}
}

type retainedHistoryPublication struct {
	generation        dbgen.InsertDerivedGenerationParams
	history           dbgen.InsertPreparedSloHistoryParams
	live              bool
	validUntil        time.Time
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
	now := normalizedAgentNow(input.Now)
	calculation := input
	calculation.Now = func() time.Time { return now }
	bound, err := resolveOperationalBounds(calculation, time.Duration(row.CadenceSeconds)*time.Second, row.RollingWindowSeconds)
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
	retention := timestamp(ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
	key := dbgen.FindPreparedSloHistoryParams{Now: timestamp(now), RetentionStart: retention, CalculationVersion: operationalCalculationVersion, Kind: kind, SloID: row.ID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration,
		EvaluatedThrough: timestamp(bound.effectiveAt), RangeStart: timestamp(bound.start), RangeEnd: timestamp(bound.end), BurnWindowSeconds: int32(input.BurnWindow), Live: input.At == nil}
	cached, err := q.FindPreparedSloHistory(ctx, key)
	if err == nil {
		var result api.SloOperationalDetail
		if json.Unmarshal(cached.Payload, &result) == nil && preparedHistoryMatches(result, key, cached.GenerationID) {
			if input.Runtime != nil {
				input.Runtime.metrics.historyHits.Add(1)
			}
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
	if input.Runtime != nil {
		input.Runtime.metrics.historyMisses.Add(1)
	}
	calculation.retainedReader = &retainedSeriesReader{q: q, row: row, source: source, now: now}
	reader := &extremaReader{retainedSeriesReader: *calculation.retainedReader}
	base := extremaWindowInput(row, source, now, retention.Time)
	if err := reader.prepare(ctx, base, bound); err != nil {
		return api.SloOperationalDetail{}, err
	}
	calculation.retainedReader = &reader.retainedSeriesReader
	result, err = buildSloOperationalDetail(ctx, q, row, source, calculation)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	input.historyPublication.generation = dbgen.InsertDerivedGenerationParams{SloID: row.ID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration,
		CalculationVersion: operationalCalculationVersion, EvaluatedThrough: timestamp(bound.effectiveAt), RetentionStart: retention, PublishedAt: timestamp(normalizedAgentNow(input.Now))}
	input.historyPublication.history = dbgen.InsertPreparedSloHistoryParams{RangeStart: timestamp(bound.start), RangeEnd: timestamp(bound.end), BurnWindowSeconds: int32(input.BurnWindow), Kind: kind}
	input.historyPublication.live = input.At == nil
	if input.At == nil {
		input.historyPublication.validUntil = currentCacheDeadline(row, source, now, result.Current.Quality, result.Burn1h.Quality, result.Burn6h.Quality)
	}
	return result, nil
}

// Cache accounting shares a budget row. Publish after the protected calculation
// snapshot commits so unrelated cache writers cannot invalidate that snapshot.
func publishRetainedHistory(ctx context.Context, q *dbgen.Queries, result api.SloOperationalDetail, publication retainedHistoryPublication) (api.SloOperationalDetail, error) {
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
	payload, err := json.Marshal(struct {
		api.SloOperationalDetail
		Live       bool      `json:"_live"`
		ValidUntil time.Time `json:"_current_valid_until,omitzero"`
	}{SloOperationalDetail: result, Live: publication.live, ValidUntil: publication.validUntil})
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
	return result, nil
}
