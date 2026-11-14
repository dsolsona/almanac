package slo

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"
)

// ExclusionInterval is a half-open interval already resolved by the caller to
// the evaluated SLO and a consistent exclusion revision. It never masks its SLI.
type ExclusionInterval struct {
	Start, End time.Time
}

// Prefix counts keep clipping proportional to the mask size, never the number
// of absent observations. Unsigned differences also cover spans across epoch.
type exclusionMask struct {
	intervals []ExclusionInterval
	prefix    []uint64
	cadence   time.Duration
}

func alignedExclusionInstant(at time.Time, cadence time.Duration) bool {
	return !at.IsZero() && cadence >= time.Microsecond && cadence%time.Microsecond == 0 &&
		at.Nanosecond()%1000 == 0 && time.UnixMicro(at.UnixMicro()).Equal(at) && at.UnixMicro()%cadence.Microseconds() == 0
}

func newExclusionMask(ctx context.Context, intervals []ExclusionInterval, cadence time.Duration) (exclusionMask, error) {
	if err := ctx.Err(); err != nil {
		return exclusionMask{}, err
	}
	mask := exclusionMask{cadence: cadence}
	if len(intervals) == 0 {
		return mask, nil
	}
	ordered := slices.Clone(intervals)
	for i := range ordered {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return exclusionMask{}, err
			}
		}
		span := &ordered[i]
		if !alignedExclusionInstant(span.Start, cadence) || !alignedExclusionInstant(span.End, cadence) || !span.Start.Before(span.End) {
			return exclusionMask{}, fmt.Errorf("%w: exclusion bounds must be ordered whole cadence instants", ErrInvalidCalculation)
		}
		span.Start, span.End = span.Start.UTC(), span.End.UTC()
	}
	slices.SortFunc(ordered, func(a, b ExclusionInterval) int { return a.Start.Compare(b.Start) })
	for i, span := range ordered {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return exclusionMask{}, err
			}
		}
		if len(mask.intervals) != 0 && !span.Start.After(mask.intervals[len(mask.intervals)-1].End) {
			last := &mask.intervals[len(mask.intervals)-1]
			if span.End.After(last.End) {
				last.End = span.End
			}
		} else {
			mask.intervals = append(mask.intervals, span)
		}
	}
	mask.prefix = make([]uint64, len(mask.intervals)+1)
	for i, span := range mask.intervals {
		mask.prefix[i+1] = mask.prefix[i] + (uint64(span.End.UnixMicro())-uint64(span.Start.UnixMicro()))/uint64(cadence.Microseconds())
	}
	return mask, nil
}

func (m exclusionMask) contains(at time.Time) bool {
	i := sort.Search(len(m.intervals), func(i int) bool { return m.intervals[i].End.After(at) })
	return i < len(m.intervals) && !m.intervals[i].Start.After(at)
}

func (m exclusionMask) before(at time.Time) uint64 {
	if len(m.intervals) == 0 {
		return 0
	}
	i := sort.Search(len(m.intervals), func(i int) bool { return m.intervals[i].End.After(at) })
	count := m.prefix[i]
	if i < len(m.intervals) && m.intervals[i].Start.Before(at) {
		count += (uint64(at.UnixMicro()) - uint64(m.intervals[i].Start.UnixMicro())) / uint64(m.cadence.Microseconds())
	}
	return count
}

func (m exclusionMask) count(start, end time.Time) int64 {
	if !start.Before(end) {
		return 0
	}
	return int64(m.before(end) - m.before(start))
}

func (m exclusionMask) latestEligibleEnd(at time.Time) time.Time {
	i := sort.Search(len(m.intervals), func(i int) bool { return !m.intervals[i].End.Before(at) })
	if i < len(m.intervals) && m.intervals[i].Start.Before(at) {
		return m.intervals[i].Start
	}
	return at
}

func (s *WindowSweep) adjustExcluded(fact Observation, add bool) {
	delta := int64(1)
	if !add {
		delta = -1
	}
	switch fact.Outcome {
	case "observed":
		good, _ := decimal(fact.Good, false)
		total, _ := decimal(fact.Total, false)
		if add {
			s.excludedGood.Add(&s.excludedGood, good)
			s.excludedTotal.Add(&s.excludedTotal, total)
		} else {
			s.excludedGood.Sub(&s.excludedGood, good)
			s.excludedTotal.Sub(&s.excludedTotal, total)
		}
		s.excludedObserved += delta
	case "complete_empty":
		s.excludedEmpty += delta
	}
}
