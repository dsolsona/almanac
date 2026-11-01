package slo

import (
	"context"
	"errors"
	"math/big"
	"time"
)

type Observation struct {
	Start, End, Received time.Time
	Outcome              string
	Good, Total          string
}

type WindowInput struct {
	At, Now, ValidFrom, SliValidFrom, RetentionStart time.Time
	Duration, Cadence, FreshnessGrace                time.Duration
	Target                                           string
	Observations                                     []Observation
}

// WindowTotals carries exact sufficient statistics over the retained eligible
// portion of one window. It does not stand in for individual observations.
type WindowTotals struct {
	Good, Total                                      *big.Rat
	Observed, KnownEmpty, ZeroTraffic                int64
	FirstObservation, LastObservation, LatestReceipt *time.Time
}

type WindowResult struct {
	RequestedStart, EffectiveStart, EligibleStart, RetentionStart, End time.Time
	State, Freshness, Performance, ObservedPerformance, Reason         string
	Expected, Observed, KnownEmpty, Missing, Expired, ZeroTraffic      int64
	Coverage, Good, Total, Bad, Attainment, Allowed, Remaining         *string
	Consumed, RemainingFraction, Burn                                  *string
	ExactBad, ExactRemaining, ExactRemainingFraction, ExactBurn        *big.Rat
	Provisional                                                        bool
	FirstObservation, LastObservation, LatestReceipt                   *time.Time
	values                                                             *windowValues
}

type windowValues struct{ good, total, target *big.Rat }

// WithValues projects an exact window summary to the public decimal strings.
// Reducers call this only for retained points, after selecting their extrema.
func (result WindowResult) WithValues() WindowResult {
	if result.Expected > 0 {
		value := FormatFraction(result.Observed+result.KnownEmpty, result.Expected)
		result.Coverage = &value
	}
	values := result.values
	if values == nil {
		return result
	}
	result.values = nil
	if values.total.Sign() == 0 {
		zero := "0.000000000000"
		result.Good, result.Total = &zero, &zero
		return result
	}
	good, total := rationalDecimal(values.good), rationalDecimal(values.total)
	calc := calculateOccurrences(values.good, values.total, values.target, result.Provisional)
	result.Performance, result.ObservedPerformance = calc.State, calc.ObservedState
	result.Good, result.Total, result.Bad = &good, &total, &calc.Bad
	result.Attainment, result.Allowed, result.Remaining = &calc.Attainment, &calc.Allowed, &calc.Remaining
	result.Consumed, result.RemainingFraction, result.Burn = &calc.Consumed, &calc.RemainingFraction, &calc.Burn
	return result
}

type WindowSweep struct {
	totals                                 *WindowTotals
	input                                  WindowInput
	next                                   int
	active                                 []int
	activeHead                             int
	observed                               []int
	observedHead                           int
	receipts                               []int
	receiptHead                            int
	good, total                            big.Rat
	target                                 *big.Rat
	observedCount, emptyCount, zeroTraffic int64
	lastAt                                 time.Time
}

func NewWindowSweep(input WindowInput) (*WindowSweep, error) {
	return NewWindowSweepContext(context.Background(), input)
}

func NewWindowSweepContext(ctx context.Context, input WindowInput) (*WindowSweep, error) {
	if input.Cadence <= 0 || input.Duration <= 0 || input.Duration%input.Cadence != 0 || input.Target == "" || input.Now.IsZero() {
		return nil, ErrInvalidCalculation
	}
	for index, fact := range input.Observations {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if fact.Start.IsZero() || fact.End.Sub(fact.Start) != input.Cadence || index > 0 && !input.Observations[index-1].Start.Before(fact.Start) {
			return nil, ErrOperationalFacts
		}
		if fact.Outcome == "observed" {
			g, ok := decimal(fact.Good, false)
			if !ok {
				return nil, ErrOperationalFacts
			}
			t, ok := decimal(fact.Total, false)
			if !ok || g.Cmp(t) > 0 {
				return nil, ErrOperationalFacts
			}
		} else if fact.Outcome != "complete_empty" && fact.Outcome != "incomplete" && fact.Outcome != "failed" {
			return nil, ErrOperationalFacts
		}
	}
	target, ok := decimal(input.Target, true)
	if !ok || target.Cmp(big.NewRat(1, 1)) >= 0 {
		target = nil
	}
	return &WindowSweep{input: input, target: target}, nil
}

// At advances the exact rolling-window aggregation to an aligned evaluation time.
func (s *WindowSweep) At(at time.Time) (WindowResult, error) {
	return s.AtContext(context.Background(), at)
}

func (s *WindowSweep) AtContext(ctx context.Context, at time.Time) (WindowResult, error) {
	result, err := s.AtSummaryContext(ctx, at)
	if err != nil {
		return WindowResult{}, err
	}
	return result.WithValues(), nil
}

// AtSummaryContext advances exact arithmetic and quality without formatting
// decimal strings. The returned snapshot stays valid as the sweep advances.
func (s *WindowSweep) AtSummaryContext(ctx context.Context, at time.Time) (WindowResult, error) {
	if err := ctx.Err(); err != nil {
		return WindowResult{}, err
	}
	if at.IsZero() || s.lastAt.After(at) || at.Nanosecond()%1000 != 0 || floorBoundary(at, s.input.Cadence) != at {
		return WindowResult{}, ErrInvalidCalculation
	}
	s.lastAt = at
	windowStart := at.Add(-s.input.Duration)
	eligible := ceilBoundary(maxTime(windowStart, s.input.ValidFrom, s.input.SliValidFrom), s.input.Cadence)
	if eligible.After(at) {
		eligible = at
	}
	retention := ceilBoundary(s.input.RetentionStart, s.input.Cadence)
	if retention.Before(eligible) {
		retention = eligible
	}
	if retention.After(at) {
		retention = at
	}
	for s.activeHead < len(s.active) && s.input.Observations[s.active[s.activeHead]].Start.Before(retention) {
		if s.activeHead&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return WindowResult{}, err
			}
		}
		index := s.active[s.activeHead]
		if err := s.remove(index); err != nil {
			return WindowResult{}, err
		}
		s.activeHead++
	}
	for s.next < len(s.input.Observations) && !s.input.Observations[s.next].End.After(at) {
		if s.next&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return WindowResult{}, err
			}
		}
		fact := s.input.Observations[s.next]
		if !fact.Start.Before(retention) {
			if err := s.add(ctx, s.next); err != nil {
				return WindowResult{}, err
			}
			s.active = append(s.active, s.next)
		}
		s.next++
	}
	if s.activeHead > 4096 && s.activeHead*2 >= len(s.active) {
		s.active = append([]int(nil), s.active[s.activeHead:]...)
		s.activeHead = 0
	}
	return s.result(ctx, at, windowStart, eligible, retention)
}

func (s *WindowSweep) add(ctx context.Context, index int) error {
	fact := s.input.Observations[index]
	switch fact.Outcome {
	case "observed":
		g, _ := decimal(fact.Good, false)
		t, _ := decimal(fact.Total, false)
		s.good.Add(&s.good, g)
		s.total.Add(&s.total, t)
		s.observedCount++
		if t.Sign() == 0 {
			s.zeroTraffic++
		}
		s.observed = append(s.observed, index)
	case "complete_empty":
		s.emptyCount++
	}
	if fact.Outcome == "observed" || fact.Outcome == "complete_empty" {
		for len(s.receipts) > s.receiptHead && s.input.Observations[s.receipts[len(s.receipts)-1]].Received.Before(fact.Received) {
			if len(s.receipts)&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			s.receipts = s.receipts[:len(s.receipts)-1]
		}
		s.receipts = append(s.receipts, index)
	}
	return nil
}

func (s *WindowSweep) remove(index int) error {
	fact := s.input.Observations[index]
	switch fact.Outcome {
	case "observed":
		g, _ := decimal(fact.Good, false)
		t, _ := decimal(fact.Total, false)
		s.good.Sub(&s.good, g)
		s.total.Sub(&s.total, t)
		s.observedCount--
		if t.Sign() == 0 {
			s.zeroTraffic--
		}
	case "complete_empty":
		s.emptyCount--
	}
	if s.receiptHead < len(s.receipts) && s.receipts[s.receiptHead] == index {
		s.receiptHead++
	}
	return nil
}

func (s *WindowSweep) result(ctx context.Context, at, windowStart, eligible, retention time.Time) (WindowResult, error) {
	expected := int64(at.Sub(eligible) / s.input.Cadence)
	expired := int64(retention.Sub(eligible) / s.input.Cadence)
	missing := expected - expired - s.observedCount - s.emptyCount
	if missing < 0 {
		return WindowResult{}, ErrOperationalFacts
	}
	quality := "complete"
	switch {
	case expected == 0:
		quality = "not_applicable"
	case expired > 0:
		quality = "expired_history"
	case s.observedCount+s.emptyCount == 0:
		quality = "no_data"
	case missing > 0:
		quality = "partial"
	case s.observedCount > 0 && s.observedCount == s.zeroTraffic && s.emptyCount == 0:
		quality = "zero_traffic"
	}
	freshness := "unknown"
	var first, last, receipt *time.Time
	for s.observedHead < len(s.observed) && s.input.Observations[s.observed[s.observedHead]].Start.Before(retention) {
		if s.observedHead&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return WindowResult{}, err
			}
		}
		s.observedHead++
	}
	if s.observedHead < len(s.observed) && s.input.Observations[s.observed[s.observedHead]].End.Before(at) || s.observedHead < len(s.observed) && s.input.Observations[s.observed[s.observedHead]].End.Equal(at) {
		v := s.input.Observations[s.observed[s.observedHead]].Start.UTC()
		first = &v
		latest := s.input.Observations[s.observed[len(s.observed)-1]].End.UTC()
		last = &latest
		freshness = "fresh"
		if at.After(latest.Add(s.input.FreshnessGrace)) {
			freshness = "stale"
		}
	}
	for s.receiptHead < len(s.receipts) && s.input.Observations[s.receipts[s.receiptHead]].Start.Before(retention) {
		if s.receiptHead&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return WindowResult{}, err
			}
		}
		s.receiptHead++
	}
	if s.receiptHead < len(s.receipts) {
		v := s.input.Observations[s.receipts[s.receiptHead]].Received.UTC()
		receipt = &v
	}
	if s.totals != nil {
		first, last, receipt = s.totals.FirstObservation, s.totals.LastObservation, s.totals.LatestReceipt
		if last != nil {
			freshness = "fresh"
			if at.After(last.Add(s.input.FreshnessGrace)) {
				freshness = "stale"
			}
		}
	}
	result := WindowResult{RequestedStart: windowStart, EffectiveStart: windowStart, EligibleStart: eligible,
		RetentionStart: retention, End: at, State: quality, Freshness: freshness, Expected: expected,
		Observed: s.observedCount, KnownEmpty: s.emptyCount, Missing: missing, Expired: expired,
		ZeroTraffic: s.zeroTraffic, FirstObservation: first, LastObservation: last,
		LatestReceipt: receipt, Provisional: missing > 0 || expired > 0 || s.emptyCount > 0}
	if expected == 0 {
		result.Performance, result.Reason = "not_applicable", "not_applicable"
		return result, nil
	}
	if s.observedCount == 0 {
		result.Performance, result.Reason = "unknown", "no_measurements"
		return result, nil
	}
	if s.target == nil {
		return WindowResult{}, ErrInvalidCalculation
	}
	result.values = &windowValues{good: new(big.Rat).Set(&s.good), total: new(big.Rat).Set(&s.total), target: s.target}
	if s.total.Sign() == 0 {
		result.Performance = "unknown"
		if result.Provisional {
			result.Reason = "incomplete_measurements"
		} else {
			result.Reason = "zero_traffic"
		}
		return result, nil
	}
	one := big.NewRat(1, 1)
	bad := new(big.Rat).Sub(&s.total, &s.good)
	allowed := new(big.Rat).Mul(new(big.Rat).Sub(one, s.target), &s.total)
	remaining := new(big.Rat).Sub(allowed, bad)
	consumed := new(big.Rat).Quo(bad, allowed)
	result.Performance = "violated"
	if bad.Cmp(allowed) <= 0 {
		result.Performance = "met"
	}
	result.ObservedPerformance = result.Performance
	if result.Provisional {
		result.Performance = "unknown"
		result.Reason = "incomplete_measurements"
	}
	result.ExactBad = bad
	result.ExactRemaining = remaining
	result.ExactRemainingFraction = new(big.Rat).Sub(one, consumed)
	result.ExactBurn = consumed
	return result, nil
}

func floorBoundary(at time.Time, cadence time.Duration) time.Time {
	micros := cadence.Microseconds()
	return time.UnixMicro(at.UTC().UnixMicro() / micros * micros).UTC()
}

var ErrOperationalFacts = errors.New("slo: inconsistent operational facts")

// EvaluateOperationalWindow calculates an occurrence window from retained canonical facts.
// Missing and expired intervals are counted arithmetically; no absent ticks are allocated.
func EvaluateOperationalWindow(input WindowInput) (WindowResult, error) {
	return EvaluateOperationalWindowContext(context.Background(), input)
}

func EvaluateOperationalWindowContext(ctx context.Context, input WindowInput) (WindowResult, error) {
	sweep, err := NewWindowSweepContext(ctx, input)
	if err != nil {
		return WindowResult{}, err
	}
	return sweep.AtContext(ctx, input.At)
}

func ceilBoundary(value time.Time, cadence time.Duration) time.Time {
	floor := floorBoundary(value, cadence)
	if floor.Before(value) {
		return floor.Add(cadence)
	}
	return floor
}

func maxTime(first time.Time, others ...time.Time) time.Time {
	for _, value := range others {
		if value.After(first) {
			first = value
		}
	}
	return first
}

func rationalDecimal(value *big.Rat) string {
	return rounded(value)
}

// FormatRational serializes an exact rational using the public half-even decimal precision.
func FormatRational(value *big.Rat) string { return rounded(value) }
