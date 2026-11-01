package command

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// Independent fixture arithmetic: count periodic minute positions, rather than
// calling the application's evaluators, derived queries or graph reducer.
type performanceOracleCounts struct {
	Good, Total                    *big.Rat
	Observed, Empty, Zero, Missing int64
}

func checkPerformanceOracleMetrics(good, total, attainment, allowed, remaining, fraction, burn *string, want performanceOracleCounts, metric string) error {
	return checkPerformanceOracleTargetMetrics(good, total, attainment, allowed, remaining, fraction, burn, want, metric, "0.995")
}

func checkPerformanceOracleTargetMetrics(good, total, attainment, allowed, remaining, fraction, burn *string, want performanceOracleCounts, metric, target string) error {
	if err := checkPerformanceOracleCounts(good, total, want); err != nil {
		return err
	}
	if want.Total.Sign() == 0 {
		if attainment != nil || burn != nil || fraction != nil {
			return errors.New("oracle zero traffic/missing interval became a measured fraction")
		}
		return nil
	}
	if !performanceOracleRounded(attainment, new(big.Rat).Quo(want.Good, want.Total)) {
		return errors.New("oracle attainment mismatch")
	}
	if metric == "attainment" {
		return nil
	}
	ratio, valid := new(big.Rat).SetString(target)
	if !valid || ratio.Sign() < 0 || ratio.Cmp(big.NewRat(1, 1)) >= 0 {
		return errors.New("oracle invalid target")
	}
	budget := new(big.Rat).Mul(want.Total, new(big.Rat).Sub(big.NewRat(1, 1), ratio))
	bad := new(big.Rat).Sub(want.Total, want.Good)
	left := new(big.Rat).Sub(budget, bad)
	if !performanceOracleRounded(allowed, budget) || !performanceOracleRounded(remaining, left) || !performanceOracleRounded(fraction, new(big.Rat).Quo(left, budget)) || !performanceOracleRounded(burn, new(big.Rat).Quo(bad, budget)) {
		return errors.New("oracle error-budget/burn mismatch")
	}
	return nil
}

func TestPerformanceOracleDifferentTargets(t *testing.T) {
	want := performanceOracleCounts{Good: big.NewRat(999, 1), Total: big.NewRat(1000, 1), Observed: 1}
	for _, test := range []struct{ target, allowed, left, fraction, burn string }{
		{"0.99", "10.000000000000", "9.000000000000", "0.900000000000", "0.100000000000"},
		{"0.999", "1.000000000000", "0.000000000000", "0.000000000000", "1.000000000000"},
		{"0.9999", "0.100000000000", "-0.900000000000", "-9.000000000000", "10.000000000000"},
	} {
		if err := checkPerformanceOracleTargetMetrics(new("999"), new("1000"), new("0.999000000000"), &test.allowed, &test.left, &test.fraction, &test.burn, want, "budget", test.target); err != nil {
			t.Fatalf("target%s: %v", test.target, err)
		}
		if err := checkPerformanceOracleTargetMetrics(new("999"), new("1000"), new("0.999000000000"), &test.allowed, &test.left, new("0.500000000000"), &test.burn, want, "budget", test.target); err == nil {
			t.Fatal("wrong target budget accepted")
		}
	}
}

func performanceOracleRollingWindow(class int) time.Duration {
	window := 28 * 24 * time.Hour
	if class == 6 {
		window += 17 * time.Minute
	}
	return window
}

func checkPerformanceOracleCounts(good, total *string, want performanceOracleCounts) error {
	if good == nil && total == nil && want.Observed == 0 && want.Total.Sign() == 0 {
		return nil
	}
	if good == nil || total == nil {
		return errors.New("oracle found missing known counts")
	}
	g, gok := new(big.Rat).SetString(*good)
	t, tok := new(big.Rat).SetString(*total)
	if !gok || !tok || g.Cmp(want.Good) != 0 || t.Cmp(want.Total) != 0 {
		return errors.New("oracle exact good/total mismatch")
	}
	return nil
}

func TestPerformanceOracleDistinguishesMissingAndKnownZeroCounts(t *testing.T) {
	for _, test := range []struct {
		name        string
		good, total *string
		observed    int64
		valid       bool
	}{
		{"unobserved", nil, nil, 0, true},
		{"known-zero", new("0.000000000000"), new("0.000000000000"), 1, true},
		{"lost-known-zero", nil, nil, 1, false},
		{"lost-total", new("0"), nil, 1, false},
		{"nonzero-good", new("1"), new("0"), 1, false},
		{"invalid-count", new("invalid"), new("0"), 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := performanceOracleCounts{Good: new(big.Rat), Total: new(big.Rat), Observed: test.observed}
			if err := checkPerformanceOracleCounts(test.good, test.total, want); (err == nil) != test.valid {
				t.Fatalf("independent zero/missing count validation: %v", err)
			}
		})
	}
}

func performanceOracleWindow(class int, start, end time.Time) (performanceOracleCounts, error) {
	if class < 0 || class > 9 || !start.Before(end) || start.Unix()%60 != 0 || end.Unix()%60 != 0 || start.Nanosecond() != 0 || end.Nanosecond() != 0 {
		return performanceOracleCounts{}, errors.New("oracle requires a fixture class and minute-aligned interval")
	}
	a, b := start.Unix()/60, end.Unix()/60
	floor := func(n, d int64) int64 {
		q := n / d
		if n%d < 0 {
			q--
		}
		return q
	}
	positions := func(period, phase int64) int64 { return floor(b-1-phase, period) - floor(a-1-phase, period) }
	badPrefix := func(at int64) int64 {
		day := floor(at, 1440)
		return day*120 + min(max(at-day*1440-180, 0), 120)
	}
	n, bad := b-a, badPrefix(b)-badPrefix(a)
	counts := performanceOracleCounts{Observed: n}
	values := n
	switch class {
	case 1:
		counts.Missing = positions(60, 17)
		counts.Observed -= counts.Missing
		values -= counts.Missing
		bad -= positions(1440, 197) + positions(1440, 257)
	case 2, 3:
		removed := positions(60, 0)
		values -= removed
		bad -= positions(1440, 180) + positions(1440, 240)
		if class == 2 {
			counts.Empty = removed
			counts.Observed -= removed
		} else {
			counts.Zero = removed
		}
	case 4:
		counts.Good, _ = new(big.Rat).SetString("99999999999999999999999999.999999999998")
		counts.Total, _ = new(big.Rat).SetString("99999999999999999999999999.999999999999")
		counts.Good.Mul(counts.Good, big.NewRat(n, 1))
		counts.Total.Mul(counts.Total, big.NewRat(n, 1))
		return counts, nil
	}
	counts.Good = big.NewRat((values-bad)*99999+bad*97125, 1000)
	if class == 5 {
		counts.Good.Sub(counts.Good, big.NewRat(positions(1440, 31)*99999, 1000))
	}
	counts.Total = big.NewRat(values*100125, 1000)
	return counts, nil
}

// Validate output quantization independently: nearest multiple of 10^-12, with
// the even candidate selected at an exact half-unit tie. Never use float64.
func performanceOracleRounded(text *string, want *big.Rat) bool {
	if text == nil {
		return false
	}
	_, fraction, found := strings.Cut(*text, ".")
	actual, valid := new(big.Rat).SetString(*text)
	if !found || len(fraction) != 12 || !valid {
		return false
	}
	for _, digit := range fraction {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	distance := new(big.Rat).Sub(actual, want)
	distance.Abs(distance)
	comparison := distance.Cmp(big.NewRat(1, 2_000_000_000_000))
	return comparison < 0 || comparison == 0 && (fraction[len(fraction)-1]-'0')%2 == 0
}

func TestPerformanceOracleCountsAndRounding(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		class                          int
		good, total                    string
		observed, empty, zero, missing int64
	}{
		{0, "143653.68", "144180", 1440, 0, 0, 0},
		{1, "141259.452", "141777", 1416, 0, 0, 24},
		{2, "141259.452", "141777", 1416, 24, 0, 0},
		{3, "141259.452", "141777", 1440, 0, 24, 0},
		{4, "143999999999999999999999999999.999999997120", "143999999999999999999999999999.999999998560", 1440, 0, 0, 0},
		{5, "143553.681", "144180", 1440, 0, 0, 0},
	} {
		got, err := performanceOracleWindow(test.class, start, start.Add(24*time.Hour))
		good, _ := new(big.Rat).SetString(test.good)
		total, _ := new(big.Rat).SetString(test.total)
		if err != nil || got.Good.Cmp(good) != 0 || got.Total.Cmp(total) != 0 || got.Observed != test.observed || got.Empty != test.empty || got.Zero != test.zero || got.Missing != test.missing {
			t.Fatalf("fixture class%d differs from independently counted day: %+v %v", test.class, got, err)
		}
	}
	for _, test := range []struct {
		ratio, text string
		valid       bool
	}{
		{"1/3", "0.333333333333", true}, {"1/3", "0.333333333334", false},
		{"1/2000000000000", "0.000000000000", true}, {"1/2000000000000", "0.000000000001", false},
		{"-3/2000000000000", "-0.000000000002", true}, {"-3/2000000000000", "-0.000000000001", false},
	} {
		ratio, _ := new(big.Rat).SetString(test.ratio)
		if performanceOracleRounded(new(test.text), ratio) != test.valid {
			t.Fatalf("incorrect independent rounding check: %+v", test)
		}
	}
}

func TestPerformanceOracleMinuteBoundaries(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		class                          int
		start, end                     time.Duration
		good, total                    string
		observed, empty, zero, missing int64
	}{
		{0, 179 * time.Minute, 181 * time.Minute, "197.124", "200.25", 2, 0, 0, 0},
		{1, 16 * time.Minute, 19 * time.Minute, "199.998", "200.25", 2, 0, 0, 1},
		{2, 180 * time.Minute, 182 * time.Minute, "97.125", "100.125", 1, 1, 0, 0},
		{3, 180 * time.Minute, 182 * time.Minute, "97.125", "100.125", 2, 0, 1, 0},
		{0, 1439 * time.Minute, 1441 * time.Minute, "199.998", "200.25", 2, 0, 0, 0},
		{5, 30 * time.Minute, 33 * time.Minute, "199.998", "300.375", 3, 0, 0, 0},
		{5, 31 * time.Minute, 32 * time.Minute, "0", "100.125", 1, 0, 0, 0},
		{6, -28*24*time.Hour - 17*time.Minute, 0, "4024003.023", "4038742.125", 40337, 0, 0, 0},
	} {
		got, err := performanceOracleWindow(test.class, day.Add(test.start), day.Add(test.end))
		good, _ := new(big.Rat).SetString(test.good)
		total, _ := new(big.Rat).SetString(test.total)
		if err != nil || got.Good.Cmp(good) != 0 || got.Total.Cmp(total) != 0 || got.Observed != test.observed || got.Empty != test.empty || got.Zero != test.zero || got.Missing != test.missing {
			t.Fatalf("boundary oracle mismatch: %+v %+v %v", test, got, err)
		}
	}
}
