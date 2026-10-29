package storage

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOverviewCursorIsBoundToSnapshotOffsetAndSecret(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	cursor := overviewCursor{SnapshotID: "a842b928-2799-47d2-992c-ed7836b80111", Offset: 50}
	encoded := encodeOverviewCursor(secret, cursor)
	decoded, ok := decodeOverviewCursor(encoded)
	if !ok || decoded.SnapshotID != cursor.SnapshotID || decoded.Offset != cursor.Offset || !validOverviewCursor(secret, decoded) {
		t.Fatalf("round-trip cursor = %+v, %v", decoded, ok)
	}
	decoded.Offset++
	if validOverviewCursor(secret, decoded) {
		t.Fatal("cursor accepted a modified offset")
	}
	decoded.Offset = cursor.Offset
	if validOverviewCursor([]byte("11234567890123456789012345678901"), decoded) {
		t.Fatal("cursor accepted another snapshot secret")
	}
	if _, ok := decodeOverviewCursor(encoded + "!"); ok {
		t.Fatal("cursor decoder accepted invalid base64")
	}
}

func TestOperationalBucketBoundariesCoverExactRange(t *testing.T) {
	start := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	boundaries := bucketBoundaries(start, end, 10, 3, time.Minute)
	if len(boundaries) != 4 || !boundaries[0].Equal(start) || !boundaries[3].Equal(end) || boundaries[1].Sub(start) != 3*time.Minute || boundaries[2].Sub(start) != 6*time.Minute {
		t.Fatalf("bucket boundaries = %v", boundaries)
	}
}

func TestOperationalQueryLimitCountsUnicodeCharacters(t *testing.T) {
	if !validOperationalQuery(strings.Repeat("界", 128)) {
		t.Fatal("valid 128-character query was rejected due to its UTF-8 byte length")
	}
	if validOperationalQuery(strings.Repeat("界", 129)) || validOperationalQuery(string([]byte{0xff})) {
		t.Fatal("query beyond the character limit or invalid UTF-8 was accepted")
	}
}

func TestOperationalRetryAndCancellationAreBounded(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := retryOperationalRead(ctx, func() error {
		attempts++
		if attempts < 3 {
			return errReadSerialization
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("retry attempts=%d err=%v", attempts, err)
	}
	attempts = 0
	err = retryOperationalRead(ctx, func() error {
		attempts++
		return errReadSerialization
	})
	if !errors.Is(err, errReadSerialization) || attempts != sloOperationalReadAttempts {
		t.Fatalf("exhausted retries attempts=%d err=%v", attempts, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = retryOperationalRead(cancelled, func() error {
		t.Fatal("cancelled attempt was executed")
		return nil
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancelled retry error=%v", err)
	}
	if !errors.Is(readTransactionError(ctx, "test", &pgconn.PgError{Code: "40001"}), errReadSerialization) {
		t.Fatal("serialization failure was not classified for retry")
	}
}

func TestOperationalProjectionPayloadBound(t *testing.T) {
	if got, err := addProjectionPayloadBytes(sloOverviewProjectionByteLimit-1, 1); err != nil || got != sloOverviewProjectionByteLimit {
		t.Fatalf("exact payload boundary = %d, %v", got, err)
	}
	if _, err := addProjectionPayloadBytes(sloOverviewProjectionByteLimit-1, 2); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("over payload limit error=%v", err)
	}
}

func TestOperationalSamplesPreserveEvictionExtremaAndGapRuns(t *testing.T) {
	cadence := time.Minute
	start := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	base := slo.WindowInput{Now: start.Add(5 * time.Minute), ValidFrom: start, SliValidFrom: start, RetentionStart: start.Add(-time.Hour),
		Cadence: cadence, Target: "0.5", FreshnessGrace: 10 * time.Minute, Observations: []slo.Observation{
			{Start: start, End: start.Add(cadence), Received: start.Add(2 * time.Second), Outcome: "observed", Good: "0", Total: "100"},
			{Start: start.Add(cadence), End: start.Add(2 * cadence), Received: start.Add(2*cadence + 2*time.Second), Outcome: "observed", Good: "100", Total: "100"},
		}}
	samples, err := buildOperationalSamples(context.Background(), base, start, start.Add(5*cadence), 2*cadence, cadence, "1", "remaining_budget", 2)
	if err != nil {
		t.Fatal(err)
	}
	evictionAt := start.Add(3 * cadence)
	var eviction *api.SloOperationalSample
	for index := range samples {
		if samples[index].EvaluationAt.Equal(evictionAt) {
			eviction = &samples[index]
			break
		}
	}
	if eviction == nil || eviction.State != "partial" || eviction.RemainingBudget == nil || *eviction.RemainingBudget != "50.000000000000" {
		t.Fatalf("old observation eviction sample = %+v; want partial and rising remaining budget", eviction)
	}

	gapBase := slo.WindowInput{Now: start.Add(3 * cadence), ValidFrom: start, SliValidFrom: start, RetentionStart: start.Add(-time.Hour),
		Cadence: cadence, Target: "0.5", FreshnessGrace: 10 * time.Minute, Observations: []slo.Observation{
			{Start: start, End: start.Add(cadence), Received: start.Add(2 * time.Second), Outcome: "observed", Good: "100", Total: "100"},
			{Start: start.Add(2 * cadence), End: start.Add(3 * cadence), Received: start.Add(3*cadence + 2*time.Second), Outcome: "observed", Good: "100", Total: "100"},
		}}
	gapSamples, err := buildOperationalSamples(context.Background(), gapBase, start, start.Add(3*cadence), 3*cadence, cadence, "1", "remaining_budget", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(gapSamples, func(sample api.SloOperationalSample) bool {
		return sample.State == "partial" && sample.EvaluationAt.Equal(start.Add(2*cadence))
	}) {
		t.Fatalf("reduced points erased the interior gap: %+v", gapSamples)
	}
}

func TestBudgetReductionRetainsExactRemainingFractionExtrema(t *testing.T) {
	cadence := time.Minute
	start := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	facts := []slo.Observation{
		{Start: start, End: start.Add(cadence), Received: start.Add(cadence), Outcome: "observed", Good: "9950", Total: "10000"},
		{Start: start.Add(cadence), End: start.Add(2 * cadence), Received: start.Add(2 * cadence), Outcome: "observed", Good: "9910", Total: "10000"},
		{Start: start.Add(2 * cadence), End: start.Add(3 * cadence), Received: start.Add(3 * cadence), Outcome: "observed", Good: "99.9", Total: "100"},
		{Start: start.Add(3 * cadence), End: start.Add(4 * cadence), Received: start.Add(4 * cadence), Outcome: "observed", Good: "9960", Total: "10000"},
	}
	base := slo.WindowInput{Now: start.Add(4 * cadence), ValidFrom: start, SliValidFrom: start, RetentionStart: start.Add(-time.Hour),
		Cadence: cadence, Target: "0.99", FreshnessGrace: 10 * time.Minute, Observations: facts}
	samples, err := buildOperationalSamples(context.Background(), base, start.Add(cadence), start.Add(4*cadence), cadence, cadence, "1", "remaining_fraction", 1)
	if err != nil {
		t.Fatal(err)
	}
	byTime := make(map[time.Time]api.SloOperationalSample, len(samples))
	for _, sample := range samples {
		byTime[sample.EvaluationAt] = sample
	}
	minimum := byTime[start.Add(2*cadence)]
	maximum := byTime[start.Add(3*cadence)]
	if minimum.RemainingFraction == nil || *minimum.RemainingFraction != "0.100000000000" || minimum.RemainingBudget == nil || *minimum.RemainingBudget != "10.000000000000" {
		t.Fatalf("fraction minimum was not retained with its event budget: %+v; points=%+v", minimum, samples)
	}
	if maximum.RemainingFraction == nil || *maximum.RemainingFraction != "0.900000000000" || maximum.RemainingBudget == nil || *maximum.RemainingBudget != "0.900000000000" {
		t.Fatalf("fraction maximum was not retained with its event budget: %+v; points=%+v", maximum, samples)
	}
}

func TestOperationalSamplesKeepEmptyAndObservedArithmeticConsistent(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	knownEmpty := operationalSample(slo.WindowResult{End: at, State: "complete", Freshness: "unknown", KnownEmpty: 4, Expected: 4}, at, at, "1")
	if knownEmpty.State != "known_empty" || knownEmpty.Good != nil || knownEmpty.Total != nil {
		t.Fatalf("known-empty sample = %+v", knownEmpty)
	}
	zeroTraffic := operationalSample(slo.WindowResult{End: at, State: "zero_traffic", Freshness: "fresh", Observed: 2, Good: stringPointer("0.000000000000"), Total: stringPointer("0.000000000000")}, at, at, "1")
	if zeroTraffic.State != "zero_traffic" || zeroTraffic.Good == nil || zeroTraffic.Total == nil || zeroTraffic.Attainment != nil {
		t.Fatalf("0/0 sample = %+v", zeroTraffic)
	}
	partialExpired := operationalSample(slo.WindowResult{End: at, State: "expired_history", Freshness: "fresh", Observed: 1, Expired: 1, Good: stringPointer("99"), Total: stringPointer("100"), Provisional: true}, at, at, "1")
	if partialExpired.State != "partial" || partialExpired.Good == nil || partialExpired.Total == nil {
		t.Fatalf("expired arithmetic sample = %+v", partialExpired)
	}
}

func TestDailyComplianceUsesRollingResultsAndRetainsUncertainDays(t *testing.T) {
	cadence := time.Minute
	dayEnd := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	window := time.Hour
	facts := make([]slo.Observation, int(window/cadence))
	for index := range facts {
		start := dayEnd.Add(-window).Add(time.Duration(index) * cadence)
		facts[index] = slo.Observation{Start: start, End: start.Add(cadence), Received: start.Add(cadence), Outcome: "observed", Good: "99", Total: "100"}
	}
	base := slo.WindowInput{Now: dayEnd, ValidFrom: dayEnd.Add(-24 * time.Hour), SliValidFrom: dayEnd.Add(-24 * time.Hour), RetentionStart: dayEnd.Add(-400 * 24 * time.Hour),
		Cadence: cadence, Target: "0.99", FreshnessGrace: 3 * time.Minute, Observations: facts}
	series, summary, err := buildDailyCompliance(context.Background(), base, dayEnd.Add(-24*time.Hour), dayEnd, dayEnd,
		api.SloId{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x41, 0x11, 0x81, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}, "1", "0.99", window, cadence)
	if err != nil || len(series.Days) != 1 || series.Days[0].Status != api.SloDailyComplianceDayStatusMet || summary.MetDays != 1 || summary.EvaluableDays != 1 ||
		series.Days[0].Performance.Attainment == nil || !operationalTestDecimalEquals(*series.Days[0].Performance.Attainment, "0.99") || !series.Days[0].RequestedAt.Equal(dayEnd) || !series.Days[0].EffectiveAt.Equal(dayEnd) {
		t.Fatalf("exact-target rolling day = %+v summary=%+v err=%v", series, summary, err)
	}

	facts[0].Good, facts[0].Total = "0", "0"
	base.Observations = facts
	series, summary, err = buildDailyCompliance(context.Background(), base, dayEnd.Add(-24*time.Hour), dayEnd, dayEnd,
		series.Days[0].SloId, "1", "0.99", window, cadence)
	if err != nil || len(series.Days) != 1 || series.Days[0].Status != api.SloDailyComplianceDayStatusUnknown || !series.Days[0].Performance.Provisional || summary.UnknownDays != 1 || summary.EvaluableDays != 0 || series.Days[0].Explanation != "zero_traffic" {
		t.Fatalf("zero-traffic rolling day = %+v summary=%+v err=%v", series, summary, err)
	}
	if status, reason := dailyComplianceStatus(slo.WindowResult{RequestedStart: dayEnd.Add(-window), EligibleStart: dayEnd.Add(-30 * time.Minute), Expected: 30, Performance: "met"}, dayEnd, dayEnd.Add(-24*time.Hour), window, cadence); status != api.SloDailyComplianceDayStatusUnknown || reason != "shortened_window" {
		t.Fatalf("shortened rolling day = %s %s", status, reason)
	}
	if status, reason := dailyComplianceStatus(slo.WindowResult{Performance: "not_applicable"}, dayEnd, dayEnd, window, cadence); status != api.SloDailyComplianceDayStatusNotApplicable || reason != "before_evaluation_validity" {
		t.Fatalf("pre-validity day = %s %s", status, reason)
	}
}

func TestDailyComplianceFloorsNonDivisorCadenceAndExcludesUnfinishedToday(t *testing.T) {
	cadence := 7 * time.Minute
	window := 5 * cadence
	dayEnd := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	now := dayEnd.Add(12*time.Hour + 34*time.Minute)
	base := slo.WindowInput{Now: now, ValidFrom: dayEnd.Add(-7 * 24 * time.Hour), SliValidFrom: dayEnd.Add(-7 * 24 * time.Hour),
		RetentionStart: dayEnd.Add(-400 * 24 * time.Hour), Cadence: cadence, Target: "0.99", FreshnessGrace: time.Minute}
	series, summary, err := buildDailyCompliance(context.Background(), base, dayEnd.Add(-24*time.Hour), now, now,
		api.SloId{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x41, 0x11, 0x81, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}, "1", "0.99", window, cadence)
	wantEffective := time.Date(2026, 10, 27, 23, 58, 0, 0, time.UTC)
	if err != nil || len(series.Days) != 1 || !series.Days[0].DayEnd.Equal(dayEnd) || !series.Days[0].RequestedAt.Equal(dayEnd) ||
		!series.Days[0].EffectiveAt.Equal(wantEffective) || series.Days[0].Status != api.SloDailyComplianceDayStatusUnknown ||
		summary.UnknownDays != 1 || summary.NotApplicableDays != 0 {
		t.Fatalf("non-divisor cadence daily series = %+v summary=%+v err=%v", series, summary, err)
	}
}

func TestAttainmentBucketsExcludePreValidityFacts(t *testing.T) {
	start := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	cadence := time.Minute
	end := start.Add(1504 * cadence)
	base := slo.WindowInput{Now: end, ValidFrom: start.Add(90 * time.Second), SliValidFrom: start, RetentionStart: start.Add(-24 * time.Hour),
		Cadence: cadence, Target: "0.99", FreshnessGrace: 3 * time.Minute, Observations: []slo.Observation{
			{Start: start, End: start.Add(cadence), Received: start.Add(cadence), Outcome: "observed", Good: "0", Total: "100"},
			{Start: start.Add(cadence), End: start.Add(2 * cadence), Received: start.Add(2 * cadence), Outcome: "observed", Good: "0", Total: "100"},
			{Start: start.Add(2 * cadence), End: start.Add(3 * cadence), Received: start.Add(3 * cadence), Outcome: "observed", Good: "100", Total: "100"},
		}}
	samples, _, _, err := buildAttainmentSamples(context.Background(), base, start, end, cadence, "1")
	if err != nil {
		t.Fatal(err)
	}
	first := samples[0]
	if first.State != "partial" || first.Quality.ExpectedIntervals != 2 || first.Quality.ObservedIntervals != 1 || first.Quality.MissingIntervals != 1 || first.Good == nil || *first.Good != "100.000000000000" {
		t.Fatalf("mixed eligibility bucket counted pre-validity measurements: %+v", first)
	}
}

func TestOperationalFactsStartUsesTheMinimumRequiredHorizon(t *testing.T) {
	cadence := time.Minute
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	window := 30 * 24 * time.Hour
	chartStart := now.Add(-7 * 24 * time.Hour)
	start := operationalFactsStart(now, window, 6*time.Hour, chartStart, window, time.Hour,
		now.Add(-400*24*time.Hour), now.Add(-400*24*time.Hour), now.Add(-400*24*time.Hour), cadence)
	want := now.Add(-37 * 24 * time.Hour)
	if !start.Equal(want) {
		t.Fatalf("30 d objective + 7 d chart horizon = %s, want %s", start, want)
	}
	clipped := operationalFactsStart(now, window, 6*time.Hour, chartStart, window, time.Hour,
		now.Add(-10*24*time.Hour), now.Add(-400*24*time.Hour), now.Add(-400*24*time.Hour), cadence)
	if !clipped.Equal(now.Add(-10 * 24 * time.Hour)) {
		t.Fatalf("horizon before objective validity was not clipped: %s", clipped)
	}
}

func TestShortenedAndPartialOneHourInputsQualifyUrgencyAndExhaustion(t *testing.T) {
	cadence := time.Minute
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	retention := now.Add(-400 * 24 * time.Hour)
	facts := make([]slo.Observation, 60)
	for index := range facts {
		start := now.Add(-time.Duration(60-index) * cadence)
		good, total := "100", "100"
		if index == 10 {
			good = "0"
		}
		if index == 15 {
			continue
		}
		facts[index] = slo.Observation{Start: start, End: start.Add(cadence), Received: start.Add(cadence), Outcome: "observed", Good: good, Total: total}
	}
	filtered := make([]slo.Observation, 0, len(facts)-1)
	for _, fact := range facts {
		if !fact.Start.IsZero() {
			filtered = append(filtered, fact)
		}
	}
	base := slo.WindowInput{Now: now, ValidFrom: now.Add(-time.Hour), SliValidFrom: now.Add(-time.Hour), RetentionStart: retention,
		Cadence: cadence, Target: "0.99", FreshnessGrace: 3 * time.Minute, Observations: filtered}
	current, err := evaluateOperationalAt(context.Background(), base, now, 30*time.Minute)
	if err != nil || current.State != "complete" || current.Provisional {
		t.Fatalf("30-minute objective window = %+v, %v", current, err)
	}
	oneHour, err := evaluateOperationalAt(context.Background(), base, now, time.Hour)
	if err != nil || oneHour.State != "partial" || !oneHour.Provisional || oneHour.Missing != 1 {
		t.Fatalf("independent 1 h window = %+v, %v", oneHour, err)
	}
	currentValue, burn := operationalValue(current), burnValue(oneHour, 3600)
	urgency := slo.ClassifyOperational(slo.OperationalState{QualityState: current.State, Freshness: current.Freshness, PerformanceState: current.Performance,
		RemainingFraction: current.RemainingFraction, RemainingFractionExact: current.ExactRemainingFraction, Burn1h: burn.Performance.Burn,
		Burn1hExact: exactBurn(&oneHour), Burn1hAvailable: true, Burn1hQualityState: string(burn.Quality.State), Burn1hFreshness: string(burn.Quality.Freshness), Burn1hProvisional: burn.Performance.Provisional})
	if urgency.Group != "needs_attention" || !slices.ContainsFunc(urgency.Reasons, func(reason slo.OperationalReason) bool { return reason.Code == "high_1h_burn" && reason.Provisional }) {
		t.Fatalf("partial high 1 h burn urgency = %+v", urgency)
	}
	if currentValue.Performance.RemainingBudget == nil || !operationalTestDecimalEquals(*currentValue.Performance.RemainingBudget, "30") {
		t.Fatalf("current remaining events = %v, want 30", currentValue.Performance.RemainingBudget)
	}

	shortFacts := make([]slo.Observation, 30)
	for index := range shortFacts {
		start := now.Add(-time.Duration(30-index) * cadence)
		good, total := "0", "0"
		if index == 0 {
			good, total = "9990", "10000"
		}
		shortFacts[index] = slo.Observation{Start: start, End: start.Add(cadence), Received: start.Add(cadence), Outcome: "observed", Good: good, Total: total}
	}
	shortBase := slo.WindowInput{Now: now, ValidFrom: now.Add(-30 * time.Minute), SliValidFrom: now.Add(-time.Hour), RetentionStart: retention,
		Cadence: cadence, Target: "0.99", FreshnessGrace: 3 * time.Minute, Observations: shortFacts}
	shortCurrent, err := evaluateOperationalAt(context.Background(), shortBase, now, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	shortHour, err := evaluateOperationalAt(context.Background(), shortBase, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	shortBurn := burnValue(shortHour, 3600)
	if shortCurrent.ExactRemaining == nil || shortCurrent.Remaining == nil || !operationalTestDecimalEquals(*shortCurrent.Remaining, "90") || !shortHour.EligibleStart.After(shortHour.EffectiveStart) || !shortBurn.Performance.Provisional || shortBurn.Quality.Reason == nil {
		t.Fatalf("shortened objective/burn inputs = current:%+v burn:%+v", shortCurrent, shortBurn)
	}
	finite := exhaustionValue(shortCurrent, shortBurn, &shortHour)
	if finite.State != "unavailable" || finite.BadEventsPerHour != nil {
		t.Fatalf("shortened one-hour eligibility produced finite exhaustion: %+v", finite)
	}
}

func operationalTestDecimalEquals(actual, expected string) bool {
	actualRat, actualOK := new(big.Rat).SetString(actual)
	expectedRat, expectedOK := new(big.Rat).SetString(expected)
	return actualOK && expectedOK && actualRat.Cmp(expectedRat) == 0
}

func TestExhaustionUsesExactRemainingSignWhenDisplayRoundsToZero(t *testing.T) {
	current := slo.WindowResult{State: "complete", Freshness: "fresh", Remaining: stringPointer("0.000000000000"), ExactRemaining: big.NewRat(1, 1_000_000_000_000)}
	bad := "1.000000000000"
	quality := api.SloOperationalQuality{State: "complete", Freshness: "fresh"}
	burn := api.SloBurnValue{Quality: quality, Performance: api.SloEvaluationPerformance{Bad: &bad}}
	oneHour := &slo.WindowResult{State: "complete", Freshness: "fresh", ExactBad: big.NewRat(1, 1)}
	got := exhaustionValue(current, burn, oneHour)
	if got.State != "estimated" || got.HoursUntilExhaustion == nil {
		t.Fatalf("tiny positive exact budget was marked exhausted: %+v", got)
	}
	current.ExactRemaining = big.NewRat(-1, 1)
	got = exhaustionValue(current, burn, oneHour)
	if got.State != "exhausted" {
		t.Fatalf("negative exact budget = %+v", got)
	}
}
