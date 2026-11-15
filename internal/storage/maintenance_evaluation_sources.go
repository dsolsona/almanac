package storage

import (
	"context"
	"math"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type maintenanceEvaluationSource struct {
	generation                         int64
	population                         maintenanceHourPopulation
	receiptUnknown, pending, exhausted bool
}

func evaluationOptionalTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	return new(v.Time.UTC())
}
func evaluationPopulation(g, t string, expected, observed, empty, zero int64, first, last, receipt pgtype.Timestamptz) (maintenanceHourPopulation, error) {
	p := maintenanceHourPopulation{Expected: expected, Observed: observed, KnownEmpty: empty, ZeroTraffic: zero, FirstObservation: evaluationOptionalTime(first), LastObservation: evaluationOptionalTime(last), LatestReceipt: evaluationOptionalTime(receipt)}
	var a, b bool
	p.Good, a = new(big.Rat).SetString(g)
	p.Total, b = new(big.Rat).SetString(t)
	if !a || !b || p.Good.Sign() < 0 || p.Good.Cmp(p.Total) > 0 || observed < 0 || empty < 0 || zero < 0 || zero > observed || observed > expected || empty > expected-observed {
		return p, slo.ErrOperationalFacts
	}
	if observed == 0 && (p.Good.Sign() != 0 || p.Total.Sign() != 0 || p.FirstObservation != nil || p.LastObservation != nil) || observed > 0 && (p.FirstObservation == nil || p.LastObservation == nil || !p.FirstObservation.Before(*p.LastObservation)) {
		return p, slo.ErrOperationalFacts
	}
	if (p.Total.Sign() == 0) != (zero == observed) {
		return p, slo.ErrOperationalFacts
	}
	return p, nil
}
func readMaintenanceEvaluationSources(ctx context.Context, q *dbgen.Queries, in maintenanceEvaluationInput, plan maintenanceWindowPlan, base slo.WindowInput) (map[int64]maintenanceEvaluationSource, []dbgen.ReadMaintenanceHourRawRow, error) {
	hours := make(map[int64]maintenanceEvaluationSource, len(plan.SourceHours))
	if len(plan.SourceHours) > 0 {
		params := dbgen.ReadMaintenanceEvaluationHoursParams{SliID: in.SliID, MeasurementRevision: in.MeasurementRevision, Now: timestamp(base.Now)}
		for _, h := range plan.SourceHours {
			params.Hours = append(params.Hours, timestamp(h))
		}
		rows, err := q.ReadMaintenanceEvaluationHours(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		if len(rows) != len(plan.SourceHours) {
			return nil, nil, slo.ErrOperationalFacts
		}
		for i, r := range rows {
			h := plan.SourceHours[i]
			if !r.HourStart.Valid || !r.HourStart.Time.Equal(h) || r.SourceGeneration < 0 {
				return nil, nil, slo.ErrOperationalFacts
			}
			expected := int64(ceilSliBoundary(h.Add(time.Hour), base.Cadence).Sub(ceilSliBoundary(h, base.Cadence)) / base.Cadence)
			p, err := evaluationPopulation(r.Good, r.Total, expected, r.Observed, r.KnownEmpty, r.ZeroTraffic, r.FirstObservationAt, r.LastObservationAt, r.LatestReceiptAt)
			if err != nil {
				return nil, nil, err
			}
			hours[h.UnixMicro()] = maintenanceEvaluationSource{generation: r.SourceGeneration, population: p, receiptUnknown: r.ReceiptUnknown, pending: r.Pending || r.UnpreparedRaw, exhausted: r.Exhausted}
		}
	}
	params := dbgen.ReadMaintenanceEvaluationEdgesParams{SliID: in.SliID, MeasurementRevision: in.MeasurementRevision}
	for _, e := range plan.Edges {
		n := e.End.Sub(e.Start) / base.Cadence
		if n < 0 || n >= math.MaxInt32 {
			return nil, nil, ErrOperationalResource
		}
		params.Starts = append(params.Starts, timestamp(e.Start))
		params.Ends = append(params.Ends, timestamp(e.End))
		params.Caps = append(params.Caps, int32(n)+1)
	}
	if len(params.Starts) == 0 {
		return hours, nil, nil
	}
	rows, err := q.ReadMaintenanceEvaluationEdges(ctx, params)
	if err != nil {
		return nil, nil, err
	}
	raw := make([]dbgen.ReadMaintenanceHourRawRow, 0, len(rows))
	counts := make([]int, len(plan.Edges))
	// Decide boundedness from every unfiltered returned row before examining
	// facts/outcomes. The extra row is an overflow sentinel, never a contribution.
	for _, r := range rows {
		i := int(r.Ordinal) - 1
		if i < 0 || i >= len(plan.Edges) {
			return nil, nil, slo.ErrOperationalFacts
		}
		counts[i]++
		if counts[i] >= int(params.Caps[i]) {
			return nil, nil, ErrOperationalResource
		}
	}
	for _, r := range rows {
		i := int(r.Ordinal) - 1
		e := plan.Edges[i]
		if !r.IntervalStart.Valid || !r.RecordedAt.Valid || r.IntervalStart.Time.Before(e.Start) || !r.IntervalStart.Time.Before(e.End) || r.IntervalStart.Time.Add(base.Cadence).After(e.End) || !floorSliBoundary(r.IntervalStart.Time, base.Cadence).Equal(r.IntervalStart.Time) || len(raw) > 0 && !raw[len(raw)-1].IntervalStart.Time.Before(r.IntervalStart.Time) {
			return nil, nil, slo.ErrOperationalFacts
		}
		raw = append(raw, dbgen.ReadMaintenanceHourRawRow{IntervalStart: r.IntervalStart, Outcome: r.Outcome, Good: r.Good, Total: r.Total, RecordedAt: r.RecordedAt, IngestionGeneration: r.IngestionGeneration})
	}
	return hours, raw, nil
}

func addEvaluationPopulation(to *maintenanceHourPopulation, from maintenanceHourPopulation) {
	to.Good.Add(to.Good, from.Good)
	to.Total.Add(to.Total, from.Total)
	to.Expected += from.Expected
	to.Observed += from.Observed
	to.KnownEmpty += from.KnownEmpty
	to.ZeroTraffic += from.ZeroTraffic
	if from.FirstObservation != nil && (to.FirstObservation == nil || from.FirstObservation.Before(*to.FirstObservation)) {
		to.FirstObservation = new(*from.FirstObservation)
	}
	if from.LastObservation != nil && (to.LastObservation == nil || from.LastObservation.After(*to.LastObservation)) {
		to.LastObservation = new(*from.LastObservation)
	}
	if from.LatestReceipt != nil && (to.LatestReceipt == nil || from.LatestReceipt.After(*to.LatestReceipt)) {
		to.LatestReceipt = new(*from.LatestReceipt)
	}
}
func evaluationTotals(eligible, excluded maintenanceHourPopulation, unknown bool) slo.WindowTotals {
	receipt := eligible.LatestReceipt
	if unknown {
		receipt = nil
	}
	return slo.WindowTotals{Good: eligible.Good, Total: eligible.Total, Observed: eligible.Observed, KnownEmpty: eligible.KnownEmpty, ZeroTraffic: eligible.ZeroTraffic, FirstObservation: eligible.FirstObservation, LastObservation: eligible.LastObservation, LatestReceipt: receipt, Excluded: slo.ExclusionTotals{Good: excluded.Good, Total: excluded.Total, Observed: excluded.Observed, KnownEmpty: excluded.KnownEmpty}}
}
