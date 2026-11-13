package slo

import (
	"errors"
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func exclusionAggregateFixture() (WindowInput, WindowTotals) {
	input := exclusionFixture()
	input.Observations = nil
	input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom.Add(time.Minute), End: input.At}}
	totals := WindowTotals{Good: big.NewRat(900, 1), Total: big.NewRat(1000, 1), Observed: 1,
		FirstObservation: new(input.ValidFrom), LastObservation: new(input.ValidFrom.Add(time.Minute)), LatestReceipt: new(input.ValidFrom.Add(time.Minute)),
		Excluded: ExclusionTotals{Good: big.NewRat(100, 1), Total: big.NewRat(1000, 1), Observed: 1}}
	return input, totals
}

func TestExclusionAggregateExactPopulation(t *testing.T) {
	input, totals := exclusionAggregateFixture()
	got, err := EvaluateOperationalTotals(t.Context(), input, totals)
	if err != nil {
		t.Fatal(err)
	}
	exclusionValue(t, "adjusted attainment", got.Attainment, "0.900000000000")
	exclusionValue(t, "adjusted remaining", got.Remaining, "-90.000000000000")
	exclusionValue(t, "adjusted burn", got.Burn, "10.000000000000")
	exclusionValue(t, "excluded total", got.ExcludedTotal, "1000.000000000000")
	if got.Expected != 1 || got.Excluded != 1 || got.Missing != 0 || got.State != "complete" || got.Freshness != "fresh" {
		t.Fatalf("aggregate quality = %+v", got)
	}
	raw := exclusionFixture()
	raw.Exclusions = input.Exclusions
	canonical, err := EvaluateOperationalWindow(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canonical, got) {
		t.Fatalf("raw/totals disagreement:\nraw=%+v\ntotals=%+v", canonical, got)
	}
	if totals.Good.RatString() != "900" || totals.Total.RatString() != "1000" || totals.Excluded.Good.RatString() != "100" || totals.Excluded.Total.RatString() != "1000" {
		t.Fatal("consumer mutated exact input rationals")
	}
}

func TestExclusionAggregateUnknownAndEntirePopulation(t *testing.T) {
	for _, variant := range []string{"missing", "known_empty", "expired", "all_excluded", "all_excluded_missing", "pre_validity"} {
		t.Run(variant, func(t *testing.T) {
			input, totals := exclusionAggregateFixture()
			switch variant {
			case "missing":
				totals.Excluded = ExclusionTotals{}
			case "known_empty":
				totals.Excluded = ExclusionTotals{KnownEmpty: 1}
			case "expired":
				input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom, End: input.ValidFrom.Add(time.Minute)}}
				input.RetentionStart = input.ValidFrom.Add(time.Minute)
				totals.FirstObservation, totals.LastObservation, totals.LatestReceipt = new(input.RetentionStart), new(input.At), new(input.At)
				totals.Excluded = ExclusionTotals{}
			case "all_excluded", "all_excluded_missing", "pre_validity":
				input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom, End: input.At}}
				totals = WindowTotals{Good: new(big.Rat), Total: new(big.Rat), Excluded: ExclusionTotals{Good: big.NewRat(1000, 1), Total: big.NewRat(2000, 1), Observed: 2}}
				if variant == "all_excluded_missing" {
					totals.Excluded = ExclusionTotals{}
				}
				if variant == "pre_validity" {
					input.ValidFrom = input.At
					totals.Excluded = ExclusionTotals{}
				}
			}
			got, err := EvaluateOperationalTotals(t.Context(), input, totals)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "all_excluded", "all_excluded_missing":
				if got.State != "excluded" || got.Excluded != 2 || got.Expected != 0 || got.Burn != nil || got.Total != nil {
					t.Fatalf("full exclusion = %+v", got)
				}
			case "pre_validity":
				if got.State != "not_applicable" || got.Excluded != 0 {
					t.Fatalf("pre-validity = %+v", got)
				}
			default:
				if got.State != "complete" || got.Expected != 1 || got.Missing != 0 || got.Expired != 0 {
					t.Fatalf("unknown excluded population tainted eligible quality: %+v", got)
				}
			}
			if variant == "all_excluded" {
				exclusionValue(t, "excluded total", got.ExcludedTotal, "2000.000000000000")
			} else if got.ExcludedGood != nil || got.ExcludedTotal != nil {
				t.Fatalf("unknown excluded event population became known: %+v", got)
			}
		})
	}
}

func TestExclusionAggregateRejectsInconsistentFacts(t *testing.T) {
	for _, variant := range []string{"too_many_excluded", "excluded_overflow", "too_many_empty", "negative_empty", "missing_count", "negative_good", "good_above_total", "counts_without_observations", "no_mask", "mask_outside", "expired_excluded", "eligible_overflow", "first_excluded", "last_excluded", "unaligned_first", "impossible_observed_span"} {
		t.Run(variant, func(t *testing.T) {
			input, totals := exclusionAggregateFixture()
			switch variant {
			case "too_many_excluded":
				totals.Excluded.Observed = 2
			case "excluded_overflow":
				totals.Excluded.Observed, totals.Excluded.KnownEmpty = math.MaxInt64, math.MaxInt64
			case "too_many_empty":
				totals.Excluded.KnownEmpty = 1
			case "negative_empty":
				totals.Excluded.KnownEmpty = -1
			case "missing_count":
				totals.Excluded.Good = nil
			case "negative_good":
				totals.Excluded.Good = big.NewRat(-1, 1)
			case "good_above_total":
				totals.Excluded.Good = big.NewRat(1001, 1)
			case "counts_without_observations":
				totals.Excluded.Observed = 0
			case "no_mask":
				input.Exclusions = nil
			case "mask_outside":
				input.Exclusions = []ExclusionInterval{{Start: input.At, End: input.At.Add(time.Minute)}}
			case "expired_excluded":
				input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom, End: input.ValidFrom.Add(time.Minute)}}
				input.RetentionStart = input.ValidFrom.Add(time.Minute)
				totals.FirstObservation, totals.LastObservation = new(input.RetentionStart), new(input.At)
			case "eligible_overflow":
				totals.Observed, totals.KnownEmpty = math.MaxInt64, math.MaxInt64
			case "first_excluded":
				totals.FirstObservation, totals.LastObservation = new(input.ValidFrom.Add(time.Minute)), new(input.At)
			case "last_excluded":
				totals.LastObservation = new(input.At)
			case "unaligned_first":
				totals.FirstObservation = new(input.ValidFrom.Add(time.Second))
			case "impossible_observed_span":
				input.Duration = 3 * time.Minute
				input.ValidFrom = input.ValidFrom.Add(-time.Minute)
				input.RetentionStart = input.ValidFrom
				input.SliValidFrom = input.ValidFrom
				totals.Observed = 2
			}
			if _, err := EvaluateOperationalTotals(t.Context(), input, totals); !errors.Is(err, ErrOperationalFacts) {
				t.Fatalf("inconsistent %s accepted: %v", variant, err)
			}
		})
	}
}
