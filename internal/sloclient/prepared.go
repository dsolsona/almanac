package sloclient

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strconv"

	"github.com/dsolsona/almanac/internal/api"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Generated clients implement these operations; older injected management
// adapters need not acquire capabilities their consumers do not use.
type preparedOperations interface {
	GetSloCurrentStatus(context.Context, api.SloId, ...api.RequestEditorFn) (*http.Response, error)
	GetSloHistoryValues(context.Context, api.SloId, openapi_types.UUID, *api.GetSloHistoryValuesParams, ...api.RequestEditorFn) (*http.Response, error)
}

func validGeneration(value api.DerivedGeneration) bool {
	for _, revision := range []string{value.SourceGeneration, value.DefinitionRevision, value.MeasurementRevision, value.ExclusionRevision} {
		if _, err := strconv.ParseUint(revision, 10, 64); err != nil {
			return false
		}
	}
	return value.GenerationId != (openapi_types.UUID{}) && value.CalculationVersion != "" && !value.EvaluatedThrough.IsZero() && !value.PublishedAt.IsZero() && value.CalculationLagSeconds >= 0
}

func validCurrentStatusResponse(data []byte, id api.SloId) bool {
	var value api.SloCurrentStatus
	if !fields(data, "slo", "current", "burn_1h", "burn_6h", "urgency", "leading_reason", "reasons", "exhaustion", "generation") || json.Unmarshal(data, &value) != nil {
		return false
	}
	if value.Slo.Id != id || !validSlo(value.Slo) || !validGeneration(value.Generation) || !validOperationalValue(value.Current) || !validBurn(value.Burn1h, 3600) || !validBurn(value.Burn6h, 21600) || !validExhaustion(value.Exhaustion) || !validUrgency(value.LeadingReason) || len(value.Reasons) == 0 || len(value.Reasons) > 14 {
		return false
	}
	for _, reason := range value.Reasons {
		if !validUrgency(reason) {
			return false
		}
	}
	return value.Urgency == "needs_attention" || value.Urgency == "watch" || value.Urgency == "healthy"
}

func validHistoryValuesResponse(data []byte, input Input) bool {
	var page api.SloHistoryValuePage
	if !fields(data, "generation", "lane", "offset", "next_offset", "total_values", "samples", "days") || json.Unmarshal(data, &page) != nil || !validGeneration(page.Generation) || page.Generation.GenerationId.String() != input.GenerationID || string(page.Lane) != input.Lane || page.Offset != input.Offset || page.TotalValues < page.Offset || page.TotalValues > 1500 {
		return false
	}
	if len(page.Samples)+len(page.Days) != min(input.Limit, page.TotalValues-page.Offset) || (input.Lane == "compliance" && len(page.Samples) != 0) || (input.Lane != "compliance" && len(page.Days) != 0) {
		return false
	}
	next := page.Offset + input.Limit
	if next < page.TotalValues {
		if page.NextOffset == nil || *page.NextOffset != next {
			return false
		}
	} else if page.NextOffset != nil {
		return false
	}
	for _, sample := range page.Samples {
		if !validOperationalSample(sample) {
			return false
		}
	}
	for _, day := range page.Days {
		if day.SloId.String() != input.ID || day.DefinitionRevision != page.Generation.DefinitionRevision || day.DayEnd.IsZero() || day.Explanation == "" || !validOperationalQuality(day.Quality) || !validOperationalPerformance(day.Performance) {
			return false
		}
	}
	return true
}
