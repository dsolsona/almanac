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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type EvaluateSloInput struct {
	Proof                          DatasourceProof
	ID                             string
	At, TimelineStart, TimelineEnd *time.Time
	Now                            func() time.Time
}

// Keep database details private while allowing the evaluation to retry the
// whole transaction when PostgreSQL rejects its repeatable-read snapshot.
var errReadSerialization = errors.New("storage: read transaction serialization failure")

func readTransactionError(ctx context.Context, message string, err error) error {
	if ctx.Err() == nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "40001" {
			return errReadSerialization
		}
	}
	return operationError(ctx, message, err)
}

const sloEvaluationAttempts = 3

func retrySloEvaluation(ctx context.Context, attempt func() (api.SloEvaluation, error)) (api.SloEvaluation, error) {
	for range sloEvaluationAttempts {
		if err := ctx.Err(); err != nil {
			return api.SloEvaluation{}, operationError(ctx, "storage: SLO evaluation cancelled", err)
		}
		result, err := attempt()
		if err == nil {
			return result, nil
		}
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "40001" {
			err = errReadSerialization
		}
		if !errors.Is(err, errReadSerialization) {
			return api.SloEvaluation{}, err
		}
	}
	return api.SloEvaluation{}, errReadSerialization
}

func optionalTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	return new(v.Time.UTC())
}

// EvaluateSlo reads the aggregate and bounded timeline in one repeatable-read snapshot.
func EvaluateSlo(ctx context.Context, databaseURL string, input EvaluateSloInput) (api.SloEvaluation, error) {
	id, valid := canonicalUUID(input.ID)
	if !valid || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return api.SloEvaluation{}, ErrObjectiveInvalid
	}
	if (input.TimelineStart == nil) != (input.TimelineEnd == nil) {
		return api.SloEvaluation{}, ErrObjectiveTimelineRange
	}
	return retrySloEvaluation(ctx, func() (api.SloEvaluation, error) {
		return evaluateSloAttempt(ctx, databaseURL, input, id)
	})
}

func evaluateSloAttempt(ctx context.Context, databaseURL string, input EvaluateSloInput, id pgtype.UUID) (api.SloEvaluation, error) {
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloEvaluation{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := q.GetSlo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloEvaluation{}, ErrObjectiveNotFound
	}
	if err != nil {
		return api.SloEvaluation{}, err
	}
	// This increment supports only the one immutable SLI revision captured at creation.
	measurement, err := q.GetSli(ctx, row.SliID)
	if err != nil {
		return api.SloEvaluation{}, err
	}
	if measurement.MeasurementRevision != row.MeasurementRevision || measurement.PollingIntervalSeconds != row.CadenceSeconds || measurement.MeasurementKind != "event_counts" || measurement.ComponentMode != "interval_count" {
		return api.SloEvaluation{}, ErrObjectiveInvalid
	}
	now := normalizedAgentNow(input.Now)
	requested := now
	if input.At != nil {
		_, offset := input.At.Zone()
		requested = input.At.UTC()
		if offset != 0 || requested.IsZero() || requested.Nanosecond()%1000 != 0 || requested.After(now) {
			return api.SloEvaluation{}, ErrObjectiveEvaluationTime
		}
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	end := floorSliBoundary(requested, cadence)
	windowDuration := time.Duration(row.RollingWindowSeconds) * time.Second
	start := end.Add(-windowDuration)
	requestedStart := requested.Add(-windowDuration)
	eligible := ceilSliBoundary(maxTime(start, row.EvaluationValidFrom.Time, row.SliValidFrom.Time), cadence)
	if eligible.After(end) {
		eligible = end
	}
	retention := ceilSliBoundary(now.Add(-sli.ObservationRetention), cadence)
	retainedStart := maxTime(eligible, retention)
	if retainedStart.After(end) {
		retainedStart = end
	}
	expected := int64(end.Sub(eligible) / cadence)
	expired := int64(retainedStart.Sub(eligible) / cadence)
	intervalStart, intervalEnd := end.Add(-min(windowDuration, 1500*cadence)), end
	if input.TimelineStart != nil {
		_, startOffset := input.TimelineStart.Zone()
		_, endOffset := input.TimelineEnd.Zone()
		intervalStart = input.TimelineStart.UTC()
		intervalEnd = input.TimelineEnd.UTC()
		if startOffset != 0 || endOffset != 0 || intervalStart.IsZero() || intervalEnd.IsZero() || intervalStart.Before(start) || intervalEnd.After(end) || !intervalEnd.After(intervalStart) || !floorSliBoundary(intervalStart, cadence).Equal(intervalStart) || !floorSliBoundary(intervalEnd, cadence).Equal(intervalEnd) || intervalEnd.Sub(intervalStart) > 1500*cadence {
			return api.SloEvaluation{}, ErrObjectiveTimelineRange
		}
	}
	aggregate, err := q.AggregateSloObservations(ctx, dbgen.AggregateSloObservationsParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, IntervalStart: timestamp(retainedStart), IntervalEnd: timestamp(end)})
	if err != nil {
		return api.SloEvaluation{}, readTransactionError(ctx, "storage: SLO aggregate failed", err)
	}
	facts, err := q.ListSloTimelineFacts(ctx, dbgen.ListSloTimelineFactsParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, IntervalStart: timestamp(maxTime(intervalStart, retainedStart)), IntervalEnd: timestamp(intervalEnd)})
	if err != nil {
		return api.SloEvaluation{}, readTransactionError(ctx, "storage: SLO timeline failed", err)
	}
	missing := expected - expired - aggregate.ObservedIntervals - aggregate.KnownEmptyIntervals
	if missing < 0 {
		return api.SloEvaluation{}, errors.New("storage: inconsistent SLO coverage")
	}
	quality := api.SloEvaluationQuality{State: "complete", ExpectedIntervals: expected, ExpiredIntervals: expired, ObservedIntervals: aggregate.ObservedIntervals, KnownEmptyIntervals: aggregate.KnownEmptyIntervals, ZeroTrafficIntervals: aggregate.ZeroTrafficIntervals, MissingIntervals: missing, FirstObservationAt: optionalTime(aggregate.FirstObservationAt), LastObservationAt: optionalTime(aggregate.LastObservationAt), LatestReceiptAt: optionalTime(aggregate.LatestReceiptAt), Freshness: "unknown"}
	if expected > 0 {
		quality.Coverage = new(slo.FormatFraction(aggregate.ObservedIntervals+aggregate.KnownEmptyIntervals, expected))
	}
	if quality.LastObservationAt != nil {
		quality.Freshness = "fresh"
		freshUntil := quality.LastObservationAt.Add(cadence + time.Duration(measurement.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds)))
		if requested.After(freshUntil) {
			quality.Freshness = "stale"
		}
	}
	switch {
	case expected == 0:
		quality.State = "not_applicable"
	case expired > 0:
		quality.State = "expired_history"
	case aggregate.ObservedIntervals+aggregate.KnownEmptyIntervals == 0:
		quality.State = "no_data"
	case missing > 0:
		quality.State = "partial"
	case aggregate.ObservedIntervals > 0 && aggregate.ObservedIntervals == aggregate.ZeroTrafficIntervals && aggregate.KnownEmptyIntervals == 0:
		quality.State = "zero_traffic"
	}
	performance := api.SloEvaluationPerformance{State: "unknown", BudgetUnit: "events"}
	provisional := missing > 0 || expired > 0 || aggregate.KnownEmptyIntervals > 0
	performance.Provisional = provisional
	if expected == 0 {
		performance.State = "not_applicable"
		performance.Reason = new("not_applicable")
	} else if aggregate.ObservedIntervals == 0 {
		performance.Reason = new("no_measurements")
	} else {
		good, total := sli.NormalizeDecimal(aggregate.Good), sli.NormalizeDecimal(aggregate.Total)
		performance.Good = &good
		performance.Total = &total
		calc, e := slo.CalculateOccurrences(good, total, row.Target, provisional)
		if errors.Is(e, slo.ErrNoTraffic) {
			if provisional {
				performance.Reason = new("incomplete_measurements")
			} else {
				performance.Reason = new("zero_traffic")
			}
		} else if e != nil {
			return api.SloEvaluation{}, e
		} else {
			performance.State = api.SloEvaluationPerformanceState(calc.State)
			observed := api.SloEvaluationPerformanceObservedState(calc.ObservedState)
			performance.ObservedState = &observed
			performance.Provisional = calc.Provisional
			if provisional {
				performance.Reason = new("incomplete_measurements")
			}
			performance.Bad = &calc.Bad
			performance.Attainment = &calc.Attainment
			performance.AllowedBudget = &calc.Allowed
			performance.RemainingBudget = &calc.Remaining
			performance.ConsumedFraction = &calc.Consumed
			performance.RemainingFraction = &calc.RemainingFraction
			performance.Burn = &calc.Burn
		}
	}
	byStart := make(map[int64]dbgen.ListSloTimelineFactsRow, len(facts))
	for _, fact := range facts {
		byStart[fact.IntervalStart.Time.UnixMicro()] = fact
	}
	items := make([]api.SloTimelineItem, 0, int(intervalEnd.Sub(intervalStart)/cadence))
	for at := intervalStart; at.Before(intervalEnd); at = at.Add(cadence) {
		item := api.SloTimelineItem{Start: at, End: at.Add(cadence), State: "missing"}
		switch {
		case at.Before(eligible):
			item.State = "not_applicable"
		case at.Before(retention):
			item.State = "expired"
		default:
			if fact, ok := byStart[at.UnixMicro()]; ok {
				item.ReceivedAt = optionalTime(fact.RecordedAt)
				if fact.Outcome == "complete_empty" {
					item.State = "known_empty"
				} else {
					item.State = "observed"
					good, total := sli.NormalizeDecimal(fact.Good), sli.NormalizeDecimal(fact.Total)
					item.Good = &good
					item.Total = &total
					c, e := slo.CalculateOccurrences(good, total, row.Target, false)
					if errors.Is(e, slo.ErrNoTraffic) {
						item.State = "zero_traffic"
					} else if e != nil {
						return api.SloEvaluation{}, e
					} else {
						item.Attainment = &c.Attainment
						item.Burn = &c.Burn
					}
				}
			}
		}
		items = append(items, item)
	}
	evaluationID, err := uuid.NewRandom()
	if err != nil {
		return api.SloEvaluation{}, err
	}
	result := api.SloEvaluation{EvaluationId: evaluationID, RequestedAt: requested, EffectiveEnd: end, CalculationVersion: "occurrences-v1", SnapshotKind: "current_evaluation", Objective: api.SloEvaluationObjective{SloId: uuid.UUID(row.ID.Bytes), DefinitionRevision: strconv.FormatInt(row.DefinitionRevision, 10), SliId: uuid.UUID(row.SliID.Bytes), MeasurementRevision: strconv.FormatInt(row.MeasurementRevision, 10), Target: sli.NormalizeDecimal(row.Target), Method: "occurrences", RollingWindowSeconds: row.RollingWindowSeconds, CadenceSeconds: int(row.CadenceSeconds)}, Window: api.SloEvaluationWindow{RequestedStart: requestedStart, EffectiveStart: start, End: end, EligibleStart: eligible, RetentionStart: retention, ActivatedAt: row.ActivatedAt.Time.UTC(), EvaluationValidFrom: row.EvaluationValidFrom.Time.UTC(), HistoricalRequestedAt: optionalTime(row.HistoricalRequestedAt)}, Quality: quality, Performance: performance, Timeline: api.SloEvaluationTimeline{Start: intervalStart, End: intervalEnd, CadenceSeconds: int(row.CadenceSeconds), MaxIntervals: 1500, Items: items}}
	if row.HistoricalRequestedBy.Valid {
		result.Window.HistoricalRequestedBy = new(uuid.UUID(row.HistoricalRequestedBy.Bytes))
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloEvaluation{}, err
	}
	return result, nil
}

func maxTime(first time.Time, others ...time.Time) time.Time {
	for _, v := range others {
		if v.After(first) {
			first = v
		}
	}
	return first
}
