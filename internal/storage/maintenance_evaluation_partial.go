package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func evaluationPartialHours(ctx context.Context, tx pgx.Tx, in maintenanceEvaluationInput, mask maintenanceComputedMask, cadence time.Duration, hours []time.Time, sources map[int64]maintenanceEvaluationSource, populations map[int64]maintenanceHourRecord, preparation *[]maintenanceEvaluationPreparation, unavailable map[int64]string) ([]maintenanceReusedHour, error) {
	if len(hours) == 0 {
		return nil, nil
	}
	p := dbgen.ReadMaintenanceEvaluationScopedHoursParams{SloID: in.SloID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision, BuildID: mask.BuildID, ExclusionRevision: mask.Revision}
	for _, h := range hours {
		p.Hours = append(p.Hours, timestamp(h))
		p.Generations = append(p.Generations, sources[h.UnixMicro()].generation)
	}
	rows, err := dbgen.New(tx).ReadMaintenanceEvaluationScopedHours(ctx, p)
	if err != nil {
		return nil, err
	}
	var reused []maintenanceReusedHour
	var proofs []maintenanceCompatibilityProof
	var candidates []maintenanceHourRecord
	var comparisons []maintenanceEvaluationBuildPair
	reusePending := map[int64]bool{}
	for _, r := range rows {
		i := int(r.Ordinal) - 1
		if i < 0 || i >= len(hours) || r.SloID != in.SloID || r.SliID != in.SliID || r.DefinitionRevision != in.DefinitionRevision || r.MeasurementRevision != in.MeasurementRevision || !r.HourStart.Valid || !r.HourStart.Time.Equal(hours[i]) || r.SourceGeneration != p.Generations[i] || r.ExclusionRevision > mask.Revision || r.ExclusionRevision < 0 || !r.PreparedAt.Valid {
			return nil, slo.ErrOperationalFacts
		}
		h := hours[i]
		record := maintenanceHourRecord{Key: maintenanceHourKey{SliID: in.SliID, SloID: in.SloID, BuildID: r.BuildID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision, HourStart: h, SourceGeneration: r.SourceGeneration}, ExclusionRevision: r.ExclusionRevision, PreparedAt: r.PreparedAt.Time.UTC()}
		record.Eligible, err = evaluationPopulation(r.EligibleGood, r.EligibleTotal, r.EligibleExpected, r.EligibleObserved, r.EligibleKnownEmpty, r.EligibleZeroTraffic, r.EligibleFirstObservation, r.EligibleLastObservation, r.EligibleLatestReceipt)
		if err != nil {
			return nil, err
		}
		record.Excluded, err = evaluationPopulation(r.ExcludedGood, r.ExcludedTotal, r.ExcludedExpected, r.ExcludedObserved, r.ExcludedKnownEmpty, r.ExcludedZeroTraffic, r.ExcludedFirstObservation, r.ExcludedLastObservation, r.ExcludedLatestReceipt)
		if err != nil {
			return nil, err
		}
		combined := emptyMaintenancePopulation()
		addEvaluationPopulation(&combined, record.Eligible)
		addEvaluationPopulation(&combined, record.Excluded)
		canonical := sources[h.UnixMicro()].population
		combined.LatestReceipt = canonical.LatestReceipt
		if !equalMaintenancePopulation(combined, canonical) {
			return nil, slo.ErrOperationalFacts
		}
		if r.BuildID == mask.BuildID {
			if r.ExclusionRevision != mask.Revision {
				return nil, slo.ErrOperationalFacts
			}
			populations[h.UnixMicro()] = record
		} else {
			candidates = append(candidates, record)
			proofs = append(proofs, maintenanceCompatibilityProof{SloID: in.SloID.String(), ComputedRevision: r.ExclusionRevision, RequestedRevision: mask.Revision, Start: ceilSliBoundary(h, cadence), End: ceilSliBoundary(h.Add(time.Hour), cadence)})
		}
	}
	if len(proofs) > 0 {
		proofs, err = readMaintenanceEvaluationProofs(ctx, tx, in.SloID, proofs)
		if err != nil {
			if !errors.Is(err, ErrMaintenanceCompilePending) && !errors.Is(err, ErrMaintenanceCompileUnavailable) {
				return nil, err
			}
			if errors.Is(err, ErrMaintenanceCompilePending) {
				terminal, e := dbgen.New(tx).ReadMaintenanceEvaluationBuildHealth(ctx, dbgen.ReadMaintenanceEvaluationBuildHealthParams{Kind: "compatibility", SloID: in.SloID, TargetRevision: mask.Revision, BuildID: mask.BuildID})
				if e != nil {
					return nil, e
				}
				if terminal == "" {
					for _, candidate := range candidates {
						h := candidate.Key.HourStart
						reusePending[h.UnixMicro()] = true
						*preparation = append(*preparation, maintenanceEvaluationPreparation{Kind: "compatibility", RequestedRevision: mask.Revision, BuildID: mask.BuildID, Hour: candidate.Key, Start: ceilSliBoundary(h, cadence), End: ceilSliBoundary(h.Add(time.Hour), cadence)})
					}
				}
			}
			// Missing historical compatibility disables this reuse route only.
			// Exact current-build preparation does not depend on that index.
			proofs = nil
		}
		for i, proof := range proofs {
			if proof.Kind != "changed" {
				r := candidates[i]
				populations[r.Key.HourStart.UnixMicro()] = r
				reused = append(reused, maintenanceReusedHour{Key: r.Key, ComputedRevision: r.ExclusionRevision, Proof: proof})
			} else {
				comparisons = append(comparisons, maintenanceEvaluationBuildPair{FirstBuild: candidates[i].Key.BuildID, SecondBuild: mask.BuildID})
			}
		}
	}
	comparisonStart := len(*preparation)
	if err := prepareEvaluationComparisons(ctx, tx, in.SloID, mask.Revision, comparisons, preparation); err != nil {
		return nil, err
	}
	pendingPairs := map[maintenanceEvaluationBuildPair]bool{}
	for _, request := range (*preparation)[comparisonStart:] {
		if request.Comparison != nil {
			pendingPairs[*request.Comparison] = true
		}
	}
	for _, candidate := range candidates {
		if pendingPairs[maintenanceEvaluationBuildPair{FirstBuild: candidate.Key.BuildID, SecondBuild: mask.BuildID}] {
			reusePending[candidate.Key.HourStart.UnixMicro()] = true
		}
	}
	health := dbgen.ReadMaintenanceEvaluationHourHealthParams{SloID: in.SloID, SliID: in.SliID, DefinitionRevision: pgtype.Int8{Int64: in.DefinitionRevision, Valid: true}, MeasurementRevision: pgtype.Int8{Int64: in.MeasurementRevision, Valid: true}, BuildID: mask.BuildID, TargetRevision: mask.Revision}
	var missing []time.Time
	for _, h := range hours {
		if _, ok := populations[h.UnixMicro()]; !ok {
			missing = append(missing, h)
			health.Hours = append(health.Hours, timestamp(h))
			health.Generations = append(health.Generations, sources[h.UnixMicro()].generation)
		}
	}
	if len(missing) > 0 {
		rows, err := dbgen.New(tx).ReadMaintenanceEvaluationHourHealth(ctx, health)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(missing) {
			return nil, slo.ErrOperationalFacts
		}
		for i, r := range rows {
			if r.Ordinal != int32(i+1) {
				return nil, slo.ErrOperationalFacts
			}
			h := missing[i]
			if r.Reason != "" {
				// An admitted comparison/index request can still recover an older
				// retained population when exact preparation lost its raw source.
				if reusePending[h.UnixMicro()] {
					continue
				}
				unavailable[h.UnixMicro()] = r.Reason
				continue
			}
			*preparation = append(*preparation, maintenanceEvaluationPreparation{Kind: "scoped_hour", RequestedRevision: mask.Revision, BuildID: mask.BuildID, Hour: maintenanceHourKey{SliID: in.SliID, SloID: in.SloID, BuildID: mask.BuildID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision, HourStart: h, SourceGeneration: sources[h.UnixMicro()].generation}})
		}
	}

	return reused, nil
}

// This is a request description, not a write or queue registration. A pending
// exact-union comparison never replaces the independent urgent hour request.
func prepareEvaluationComparisons(ctx context.Context, tx pgx.Tx, sloID pgtype.UUID, requested int64, pairs []maintenanceEvaluationBuildPair, preparation *[]maintenanceEvaluationPreparation) error {
	seen := map[maintenanceEvaluationBuildPair]bool{}
	var unique []maintenanceEvaluationBuildPair
	params := dbgen.ReadMaintenanceEvaluationComparisonsParams{SloID: sloID, TargetRevision: requested}
	for _, pair := range pairs {
		if !seen[pair] {
			seen[pair] = true
			unique = append(unique, pair)
			params.FirstBuilds = append(params.FirstBuilds, pair.FirstBuild)
			params.SecondBuilds = append(params.SecondBuilds, pair.SecondBuild)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	rows, err := dbgen.New(tx).ReadMaintenanceEvaluationComparisons(ctx, params)
	if err != nil {
		return err
	}
	if len(rows) != len(unique) {
		return ErrMaintenanceCompileUnavailable
	}
	for i, row := range rows {
		if row.Ordinal != int32(i+1) {
			return ErrMaintenanceCompileUnavailable
		}
		switch row.State {
		case "different", "equal", "cancelled", "unavailable", "exhausted":
			continue
		case "absent", "pending":
			*preparation = append(*preparation, maintenanceEvaluationPreparation{Kind: "comparison", RequestedRevision: requested, Comparison: new(unique[i])})
		default:
			return ErrMaintenanceCompileUnavailable
		}
	}
	return nil
}
