package slo

import (
	"context"
	"errors"
	"math"
	"math/big"
	"reflect"
	"slices"
	"testing"
	"time"
)

// This deliberately enumerates a small grid and tests literal membership. It
// does not call the production mask/prefix implementation to build its oracle.
func coveredFixture(t *testing.T, input WindowInput) (WindowTotals, WindowCoverage) {
	t.Helper()
	contains := func(at time.Time) bool {
		for _, m := range input.Exclusions {
			if !at.Before(m.Start) && at.Before(m.End) {
				return true
			}
		}
		return false
	}
	start := input.At.Add(-input.Duration)
	eligible := start
	for _, v := range []time.Time{input.ValidFrom, input.SliValidFrom} {
		if v.After(eligible) {
			eligible = v
		}
	}
	ceil := func(at time.Time) time.Time {
		if remainder := at.UnixMicro() % input.Cadence.Microseconds(); remainder != 0 {
			return at.Add(time.Duration(input.Cadence.Microseconds()-remainder) * time.Microsecond)
		}
		return at
	}
	eligible = ceil(eligible)
	if eligible.After(input.At) {
		eligible = input.At
	}
	retained := eligible
	if input.RetentionStart.After(retained) {
		retained = input.RetentionStart
	}
	retained = ceil(retained)
	if retained.After(input.At) {
		retained = input.At
	}
	totals := WindowTotals{Good: new(big.Rat), Total: new(big.Rat)}
	var excludedGood, excludedTotal big.Rat
	for _, o := range input.Observations {
		if o.Start.Before(retained) || o.End.After(input.At) {
			continue
		}
		if contains(o.Start) {
			if o.Outcome == "observed" {
				g, _ := new(big.Rat).SetString(o.Good)
				v, _ := new(big.Rat).SetString(o.Total)
				excludedGood.Add(&excludedGood, g)
				excludedTotal.Add(&excludedTotal, v)
				totals.Excluded.Observed++
			} else if o.Outcome == "complete_empty" {
				totals.Excluded.KnownEmpty++
			}
			continue
		}
		if o.Outcome == "observed" {
			g, _ := new(big.Rat).SetString(o.Good)
			v, _ := new(big.Rat).SetString(o.Total)
			totals.Good.Add(totals.Good, g)
			totals.Total.Add(totals.Total, v)
			totals.Observed++
			if v.Sign() == 0 {
				totals.ZeroTraffic++
			}
			if totals.FirstObservation == nil {
				totals.FirstObservation = new(o.Start)
			}
			totals.LastObservation = new(o.End)
		} else if o.Outcome == "complete_empty" {
			totals.KnownEmpty++
		} else {
			continue
		}
		if totals.LatestReceipt == nil || o.Received.After(*totals.LatestReceipt) {
			totals.LatestReceipt = new(o.Received)
		}
	}
	if totals.Excluded.Observed > 0 {
		totals.Excluded.Good = new(big.Rat).Set(&excludedGood)
		totals.Excluded.Total = new(big.Rat).Set(&excludedTotal)
	}
	latest := input.At
	for contains(latest.Add(-input.Cadence)) {
		latest = latest.Add(-input.Cadence)
	}
	anchors := []time.Time{eligible, retained, input.At, latest, latest.Add(-input.Cadence)}
	if totals.FirstObservation != nil {
		anchors = append(anchors, *totals.FirstObservation, totals.FirstObservation.Add(input.Cadence), totals.LastObservation.Add(-input.Cadence), *totals.LastObservation)
	}
	slices.SortFunc(anchors, func(a, b time.Time) int { return a.Compare(b) })
	anchors = slices.CompactFunc(anchors, func(a, b time.Time) bool { return a.Equal(b) })
	origin := anchors[0]
	coverage := WindowCoverage{LatestEligibleEnd: latest}
	for _, a := range anchors {
		var rank uint64 = 19
		for at := origin; at.Before(a); at = at.Add(input.Cadence) {
			if contains(at) {
				rank++
			}
		}
		coverage.Points = append(coverage.Points, ExclusionCoveragePoint{At: a, ExcludedBefore: rank})
	}
	return totals, coverage
}

func TestCoveredTotalsRawOracle(t *testing.T) {
	for _, cadence := range []time.Duration{time.Minute, 70 * time.Second, 2 * time.Hour, time.Microsecond} {
		for _, variant := range []string{"none", "overlap", "adjacent", "unaligned_bounds", "fragmented", "trailing", "fully_excluded_before_window", "expired", "validity", "not_applicable", "zero", "missing", "empty", "failed", "exact_decimal"} {
			t.Run(cadence.String()+"/"+variant, func(t *testing.T) {
				start := time.UnixMicro(100000 * cadence.Microseconds()).UTC()
				input := WindowInput{At: start.Add(12 * cadence), Now: start.Add(13 * cadence), ValidFrom: start, SliValidFrom: start, RetentionStart: start, Duration: 12 * cadence, Cadence: cadence, FreshnessGrace: 0, Target: "0.99"}
				switch variant {
				case "overlap":
					input.Exclusions = []ExclusionInterval{{start.Add(cadence), start.Add(5 * cadence)}, {start.Add(3 * cadence), start.Add(7 * cadence)}}
				case "adjacent":
					input.Exclusions = []ExclusionInterval{{start.Add(cadence), start.Add(3 * cadence)}, {start.Add(3 * cadence), start.Add(5 * cadence)}}
				case "unaligned_bounds":
					input.ValidFrom = start.Add(cadence + time.Microsecond)
					input.RetentionStart = start.Add(3*cadence + time.Microsecond)
					input.Exclusions = []ExclusionInterval{{start, start.Add(5 * cadence)}}
				case "fragmented":
					for i := 1; i < 12; i += 2 {
						input.Exclusions = append(input.Exclusions, ExclusionInterval{start.Add(time.Duration(i) * cadence), start.Add(time.Duration(i+1) * cadence)})
					}
				case "trailing", "empty", "failed", "exact_decimal", "zero", "missing":
					input.Exclusions = []ExclusionInterval{{start.Add(8 * cadence), input.At}}
				case "fully_excluded_before_window":
					input.Exclusions = []ExclusionInterval{{start.Add(-3 * cadence), input.At.Add(2 * cadence)}}
				case "expired":
					input.RetentionStart = start.Add(4 * cadence)
					input.Exclusions = []ExclusionInterval{{start.Add(cadence), start.Add(6 * cadence)}}
				case "validity":
					input.ValidFrom = start.Add(3 * cadence)
					input.SliValidFrom = start.Add(4 * cadence)
					input.Exclusions = []ExclusionInterval{{start, start.Add(6 * cadence)}}
				case "not_applicable":
					input.ValidFrom = input.At.Add(cadence)
					input.Exclusions = []ExclusionInterval{{start, input.At}}
				}
				for i := range 12 {
					if variant == "missing" && (i == 2 || i == 9) {
						continue
					}
					o := Observation{Start: start.Add(time.Duration(i) * cadence), End: start.Add(time.Duration(i+1) * cadence), Received: start.Add(time.Duration(i+2) * cadence), Outcome: "observed", Good: "99", Total: "100"}
					if variant == "zero" {
						o.Good, o.Total = "0", "0"
					}
					if variant == "exact_decimal" {
						o.Good, o.Total = "99999999999999999999999999.999999999998", "99999999999999999999999999.999999999999"
					}
					if variant == "empty" && (i == 2 || i == 9) {
						o.Outcome, o.Good, o.Total = "complete_empty", "", ""
					}
					if variant == "failed" && (i == 2 || i == 9) {
						o.Outcome, o.Good, o.Total = "failed", "", ""
					}
					input.Observations = append(input.Observations, o)
				}
				want, err := EvaluateOperationalWindowContext(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				totals, coverage := coveredFixture(t, input)
				input.Observations, input.Exclusions = nil, nil
				got, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("raw/covered differ:\n got=%+v\nwant=%+v", got, want)
				}
				summary, err := EvaluateOperationalCoveredTotalsSummary(t.Context(), input, totals, coverage)
				if err != nil || !reflect.DeepEqual(summary.WithValues(), want) {
					t.Fatalf("summary %+v %v", summary, err)
				}
			})
		}
	}
}

func TestCoveredTotalsExampleAndOwnership(t *testing.T) {
	input := exclusionFixture()
	input.Exclusions = []ExclusionInterval{{input.Observations[1].Start, input.At}}
	totals, coverage := coveredFixture(t, input)
	input.Exclusions, input.Observations = nil, nil
	original := slices.Clone(coverage.Points)
	got, err := EvaluateOperationalCoveredTotalsSummary(t.Context(), input, totals, coverage)
	if err != nil {
		t.Fatal(err)
	}
	want := got.WithValues()
	exclusionValue(t, "burn", want.Burn, "10.000000000000")
	exclusionValue(t, "excluded", want.ExcludedTotal, "1000.000000000000")
	if !slices.Equal(coverage.Points, original) || totals.Good.RatString() != "900" {
		t.Fatal("inputs changed")
	}
	totals.Good.SetInt64(0)
	totals.Total.SetInt64(0)
	totals.Excluded.Good.SetInt64(0)
	totals.Excluded.Total.SetInt64(0)
	*totals.FirstObservation = time.Time{}
	*totals.LastObservation = time.Time{}
	*totals.LatestReceipt = time.Time{}
	coverage.Points[0] = ExclusionCoveragePoint{}
	if !reflect.DeepEqual(got.WithValues(), want) {
		t.Fatal("result aliases input memory")
	}
}

func TestCoveredTotalsInvalidCertificate(t *testing.T) {
	for _, variant := range []string{"missing", "extra", "duplicate", "decreasing", "excessive_rank", "zero_latest", "future_latest", "misaligned", "masked_observation_bounds", "span_capacity", "raw_mask", "raw_observations", "count_overflow", "submicro_cadence", "unrepresentable_time", "outside_observation"} {
		t.Run(variant, func(t *testing.T) {
			input := exclusionFixture()
			input.Exclusions = []ExclusionInterval{{input.Observations[1].Start, input.At}}
			totals, coverage := coveredFixture(t, input)
			input.Observations, input.Exclusions = nil, nil
			switch variant {
			case "missing":
				coverage.Points = coverage.Points[1:]
			case "extra":
				coverage.Points = append(coverage.Points, ExclusionCoveragePoint{At: input.At.Add(time.Minute), ExcludedBefore: 21})
			case "duplicate":
				coverage.Points = append(coverage.Points, coverage.Points[len(coverage.Points)-1])
			case "decreasing":
				coverage.Points[len(coverage.Points)-1].ExcludedBefore = 0
			case "excessive_rank":
				coverage.Points[len(coverage.Points)-1].ExcludedBefore = math.MaxUint64
			case "zero_latest":
				coverage.LatestEligibleEnd = time.Time{}
			case "future_latest":
				coverage.LatestEligibleEnd = input.At.Add(time.Minute)
			case "misaligned":
				coverage.Points[0].At = coverage.Points[0].At.Add(time.Second)
			case "masked_observation_bounds":
				*totals.FirstObservation = input.At.Add(-time.Minute)
				*totals.LastObservation = input.At
			case "span_capacity":
				totals.Observed = 2
			case "raw_mask":
				input.Exclusions = []ExclusionInterval{{input.ValidFrom, input.At}}
			case "raw_observations":
				input.Observations = exclusionFixture().Observations
			case "count_overflow":
				totals.KnownEmpty = math.MaxInt64
			case "submicro_cadence":
				input.Cadence = time.Nanosecond
			case "unrepresentable_time":
				input.At = time.UnixMicro(math.MaxInt64).Add(time.Microsecond)
			case "outside_observation":
				*totals.FirstObservation = input.ValidFrom.Add(-time.Minute)
			}
			if _, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage); err == nil {
				t.Fatal("invalid certificate accepted")
			}
		})
	}
}

func TestCoveredTotalsCancellationAndOptionalReceipt(t *testing.T) {
	input := exclusionFixture()
	totals, coverage := coveredFixture(t, input)
	input.Observations = nil
	want, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
	if err != nil {
		t.Fatal(err)
	}
	totals.LatestReceipt = nil
	want.LatestReceipt = nil
	got, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("optional receipt changed facts: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := EvaluateOperationalCoveredTotals(ctx, input, totals, coverage); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

// Overall retained capacity is sufficient in both cases. Only the individual
// endpoint proofs distinguish these impossible observed populations.
func TestCoveredTotalsRejectsMaskedEndpointsWithinCapacity(t *testing.T) {
	for _, masked := range []string{"first", "last"} {
		t.Run(masked, func(t *testing.T) {
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			input := WindowInput{At: start.Add(4 * time.Minute), Now: start.Add(4 * time.Minute), ValidFrom: start, RetentionStart: start, Cadence: time.Minute, Duration: 4 * time.Minute, Target: "0.99"}
			totals := WindowTotals{Good: big.NewRat(2, 1), Total: big.NewRat(2, 1), Observed: 2, FirstObservation: new(start), LastObservation: new(input.At)}
			coverage := WindowCoverage{LatestEligibleEnd: input.At}
			for i := range 5 {
				rank := uint64(10)
				if masked == "first" && i >= 1 || masked == "last" && i == 4 {
					rank++
				}
				coverage.Points = append(coverage.Points, ExclusionCoveragePoint{At: start.Add(time.Duration(i) * time.Minute), ExcludedBefore: rank})
			}
			if masked == "first" {
				coverage.Points = slices.Delete(coverage.Points, 2, 3)
			} else {
				coverage.LatestEligibleEnd = input.At.Add(-time.Minute)
			}
			if _, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage); !errors.Is(err, ErrOperationalFacts) {
				t.Fatalf("masked %s accepted: %v", masked, err)
			}
		})
	}
}

func TestCoveredTotalsPrefixOriginDoesNotChangeResult(t *testing.T) {
	input := exclusionFixture()
	input.Exclusions = []ExclusionInterval{{input.Observations[1].Start, input.At}}
	totals, coverage := coveredFixture(t, input)
	input.Exclusions, input.Observations = nil, nil
	want, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
	if err != nil {
		t.Fatal(err)
	}
	shift := uint64(math.MaxUint64) - coverage.Points[len(coverage.Points)-1].ExcludedBefore
	for i := range coverage.Points {
		coverage.Points[i].ExcludedBefore += shift
	}
	got, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix origin changed result: %+v %v", got, err)
	}
}

func TestCoveredTotalsWidePrefixDistanceAndPreviousOverflow(t *testing.T) {
	at := time.UnixMicro(math.MaxInt64).UTC()
	start := at.Add(-4 * time.Microsecond)
	latest := time.UnixMicro(math.MinInt64 + 1).UTC()
	input := WindowInput{At: at, Now: at, Duration: 4 * time.Microsecond, Cadence: time.Microsecond, ValidFrom: start, RetentionStart: start, Target: "0.99"}
	totals := WindowTotals{Good: new(big.Rat), Total: new(big.Rat)}
	coverage := WindowCoverage{LatestEligibleEnd: latest, Points: []ExclusionCoveragePoint{
		{At: latest.Add(-time.Microsecond), ExcludedBefore: 0},
		{At: latest, ExcludedBefore: 0},
		{At: start, ExcludedBefore: math.MaxUint64 - 5},
		{At: at, ExcludedBefore: math.MaxUint64 - 1},
	}}
	got, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
	if err != nil || got.State != "excluded" || got.Excluded != 4 || got.Expected != 0 || got.Good != nil {
		t.Fatalf("wide prefix: %+v %v", got, err)
	}
	coverage.LatestEligibleEnd = time.UnixMicro(math.MinInt64).UTC()
	if _, err := EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage); !errors.Is(err, ErrOperationalFacts) {
		t.Fatalf("unrepresentable predecessor accepted: %v", err)
	}
}
