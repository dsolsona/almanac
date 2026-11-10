package storage

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/google/uuid"
)

// Raw sweeps remain test oracles for retained values; production reads use maintained series.
type canonicalSampleBucket struct {
	start, end                        time.Time
	byTime                            map[int64]canonicalSampleCandidate
	firstKey, lastKey, minKey, maxKey int64
	hasFirst, hasLast, hasMin, hasMax bool
	minimum, maximum                  *big.Rat
	critical                          map[int64]bool
}

type canonicalSampleCandidate struct {
	value slo.WindowResult
}

func buildCanonicalDailyCompliance(ctx context.Context, base slo.WindowInput, rangeStart, rangeEnd, effectiveAt time.Time,
	sloID uuid.UUID, definitionRevision, target string, window, cadence time.Duration,
) (api.SloDailyComplianceSeries, api.SloDailyComplianceSummary, error) {
	series := api.SloDailyComplianceSeries{Kind: api.SloDailyComplianceSeriesKind("daily_compliance"), Days: []api.SloDailyComplianceDay{}}
	summary := api.SloDailyComplianceSummary{}
	visibleEnd := rangeEnd.UTC()
	completedThrough := utcDayStart(effectiveAt)
	if visibleEnd.After(completedThrough) {
		visibleEnd = completedThrough
	}
	dayEnd := utcDayStart(rangeStart).Add(24 * time.Hour)
	for dayEnd.Before(visibleEnd) || dayEnd.Equal(visibleEnd) {
		if err := ctx.Err(); err != nil {
			return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, err
		}
		if len(series.Days) == 365 {
			return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, ErrOperationalRangeDense
		}
		series.Days = append(series.Days, api.SloDailyComplianceDay{})
		dayEnd = dayEnd.Add(24 * time.Hour)
	}
	if len(series.Days) == 0 {
		return series, summary, nil
	}
	base.Duration = window
	sweep, err := slo.NewWindowSweepContext(ctx, base)
	if err != nil {
		return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, err
	}
	firstDayEnd := utcDayStart(rangeStart).Add(24 * time.Hour)
	for index := range series.Days {
		if index&31 == 0 {
			if err := ctx.Err(); err != nil {
				return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, err
			}
		}
		end := firstDayEnd.Add(time.Duration(index) * 24 * time.Hour)
		evaluationAt := floorSliBoundary(end, cadence)
		result, evalErr := sweep.AtContext(ctx, evaluationAt)
		if evalErr != nil {
			return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, evalErr
		}
		series.Days[index] = operationalComplianceDay(result, end, base.ValidFrom, sloID, definitionRevision, target, window, cadence)
		switch series.Days[index].Status {
		case api.SloDailyComplianceDayStatusMet:
			summary.MetDays++
		case api.SloDailyComplianceDayStatusViolated:
			summary.ViolatedDays++
		case api.SloDailyComplianceDayStatusUnknown:
			summary.UnknownDays++
		case api.SloDailyComplianceDayStatusNotApplicable:
			summary.NotApplicableDays++
		}
	}
	summary.EvaluableDays = summary.MetDays + summary.ViolatedDays
	return series, summary, nil
}

func clipCanonicalFacts(facts []slo.Observation, start time.Time) []slo.Observation {
	index := sort.Search(len(facts), func(index int) bool { return !facts[index].Start.Before(start) })
	return facts[index:]
}

func buildCanonicalAttainmentSamples(ctx context.Context, base slo.WindowInput, start, end time.Time, cadence time.Duration, revision string, explicitBounds ...[]time.Time) ([]api.SloOperationalSample, time.Duration, bool, error) {
	ticks := int(end.Sub(start) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}, cadence, false, nil
	}
	buckets := sampleBucketLimit(start, end, cadence)
	if len(explicitBounds) == 1 {
		buckets = len(explicitBounds[0]) - 1
	}
	resolutionTicks := (ticks + buckets - 1) / buckets
	resolution := time.Duration(resolutionTicks) * cadence
	samples := make([]api.SloOperationalSample, 0, buckets)
	factIndex := 0
	boundaries := bucketBoundaries(start, end, ticks, buckets, cadence)
	if len(explicitBounds) == 1 {
		boundaries = explicitBounds[0]
	}
	for bucket := range buckets {
		if bucket&127 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, false, err
			}
		}
		bucketStart, bucketEnd := boundaries[bucket], boundaries[bucket+1]
		var good, total big.Rat
		var observed, empty, zeros int64
		var first, last, receipt *time.Time
		for factIndex < len(base.Observations) && !base.Observations[factIndex].End.After(bucketStart) {
			factIndex++
		}
		index := factIndex
		for index < len(base.Observations) && !base.Observations[index].End.After(bucketEnd) {
			if index&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, 0, false, err
				}
			}
			fact := base.Observations[index]
			eligibleStart := ceilSliBoundary(maxTime(bucketStart, base.ValidFrom, base.SliValidFrom), cadence)
			retainedStart := maxTime(eligibleStart, ceilSliBoundary(base.Now.Add(-sli.ObservationRetention), cadence))
			if fact.Start.Before(bucketEnd) && !fact.Start.Before(retainedStart) && !fact.End.After(bucketEnd) {
				switch fact.Outcome {
				case "observed":
					g, ok := new(big.Rat).SetString(fact.Good)
					if !ok {
						return nil, 0, false, ErrOperationalRange
					}
					t, ok := new(big.Rat).SetString(fact.Total)
					if !ok || g.Cmp(t) > 0 {
						return nil, 0, false, ErrOperationalRange
					}
					good.Add(&good, g)
					total.Add(&total, t)
					observed++
					if t.Sign() == 0 {
						zeros++
					}
					startAt, endAt, receivedAt := fact.Start.UTC(), fact.End.UTC(), fact.Received.UTC()
					if first == nil {
						first = &startAt
					}
					last = &endAt
					if receipt == nil || receivedAt.After(*receipt) {
						receipt = &receivedAt
					}
				case "complete_empty":
					empty++
				}
			}
			index++
		}
		factIndex = index
		eligibleStart := ceilSliBoundary(maxTime(bucketStart, base.ValidFrom, base.SliValidFrom), cadence)
		if eligibleStart.After(bucketEnd) {
			eligibleStart = bucketEnd
		}
		expected := int64(bucketEnd.Sub(eligibleStart) / cadence)
		retainedStart := maxTime(eligibleStart, ceilSliBoundary(base.Now.Add(-sli.ObservationRetention), cadence))
		if retainedStart.After(bucketEnd) {
			retainedStart = bucketEnd
		}
		expired := int64(retainedStart.Sub(eligibleStart) / cadence)
		missing := expected - expired - observed - empty
		if missing < 0 {
			return nil, 0, false, errors.New("storage: inconsistent attainment bucket coverage")
		}
		qualityState := "complete"
		switch {
		case expected == 0:
			qualityState = "not_applicable"
		case expired > 0:
			qualityState = "expired_history"
		case observed+empty == 0:
			qualityState = "no_data"
		case missing > 0:
			qualityState = "partial"
		case observed > 0 && observed == zeros && empty == 0:
			qualityState = "zero_traffic"
		}
		freshness := "unknown"
		if last != nil {
			freshness = "fresh"
			if bucketEnd.After(last.Add(base.FreshnessGrace)) {
				freshness = "stale"
			}
		}
		coverage := (*string)(nil)
		if expected > 0 {
			value := slo.FormatFraction(observed+empty, expected)
			coverage = &value
		}
		quality := api.SloOperationalQuality{State: api.SloOperationalQualityState(qualityState), Freshness: api.SloOperationalQualityFreshness(freshness),
			ExpectedIntervals: expected, ObservedIntervals: observed, KnownEmptyIntervals: empty, MissingIntervals: missing, ExpiredIntervals: expired, ZeroTrafficIntervals: zeros,
			Coverage: coverage, FirstObservationAt: first, LastObservationAt: last, LatestReceiptAt: receipt}
		var attainment, goodValue, totalValue *string
		state := api.SloOperationalSampleState("observed")
		if observed > 0 {
			g := slo.FormatRational(&good)
			goodValue = &g
			t := slo.FormatRational(&total)
			totalValue = &t
			if total.Sign() > 0 {
				a := slo.FormatRational(new(big.Rat).Quo(&good, &total))
				attainment = &a
			}
		}
		switch qualityState {
		case "no_data":
			state = "missing"
		case "zero_traffic":
			state = "zero_traffic"
		case "expired_history":
			state = "expired"
		case "not_applicable":
			state = "not_applicable"
		case "partial":
			state = "partial"
		}
		if observed == 0 && empty > 0 && expired == 0 && missing == 0 {
			state = "known_empty"
		} else if observed > 0 && (empty > 0 || expired > 0 || missing > 0) {
			state = "partial"
		}
		samples = append(samples, api.SloOperationalSample{BucketStart: bucketStart, BucketEnd: bucketEnd, EvaluationAt: bucketEnd, State: state,
			Quality: quality, Revision: revision, Good: goodValue, Total: totalValue, Attainment: attainment, RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}})
	}
	return samples, resolution, ticks > sloOperationalSeriesLimit, nil
}

func canonicalFactsStart(currentAt time.Time, currentWindow, currentBurn time.Duration, chartStart time.Time, chartWindow, chartBurn time.Duration, validFrom, sliValidFrom, retentionStart time.Time, cadence time.Duration) time.Time {
	currentHorizon := currentAt.Add(-max(currentWindow, currentBurn))
	chartHorizon := chartStart.Add(-max(chartWindow, chartBurn))
	horizon := currentHorizon
	if chartHorizon.Before(horizon) {
		horizon = chartHorizon
	}
	return ceilSliBoundary(maxTime(horizon, validFrom, sliValidFrom, retentionStart), cadence)
}

func buildCanonicalSamples(ctx context.Context, base slo.WindowInput, start, end time.Time, duration, cadence time.Duration, revision, metric string, maxBuckets int) ([]api.SloOperationalSample, error) {
	ticks := int(end.Sub(start) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}, nil
	}
	buckets := min(ticks, maxBuckets)
	if buckets <= 0 {
		return nil, ErrOperationalRangeDense
	}
	sampleLimit := min(sloOperationalSeriesLimit, buckets*4)
	boundaries := bucketBoundaries(start, end, ticks, buckets, cadence)
	return buildCanonicalRollingBuckets(ctx, base, boundaries, duration, cadence, revision, metric, true, sampleLimit)
}

func buildCanonicalRollingBuckets(ctx context.Context, base slo.WindowInput, boundaries []time.Time, duration, cadence time.Duration, revision, metric string, includeStart bool, sampleLimit int) ([]api.SloOperationalSample, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.rolling.buckets")
	defer span.End()
	buckets := len(boundaries) - 1
	if buckets <= 0 {
		return []api.SloOperationalSample{}, nil
	}
	start, end := boundaries[0], boundaries[len(boundaries)-1]
	base.Observations = clipCanonicalFacts(base.Observations, maxTime(start.Add(-duration), base.RetentionStart))
	events := make([]int64, 0, 2*len(base.Observations)+len(boundaries)+4)
	for _, boundary := range boundaries {
		events = append(events, boundary.UnixMicro())
	}
	nextObservedIndex := 0
	for factIndex, fact := range base.Observations {
		if factIndex&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for _, point := range []time.Time{fact.End, fact.End.Add(duration)} {
			if point.After(start) && !point.After(end) {
				events = append(events, point.UnixMicro())
			}
		}
		if factIndex > 0 && base.Observations[factIndex-1].End.Before(fact.Start) {
			gapEntry := ceilSliBoundary(base.Observations[factIndex-1].End.Add(cadence), cadence)
			if gapEntry.After(start) && !gapEntry.After(end) {
				events = append(events, gapEntry.UnixMicro())
			}
			gapExit := fact.Start.Add(duration)
			if gapExit.After(start) && !gapExit.After(end) {
				events = append(events, gapExit.UnixMicro())
			}
		}
		if fact.Outcome == "observed" {
			if nextObservedIndex <= factIndex {
				nextObservedIndex = factIndex + 1
			}
			for nextObservedIndex < len(base.Observations) && base.Observations[nextObservedIndex].Outcome != "observed" {
				nextObservedIndex++
			}
			freshnessExpiry := fact.End.Add(base.FreshnessGrace)
			if nextObservedIndex == len(base.Observations) || base.Observations[nextObservedIndex].End.After(freshnessExpiry) {
				freshnessChange := ceilSliBoundary(freshnessExpiry, cadence)
				if !freshnessChange.After(freshnessExpiry) {
					freshnessChange = freshnessChange.Add(cadence)
				}
				if freshnessChange.After(start) && !freshnessChange.After(end) {
					events = append(events, freshnessChange.UnixMicro())
				}
			}
		}
	}
	validityPoints := []time.Time{ceilSliBoundary(base.ValidFrom.Add(duration), cadence), ceilSliBoundary(base.SliValidFrom.Add(duration), cadence), ceilSliBoundary(base.RetentionStart.Add(duration), cadence), ceilSliBoundary(base.RetentionStart, cadence)}
	for _, point := range validityPoints {
		if point.After(start) && !point.After(end) && floorSliBoundary(point, cadence).Equal(point) {
			events = append(events, point.UnixMicro())
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.Sort(events)
	events = slices.Compact(events)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base.Duration = duration
	sweep, err := slo.NewWindowSweepContext(ctx, base)
	if err != nil {
		return nil, err
	}
	collected := make([]canonicalSampleBucket, buckets)
	for index := range collected {
		collected[index] = canonicalSampleBucket{start: boundaries[index], end: boundaries[index+1], byTime: make(map[int64]canonicalSampleCandidate, 4), critical: make(map[int64]bool)}
	}
	previousState := ""
	retainedCount := 0
	for eventIndex, eventMicros := range events {
		if eventIndex&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		at := time.UnixMicro(eventMicros).UTC()
		if at.Before(start) || at.After(end) {
			continue
		}
		bucketIndex := sort.Search(len(collected), func(index int) bool { return !at.After(collected[index].end) })
		if bucketIndex == len(collected) {
			bucketIndex--
		}
		result, evalErr := sweep.AtSummaryContext(ctx, at)
		if evalErr != nil {
			return nil, evalErr
		}
		sample := operationalSample(result, collected[bucketIndex].start, collected[bucketIndex].end, revision)
		key := at.UnixMicro()
		bucket := &collected[bucketIndex]
		stateKey := string(sample.State) + ":" + string(sample.Quality.State) + ":" + string(sample.Quality.Freshness)
		if !includeStart && at.Equal(start) {
			previousState = stateKey
			continue
		}
		if previousState != "" && stateKey != previousState {
			bucket.critical[key] = true
		}
		previousState = stateKey
		if !bucket.hasFirst {
			bucket.firstKey, bucket.hasFirst = key, true
		}
		bucket.lastKey, bucket.hasLast = key, true
		if value := canonicalSampleMetric(result, metric); value != nil {
			current := new(big.Rat).Set(value)
			if !bucket.hasMin || current.Cmp(bucket.minimum) < 0 {
				bucket.minKey, bucket.minimum, bucket.hasMin = key, current, true
			}
			if !bucket.hasMax || current.Cmp(bucket.maximum) > 0 {
				bucket.maxKey, bucket.maximum, bucket.hasMax = key, current, true
			}
		}
		keys := make(map[int64]bool, 4+len(bucket.critical))
		for _, candidateKey := range []int64{bucket.firstKey, bucket.lastKey, bucket.minKey, bucket.maxKey} {
			if bucket.hasFirst && candidateKey == bucket.firstKey || bucket.hasLast && candidateKey == bucket.lastKey || bucket.hasMin && candidateKey == bucket.minKey || bucket.hasMax && candidateKey == bucket.maxKey {
				keys[candidateKey] = true
			}
		}
		for criticalKey := range bucket.critical {
			keys[criticalKey] = true
		}
		newRetainedCount := retainedCount - len(bucket.byTime) + len(keys)
		if newRetainedCount > sampleLimit {
			return nil, ErrOperationalRangeDense
		}
		retainedCount = newRetainedCount
		for existingKey := range bucket.byTime {
			if !keys[existingKey] {
				delete(bucket.byTime, existingKey)
			}
		}
		candidate := canonicalSampleCandidate{value: result}
		if keys[key] {
			bucket.byTime[key] = candidate
		}
		for candidateKey := range keys {
			if _, exists := bucket.byTime[candidateKey]; !exists {
				bucket.byTime[candidateKey] = canonicalSampleCandidate{value: canonicalSampleAtKey(bucket, candidateKey, key, result)}
			}
		}
	}
	result := make([]api.SloOperationalSample, 0, sampleLimit)
	for bucketIndex, bucket := range collected {
		if bucketIndex&127 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		keys := make([]int64, 0, len(bucket.byTime))
		for key := range bucket.byTime {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			candidate := bucket.byTime[key]
			labels := make([]api.SloOperationalSampleRetainedExtrema, 0, 4)
			if bucket.hasFirst && key == bucket.firstKey {
				labels = append(labels, "first")
			}
			if bucket.hasLast && key == bucket.lastKey {
				labels = append(labels, "last")
			}
			if bucket.hasMin && key == bucket.minKey {
				labels = append(labels, "minimum")
			}
			if bucket.hasMax && key == bucket.maxKey {
				labels = append(labels, "maximum")
			}
			value := operationalSample(candidate.value.WithValues(), bucket.start, bucket.end, revision)
			value.RetainedExtrema = labels
			result = append(result, value)
		}
	}
	if len(result) > sampleLimit {
		return nil, ErrOperationalRangeDense
	}
	return result, nil
}

func canonicalSampleAtKey(bucket *canonicalSampleBucket, wanted, current int64, currentSample slo.WindowResult) slo.WindowResult {
	if wanted == current {
		return currentSample
	}
	if existing, ok := bucket.byTime[wanted]; ok {
		return existing.value
	}
	return slo.WindowResult{End: time.UnixMicro(wanted).UTC(), State: "unavailable", Freshness: "unknown"}
}

func canonicalSampleMetric(result slo.WindowResult, metric string) *big.Rat {
	if metric == "burn" {
		return result.ExactBurn
	}
	if metric == "remaining_fraction" {
		return result.ExactRemainingFraction
	}
	return result.ExactRemaining
}

func buildCanonicalOperationalDetail(ctx context.Context, facts []slo.Observation, row dbgen.GetSloRow, contextRow dbgen.GetSloOperationalContextRow, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	now := normalizedAgentNow(input.Now)
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	bound, err := resolveOperationalBounds(input, cadence, row.RollingWindowSeconds)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	requestedAt, effectiveAt, rangeStart, rangeEnd := bound.requestedAt, bound.effectiveAt, bound.start, bound.end
	window := time.Duration(row.RollingWindowSeconds) * time.Second
	base := slo.WindowInput{Now: now, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: now.Add(-sli.ObservationRetention), Cadence: cadence,
		FreshnessGrace: cadence + time.Duration(contextRow.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds)),
		Target:         sli.NormalizeDecimal(row.Target), Observations: facts}
	rolling, attainment, daily := buildCanonicalSamples, buildCanonicalAttainmentSamples, buildCanonicalDailyCompliance
	evaluate := func(duration time.Duration) (slo.WindowResult, error) {
		return evaluateOperationalAt(ctx, base, effectiveAt, duration)
	}
	currentResult, err := evaluate(window)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	burn1h := unavailableBurn(3600, effectiveAt, "cadence_mismatch")
	var burn1hResult *slo.WindowResult
	if time.Hour%cadence == 0 {
		result, evalErr := evaluate(time.Hour)
		if evalErr != nil {
			return api.SloOperationalDetail{}, evalErr
		}
		burn1h = burnValue(result, 3600)
		burn1hResult = &result
	}
	burn6h := unavailableBurn(21600, effectiveAt, "cadence_mismatch")
	if 6*time.Hour%cadence == 0 {
		result, evalErr := evaluate(6 * time.Hour)
		if evalErr != nil {
			return api.SloOperationalDetail{}, evalErr
		}
		burn6h = burnValue(result, 21600)
	}
	current := operationalValue(currentResult)
	current.Window.RequestedStart = requestedAt.Add(-time.Duration(row.RollingWindowSeconds) * time.Second)
	applyOperationalDefinitionWindow(&current, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
	urgency := slo.ClassifyOperational(slo.OperationalState{QualityState: currentResult.State, Freshness: currentResult.Freshness, PerformanceState: currentResult.Performance,
		Provisional: currentResult.Provisional, ShortenedEligibility: currentResult.EligibleStart.After(currentResult.EffectiveStart), RemainingBudget: currentResult.Remaining,
		RemainingFraction: currentResult.RemainingFraction, Burn1h: burn1h.Performance.Burn, Burn1hAvailable: burn1h.Performance.Burn != nil,
		Burn1hQualityState: string(burn1h.Quality.State), Burn1hFreshness: string(burn1h.Quality.Freshness), Burn1hProvisional: burn1h.Performance.Provisional,
		Burn1hShortenedEligibility: burn1hResult != nil && burn1hResult.EligibleStart.After(burn1hResult.EffectiveStart),
		RemainingExact:             currentResult.ExactRemaining, RemainingFractionExact: currentResult.ExactRemainingFraction,
		Burn1hExact: exactBurn(burn1hResult)})
	reasons, leading := operationalReasons(urgency.Reasons)
	if input.currentOnly {
		return api.SloOperationalDetail{Objective: sloRecordDTO(sloRecord(row)), RequestedAt: requestedAt, EffectiveAt: effectiveAt, CalculationVersion: operationalCalculationVersion,
			Current: current, Urgency: api.SloOperationalDetailUrgency(urgency.Group), LeadingReason: leading, Reasons: reasons,
			Burn1h: burn1h, Burn6h: burn6h, Exhaustion: exhaustionValue(currentResult, burn1h, burn1hResult)}, nil
	}
	budgetSamples, err := rolling(ctx, base, rangeStart, rangeEnd, time.Duration(row.RollingWindowSeconds)*time.Second, cadence, strconv.FormatInt(row.DefinitionRevision, 10), "remaining_fraction", sampleBucketLimit(rangeStart, rangeEnd, cadence))
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	selectedBurn := time.Duration(input.BurnWindow) * time.Second
	var burnSamples []api.SloOperationalSample
	if selectedBurn%cadence != 0 {
		burnSamples = unavailableOperationalSamples(rangeStart, rangeEnd, cadence, strconv.FormatInt(row.DefinitionRevision, 10), sampleBucketLimit(rangeStart, rangeEnd, cadence), "cadence_mismatch")
	} else {
		burnSamples, err = rolling(ctx, base, rangeStart, rangeEnd, selectedBurn, cadence, strconv.FormatInt(row.DefinitionRevision, 10), "burn", sampleBucketLimit(rangeStart, rangeEnd, cadence))
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
	}
	attainmentSamples, resolution, reduced, err := attainment(ctx, base, rangeStart, rangeEnd, cadence, strconv.FormatInt(row.DefinitionRevision, 10))
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	complianceSeries, complianceSummary, err := daily(ctx, base, rangeStart, rangeEnd, effectiveAt,
		uuid.UUID(row.ID.Bytes), strconv.FormatInt(row.DefinitionRevision, 10), sli.NormalizeDecimal(row.Target), window, cadence)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	evaluationID, err := uuid.NewRandom()
	if err != nil {
		return api.SloOperationalDetail{}, errors.New("storage: operational evaluation identifier unavailable")
	}
	result := api.SloOperationalDetail{RequestedAt: requestedAt, EffectiveAt: effectiveAt, EvaluationId: evaluationID,
		CalculationVersion: operationalCalculationVersion, SnapshotKind: "current_operational_read", Objective: sloRecordDTO(sloRecord(row)),
		Context: api.SloOperationalContext{SliId: uuid.UUID(contextRow.SliID.Bytes), SliName: contextRow.SliName, SliDisplayName: contextRow.SliDisplayName,
			DatasourceId: uuid.UUID(contextRow.DatasourceID.Bytes), DatasourceDisplayName: contextRow.DatasourceDisplayName,
			MeasurementRevision: strconv.FormatInt(contextRow.MeasurementRevision, 10), PollingIntervalSeconds: int(contextRow.PollingIntervalSeconds),
			SettlingDelaySeconds: int(contextRow.SettlingDelaySeconds), GoodQuery: contextRow.GoodQuery, TotalQuery: contextRow.TotalQuery},
		Current: current, Urgency: api.SloOperationalDetailUrgency(urgency.Group), LeadingReason: leading, Reasons: reasons,
		Burn1h: burn1h, Burn6h: burn6h, Exhaustion: exhaustionValue(currentResult, burn1h, burn1hResult),
		Range: api.SloOperationalRange{RequestedStart: rangeStart, RequestedEnd: rangeEnd, EffectiveStart: rangeStart, EffectiveEnd: rangeEnd,
			SourceCadenceSeconds: int(row.CadenceSeconds), ResolutionSeconds: int(resolution / time.Second), Reduced: reduced,
			MaxSamplesPerSeries: 1500, BucketBounds: "half_open"},
		AttainmentSeries: api.SloOperationalSeries{Kind: "attainment", Samples: attainmentSamples},
		BudgetSeries:     api.SloOperationalSeries{Kind: "rolling_budget", Samples: budgetSamples},
		BurnSeries:       api.SloOperationalSeries{Kind: "burn", BurnWindowSeconds: new(api.SloOperationalSeriesBurnWindowSeconds(input.BurnWindow)), Samples: burnSamples},
		ComplianceSeries: complianceSeries, ComplianceSummary: complianceSummary}
	return result, nil
}
