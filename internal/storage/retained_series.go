package storage

import (
	"context"
	"encoding/json/v2"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type retainedSloPoint struct {
	Current    api.SloOperationalValue  `json:"current"`
	Burn1h     api.SloBurnValue         `json:"burn_1h"`
	Burn6h     api.SloBurnValue         `json:"burn_6h"`
	Attainment api.SloOperationalSample `json:"attainment"`
}

func publishRetainedCurrent(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, value api.SloOperationalDetail, generation int64, now time.Time) error {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	interval, err := evaluateDerivedWindow(ctx, q, row.SliID, row.MeasurementRevision, slo.WindowInput{
		At: value.EffectiveAt, Now: now, Duration: cadence, Cadence: cadence,
		ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: now.Add(-sli.ObservationRetention), Target: sli.NormalizeDecimal(row.Target),
		FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds)),
	})
	if err != nil {
		return err
	}
	point := retainedSloPoint{Current: value.Current, Burn1h: value.Burn1h, Burn6h: value.Burn6h,
		Attainment: operationalSample(interval, value.EffectiveAt.Add(-cadence), value.EffectiveAt, value.Objective.DefinitionRevision)}
	point.Current.Window.RequestedStart = value.EffectiveAt.Add(-time.Duration(row.RollingWindowSeconds) * time.Second)
	payload, err := json.Marshal(point)
	if err != nil {
		return err
	}
	_, err = q.PublishRetainedSloPoint(ctx, dbgen.PublishRetainedSloPointParams{SloID: row.ID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(value.EffectiveAt),
		SourceGeneration: generation, PublishedAt: timestamp(now), Payload: payload})
	return err
}
