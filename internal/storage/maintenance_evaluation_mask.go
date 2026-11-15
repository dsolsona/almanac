package storage

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func evaluateMaintenanceMask(ctx context.Context, tx pgx.Tx, in maintenanceEvaluationInput, mask maintenanceComputedMask, plan maintenanceWindowPlan, sources map[int64]maintenanceEvaluationSource, raw []dbgen.ReadMaintenanceHourRawRow, preparation *[]maintenanceEvaluationPreparation, unavailable []string) ([]slo.WindowResult, []maintenanceReusedHour, error) {
	if len(plan.Windows) == 0 {
		return []slo.WindowResult{}, nil, nil
	}
	q := dbgen.New(tx)
	cadence := plan.Windows[0].Input.Cadence
	ranks, err := evaluationHourRanks(ctx, q, mask.BuildID, plan.SourceHours, cadence)
	if err != nil {
		return nil, nil, err
	}
	populations := make(map[int64]maintenanceHourRecord, len(plan.SourceHours))
	partial := []time.Time{}
	var exhausted bool
	for _, h := range plan.SourceHours {
		s, ok := sources[h.UnixMicro()]
		if !ok {
			return nil, nil, slo.ErrOperationalFacts
		}
		key := maintenanceHourKey{SliID: in.SliID, SloID: in.SloID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision, BuildID: mask.BuildID, HourStart: h, SourceGeneration: s.generation}
		if s.exhausted {
			exhausted = true
		}
		if s.pending {
			*preparation = append(*preparation, maintenanceEvaluationPreparation{Kind: "source_hour", RequestedRevision: mask.Revision, BuildID: mask.BuildID, Hour: key})
			continue
		}
		start, end := ceilSliBoundary(h, cadence), ceilSliBoundary(h.Add(time.Hour), cadence)
		count := ranks[end.UnixMicro()] - ranks[start.UnixMicro()]
		expected := uint64(end.Sub(start) / cadence)
		if count > expected {
			return nil, nil, slo.ErrOperationalFacts
		}
		r := maintenanceHourRecord{Key: key, ExclusionRevision: mask.Revision, Eligible: emptyMaintenancePopulation(), Excluded: emptyMaintenancePopulation()}
		switch count {
		case 0:
			r.Eligible = s.population
		case expected:
			r.Excluded = s.population
		default:
			if s.generation == 0 {
				r.Eligible.Expected = int64(expected - count)
				r.Excluded.Expected = int64(count)
			} else {
				partial = append(partial, h)
				continue
			}
		}
		populations[h.UnixMicro()] = r
	}
	if exhausted {
		return nil, nil, ErrDerivedSourceFailed
	}
	terminal := map[int64]string{}
	reused, err := evaluationPartialHours(ctx, tx, in, mask, cadence, partial, sources, populations, preparation, terminal)
	if err != nil {
		return nil, nil, err
	}
	if len(unavailable) != len(plan.Windows) {
		return nil, nil, slo.ErrOperationalFacts
	}
	for i, w := range plan.Windows {
		for _, h := range plan.SourceHours {
			if !h.Before(w.FullStart) && h.Before(w.FullEnd) && terminal[h.UnixMicro()] != "" {
				unavailable[i] = terminal[h.UnixMicro()]
				break
			}
		}
	}
	// A still-recoverable dependency holds the entire boundary even when
	// another required population in that window is terminally unavailable.
	for _, h := range plan.SourceHours {
		if _, ok := populations[h.UnixMicro()]; !ok && terminal[h.UnixMicro()] == "" {
			for _, request := range *preparation {
				if request.Kind == "compatibility" {
					return nil, reused, errors.Join(ErrDerivedPending, ErrMaintenanceCompilePending)
				}
			}
			return nil, reused, ErrDerivedPending
		}
	}

	fragments, err := evaluationEdgeFragments(ctx, q, mask.BuildID, plan.Edges, cadence)
	if err != nil {
		return nil, nil, err
	}
	var totals []slo.WindowTotals
	var inputs []slo.WindowInput
	var positions []int
	for i, w := range plan.Windows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if unavailable[i] != "" {
			continue
		}
		positions = append(positions, i)
		inputs = append(inputs, w.Input)
		eligible, excluded := emptyMaintenancePopulation(), emptyMaintenancePopulation()
		unknown := false
		for _, h := range plan.SourceHours {
			if h.Before(w.FullStart) || !h.Before(w.FullEnd) {
				continue
			}
			p := populations[h.UnixMicro()]
			addEvaluationPopulation(&eligible, p.Eligible)
			addEvaluationPopulation(&excluded, p.Excluded)
			// A scoped row was constructed from complete raw contributions, so its
			// receipt is independently exact even if the canonical certificate is old.
			s := sources[h.UnixMicro()]
			if p.PreparedAt.IsZero() && p.Eligible.Observed+p.Eligible.KnownEmpty > 0 && s.receiptUnknown {
				unknown = true
			}
		}
		for _, r := range raw {
			at := r.IntervalStart.Time
			inside := false
			for _, e := range w.Edges {
				if !at.Before(e.Start) && at.Before(e.End) {
					inside = true
					break
				}
			}
			if !inside {
				continue
			}
			p := &eligible
			for _, f := range fragments {
				if !at.Before(f.Start) && at.Before(f.End) {
					p = &excluded
					break
				}
			}
			if err := addMaintenanceObservation(p, r, cadence); err != nil {
				return nil, nil, err
			}
		}
		totals = append(totals, evaluationTotals(eligible, excluded, unknown))
	}
	out := make([]slo.WindowResult, len(plan.Windows))
	if len(inputs) == 0 {
		return out, reused, nil
	}
	coverage, err := readMaintenanceWindowCoverage(ctx, tx, in.SloID, mask.BuildID, mask.Revision, inputs, totals)
	if err != nil {
		return nil, nil, err
	}
	for i := range inputs {
		out[positions[i]], err = slo.EvaluateOperationalCoveredTotals(ctx, inputs[i], totals[i], coverage[i])
		if err != nil {
			return nil, nil, err
		}
	}
	return out, reused, nil
}

func evaluationHourRanks(ctx context.Context, q *dbgen.Queries, build pgtype.UUID, hours []time.Time, cadence time.Duration) (map[int64]uint64, error) {
	ranks := make(map[int64]uint64, 2*len(hours))
	var anchors []pgtype.Timestamptz
	seen := map[int64]bool{}
	for _, h := range hours {
		for _, a := range []time.Time{ceilSliBoundary(h, cadence), ceilSliBoundary(h.Add(time.Hour), cadence)} {
			if !seen[a.UnixMicro()] {
				seen[a.UnixMicro()] = true
				anchors = append(anchors, timestamp(a))
			}
		}
	}
	if len(anchors) == 0 {
		return ranks, nil
	}
	rows, err := q.ReadMaintenanceCoverageRanks(ctx, dbgen.ReadMaintenanceCoverageRanksParams{BuildID: build, Anchors: anchors})
	if err != nil {
		return nil, err
	}
	if len(rows) != len(anchors) {
		return nil, ErrMaintenanceCompileUnavailable
	}
	for _, r := range rows {
		if !r.At.Valid {
			return nil, ErrMaintenanceCompileUnavailable
		}
		value := r.CumulativeUs
		if r.StartsAt.Valid {
			a, z := r.StartsAt.Time, r.EndsAt.Time
			if !r.EndsAt.Valid || !validMaintenanceTime(a) || !validMaintenanceTime(z) || !a.Before(z) || !a.Before(r.At.Time) || !floorSliBoundary(a, cadence).Equal(a) || !floorSliBoundary(z, cadence).Equal(z) || value < z.UnixMicro()-a.UnixMicro() {
				return nil, ErrMaintenanceCompileUnavailable
			}
			if z.After(r.At.Time) {
				value -= z.UnixMicro() - r.At.Time.UnixMicro()
			}
		} else if r.EndsAt.Valid || value != 0 {
			return nil, ErrMaintenanceCompileUnavailable
		}
		if value < 0 || value%cadence.Microseconds() != 0 {
			return nil, ErrMaintenanceCompileUnavailable
		}
		ranks[r.At.Time.UnixMicro()] = uint64(value / cadence.Microseconds())
	}
	return ranks, nil
}
func evaluationEdgeFragments(ctx context.Context, q *dbgen.Queries, build pgtype.UUID, edges []maintenanceWindowRange, cadence time.Duration) ([]maintenanceWindowRange, error) {
	p := dbgen.ReadMaintenanceEvaluationFragmentsParams{BuildID: build}
	for _, e := range edges {
		n := e.End.Sub(e.Start) / cadence
		if n < 0 || n >= math.MaxInt32 {
			return nil, ErrOperationalResource
		}
		p.Starts = append(p.Starts, timestamp(e.Start))
		p.Ends = append(p.Ends, timestamp(e.End))
		p.Caps = append(p.Caps, int32(n)+1)
	}
	if len(edges) == 0 {
		return nil, nil
	}
	rows, err := q.ReadMaintenanceEvaluationFragments(ctx, p)
	if err != nil {
		return nil, err
	}
	counts := make([]int, len(edges))
	last := make([]time.Time, len(edges))
	out := make([]maintenanceWindowRange, 0, len(rows))
	for _, r := range rows {
		i := int(r.Ordinal) - 1
		if i < 0 || i >= len(edges) {
			return nil, ErrMaintenanceCompileUnavailable
		}
		counts[i]++
		if counts[i] > int(p.Caps[i]) {
			return nil, ErrOperationalResource
		}
		if !r.StartsAt.Valid || !r.EndsAt.Valid || !validMaintenanceTime(r.StartsAt.Time) || !validMaintenanceTime(r.EndsAt.Time) || !r.StartsAt.Time.Before(r.EndsAt.Time) || !floorSliBoundary(r.StartsAt.Time, cadence).Equal(r.StartsAt.Time) || !floorSliBoundary(r.EndsAt.Time, cadence).Equal(r.EndsAt.Time) || !last[i].IsZero() && !last[i].Before(r.StartsAt.Time) {
			return nil, ErrMaintenanceCompileUnavailable
		}
		last[i] = r.EndsAt.Time
		out = append(out, maintenanceWindowRange{Start: r.StartsAt.Time, End: r.EndsAt.Time})
	}
	return out, nil
}
