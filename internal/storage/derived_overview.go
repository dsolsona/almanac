package storage

import (
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func generationStamp(id pgtype.UUID, source, definition, measurement int64, version string, at, published, retention time.Time) api.DerivedGeneration {
	return api.DerivedGeneration{GenerationId: openapi_types.UUID(id.Bytes), SourceGeneration: strconv.FormatInt(source, 10), DefinitionRevision: strconv.FormatInt(definition, 10),
		MeasurementRevision: strconv.FormatInt(measurement, 10), ExclusionRevision: "0", CalculationVersion: version, EvaluatedThrough: at.UTC(), PublishedAt: published.UTC(), RetentionStart: new(retention.UTC()), CalculationLagSeconds: 0}
}

func overviewScopeFilters(input OperationalOverviewInput) (pgtype.UUID, pgtype.Text) {
	var serviceID pgtype.UUID
	if input.ServiceID != "" {
		serviceID, _ = canonicalUUID(input.ServiceID)
	}
	return serviceID, pgtype.Text{String: input.Urgency, Valid: input.Urgency != ""}
}

func unavailableOverviewItem(previous api.SloOperationalOverviewItem, now time.Time, reason string) api.SloOperationalOverviewItem {
	objective := previous.Slo
	at := floorSliBoundary(now, time.Duration(objective.CadenceSeconds)*time.Second)
	start := at.Add(-time.Duration(objective.RollingWindowSeconds) * time.Second)
	eligible := maxTime(start, objective.EvaluationValidFrom)
	if eligible.After(at) {
		eligible = at
	}
	history := api.SloOperationalOverviewItemBudgetHistoryState("preparing")
	if reason != "current_calculation_pending" {
		history = "unavailable"
	}
	value := slo.WindowResult{End: at, RequestedStart: start, EffectiveStart: start, EligibleStart: eligible,
		RetentionStart: maxTime(eligible, now.Add(-sli.ObservationRetention)), State: "unavailable", Freshness: "unknown", Performance: "unknown", Reason: reason, Provisional: true}
	current := operationalValue(value)
	current.Window.ActivatedAt, current.Window.EvaluationValidFrom = objective.ActivatedAt, objective.EvaluationValidFrom
	current.Window.HistoricalRequestedBy, current.Window.HistoricalRequestedAt = objective.HistoricalRequestedBy, objective.HistoricalRequestedAt
	burn1h, burn6h := unavailableBurn(3600, at, reason), unavailableBurn(21600, at, reason)
	urgency := slo.ClassifyOperational(slo.OperationalState{QualityState: "unavailable", Freshness: "unknown", PerformanceState: "unknown", Provisional: true})
	reasons, leading := operationalReasons(urgency.Reasons)
	return api.SloOperationalOverviewItem{Slo: objective, CurrentState: new(api.SloOperationalOverviewItemCurrentState("unavailable")), Current: current, Burn1h: burn1h, Burn6h: burn6h,
		Urgency: api.SloOperationalOverviewItemUrgency(urgency.Group), Reasons: reasons, LeadingReason: leading,
		Exhaustion: exhaustionValue(value, burn1h, nil), BudgetHistoryState: &history,
		BudgetSparkline: previous.BudgetSparkline, BudgetHistoryGeneration: previous.BudgetHistoryGeneration}
}
