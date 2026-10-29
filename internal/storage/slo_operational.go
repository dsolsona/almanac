package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	sloOverviewTTL                 = 2 * time.Minute
	sloOverviewSessionLimit        = 4
	sloOverviewInstallationLimit   = 64
	sloOverviewCleanupBatch        = 100
	sloOperationalFactsLimit       = 2_500_000
	sloOverviewDefinitionLimit     = 10_000
	sloOverviewProjectionByteLimit = 16 << 20
	sloOperationalSeriesLimit      = 1500
	sloOperationalReadAttempts     = 3
)

var (
	ErrOperationalRange       = errors.New("storage: invalid operational range")
	ErrOperationalRangeDense  = errors.New("storage: operational range too dense")
	ErrOperationalResource    = errors.New("storage: operational resource limit")
	ErrOperationalSnapshot    = errors.New("storage: invalid operational snapshot cursor")
	ErrOperationalSnapshotOld = errors.New("storage: operational snapshot expired")
)

func validOperationalQuery(query string) bool {
	return utf8.ValidString(query) && utf8.RuneCountInString(query) <= 128
}

func addProjectionPayloadBytes(total, added int) (int, error) {
	if added < 0 || total < 0 || total > sloOverviewProjectionByteLimit-added {
		return total, ErrOperationalResource
	}
	return total + added, nil
}

func retryOperationalRead(ctx context.Context, attempt func() error) error {
	for range sloOperationalReadAttempts {
		if err := ctx.Err(); err != nil {
			return operationError(ctx, "storage: operational read cancelled", err)
		}
		err := attempt()
		if !errors.Is(err, errReadSerialization) {
			return err
		}
	}
	return errReadSerialization
}

type OperationalOverviewInput struct {
	Proof            DatasourceProof
	Query, ServiceID string
	Urgency, Cursor  string
	snapshotID       string
	Limit            int
	Now              func() time.Time
}

type OperationalDetailInput struct {
	Proof          DatasourceProof
	ID             string
	At, Start, End *time.Time
	BurnWindow     int
	Now            func() time.Time
}

// ReadSloOperationalDetail computes summary windows and reduced history in one
// authorized repeatable-read snapshot.
func ReadSloOperationalDetail(ctx context.Context, databaseURL string, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	var result api.SloOperationalDetail
	err := retryOperationalRead(ctx, func() error {
		var attemptErr error
		result, attemptErr = readSloOperationalDetailAttempt(ctx, databaseURL, input)
		return attemptErr
	})
	return result, err
}

func readSloOperationalDetailAttempt(ctx context.Context, databaseURL string, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || !canonicalUUIDCheck(input.ID) {
		return api.SloOperationalDetail{}, ErrObjectiveInvalid
	}
	if input.BurnWindow == 0 {
		input.BurnWindow = 3600
	}
	if input.BurnWindow != 3600 && input.BurnWindow != 21600 || (input.Start == nil) != (input.End == nil) {
		return api.SloOperationalDetail{}, ErrOperationalRange
	}
	id, _ := canonicalUUID(input.ID)
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := q.GetSlo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalDetail{}, ErrObjectiveNotFound
	}
	if err != nil {
		return api.SloOperationalDetail{}, readTransactionError(ctx, "storage: operational SLO definition read failed", err)
	}
	contextRow, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return api.SloOperationalDetail{}, readTransactionError(ctx, "storage: operational SLI context read failed", err)
	}
	if contextRow.PollingIntervalSeconds != row.CadenceSeconds || contextRow.MeasurementRevision != row.MeasurementRevision {
		return api.SloOperationalDetail{}, ErrObjectiveInvalid
	}
	now := normalizedAgentNow(input.Now)
	requestedAt := now
	if input.At != nil {
		_, offset := input.At.Zone()
		requestedAt = input.At.UTC()
		if offset != 0 || requestedAt.IsZero() || requestedAt.Nanosecond()%1000 != 0 || requestedAt.After(now) {
			return api.SloOperationalDetail{}, ErrObjectiveEvaluationTime
		}
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	effectiveAt := floorSliBoundary(requestedAt, cadence)
	var rangeStart, rangeEnd time.Time
	if input.Start == nil {
		rangeEnd = effectiveAt
		rangeStart = floorSliBoundary(rangeEnd.Add(-7*24*time.Hour), cadence)
	} else {
		_, startOffset := input.Start.Zone()
		_, endOffset := input.End.Zone()
		rangeStart, rangeEnd = input.Start.UTC(), input.End.UTC()
		if startOffset != 0 || endOffset != 0 || rangeStart.IsZero() || rangeEnd.IsZero() || rangeStart.Nanosecond()%1000 != 0 || rangeEnd.Nanosecond()%1000 != 0 ||
			!floorSliBoundary(rangeStart, cadence).Equal(rangeStart) || !floorSliBoundary(rangeEnd, cadence).Equal(rangeEnd) || !rangeEnd.After(rangeStart) || rangeEnd.After(effectiveAt) || rangeEnd.Sub(rangeStart) > 365*24*time.Hour {
			return api.SloOperationalDetail{}, ErrOperationalRange
		}
	}
	if !rangeEnd.After(rangeStart) {
		return api.SloOperationalDetail{}, ErrOperationalRange
	}
	retentionStart := ceilSliBoundary(now.Add(-sli.ObservationRetention), cadence)
	window := time.Duration(row.RollingWindowSeconds) * time.Second
	factStart := operationalFactsStart(effectiveAt, window, 6*time.Hour,
		rangeStart, window, time.Duration(input.BurnWindow)*time.Second, row.EvaluationValidFrom.Time.UTC(), row.SliValidFrom.Time.UTC(), retentionStart, cadence)
	rows, factsCount, err := loadOperationalFacts(ctx, q, operationalStreamKey(row.SliID.String(), row.MeasurementRevision), operationalStream{cadence: row.CadenceSeconds}, factStart, effectiveAt, sloOperationalFactsLimit+1)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	if factsCount > sloOperationalFactsLimit {
		return api.SloOperationalDetail{}, ErrOperationalResource
	}
	base := slo.WindowInput{Now: now, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: now.Add(-sli.ObservationRetention), Cadence: cadence,
		FreshnessGrace: cadence + time.Duration(contextRow.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds)),
		Target:         sli.NormalizeDecimal(row.Target), Observations: rows}
	currentResult, err := evaluateOperationalAt(ctx, base, effectiveAt, time.Duration(row.RollingWindowSeconds)*time.Second)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	burn1h := unavailableBurn(3600, effectiveAt, "cadence_mismatch")
	var burn1hResult *slo.WindowResult
	if time.Hour%cadence == 0 {
		result, evalErr := evaluateOperationalAt(ctx, base, effectiveAt, time.Hour)
		if evalErr != nil {
			return api.SloOperationalDetail{}, evalErr
		}
		burn1h = burnValue(result, 3600)
		burn1hResult = &result
	}
	burn6h := unavailableBurn(21600, effectiveAt, "cadence_mismatch")
	if 6*time.Hour%cadence == 0 {
		result, evalErr := evaluateOperationalAt(ctx, base, effectiveAt, 6*time.Hour)
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
	budgetSamples, err := buildOperationalSamples(ctx, base, rangeStart, rangeEnd, time.Duration(row.RollingWindowSeconds)*time.Second, cadence, strconv.FormatInt(row.DefinitionRevision, 10), "remaining_fraction", sampleBucketLimit(rangeStart, rangeEnd, cadence))
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	selectedBurn := time.Duration(input.BurnWindow) * time.Second
	var burnSamples []api.SloOperationalSample
	if selectedBurn%cadence != 0 {
		burnSamples = unavailableOperationalSamples(rangeStart, rangeEnd, cadence, strconv.FormatInt(row.DefinitionRevision, 10), sampleBucketLimit(rangeStart, rangeEnd, cadence), "cadence_mismatch")
	} else {
		burnSamples, err = buildOperationalSamples(ctx, base, rangeStart, rangeEnd, selectedBurn, cadence, strconv.FormatInt(row.DefinitionRevision, 10), "burn", sampleBucketLimit(rangeStart, rangeEnd, cadence))
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
	}
	attainmentSamples, resolution, reduced, err := buildAttainmentSamples(ctx, base, rangeStart, rangeEnd, cadence, strconv.FormatInt(row.DefinitionRevision, 10))
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	complianceSeries, complianceSummary, err := buildDailyCompliance(ctx, base, rangeStart, rangeEnd, effectiveAt,
		uuid.UUID(row.ID.Bytes), strconv.FormatInt(row.DefinitionRevision, 10), sli.NormalizeDecimal(row.Target), window, cadence)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	evaluationID, err := uuid.NewRandom()
	if err != nil {
		return api.SloOperationalDetail{}, errors.New("storage: operational evaluation identifier unavailable")
	}
	result := api.SloOperationalDetail{RequestedAt: requestedAt, EffectiveAt: effectiveAt, EvaluationId: evaluationID,
		CalculationVersion: "occurrences-operational-v1", SnapshotKind: "current_operational_read", Objective: sloRecordDTO(sloRecord(row)),
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
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloOperationalDetail{}, err
	}
	return result, nil
}

func sampleBucketLimit(start, end time.Time, cadence time.Duration) int {
	ticks := int(end.Sub(start) / cadence)
	if ticks > sloOperationalSeriesLimit {
		return 375
	}
	return max(1, ticks)
}

func buildDailyCompliance(ctx context.Context, base slo.WindowInput, rangeStart, rangeEnd, effectiveAt time.Time,
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
		status, explanation := dailyComplianceStatus(result, evaluationAt, base.ValidFrom, window, cadence)
		quality, performance := operationalQuality(result), operationalPerformance(result)
		if status == api.SloDailyComplianceDayStatusUnknown {
			if performance.ObservedState == nil && (performance.State == "met" || performance.State == "violated") {
				observed := api.SloEvaluationPerformanceObservedState(performance.State)
				performance.ObservedState = &observed
			}
			performance.State = "unknown"
			performance.Provisional = true
			reason := explanation
			performance.Reason, quality.Reason = &reason, &reason
		}
		series.Days[index] = api.SloDailyComplianceDay{UtcDate: openapi_types.Date{Time: end.Add(-24 * time.Hour)},
			DayStart: end.Add(-24 * time.Hour), DayEnd: end, RequestedAt: end, EffectiveAt: result.End, SloId: sloID,
			DefinitionRevision: definitionRevision, Target: target, Quality: quality, Performance: performance,
			Status: status, Explanation: explanation}
		switch status {
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

func utcDayStart(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func dailyComplianceStatus(result slo.WindowResult, dayEnd, validFrom time.Time, window, cadence time.Duration) (api.SloDailyComplianceDayStatus, string) {
	if !dayEnd.After(validFrom) || result.Performance == "not_applicable" && result.Expected == 0 {
		return api.SloDailyComplianceDayStatusNotApplicable, "before_evaluation_validity"
	}
	if result.EligibleStart.After(result.RequestedStart) || result.Expected != int64(window/cadence) {
		return api.SloDailyComplianceDayStatusUnknown, "shortened_window"
	}
	if result.State == "zero_traffic" {
		return api.SloDailyComplianceDayStatusUnknown, "zero_traffic"
	}
	if result.KnownEmpty > 0 {
		return api.SloDailyComplianceDayStatusUnknown, "known_empty"
	}
	if result.Expired > 0 || result.State == "expired_history" {
		return api.SloDailyComplianceDayStatusUnknown, "expired_history"
	}
	if result.Missing > 0 || result.State == "partial" || result.State == "no_data" {
		return api.SloDailyComplianceDayStatusUnknown, "incomplete_measurements"
	}
	if result.State == "unavailable" || result.Provisional {
		if result.Reason != "" {
			return api.SloDailyComplianceDayStatusUnknown, result.Reason
		}
		return api.SloDailyComplianceDayStatusUnknown, "unavailable"
	}
	if result.Performance == "met" {
		return api.SloDailyComplianceDayStatusMet, "target_met"
	}
	if result.Performance == "violated" {
		return api.SloDailyComplianceDayStatusViolated, "target_violated"
	}
	if result.Reason != "" {
		return api.SloDailyComplianceDayStatusUnknown, result.Reason
	}
	return api.SloDailyComplianceDayStatusUnknown, "performance_unknown"
}

func bucketBoundaries(start, end time.Time, ticks, buckets int, cadence time.Duration) []time.Time {
	boundaries := make([]time.Time, buckets+1)
	for index := range boundaries {
		boundaries[index] = start.Add(time.Duration(ticks*index/buckets) * cadence)
	}
	boundaries[len(boundaries)-1] = end
	return boundaries
}

func clipOperationalFacts(facts []slo.Observation, start time.Time) []slo.Observation {
	index := sort.Search(len(facts), func(index int) bool { return !facts[index].Start.Before(start) })
	return facts[index:]
}

func sloRecordDTO(value SloRecord) api.Slo {
	result := api.Slo{Id: uuid.MustParse(value.ID), Name: value.Name, DisplayName: value.DisplayName, Version: strconv.FormatInt(value.Version, 10),
		DefinitionRevision: strconv.FormatInt(value.DefinitionRevision, 10), ServiceId: uuid.MustParse(value.ServiceID), ServiceDisplayName: value.ServiceDisplayName,
		SliId: uuid.MustParse(value.SliID), SliDisplayName: value.SliDisplayName, Target: value.Target, RollingWindowSeconds: value.RollingWindowSeconds,
		CadenceSeconds: int(value.CadenceSeconds), ActivatedAt: value.ActivatedAt, EvaluationValidFrom: value.EvaluationValidFrom,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	if value.HistoricalRequestedBy != nil {
		result.HistoricalRequestedBy = new(uuid.MustParse(*value.HistoricalRequestedBy))
	}
	if value.HistoricalRequestedAt != nil {
		result.HistoricalRequestedAt = value.HistoricalRequestedAt
	}
	return result
}

func buildAttainmentSamples(ctx context.Context, base slo.WindowInput, start, end time.Time, cadence time.Duration, revision string) ([]api.SloOperationalSample, time.Duration, bool, error) {
	ticks := int(end.Sub(start) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}, cadence, false, nil
	}
	buckets := sampleBucketLimit(start, end, cadence)
	resolutionTicks := (ticks + buckets - 1) / buckets
	resolution := time.Duration(resolutionTicks) * cadence
	samples := make([]api.SloOperationalSample, 0, buckets)
	factIndex := 0
	boundaries := bucketBoundaries(start, end, ticks, buckets, cadence)
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
		expected := int64(bucketEnd.Sub(bucketStart) / cadence)
		eligibleStart := ceilSliBoundary(maxTime(bucketStart, base.ValidFrom, base.SliValidFrom), cadence)
		if eligibleStart.After(bucketEnd) {
			eligibleStart = bucketEnd
		}
		expected = int64(bucketEnd.Sub(eligibleStart) / cadence)
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
		if first != nil && last != nil {
			freshness = "fresh"
			if bucketEnd.After(last.Add(base.FreshnessGrace)) {
				freshness = "stale"
			}
			_ = freshness
		}
		samples = append(samples, api.SloOperationalSample{BucketStart: bucketStart, BucketEnd: bucketEnd, EvaluationAt: bucketEnd, State: state,
			Quality: quality, Revision: revision, Good: goodValue, Total: totalValue, Attainment: attainment, RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}})
	}
	return samples, resolution, ticks > sloOperationalSeriesLimit, nil
}

func unavailableOperationalSamples(start, end time.Time, cadence time.Duration, revision string, buckets int, reason string) []api.SloOperationalSample {
	ticks := int(end.Sub(start) / cadence)
	buckets = min(max(1, buckets), ticks)
	boundaries := bucketBoundaries(start, end, ticks, buckets, cadence)
	samples := make([]api.SloOperationalSample, 0, buckets)
	for index := range buckets {
		reasonValue := reason
		samples = append(samples, api.SloOperationalSample{BucketStart: boundaries[index], BucketEnd: boundaries[index+1], EvaluationAt: boundaries[index+1],
			State: "missing", Quality: api.SloOperationalQuality{State: "unavailable", Freshness: "unknown", Reason: &reasonValue}, Revision: revision,
			RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}})
	}
	return samples
}

type operationalDefinition struct {
	row   dbgen.ListOperationalSloDefinitionsRow
	facts []slo.Observation
	ctx   api.SloOperationalContext
}

type overviewCursor struct {
	SnapshotID string `json:"s"`
	Offset     int    `json:"o"`
	MAC        string `json:"m"`
}

type overviewSnapshot struct {
	ID              pgtype.UUID
	ActorID         pgtype.UUID
	SessionVerifier []byte
	Role            string
	QueryText       string
	ServiceID       pgtype.UUID
	Urgency         pgtype.Text
	PageLimit       int32
	GroupCounts     []byte
	State           string
	ExpiresAt       pgtype.Timestamptz
}

// ReadSloOperationalOverview returns a fixed-expiry, actor/session-bound page.
func ReadSloOperationalOverview(ctx context.Context, databaseURL string, input OperationalOverviewInput) (api.SloOperationalOverviewPage, error) {
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || input.Limit < 0 || input.Limit > 200 || !validOperationalQuery(input.Query) || len(input.Cursor) > 512 {
		return api.SloOperationalOverviewPage{}, ErrOperationalRange
	}
	if input.Limit == 0 {
		input.Limit = 50
	}
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	if input.Urgency != "" && input.Urgency != "needs_attention" && input.Urgency != "watch" && input.Urgency != "healthy" {
		return api.SloOperationalOverviewPage{}, ErrOperationalRange
	}
	if input.ServiceID != "" {
		if _, ok := canonicalUUID(input.ServiceID); !ok {
			return api.SloOperationalOverviewPage{}, ErrOperationalRange
		}
	}
	if input.Cursor == "" && input.snapshotID == "" {
		var id uuid.UUID
		var role string
		var err error
		err = retryOperationalRead(ctx, func() error {
			id, role, err = reserveSloOverviewSnapshot(ctx, databaseURL, input)
			return err
		})
		if err != nil {
			return api.SloOperationalOverviewPage{}, err
		}
		err = retryOperationalRead(ctx, func() error {
			return materializeSloOverviewSnapshot(ctx, databaseURL, input, id, role)
		})
		if err != nil {
			cleanupErr := deleteSloOverviewSnapshot(ctx, databaseURL, input, id)
			return api.SloOperationalOverviewPage{}, errors.Join(err, cleanupErr)
		}
		input.snapshotID = id.String()
	}
	var result api.SloOperationalOverviewPage
	err := retryOperationalRead(ctx, func() error {
		var attemptErr error
		result, attemptErr = readSloOverviewPage(ctx, databaseURL, input)
		return attemptErr
	})
	return result, err
}

func reserveSloOverviewSnapshot(ctx context.Context, databaseURL string, input OperationalOverviewInput) (uuid.UUID, string, error) {
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{}, "administrator", "editor", "viewer")
	if err != nil {
		return uuid.Nil, "", err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	var acquired bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended('slo_overview_snapshot_admission', 0))").Scan(&acquired); err != nil {
		return uuid.Nil, "", readTransactionError(ctx, "storage: overview admission lock failed", err)
	}
	if !acquired {
		return uuid.Nil, "", ErrOperationalResource
	}
	if err = q.DeleteExpiredSloOverviewSnapshots(ctx, dbgen.DeleteExpiredSloOverviewSnapshotsParams{ExpiresAt: timestamp(now), Limit: sloOverviewCleanupBatch}); err != nil {
		return uuid.Nil, "", readTransactionError(ctx, "storage: overview snapshot cleanup failed", err)
	}
	sessionCount, err := q.CountLiveSloOverviewSnapshotsForSession(ctx, dbgen.CountLiveSloOverviewSnapshotsForSessionParams{ActorID: user.ID, SessionVerifier: input.Proof.SessionVerifier[:], ExpiresAt: timestamp(now)})
	if err != nil {
		return uuid.Nil, "", readTransactionError(ctx, "storage: overview session admission failed", err)
	}
	installationCount, err := q.CountLiveSloOverviewSnapshots(ctx, timestamp(now))
	if err != nil {
		return uuid.Nil, "", readTransactionError(ctx, "storage: overview installation admission failed", err)
	}
	if sessionCount >= sloOverviewSessionLimit || installationCount >= sloOverviewInstallationLimit {
		return uuid.Nil, "", ErrOperationalResource
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return uuid.Nil, "", errors.New("storage: overview snapshot identifier unavailable")
	}
	cursorSecret := make([]byte, 32)
	if _, err = rand.Read(cursorSecret); err != nil {
		return uuid.Nil, "", errors.New("storage: overview cursor secret unavailable")
	}
	var serviceID pgtype.UUID
	if input.ServiceID != "" {
		parsed, _ := canonicalUUID(input.ServiceID)
		serviceID = parsed
	}
	var urgency pgtype.Text
	if input.Urgency != "" {
		urgency = pgtype.Text{String: input.Urgency, Valid: true}
	}
	counts, _ := json.Marshal(api.SloUrgencyCounts{})
	if err = q.InsertSloOverviewSnapshot(ctx, dbgen.InsertSloOverviewSnapshotParams{
		ID: uuidToPG(id), CursorSecret: cursorSecret, ActorID: user.ID, SessionVerifier: input.Proof.SessionVerifier[:], Role: user.Role,
		QueryText: strings.TrimSpace(input.Query), ServiceID: serviceID, Urgency: urgency, PageLimit: int32(input.Limit),
		GroupCounts: counts, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(sloOverviewTTL)),
	}); err != nil {
		return uuid.Nil, "", readTransactionError(ctx, "storage: overview snapshot reservation failed", err)
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return uuid.Nil, "", err
	}
	return id, user.Role, nil
}

func materializeSloOverviewSnapshot(ctx context.Context, databaseURL string, input OperationalOverviewInput, snapshotID uuid.UUID, expectedRole string) error {
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if user.Role != expectedRole {
		return ErrAgentForbidden
	}
	now := normalizedAgentNow(input.Now)
	defs, streams, err := loadOperationalDefinitions(ctx, q, input, now)
	if err != nil {
		return err
	}
	if len(defs) > sloOverviewDefinitionLimit {
		return ErrOperationalResource
	}
	factsCount := 0
	for key, stream := range streams {
		remaining := sloOperationalFactsLimit - factsCount
		rows, count, readErr := loadOperationalFacts(ctx, q, key, stream, stream.factStart, floorSliBoundary(now, time.Duration(stream.cadence)*time.Second), remaining+1)
		if readErr != nil {
			return readErr
		}
		factsCount += count
		if factsCount > sloOperationalFactsLimit {
			return ErrOperationalResource
		}
		streams[key] = operationalStream{cadence: stream.cadence, factStart: stream.factStart, facts: rows}
	}
	items := make([]api.SloOperationalOverviewItem, 0, len(defs))
	counts := api.SloUrgencyCounts{}
	selectedBytes := 0
	for definitionIndex, definition := range defs {
		streamKey := operationalStreamKey(definition.row.SliID.String(), definition.row.MeasurementRevision)
		definition.facts = streams[streamKey].facts
		if definitionIndex&127 == 0 {
			if err := ctx.Err(); err != nil {
				return operationError(ctx, "storage: overview projection cancelled", err)
			}
		}
		item, buildErr := buildOverviewItem(ctx, definition, now, sli.ObservationRetention)
		if buildErr != nil {
			return buildErr
		}
		switch item.Urgency {
		case "needs_attention":
			counts.NeedsAttention++
		case "watch":
			counts.Watch++
		case "healthy":
			counts.Healthy++
		default:
			return errors.New("storage: invalid operational urgency projection")
		}
		if input.Urgency != "" && string(item.Urgency) != input.Urgency {
			continue
		}
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return errors.New("storage: overview projection serialization failed")
		}
		selectedBytes, err = addProjectionPayloadBytes(selectedBytes, len(encoded))
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := urgencyRank(string(items[i].Urgency)), urgencyRank(string(items[j].Urgency))
		if left != right {
			return left < right
		}
		leftName, rightName := strings.ToLower(items[i].Slo.Name), strings.ToLower(items[j].Slo.Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return items[i].Slo.Id.String() < items[j].Slo.Id.String()
	})
	if len(items) > 10_000 {
		return ErrOperationalResource
	}
	reservation, err := q.GetSloOverviewSnapshot(ctx, uuidToPG(snapshotID))
	if err != nil {
		return readTransactionError(ctx, "storage: overview reservation read failed", err)
	}
	if reservation.ActorID != user.ID || !slices.Equal(reservation.SessionVerifier, input.Proof.SessionVerifier[:]) || reservation.Role != user.Role || !now.Before(reservation.ExpiresAt.Time) || reservation.State != "preparing" {
		return ErrOperationalSnapshotOld
	}
	countsJSON, err := json.Marshal(counts)
	if err != nil {
		return errors.New("storage: overview counts serialization failed")
	}
	for ordinal, item := range items {
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return errors.New("storage: overview projection serialization failed")
		}
		if err = q.InsertSloOverviewSnapshotItem(ctx, dbgen.InsertSloOverviewSnapshotItemParams{SnapshotID: uuidToPG(snapshotID), Ordinal: int32(ordinal), Urgency: string(item.Urgency), Payload: encoded, PayloadBytes: int32(len(encoded))}); err != nil {
			return readTransactionError(ctx, "storage: overview projection persistence failed", err)
		}
	}
	if err = q.FinishSloOverviewSnapshot(ctx, dbgen.FinishSloOverviewSnapshotParams{ID: uuidToPG(snapshotID), PayloadBytes: int32(selectedBytes)}); err != nil {
		return readTransactionError(ctx, "storage: overview snapshot finalization failed", err)
	}
	if _, err = tx.Exec(ctx, "UPDATE public.almanac_slo_overview_snapshots SET group_counts=$2 WHERE id=$1", uuidToPG(snapshotID), countsJSON); err != nil {
		return readTransactionError(ctx, "storage: overview counts persistence failed", err)
	}
	return finishAgentRead(ctx, tx, input.Now, started, user, installation, session)
}

func readSloOverviewPage(ctx context.Context, databaseURL string, input OperationalOverviewInput) (api.SloOperationalOverviewPage, error) {
	cursor := overviewCursor{SnapshotID: input.snapshotID, Offset: 0}
	if input.Cursor != "" {
		var valid bool
		cursor, valid = decodeOverviewCursor(input.Cursor)
		if !valid {
			return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
		}
	}
	if cursor.Offset < 0 || cursor.Offset > sloOverviewDefinitionLimit {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	id, valid := canonicalUUID(cursor.SnapshotID)
	if !valid {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	if err = q.DeleteExpiredSloOverviewSnapshots(ctx, dbgen.DeleteExpiredSloOverviewSnapshotsParams{ExpiresAt: timestamp(now), Limit: sloOverviewCleanupBatch}); err != nil {
		return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: overview snapshot cleanup failed", err)
	}
	snapshotRow, err := q.GetSloOverviewSnapshot(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshotOld
	}
	if err != nil {
		return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: overview snapshot read failed", err)
	}
	if !now.Before(snapshotRow.ExpiresAt.Time) {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshotOld
	}
	if snapshotRow.ActorID != user.ID || !slices.Equal(snapshotRow.SessionVerifier, input.Proof.SessionVerifier[:]) || snapshotRow.Role != user.Role || snapshotRow.QueryText != strings.TrimSpace(input.Query) || snapshotRow.PageLimit != int32(input.Limit) || cursor.Offset%input.Limit != 0 {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	if snapshotRow.ServiceID.Valid != (input.ServiceID != "") || snapshotRow.ServiceID.Valid && snapshotRow.ServiceID.String() != input.ServiceID || snapshotRow.Urgency.Valid != (input.Urgency != "") || snapshotRow.Urgency.Valid && snapshotRow.Urgency.String != input.Urgency {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	if snapshotRow.State != "ready" {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshotOld
	}
	if input.Cursor != "" && !validOverviewCursor(snapshotRow.CursorSecret, cursor) {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	var counts api.SloUrgencyCounts
	if json.Unmarshal(snapshotRow.GroupCounts, &counts) != nil {
		return api.SloOperationalOverviewPage{}, errors.New("storage: invalid overview snapshot counts")
	}
	rows, err := q.ListSloOverviewSnapshotItems(ctx, dbgen.ListSloOverviewSnapshotItemsParams{SnapshotID: id, Ordinal: int32(cursor.Offset), Limit: int32(input.Limit + 1)})
	if err != nil {
		return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: overview page read failed", err)
	}
	more := len(rows) > input.Limit
	if more {
		rows = rows[:input.Limit]
	}
	items := make([]api.SloOperationalOverviewItem, 0, len(rows))
	for _, row := range rows {
		var item api.SloOperationalOverviewItem
		if json.Unmarshal(row.Payload, &item) != nil || !item.Urgency.Valid() {
			return api.SloOperationalOverviewPage{}, errors.New("storage: invalid overview snapshot item")
		}
		items = append(items, item)
	}
	next := ""
	previous := ""
	if cursor.Offset > 0 {
		previousOffset := max(0, cursor.Offset-input.Limit)
		previous = encodeOverviewCursor(snapshotRow.CursorSecret, overviewCursor{SnapshotID: id.String(), Offset: previousOffset})
	}
	if more {
		next = encodeOverviewCursor(snapshotRow.CursorSecret, overviewCursor{SnapshotID: id.String(), Offset: cursor.Offset + len(items)})
	} else if input.Cursor == "" {
		// A first page with no continuation needs no persisted cross-request state.
		if err = q.DeleteSloOverviewSnapshot(ctx, id); err != nil {
			return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: single-page overview snapshot cleanup failed", err)
		}
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	return api.SloOperationalOverviewPage{SnapshotId: uuid.UUID(id.Bytes), Items: items, GroupCounts: counts, NextCursor: next, PreviousCursor: previous}, nil
}

func deleteSloOverviewSnapshot(ctx context.Context, databaseURL string, input OperationalOverviewInput, id uuid.UUID) error {
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{}, "administrator", "editor", "viewer")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err = tx.Exec(ctx, "DELETE FROM public.almanac_slo_overview_snapshots WHERE id=$1 AND actor_id=$2 AND session_verifier=$3", uuidToPG(id), user.ID, input.Proof.SessionVerifier[:]); err != nil {
		return readTransactionError(ctx, "storage: failed overview reservation cleanup", err)
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return err
	}
	_ = queries
	return nil
}

func encodeOverviewCursor(secret []byte, value overviewCursor) string {
	value.MAC = overviewCursorMAC(secret, value)
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeOverviewCursor(value string) (overviewCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > 256 {
		return overviewCursor{}, false
	}
	var cursor overviewCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.SnapshotID == "" || cursor.MAC == "" {
		return overviewCursor{}, false
	}
	return cursor, true
}

func validOverviewCursor(secret []byte, cursor overviewCursor) bool {
	return hmac.Equal([]byte(cursor.MAC), []byte(overviewCursorMAC(secret, cursor)))
}

func overviewCursorMAC(secret []byte, cursor overviewCursor) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(cursor.SnapshotID + ":" + strconv.Itoa(cursor.Offset)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type operationalStream struct {
	cadence   int32
	factStart time.Time
	facts     []slo.Observation
}

func operationalStreamKey(sliID string, revision int64) string {
	return sliID + ":" + strconv.FormatInt(revision, 10)
}

func loadOperationalDefinitions(ctx context.Context, q *dbgen.Queries, input OperationalOverviewInput, now time.Time) ([]operationalDefinition, map[string]operationalStream, error) {
	var query pgtype.Text
	if strings.TrimSpace(input.Query) != "" {
		query = pgtype.Text{String: strings.TrimSpace(input.Query), Valid: true}
	}
	var serviceID pgtype.UUID
	if input.ServiceID != "" {
		id, _ := canonicalUUID(input.ServiceID)
		serviceID = id
	}
	rows, err := q.ListOperationalSloDefinitions(ctx, dbgen.ListOperationalSloDefinitionsParams{Limit: int32(sloOverviewDefinitionLimit + 1), QueryText: query, ServiceID: serviceID})
	if err != nil {
		return nil, nil, readTransactionError(ctx, "storage: operational SLO definitions read failed", err)
	}
	if len(rows) > sloOverviewDefinitionLimit {
		return nil, nil, ErrOperationalResource
	}
	defs := make([]operationalDefinition, 0, len(rows))
	streams := make(map[string]operationalStream)
	for _, row := range rows {
		if row.CadenceSeconds < 15 || row.RollingWindowSeconds <= 0 || row.RollingWindowSeconds%int64(row.CadenceSeconds) != 0 || row.MeasurementRevision <= 0 {
			return nil, nil, errors.New("storage: inconsistent operational SLO definition")
		}
		def := operationalDefinition{row: row}
		key := operationalStreamKey(row.SliID.String(), row.MeasurementRevision)
		if previous, ok := streams[key]; ok && previous.cadence != row.CadenceSeconds {
			return nil, nil, errors.New("storage: shared SLI stream has inconsistent cadence")
		}
		cadence := time.Duration(row.CadenceSeconds) * time.Second
		effective := floorSliBoundary(now, cadence)
		chartStart := floorSliBoundary(effective.Add(-7*24*time.Hour), cadence)
		window := time.Duration(row.RollingWindowSeconds) * time.Second
		factStart := operationalFactsStart(effective, window, 6*time.Hour,
			chartStart, window, 0, row.EvaluationValidFrom.Time.UTC(), row.SliValidFrom.Time.UTC(), ceilSliBoundary(now.Add(-sli.ObservationRetention), cadence), cadence)
		if previous, ok := streams[key]; ok && previous.factStart.Before(factStart) {
			factStart = previous.factStart
		}
		streams[key] = operationalStream{cadence: row.CadenceSeconds, factStart: factStart}
		defs = append(defs, def)
	}
	return defs, streams, nil
}

func operationalFactsStart(currentAt time.Time, currentWindow, currentBurn time.Duration, chartStart time.Time, chartWindow, chartBurn time.Duration, validFrom, sliValidFrom, retentionStart time.Time, cadence time.Duration) time.Time {
	currentHorizon := currentAt.Add(-max(currentWindow, currentBurn))
	chartHorizon := chartStart.Add(-max(chartWindow, chartBurn))
	horizon := currentHorizon
	if chartHorizon.Before(horizon) {
		horizon = chartHorizon
	}
	return ceilSliBoundary(maxTime(horizon, validFrom, sliValidFrom, retentionStart), cadence)
}

func loadOperationalFacts(ctx context.Context, q *dbgen.Queries, key string, stream operationalStream, start, end time.Time, rowLimit int) ([]slo.Observation, int, error) {
	sliIDText, revisionText, ok := strings.Cut(key, ":")
	if !ok {
		return nil, 0, errors.New("storage: invalid SLO stream identity")
	}
	sliID, valid := canonicalUUID(sliIDText)
	revision, parseErr := strconv.ParseInt(revisionText, 10, 64)
	if !valid || parseErr != nil || revision <= 0 {
		return nil, 0, errors.New("storage: invalid SLO stream identity")
	}
	if rowLimit < 1 || rowLimit > sloOperationalFactsLimit+1 {
		return nil, 0, ErrOperationalResource
	}
	rows, err := q.ListOperationalSloFacts(ctx, dbgen.ListOperationalSloFactsParams{SliID: sliID, MeasurementRevision: revision, IntervalStart: timestamp(start), IntervalEnd: timestamp(end), Limit: int32(rowLimit)})
	if err != nil {
		return nil, 0, readTransactionError(ctx, "storage: operational SLO observations read failed", err)
	}
	if len(rows) >= rowLimit && rowLimit <= sloOperationalFactsLimit {
		return nil, len(rows), ErrOperationalResource
	}
	facts := make([]slo.Observation, 0, len(rows))
	for _, row := range rows {
		if len(facts)&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, len(rows), err
			}
		}
		fact := slo.Observation{Start: row.IntervalStart.Time.UTC(), End: row.IntervalEnd.Time.UTC(), Outcome: row.Outcome, Received: row.RecordedAt.Time.UTC()}
		if row.Outcome == "observed" {
			fact.Good, fact.Total = row.Good, row.Total
		}
		facts = append(facts, fact)
	}
	return facts, len(rows), nil
}

func buildOverviewItem(ctx context.Context, def operationalDefinition, now time.Time, retention time.Duration) (api.SloOperationalOverviewItem, error) {
	row := def.row
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	effective := floorSliBoundary(now, cadence)
	freshnessGrace := cadence + time.Duration(row.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))
	base := slo.WindowInput{Now: now, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(), RetentionStart: now.Add(-retention), Cadence: cadence, Target: row.Target, Observations: def.facts, FreshnessGrace: freshnessGrace}
	current, err := evaluateOperationalAt(ctx, base, effective, time.Duration(row.RollingWindowSeconds)*time.Second)
	if err != nil {
		return api.SloOperationalOverviewItem{}, err
	}
	burn1h := unavailableBurn(3600, effective, "cadence_mismatch")
	var burn1hResult *slo.WindowResult
	if time.Hour%cadence == 0 {
		result, evalErr := evaluateOperationalAt(ctx, base, effective, time.Hour)
		if evalErr != nil {
			return api.SloOperationalOverviewItem{}, evalErr
		}
		burn1h = burnValue(result, 3600)
		burn1hResult = &result
	}
	burn6h := unavailableBurn(21600, effective, "cadence_mismatch")
	if 6*time.Hour%cadence == 0 {
		burn6hResult, evalErr := evaluateOperationalAt(ctx, base, effective, 6*time.Hour)
		if evalErr != nil {
			return api.SloOperationalOverviewItem{}, evalErr
		}
		burn6h = burnValue(burn6hResult, 21600)
	}
	currentValue := operationalValue(current)
	applyOperationalDefinitionWindow(&currentValue, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
	state := slo.ClassifyOperational(slo.OperationalState{QualityState: current.State, Freshness: current.Freshness, PerformanceState: current.Performance, Provisional: current.Provisional, ShortenedEligibility: current.EligibleStart.After(current.EffectiveStart), RemainingBudget: current.Remaining, RemainingFraction: current.RemainingFraction, Burn1h: burn1h.Performance.Burn, Burn1hAvailable: burn1h.Performance.Burn != nil, Burn1hQualityState: string(burn1h.Quality.State), Burn1hFreshness: string(burn1h.Quality.Freshness), Burn1hProvisional: burn1h.Performance.Provisional, Burn1hShortenedEligibility: burn1hResult != nil && burn1hResult.EligibleStart.After(burn1hResult.EffectiveStart), RemainingExact: current.ExactRemaining, RemainingFractionExact: current.ExactRemainingFraction, Burn1hExact: exactBurn(burn1hResult)})
	reasons, leading := operationalReasons(state.Reasons)
	samples, sampleErr := buildOverviewSparkline(ctx, base, row, now, cadence)
	if sampleErr != nil {
		return api.SloOperationalOverviewItem{}, sampleErr
	}
	return api.SloOperationalOverviewItem{Slo: operationalSloDTO(row), Urgency: api.SloOperationalOverviewItemUrgency(state.Group), LeadingReason: leading, Reasons: reasons, Current: currentValue, Burn1h: burn1h, Burn6h: burn6h, Exhaustion: exhaustionValue(current, burn1h, burn1hResult), BudgetSparkline: samples}, nil
}

func evaluateOperationalAt(ctx context.Context, base slo.WindowInput, at time.Time, duration time.Duration) (slo.WindowResult, error) {
	base.At, base.Duration = at, duration
	return slo.EvaluateOperationalWindowContext(ctx, base)
}

func operationalSloDTO(row dbgen.ListOperationalSloDefinitionsRow) api.Slo {
	result := api.Slo{Id: uuid.UUID(row.ID.Bytes), Name: row.Name, DisplayName: row.DisplayName,
		Version: strconv.FormatInt(row.Version, 10), DefinitionRevision: strconv.FormatInt(row.DefinitionRevision, 10),
		ServiceId: uuid.UUID(row.ServiceID.Bytes), ServiceDisplayName: row.ServiceDisplayName,
		SliId: uuid.UUID(row.SliID.Bytes), SliDisplayName: row.SliDisplayName, Target: sli.NormalizeDecimal(row.Target),
		RollingWindowSeconds: row.RollingWindowSeconds, CadenceSeconds: int(row.CadenceSeconds),
		ActivatedAt: row.ActivatedAt.Time.UTC(), EvaluationValidFrom: row.EvaluationValidFrom.Time.UTC(),
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}
	if row.HistoricalRequestedBy.Valid {
		result.HistoricalRequestedBy = new(uuid.UUID(row.HistoricalRequestedBy.Bytes))
	}
	if row.HistoricalRequestedAt.Valid {
		result.HistoricalRequestedAt = optionalTime(row.HistoricalRequestedAt)
	}
	return result
}

func operationalQuality(result slo.WindowResult) api.SloOperationalQuality {
	return api.SloOperationalQuality{State: api.SloOperationalQualityState(result.State), Freshness: api.SloOperationalQualityFreshness(result.Freshness),
		ExpectedIntervals: result.Expected, ObservedIntervals: result.Observed, KnownEmptyIntervals: result.KnownEmpty,
		MissingIntervals: result.Missing, ExpiredIntervals: result.Expired, ZeroTrafficIntervals: result.ZeroTraffic,
		Coverage: result.Coverage, FirstObservationAt: result.FirstObservation, LastObservationAt: result.LastObservation,
		LatestReceiptAt: result.LatestReceipt, Reason: stringPointer(result.Reason)}
}

func operationalPerformance(result slo.WindowResult) api.SloEvaluationPerformance {
	performance := api.SloEvaluationPerformance{State: api.SloEvaluationPerformanceState(result.Performance), Provisional: result.Provisional,
		Reason: stringPointer(result.Reason), Good: result.Good, Total: result.Total, Bad: result.Bad, Attainment: result.Attainment,
		AllowedBudget: result.Allowed, RemainingBudget: result.Remaining, ConsumedFraction: result.Consumed,
		RemainingFraction: result.RemainingFraction, Burn: result.Burn, BudgetUnit: "events"}
	if result.ObservedPerformance != "" {
		state := api.SloEvaluationPerformanceObservedState(result.ObservedPerformance)
		performance.ObservedState = &state
	}
	return performance
}

func operationalValue(result slo.WindowResult) api.SloOperationalValue {
	return api.SloOperationalValue{EvaluationAt: result.End, Quality: operationalQuality(result), Performance: operationalPerformance(result), Window: api.SloEvaluationWindow{RequestedStart: result.RequestedStart,
		EffectiveStart: result.EffectiveStart, End: result.End, EligibleStart: result.EligibleStart, RetentionStart: result.RetentionStart,
		ActivatedAt: result.End, EvaluationValidFrom: result.EligibleStart}}
}

func applyOperationalDefinitionWindow(value *api.SloOperationalValue, activatedAt, validFrom time.Time, requestedBy pgtype.UUID, requestedAt pgtype.Timestamptz) {
	value.Window.ActivatedAt = activatedAt
	value.Window.EvaluationValidFrom = validFrom
	if requestedAt.Valid {
		value.Window.HistoricalRequestedAt = optionalTime(requestedAt)
	}
	if requestedBy.Valid {
		value.Window.HistoricalRequestedBy = new(uuid.UUID(requestedBy.Bytes))
	}
}

func burnValue(result slo.WindowResult, seconds int) api.SloBurnValue {
	quality, performance := operationalQuality(result), operationalPerformance(result)
	if result.EligibleStart.After(result.EffectiveStart) {
		reason := "incomplete_measurements"
		quality.Reason, performance.Reason, performance.Provisional = &reason, &reason, true
	}
	return api.SloBurnValue{WindowSeconds: api.SloBurnValueWindowSeconds(seconds), Start: result.RequestedStart, End: result.End, Quality: quality, Performance: performance}
}

func unavailableBurn(seconds int, at time.Time, reason string) api.SloBurnValue {
	quality := api.SloOperationalQuality{State: "unavailable", Freshness: "unknown", Reason: &reason}
	performance := api.SloEvaluationPerformance{State: "unknown", BudgetUnit: "events", Reason: &reason}
	return api.SloBurnValue{WindowSeconds: api.SloBurnValueWindowSeconds(seconds), Start: at.Add(-time.Duration(seconds) * time.Second), End: at, Quality: quality, Performance: performance}
}

func exhaustionValue(current slo.WindowResult, burn api.SloBurnValue, oneHour *slo.WindowResult) api.SloExhaustionEstimate {
	assumptions := []api.SloExhaustionEstimateAssumptions{"freezes_current_allowed_budget", "continues_observed_one_hour_bad_event_rate", "rolling_window_expiry_can_change_result", "future_traffic_can_change_result"}
	result := api.SloExhaustionEstimate{State: "unavailable", CurrentQuality: operationalQuality(current), OneHourQuality: burn.Quality, Assumptions: assumptions}
	if current.ExactRemaining == nil || oneHour == nil || oneHour.ExactBad == nil || current.State != "complete" || current.Provisional || current.Freshness != "fresh" || current.EligibleStart.After(current.EffectiveStart) || oneHour.State != "complete" || oneHour.Provisional || oneHour.Freshness != "fresh" || oneHour.EligibleStart.After(oneHour.EffectiveStart) {
		reason := "incomplete_or_stale_inputs"
		result.Reason = &reason
		return result
	}
	remaining := new(big.Rat).Set(current.ExactRemaining)
	rate := new(big.Rat).Set(oneHour.ExactBad)
	remainingText := slo.FormatRational(remaining)
	result.RemainingEvents = &remainingText
	rateText := slo.FormatRational(rate)
	result.BadEventsPerHour = &rateText
	if remaining.Sign() <= 0 {
		result.State = "exhausted"
		zero := "0.000000000000"
		result.HoursUntilExhaustion = &zero
		return result
	}
	if rate.Sign() == 0 {
		reason := "zero_bad_event_rate"
		result.Reason = &reason
		return result
	}
	result.State = "estimated"
	hours := new(big.Rat).Quo(remaining, rate)
	hoursText := slo.FormatRational(hours)
	result.HoursUntilExhaustion = &hoursText
	return result
}

func exactBurn(result *slo.WindowResult) *big.Rat {
	if result == nil || result.ExactBurn == nil {
		return nil
	}
	return new(big.Rat).Set(result.ExactBurn)
}

func operationalReasons(values []slo.OperationalReason) ([]api.SloUrgencyReason, api.SloUrgencyReason) {
	reasons := make([]api.SloUrgencyReason, 0, len(values))
	for _, value := range values {
		reasons = append(reasons, api.SloUrgencyReason{Code: api.SloUrgencyReasonCode(value.Code), Provisional: value.Provisional})
	}
	if len(reasons) == 0 {
		reasons = append(reasons, api.SloUrgencyReason{Code: "on_track"})
	}
	return reasons, reasons[0]
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func urgencyRank(value string) int {
	switch value {
	case "needs_attention":
		return 0
	case "watch":
		return 1
	case "healthy":
		return 2
	default:
		return 3
	}
}

func ceilMultiple(value time.Time, cadence time.Duration) time.Time {
	return ceilSliBoundary(value, cadence)
}

func parseOperationalTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	_, offset := parsed.Zone()
	return parsed.UTC(), offset == 0 && parsed.Nanosecond()%1000 == 0
}

func sortedUniqueTimes(values []time.Time) []time.Time {
	sort.Slice(values, func(i, j int) bool { return values[i].Before(values[j]) })
	return slices.CompactFunc(values, func(a, b time.Time) bool { return a.Equal(b) })
}

func buildOverviewSparkline(ctx context.Context, base slo.WindowInput, row dbgen.ListOperationalSloDefinitionsRow, now time.Time, cadence time.Duration) ([]api.SloOperationalSample, error) {
	end := floorSliBoundary(now, cadence)
	start := floorSliBoundary(end.Add(-7*24*time.Hour), cadence)
	base.Observations = clipOperationalFacts(base.Observations, maxTime(start.Add(-time.Duration(row.RollingWindowSeconds)*time.Second), base.RetentionStart))
	return buildOperationalSamples(ctx, base, start, end, time.Duration(row.RollingWindowSeconds)*time.Second, cadence,
		strconv.FormatInt(row.DefinitionRevision, 10), "remaining_fraction", 30)
}

type sampleBucket struct {
	start, end                        time.Time
	byTime                            map[int64]sampleCandidate
	firstKey, lastKey, minKey, maxKey int64
	hasFirst, hasLast, hasMin, hasMax bool
	minimum, maximum                  *big.Rat
	critical                          map[int64]bool
}

type sampleCandidate struct {
	value  api.SloOperationalSample
	labels map[api.SloOperationalSampleRetainedExtrema]bool
}

func buildOperationalSamples(ctx context.Context, base slo.WindowInput, start, end time.Time, duration, cadence time.Duration, revision, metric string, maxBuckets int) ([]api.SloOperationalSample, error) {
	ticks := int(end.Sub(start) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}, nil
	}
	base.Observations = clipOperationalFacts(base.Observations, maxTime(start.Add(-duration), base.RetentionStart))
	buckets := min(ticks, maxBuckets)
	if buckets <= 0 {
		return nil, ErrOperationalRangeDense
	}
	sampleLimit := min(sloOperationalSeriesLimit, buckets*4)
	boundaries := bucketBoundaries(start, end, ticks, buckets, cadence)
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
	collected := make([]sampleBucket, buckets)
	for index := range collected {
		collected[index] = sampleBucket{start: boundaries[index], end: boundaries[index+1], byTime: make(map[int64]sampleCandidate, 4), critical: make(map[int64]bool)}
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
		result, evalErr := sweep.AtContext(ctx, at)
		if evalErr != nil {
			return nil, evalErr
		}
		sample := operationalSample(result, collected[bucketIndex].start, collected[bucketIndex].end, revision)
		key := at.UnixMicro()
		bucket := &collected[bucketIndex]
		stateKey := string(sample.State) + ":" + string(sample.Quality.State) + ":" + string(sample.Quality.Freshness)
		if previousState != "" && stateKey != previousState {
			bucket.critical[key] = true
		}
		previousState = stateKey
		if !bucket.hasFirst {
			bucket.firstKey, bucket.hasFirst = key, true
		}
		bucket.lastKey, bucket.hasLast = key, true
		if value := sampleMetric(result, metric); value != nil {
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
		candidate := sampleCandidate{value: sample, labels: make(map[api.SloOperationalSampleRetainedExtrema]bool, 4)}
		if keys[key] {
			bucket.byTime[key] = candidate
		}
		for candidateKey := range keys {
			if _, exists := bucket.byTime[candidateKey]; !exists {
				bucket.byTime[candidateKey] = sampleCandidate{value: sampleAtKey(bucket, candidateKey, key, sample), labels: make(map[api.SloOperationalSampleRetainedExtrema]bool, 4)}
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
			candidate.value.RetainedExtrema = labels
			result = append(result, candidate.value)
		}
	}
	if len(result) > sampleLimit {
		return nil, ErrOperationalRangeDense
	}
	return result, nil
}

func sampleAtKey(bucket *sampleBucket, wanted, current int64, currentSample api.SloOperationalSample) api.SloOperationalSample {
	if wanted == current {
		return currentSample
	}
	if existing, ok := bucket.byTime[wanted]; ok {
		return existing.value
	}
	return api.SloOperationalSample{BucketStart: bucket.start, BucketEnd: bucket.end, EvaluationAt: time.UnixMicro(wanted).UTC(),
		State: "missing", Quality: api.SloOperationalQuality{State: "unavailable", Freshness: "unknown"},
		RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}}
}

func sampleMetric(result slo.WindowResult, metric string) *big.Rat {
	if metric == "burn" {
		return result.ExactBurn
	}
	if metric == "remaining_fraction" {
		return result.ExactRemainingFraction
	}
	return result.ExactRemaining
}

func operationalSample(result slo.WindowResult, bucketStart, bucketEnd time.Time, revision string) api.SloOperationalSample {
	state := api.SloOperationalSampleState("observed")
	switch result.State {
	case "zero_traffic":
		state = "zero_traffic"
	case "complete":
		if result.Observed == 0 && result.KnownEmpty > 0 {
			state = "known_empty"
		} else if result.Observed > 0 && (result.KnownEmpty > 0 || result.Expired > 0 || result.Missing > 0) {
			state = "partial"
		}
	case "no_data":
		state = "missing"
	case "expired_history":
		if result.Observed > 0 {
			state = "partial"
		} else {
			state = "expired"
		}
	case "not_applicable":
		state = "not_applicable"
	case "partial":
		state = "partial"
	}
	if state == "observed" && result.Freshness == "stale" {
		state = "stale"
	}
	return api.SloOperationalSample{BucketStart: bucketStart, BucketEnd: bucketEnd, EvaluationAt: result.End, State: state,
		Quality: operationalQuality(result), Revision: revision, Good: result.Good, Total: result.Total,
		Attainment: result.Attainment, AllowedBudget: result.Allowed, RemainingBudget: result.Remaining,
		RemainingFraction: result.RemainingFraction, Burn: result.Burn, ReceivedAt: result.LatestReceipt,
		RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}}
}
