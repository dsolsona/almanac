package storage

import (
	"context"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func readStoredMaintenanceHour(ctx context.Context, q *dbgen.Queries, k maintenanceHourKey, revision int64) (maintenanceHourRecord, error) {
	var result maintenanceHourRecord
	r, err := q.ReadMaintenanceHour(ctx, dbgen.ReadMaintenanceHourParams{SloID: k.SloID, DefinitionRevision: k.DefinitionRevision, MeasurementRevision: k.MeasurementRevision, BuildID: k.BuildID, HourStart: timestamp(k.HourStart), SourceGeneration: k.SourceGeneration})
	if err != nil {
		return result, err
	}
	if r.SliID != k.SliID || r.ExclusionRevision != revision || !r.PreparedAt.Valid {
		return result, ErrMaintenanceHourUnavailable
	}
	result = maintenanceHourRecord{Key: k, ExclusionRevision: revision, PreparedAt: r.PreparedAt.Time.UTC()}
	result.Eligible = maintenanceHourPopulation{Expected: r.EligibleExpected, Observed: r.EligibleObserved, KnownEmpty: r.EligibleKnownEmpty, ZeroTraffic: r.EligibleZeroTraffic}
	var eligibleGoodOK, eligibleTotalOK bool
	result.Eligible.Good, eligibleGoodOK = new(big.Rat).SetString(r.EligibleGood)
	result.Eligible.Total, eligibleTotalOK = new(big.Rat).SetString(r.EligibleTotal)
	if !eligibleGoodOK || !eligibleTotalOK {
		return maintenanceHourRecord{}, ErrMaintenanceHourUnavailable
	}
	if r.EligibleFirstObservation.Valid {
		result.Eligible.FirstObservation = new(r.EligibleFirstObservation.Time.UTC())
	}
	if r.EligibleLastObservation.Valid {
		result.Eligible.LastObservation = new(r.EligibleLastObservation.Time.UTC())
	}
	if r.EligibleLatestReceipt.Valid {
		result.Eligible.LatestReceipt = new(r.EligibleLatestReceipt.Time.UTC())
	}
	if !validMaintenancePopulation(result.Eligible) {
		return maintenanceHourRecord{}, ErrMaintenanceHourUnavailable
	}
	result.Excluded = maintenanceHourPopulation{Expected: r.ExcludedExpected, Observed: r.ExcludedObserved, KnownEmpty: r.ExcludedKnownEmpty, ZeroTraffic: r.ExcludedZeroTraffic}
	var excludedGoodOK, excludedTotalOK bool
	result.Excluded.Good, excludedGoodOK = new(big.Rat).SetString(r.ExcludedGood)
	result.Excluded.Total, excludedTotalOK = new(big.Rat).SetString(r.ExcludedTotal)
	if !excludedGoodOK || !excludedTotalOK {
		return maintenanceHourRecord{}, ErrMaintenanceHourUnavailable
	}
	if r.ExcludedFirstObservation.Valid {
		result.Excluded.FirstObservation = new(r.ExcludedFirstObservation.Time.UTC())
	}
	if r.ExcludedLastObservation.Valid {
		result.Excluded.LastObservation = new(r.ExcludedLastObservation.Time.UTC())
	}
	if r.ExcludedLatestReceipt.Valid {
		result.Excluded.LatestReceipt = new(r.ExcludedLatestReceipt.Time.UTC())
	}
	if !validMaintenancePopulation(result.Excluded) {
		return maintenanceHourRecord{}, ErrMaintenanceHourUnavailable
	}
	if result.Eligible.Expected+result.Excluded.Expected > 3600 {
		return maintenanceHourRecord{}, ErrMaintenanceHourUnavailable
	}
	return result, nil
}
func validMaintenancePopulation(p maintenanceHourPopulation) bool {
	if p.Good == nil || p.Total == nil || p.Good.Sign() < 0 || p.Good.Cmp(p.Total) > 0 || p.Expected < 0 || p.Expected > 3600 || p.Observed < 0 || p.KnownEmpty < 0 || p.Observed > p.Expected || p.KnownEmpty > p.Expected-p.Observed || p.ZeroTraffic < 0 || p.ZeroTraffic > p.Observed {
		return false
	}
	if p.Observed == 0 {
		if p.FirstObservation != nil || p.LastObservation != nil || p.Good.Sign() != 0 || p.Total.Sign() != 0 {
			return false
		}
	} else if p.FirstObservation == nil || p.LastObservation == nil || !p.FirstObservation.Before(*p.LastObservation) {
		return false
	}
	return (p.LatestReceipt == nil) == (p.Observed+p.KnownEmpty == 0)
}
func storeMaintenanceHour(ctx context.Context, q *dbgen.Queries, r maintenanceHourRecord) (maintenanceHourRecord, error) {
	params := dbgen.InsertMaintenanceHourParams{SliID: r.Key.SliID, SloID: r.Key.SloID, BuildID: r.Key.BuildID, DefinitionRevision: r.Key.DefinitionRevision, MeasurementRevision: r.Key.MeasurementRevision, SourceGeneration: r.Key.SourceGeneration, HourStart: timestamp(r.Key.HourStart), ExclusionRevision: r.ExclusionRevision, PreparedAt: timestamp(r.PreparedAt),
		EligibleGood:             r.Eligible.Good.FloatString(12),
		EligibleTotal:            r.Eligible.Total.FloatString(12),
		EligibleExpected:         r.Eligible.Expected,
		EligibleObserved:         r.Eligible.Observed,
		EligibleKnownEmpty:       r.Eligible.KnownEmpty,
		EligibleZeroTraffic:      r.Eligible.ZeroTraffic,
		EligibleFirstObservation: maintenanceHourTime(r.Eligible.FirstObservation),
		EligibleLastObservation:  maintenanceHourTime(r.Eligible.LastObservation),
		EligibleLatestReceipt:    maintenanceHourTime(r.Eligible.LatestReceipt),
		ExcludedGood:             r.Excluded.Good.FloatString(12),
		ExcludedTotal:            r.Excluded.Total.FloatString(12),
		ExcludedExpected:         r.Excluded.Expected,
		ExcludedObserved:         r.Excluded.Observed,
		ExcludedKnownEmpty:       r.Excluded.KnownEmpty,
		ExcludedZeroTraffic:      r.Excluded.ZeroTraffic,
		ExcludedFirstObservation: maintenanceHourTime(r.Excluded.FirstObservation),
		ExcludedLastObservation:  maintenanceHourTime(r.Excluded.LastObservation),
		ExcludedLatestReceipt:    maintenanceHourTime(r.Excluded.LatestReceipt),
	}
	changed, err := q.InsertMaintenanceHour(ctx, params)
	if err != nil {
		return maintenanceHourRecord{}, err
	}
	if changed == 1 {
		return r, nil
	}
	existing, err := readStoredMaintenanceHour(ctx, q, r.Key, r.ExclusionRevision)
	if err != nil {
		return maintenanceHourRecord{}, err
	}
	if !equalMaintenancePopulation(existing.Eligible, r.Eligible) || !equalMaintenancePopulation(existing.Excluded, r.Excluded) {
		return maintenanceHourRecord{}, ErrMaintenanceHourUnavailable
	}
	return existing, nil
}
func equalMaintenancePopulation(a, b maintenanceHourPopulation) bool {
	sameTime := func(a, b *time.Time) bool { return a == nil && b == nil || a != nil && b != nil && a.Equal(*b) }
	return a.Good.Cmp(b.Good) == 0 && a.Total.Cmp(b.Total) == 0 && a.Expected == b.Expected && a.Observed == b.Observed && a.KnownEmpty == b.KnownEmpty && a.ZeroTraffic == b.ZeroTraffic && sameTime(a.FirstObservation, b.FirstObservation) && sameTime(a.LastObservation, b.LastObservation) && sameTime(a.LatestReceipt, b.LatestReceipt)
}
