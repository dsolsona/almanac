package storage

import (
	"context"
	"encoding/json/v2"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type displayBlock struct {
	Samples []api.SloOperationalSample  `json:"samples,omitempty"`
	Days    []api.SloDailyComplianceDay `json:"days,omitempty"`
}

type sloDisplayCache struct {
	q                *dbgen.Queries
	row              dbgen.GetSloRow
	sourceGeneration int64
	now              time.Time
}

// missingBlockGroups keeps disjoint late-data dependency ranges disjoint. A
// contiguous cold range is evaluated by one canonical sweep, not one per bucket.
func missingBlockGroups(ready []bool) [][2]int {
	var groups [][2]int
	for index := 0; index < len(ready); {
		if ready[index] {
			index++
			continue
		}
		start := index
		for index < len(ready) && !ready[index] {
			index++
		}
		groups = append(groups, [2]int{start, index})
	}
	return groups
}

func (c *sloDisplayCache) loadFacts(ctx context.Context, base slo.WindowInput, start, end time.Time, duration time.Duration) (slo.WindowInput, error) {
	from := ceilSliBoundary(maxTime(start.Add(-duration), base.ValidFrom, base.SliValidFrom, base.RetentionStart), base.Cadence)
	facts, count, err := loadOperationalFacts(ctx, c.q, operationalStreamKey(c.row.SliID.String(), c.row.MeasurementRevision), operationalStream{cadence: c.row.CadenceSeconds}, from, end, sloOperationalFactsLimit+1)
	if err != nil {
		return base, err
	}
	if count > sloOperationalFactsLimit {
		return base, ErrOperationalResource
	}
	base.Observations = facts
	return base, nil
}

func validDisplayBlock(value displayBlock, lane string, start, end time.Time, includeStart bool, revision string) bool {
	if lane == "compliance" {
		return len(value.Samples) == 0 && len(value.Days) == 1 && value.Days[0].DayStart.Equal(start) && value.Days[0].DayEnd.Equal(end) && value.Days[0].DefinitionRevision == revision && value.Days[0].Status.Valid()
	}
	if len(value.Days) != 0 || len(value.Samples) == 0 || len(value.Samples) > sloOperationalSeriesLimit {
		return false
	}
	for _, sample := range value.Samples {
		if !sample.BucketStart.Equal(start) || !sample.BucketEnd.Equal(end) || sample.Revision != revision || !sample.State.Valid() || !sample.Quality.State.Valid() || !sample.Quality.Freshness.Valid() || sample.EvaluationAt.Before(start) || sample.EvaluationAt.After(end) || (!includeStart && sample.EvaluationAt.Equal(start)) {
			return false
		}
	}
	return true
}

func (c *sloDisplayCache) blocks(ctx context.Context, base slo.WindowInput, lane string, bounds []time.Time, duration time.Duration, revision string, rebuild func(int, int) ([]displayBlock, error)) ([]displayBlock, error) {
	count := len(bounds) - 1
	if count <= 0 {
		return []displayBlock{}, nil
	}
	starts, ends := make([]pgtype.Timestamptz, count), make([]pgtype.Timestamptz, count)
	includes := make([]bool, count)
	positions := make(map[int64]int, count)
	for index := range count {
		starts[index], ends[index] = timestamp(bounds[index]), timestamp(bounds[index+1])
		includes[index] = index == 0 && (lane == "budget" || lane == "burn")
		positions[bounds[index].UnixMicro()] = index
	}
	retention := timestamp(ceilSliBoundary(base.RetentionStart, base.Cadence))
	rows, err := c.q.ReadReusableSloBlocks(ctx, dbgen.ReadReusableSloBlocksParams{SloID: c.row.ID, SliID: c.row.SliID, DefinitionRevision: c.row.DefinitionRevision,
		MeasurementRevision: c.row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, Lane: lane, WindowSeconds: int64(duration / time.Second),
		FreshnessGraceUs: base.FreshnessGrace.Microseconds(), RetentionStart: retention, Starts: starts, Ends: ends, IncludeStarts: includes})
	if err != nil {
		return nil, err
	}
	result, ready := make([]displayBlock, count), make([]bool, count)
	for _, row := range rows {
		index, exists := positions[row.RangeStart.Time.UnixMicro()]
		if !exists || !row.RangeEnd.Time.Equal(bounds[index+1]) {
			continue
		}
		var value displayBlock
		if json.Unmarshal(row.Payload, &value) == nil && validDisplayBlock(value, lane, bounds[index], bounds[index+1], includes[index], revision) {
			result[index], ready[index] = value, true
		}
	}
	for _, group := range missingBlockGroups(ready) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, err := rebuild(group[0], group[1])
		if err != nil {
			return nil, err
		}
		if len(values) != group[1]-group[0] {
			return nil, ErrDerivedFailed
		}
		for offset, value := range values {
			index := group[0] + offset
			if !validDisplayBlock(value, lane, bounds[index], bounds[index+1], includes[index], revision) {
				return nil, ErrDerivedFailed
			}
			payload, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if len(payload) > 20<<20 {
				return nil, ErrOperationalResource
			}
			if err := c.q.UpsertSloDisplayBlock(ctx, dbgen.UpsertSloDisplayBlockParams{SloID: c.row.ID, DefinitionRevision: c.row.DefinitionRevision,
				MeasurementRevision: c.row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, Lane: lane, WindowSeconds: int64(duration / time.Second),
				FreshnessGraceUs: base.FreshnessGrace.Microseconds(), RangeStart: starts[index], RangeEnd: ends[index], IncludeStart: includes[index],
				SourceGeneration: c.sourceGeneration, RetentionStart: retention, Payload: payload, UpdatedAt: timestamp(c.now)}); err != nil {
				return nil, err
			}
			result[index] = value
		}
	}
	return result, nil
}

func (c *sloDisplayCache) rolling(ctx context.Context, base slo.WindowInput, start, end time.Time, duration, cadence time.Duration, revision, metric string, maxBuckets int) ([]api.SloOperationalSample, error) {
	ticks := int(end.Sub(start) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}, nil
	}
	count := min(ticks, maxBuckets)
	if count <= 0 {
		return nil, ErrOperationalRangeDense
	}
	bounds := bucketBoundaries(start, end, ticks, count, cadence)
	lane := "budget"
	if metric == "burn" {
		lane = "burn"
	}
	limit := min(sloOperationalSeriesLimit, count*4)
	blocks, err := c.blocks(ctx, base, lane, bounds, duration, revision, func(first, last int) ([]displayBlock, error) {
		loaded, err := c.loadFacts(ctx, base, bounds[first], bounds[last], duration)
		if err != nil {
			return nil, err
		}
		samples, err := buildRollingBuckets(ctx, loaded, bounds[first:last+1], duration, cadence, revision, metric, first == 0, limit)
		if err != nil {
			return nil, err
		}
		values := make([]displayBlock, last-first)
		positions := make(map[int64]int, last-first)
		for index := first; index < last; index++ {
			positions[bounds[index].UnixMicro()] = index - first
		}
		for _, sample := range samples {
			index, exists := positions[sample.BucketStart.UnixMicro()]
			if !exists {
				return nil, ErrDerivedFailed
			}
			values[index].Samples = append(values[index].Samples, sample)
		}
		return values, nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]api.SloOperationalSample, 0, limit)
	for _, block := range blocks {
		result = append(result, block.Samples...)
	}
	if len(result) > limit {
		return nil, ErrOperationalRangeDense
	}
	return result, nil
}

func (c *sloDisplayCache) attainment(ctx context.Context, base slo.WindowInput, start, end time.Time, cadence time.Duration, revision string, _ ...[]time.Time) ([]api.SloOperationalSample, time.Duration, bool, error) {
	ticks := int(end.Sub(start) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}, cadence, false, nil
	}
	count := sampleBucketLimit(start, end, cadence)
	bounds := bucketBoundaries(start, end, ticks, count, cadence)
	blocks, err := c.blocks(ctx, base, "attainment", bounds, 0, revision, func(first, last int) ([]displayBlock, error) {
		loaded, err := c.loadFacts(ctx, base, bounds[first], bounds[last], 0)
		if err != nil {
			return nil, err
		}
		samples, _, _, err := buildAttainmentSamples(ctx, loaded, bounds[first], bounds[last], cadence, revision, bounds[first:last+1])
		if err != nil {
			return nil, err
		}
		values := make([]displayBlock, len(samples))
		for index, sample := range samples {
			values[index].Samples = []api.SloOperationalSample{sample}
		}
		return values, nil
	})
	if err != nil {
		return nil, 0, false, err
	}
	samples := make([]api.SloOperationalSample, 0, count)
	for _, block := range blocks {
		samples = append(samples, block.Samples...)
	}
	return samples, time.Duration((ticks+count-1)/count) * cadence, ticks > sloOperationalSeriesLimit, nil
}

func (c *sloDisplayCache) daily(ctx context.Context, base slo.WindowInput, start, end, cutoff time.Time, id openapi_types.UUID, revision, target string, window, cadence time.Duration) (api.SloDailyComplianceSeries, api.SloDailyComplianceSummary, error) {
	last := utcDayStart(cutoff)
	if end.Before(last) {
		last = end
	}
	first := utcDayStart(start)
	count := int(last.Sub(first) / (24 * time.Hour))
	series := api.SloDailyComplianceSeries{Kind: "daily_compliance", Days: []api.SloDailyComplianceDay{}}
	var summary api.SloDailyComplianceSummary
	if count <= 0 {
		return series, summary, nil
	}
	bounds := make([]time.Time, count+1)
	for index := range bounds {
		bounds[index] = first.Add(time.Duration(index) * 24 * time.Hour)
	}
	blocks, err := c.blocks(ctx, base, "compliance", bounds, window, revision, func(first, last int) ([]displayBlock, error) {
		loaded, err := c.loadFacts(ctx, base, bounds[first], bounds[last], window)
		if err != nil {
			return nil, err
		}
		value, _, err := buildDailyCompliance(ctx, loaded, bounds[first], bounds[last], cutoff, id, revision, target, window, cadence)
		if err != nil {
			return nil, err
		}
		values := make([]displayBlock, len(value.Days))
		for index, day := range value.Days {
			values[index].Days = []api.SloDailyComplianceDay{day}
		}
		return values, nil
	})
	if err != nil {
		return series, summary, err
	}
	for _, block := range blocks {
		day := block.Days[0]
		series.Days = append(series.Days, day)
		switch day.Status {
		case "met":
			summary.MetDays++
			summary.EvaluableDays++
		case "violated":
			summary.ViolatedDays++
			summary.EvaluableDays++
		case "unknown":
			summary.UnknownDays++
		case "not_applicable":
			summary.NotApplicableDays++
		}
	}
	return series, summary, nil
}
