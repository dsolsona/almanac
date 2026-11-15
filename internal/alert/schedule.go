package alert

import (
	"errors"
	"math"
	"time"
)

var ErrSchedule = errors.New("alert: invalid or overflowing schedule bounds")

// ScheduledGrid fixes the phase and spacing of scheduled boundaries. Origin is
// a grid anchor, not an activation decision; storage supplies the next boundary.
type ScheduledGrid struct {
	Origin  time.Time
	Cadence time.Duration
}

// ScheduledRange covers boundaries Start, Start+Cadence, ... strictly before End.
// It describes skipped work, not an evaluation result or occurrence transition.
type ScheduledRange struct {
	Start, End time.Time
	Cadence    time.Duration
}

type CatchUpPlan struct {
	Next    time.Time
	Due     bool
	Skipped *ScheduledRange
}

// PlanCatchUp retains the oldest boundary at or after tick-horizon on the
// original grid. Equality stays eligible. It never consumes the retained boundary
// or changes alert state. Storage must atomically record Skipped before advancing
// its cursor to Next; leases, repeated effective cutoffs and activation are owned
// by storage. Horizon is explicit caller policy, with no default here.
//
// Work is constant in the outage length. Unrepresentable duration differences or
// rounded offsets fail explicitly rather than allowing time.Duration saturation
// or overflow to move a cursor to the wrong boundary.
func PlanCatchUp(grid ScheduledGrid, next, tick time.Time, horizon time.Duration) (CatchUpPlan, error) {
	if grid.Origin.IsZero() || next.IsZero() || tick.IsZero() || grid.Cadence <= 0 || horizon <= 0 {
		return CatchUpPlan{}, ErrSchedule
	}
	phase := next.Sub(grid.Origin)
	if !grid.Origin.Add(phase).Equal(next) || phase%grid.Cadence != 0 {
		return CatchUpPlan{}, ErrSchedule
	}
	plan := CatchUpPlan{Next: next, Due: !next.After(tick)}
	if !plan.Due {
		return plan, nil
	}
	cutoff := tick.Add(-horizon)
	if !cutoff.Before(tick) || !cutoff.Add(horizon).Equal(tick) {
		return CatchUpPlan{}, ErrSchedule
	}
	if !next.Before(cutoff) {
		return plan, nil
	}
	distance := cutoff.Sub(next)
	if distance <= 0 || !next.Add(distance).Equal(cutoff) {
		return CatchUpPlan{}, ErrSchedule
	}
	steps := distance / grid.Cadence
	if distance%grid.Cadence != 0 {
		steps++
	}
	if steps > time.Duration(math.MaxInt64)/grid.Cadence {
		return CatchUpPlan{}, ErrSchedule
	}
	offset := steps * grid.Cadence
	kept := next.Add(offset)
	if !kept.After(next) || kept.Sub(next) != offset {
		return CatchUpPlan{}, ErrSchedule
	}
	return CatchUpPlan{Next: kept, Due: !kept.After(tick), Skipped: &ScheduledRange{Start: next, End: kept, Cadence: grid.Cadence}}, nil
}
