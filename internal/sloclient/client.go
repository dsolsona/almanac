// Package sloclient provides authenticated service and objective operations.
package sloclient

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
)

type Operations interface {
	ListServices(context.Context, *api.ListServicesParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateService(context.Context, *api.CreateServiceParams, api.CreateServiceJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetService(context.Context, api.ServiceId, ...api.RequestEditorFn) (*http.Response, error)
	PatchServiceMetadata(context.Context, api.ServiceId, *api.PatchServiceMetadataParams, api.PatchServiceMetadataJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	ListSlos(context.Context, *api.ListSlosParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateSlo(context.Context, *api.CreateSloParams, api.CreateSloJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetSlo(context.Context, api.SloId, ...api.RequestEditorFn) (*http.Response, error)
	PatchSloMetadata(context.Context, api.SloId, *api.PatchSloMetadataParams, api.PatchSloMetadataJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	EvaluateSlo(context.Context, api.SloId, *api.EvaluateSloParams, ...api.RequestEditorFn) (*http.Response, error)
	GetSloOperationalOverview(context.Context, *api.GetSloOperationalOverviewParams, ...api.RequestEditorFn) (*http.Response, error)
	GetSloOperationalDetail(context.Context, api.SloId, *api.GetSloOperationalDetailParams, ...api.RequestEditorFn) (*http.Response, error)
}
type Client struct {
	ops    Operations
	editor api.RequestEditorFn
}

func New(ops Operations, editor api.RequestEditorFn) *Client { return &Client{ops, editor} }

type Input struct {
	Kind, Action, ID, Cursor, ServiceID, Key, ETag string
	Query, Urgency                                 string
	Limit                                          int
	BurnWindowSeconds                              int
	Body                                           []byte
	At, TimelineStart, TimelineEnd, Start, End     *time.Time
}

func (c *Client) Do(ctx context.Context, in Input) ([]byte, string, error) {
	var response *http.Response
	var err error
	var id api.SloId
	if in.ID != "" {
		if json.Unmarshal([]byte(strconv.Quote(in.ID)), &id) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
	}
	limit := api.PageLimit(in.Limit)
	cursor := api.PageCursor(in.Cursor)
	var cursorPtr *api.PageCursor
	if in.Cursor != "" {
		cursorPtr = &cursor
	}
	editor := c.editor
	if in.Action == "create" || in.Action == "update" {
		editor = func(ctx context.Context, r *http.Request) error {
			if err := c.editor(ctx, r); err != nil {
				return err
			}
			r.GetBody = nil
			return nil
		}
	}
	switch in.Kind + " " + in.Action {
	case "services list":
		response, err = c.ops.ListServices(ctx, &api.ListServicesParams{Limit: &limit, Cursor: cursorPtr}, editor)
	case "services get":
		response, err = c.ops.GetService(ctx, id, editor)
	case "services create":
		var body api.ServiceCreateRequest
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.CreateService(ctx, &api.CreateServiceParams{IdempotencyKey: api.IdempotencyKey(in.Key)}, body, editor)
	case "services update":
		var body api.ResourceMetadataWrite
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.PatchServiceMetadata(ctx, id, &api.PatchServiceMetadataParams{IfMatch: api.ResourceIfMatch(in.ETag)}, body, editor)
	case "slos list":
		params := &api.ListSlosParams{Limit: &limit, Cursor: cursorPtr}
		if in.ServiceID != "" {
			var serviceID api.ServiceId
			if json.Unmarshal([]byte(strconv.Quote(in.ServiceID)), &serviceID) != nil {
				return nil, "", failure("invalid_request", 0, false)
			}
			params.ServiceId = &serviceID
		}
		response, err = c.ops.ListSlos(ctx, params, editor)
	case "slos get":
		response, err = c.ops.GetSlo(ctx, id, editor)
	case "slos create":
		var body api.SloCreateRequest
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.CreateSlo(ctx, &api.CreateSloParams{IdempotencyKey: api.IdempotencyKey(in.Key)}, body, editor)
	case "slos update":
		var body api.ResourceMetadataWrite
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.PatchSloMetadata(ctx, id, &api.PatchSloMetadataParams{IfMatch: api.ResourceIfMatch(in.ETag)}, body, editor)
	case "slos evaluate":
		response, err = c.ops.EvaluateSlo(ctx, id, &api.EvaluateSloParams{At: in.At, TimelineStart: in.TimelineStart, TimelineEnd: in.TimelineEnd}, editor)
	case "slos overview":
		params := &api.GetSloOperationalOverviewParams{Limit: &limit, Cursor: cursorPtr}
		if in.Query != "" {
			if !utf8.ValidString(in.Query) || utf8.RuneCountInString(in.Query) > 128 {
				return nil, "", failure("invalid_request", 0, false)
			}
			params.Q = &in.Query
		}
		if in.ServiceID != "" {
			var serviceID api.ServiceId
			if json.Unmarshal([]byte(strconv.Quote(in.ServiceID)), &serviceID) != nil {
				return nil, "", failure("invalid_request", 0, false)
			}
			params.ServiceId = &serviceID
		}
		if in.Urgency != "" {
			switch in.Urgency {
			case "needs_attention", "watch", "healthy":
				urgency := api.GetSloOperationalOverviewParamsUrgency(in.Urgency)
				params.Urgency = &urgency
			default:
				return nil, "", failure("invalid_request", 0, false)
			}
		}
		response, err = c.ops.GetSloOperationalOverview(ctx, params, editor)
	case "slos operational":
		burnWindowSeconds := in.BurnWindowSeconds
		if burnWindowSeconds == 0 {
			burnWindowSeconds = 3600
		}
		window := api.GetSloOperationalDetailParamsBurnWindowSeconds(burnWindowSeconds)
		params := &api.GetSloOperationalDetailParams{At: in.At, Start: in.Start, End: in.End, BurnWindowSeconds: &window}
		response, err = c.ops.GetSloOperationalDetail(ctx, id, params, editor)
	default:
		return nil, "", failure("invalid_request", 0, false)
	}
	mutation := in.Action == "create" || in.Action == "update"
	if err != nil || response == nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, "", failure("management_unavailable", 0, mutation)
	}
	defer response.Body.Close()
	responseLimit := 4 << 20
	if in.Action == "overview" || in.Action == "operational" {
		responseLimit = 20 << 20
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(responseLimit)+1))
	if err != nil || len(data) > responseLimit {
		return nil, "", failure("invalid_response", 0, mutation)
	}
	expected := http.StatusOK
	if in.Action == "create" {
		expected = http.StatusCreated
	}
	if response.StatusCode != expected {
		var problem struct {
			Code        string `json:"code"`
			RequestID   string `json:"request_id"`
			FieldErrors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"field_errors"`
		}
		_ = json.Unmarshal(data, &problem)
		code := problem.Code
		switch code {
		case "invalid_request", "unauthenticated", "unauthorized", "forbidden", "not_found", "conflict", "request_timeout", "request_canceled", "snapshot_expired", "resource_limit", "range_too_dense":
		default:
			code = "management_unavailable"
		}
		f := failure(code, response.StatusCode, mutation && response.StatusCode >= 500)
		if !f.Unknown && len(problem.FieldErrors) <= 16 {
			for _, item := range problem.FieldErrors {
				if guidance := fieldGuidance[item.Field+":"+item.Code]; guidance != "" {
					f.Message = guidance
					break
				}
			}
		}
		if requestIDPattern.MatchString(problem.RequestID) {
			f.RequestID = problem.RequestID
		}
		return nil, "", f
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil || !validResponse(data, in.Kind, in.Action, id) {
		return nil, "", failure("invalid_response", 0, mutation)
	}
	if in.Action == "get" || mutation {
		resourceID, okID := object["id"].(string)
		version, okVersion := object["version"].(string)
		kind := "slo"
		if in.Kind == "services" {
			kind = "service"
		}
		tag := response.Header.Get("ETag")
		if !okID || !okVersion || tag != `"`+kind+":"+resourceID+":"+version+`"` || in.ID != "" && resourceID != in.ID {
			return nil, "", failure("invalid_response", 0, mutation)
		}
		return data, tag, nil
	}
	return data, "", nil
}

var decimalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
var requestIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
var fieldGuidance = map[string]string{
	"target:target_invalid":                          "target must be at least 0 and less than 1",
	"rolling_window_seconds:window_invalid":          "window must be positive, within the supported limit and aligned to the SLI cadence",
	"evaluation_valid_from:historical_start_invalid": "historical start must be aligned, retained, within SLI validity and no later than activation; refresh the eligible range",
	"timeline_start:timeline_range_invalid":          "both timeline bounds must align, lie within the objective window and cover at most 1500 intervals",
	"at:evaluation_time_invalid":                     "evaluation time must be a valid UTC microsecond timestamp that is not in the future",
}

func fields(data []byte, names ...string) bool {
	var value map[string]jsontext.Value
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	for _, name := range names {
		if _, ok := value[name]; !ok {
			return false
		}
	}
	return true
}
func validDecimal(value *string) bool {
	return value == nil || len(*value) <= 128 && decimalPattern.MatchString(*value)
}
func validSlo(value api.Slo) bool {
	return value.Id.String() != "00000000-0000-0000-0000-000000000000" && value.ServiceId.String() != "00000000-0000-0000-0000-000000000000" && value.SliId.String() != "00000000-0000-0000-0000-000000000000" &&
		value.Name != "" && value.DisplayName != "" && value.Version != "" && value.DefinitionRevision != "" && value.ServiceDisplayName != "" && value.SliDisplayName != "" &&
		value.ActivatedAt.IsZero() == false && !value.EvaluationValidFrom.IsZero() && !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero() &&
		validDecimal(&value.Target) && len(value.Target) <= 128 && value.Target[0] != '-' && value.Target < "1" && value.RollingWindowSeconds > 0 && value.CadenceSeconds > 0 && value.RollingWindowSeconds%int64(value.CadenceSeconds) == 0
}
func validOperationalQuality(value api.SloOperationalQuality) bool {
	stateOK := false
	switch value.State {
	case "complete", "partial", "no_data", "zero_traffic", "expired_history", "not_yet_eligible", "not_applicable", "unavailable":
		stateOK = true
	}
	freshnessOK := value.Freshness == "unknown" || value.Freshness == "fresh" || value.Freshness == "stale"
	return stateOK && freshnessOK && value.ExpectedIntervals >= 0 && value.ObservedIntervals >= 0 && value.KnownEmptyIntervals >= 0 && value.MissingIntervals >= 0 && value.ExpiredIntervals >= 0 && value.ZeroTrafficIntervals >= 0 &&
		validDecimal(value.Coverage) && (value.Reason == nil || len(*value.Reason) <= 512)
}
func validOperationalPerformance(value api.SloEvaluationPerformance) bool {
	stateOK := value.State == "met" || value.State == "violated" || value.State == "unknown" || value.State == "not_applicable"
	observedOK := value.ObservedState == nil || *value.ObservedState == "met" || *value.ObservedState == "violated"
	return stateOK && observedOK && value.BudgetUnit == "events" && validDecimal(value.Good) && validDecimal(value.Total) && validDecimal(value.Bad) && validDecimal(value.Attainment) && validDecimal(value.AllowedBudget) && validDecimal(value.RemainingBudget) && validDecimal(value.ConsumedFraction) && validDecimal(value.RemainingFraction) && validDecimal(value.Burn) &&
		(value.Reason == nil || len(*value.Reason) <= 512)
}
func validOperationalValue(value api.SloOperationalValue) bool {
	w := value.Window
	return !value.EvaluationAt.IsZero() && !w.RequestedStart.IsZero() && !w.EffectiveStart.IsZero() && !w.End.IsZero() && !w.EligibleStart.IsZero() && !w.RetentionStart.IsZero() && !w.ActivatedAt.IsZero() && !w.EvaluationValidFrom.IsZero() &&
		validOperationalQuality(value.Quality) && validOperationalPerformance(value.Performance)
}
func validUrgency(value api.SloUrgencyReason) bool {
	switch value.Code {
	case "on_track", "stale_observations", "no_data", "expired_history", "complete_window_violation", "negative_observed_budget", "low_observed_budget", "high_1h_burn", "evaluation_unavailable", "not_yet_eligible", "zero_traffic", "incomplete_coverage", "short_burn_unavailable", "budget_below_half":
		return true
	default:
		return false
	}
}
func validBurn(value api.SloBurnValue, expected int) bool {
	return int(value.WindowSeconds) == expected && !value.Start.IsZero() && !value.End.IsZero() && value.End.After(value.Start) && validOperationalQuality(value.Quality) && validOperationalPerformance(value.Performance)
}
func validExhaustion(value api.SloExhaustionEstimate) bool {
	stateOK := value.State == "estimated" || value.State == "exhausted" || value.State == "unavailable"
	assumptionsOK := len(value.Assumptions) > 0 && len(value.Assumptions) <= 4
	for _, assumption := range value.Assumptions {
		switch assumption {
		case "freezes_current_allowed_budget", "continues_observed_one_hour_bad_event_rate", "rolling_window_expiry_can_change_result", "future_traffic_can_change_result":
		default:
			assumptionsOK = false
		}
	}
	return stateOK && assumptionsOK && validDecimal(value.RemainingEvents) && validDecimal(value.BadEventsPerHour) && validDecimal(value.HoursUntilExhaustion) && validOperationalQuality(value.CurrentQuality) && validOperationalQuality(value.OneHourQuality) && (value.Reason == nil || len(*value.Reason) <= 512)
}
func validOperationalSample(value api.SloOperationalSample) bool {
	stateOK := false
	switch value.State {
	case "observed", "zero_traffic", "known_empty", "missing", "expired", "not_applicable", "partial", "stale":
		stateOK = true
	}
	if !stateOK || value.BucketStart.IsZero() || value.BucketEnd.IsZero() || !value.BucketEnd.After(value.BucketStart) || value.EvaluationAt.IsZero() || value.Revision == "" || len(value.RetainedExtrema) > 4 || !validOperationalQuality(value.Quality) ||
		!validDecimal(value.Good) || !validDecimal(value.Total) || !validDecimal(value.Attainment) || !validDecimal(value.AllowedBudget) || !validDecimal(value.RemainingBudget) || !validDecimal(value.RemainingFraction) || !validDecimal(value.Burn) {
		return false
	}
	for _, extrema := range value.RetainedExtrema {
		switch extrema {
		case "first", "last", "minimum", "maximum":
		default:
			return false
		}
	}
	return true
}
func validOperationalOverview(data []byte) bool {
	var value api.SloOperationalOverviewPage
	if !fields(data, "snapshot_id", "items", "group_counts", "next_cursor", "previous_cursor") || json.Unmarshal(data, &value) != nil || value.SnapshotId.String() == "00000000-0000-0000-0000-000000000000" || value.Items == nil || len(value.Items) > 200 || len(value.NextCursor) > 512 || len(value.PreviousCursor) > 512 || value.GroupCounts.NeedsAttention < 0 || value.GroupCounts.Watch < 0 || value.GroupCounts.Healthy < 0 {
		return false
	}
	for _, item := range value.Items {
		if !validSlo(item.Slo) || item.Urgency != "needs_attention" && item.Urgency != "watch" && item.Urgency != "healthy" || !validUrgency(item.LeadingReason) || len(item.Reasons) > 14 || item.Reasons == nil || !validOperationalValue(item.Current) || !validBurn(item.Burn1h, 3600) || !validBurn(item.Burn6h, 21600) || !validExhaustion(item.Exhaustion) || item.BudgetSparkline == nil || len(item.BudgetSparkline) > 120 {
			return false
		}
		for _, reason := range item.Reasons {
			if !validUrgency(reason) {
				return false
			}
		}
		for _, sample := range item.BudgetSparkline {
			if !validOperationalSample(sample) {
				return false
			}
		}
	}
	return true
}
func validOperationalSeries(value api.SloOperationalSeries, kind string, burnWindow *api.SloOperationalSeriesBurnWindowSeconds) bool {
	if string(value.Kind) != kind || value.Samples == nil || len(value.Samples) > 1500 {
		return false
	}
	if (value.BurnWindowSeconds == nil) != (burnWindow == nil) || value.BurnWindowSeconds != nil && *value.BurnWindowSeconds != *burnWindow {
		return false
	}
	previous := time.Time{}
	for _, sample := range value.Samples {
		if !validOperationalSample(sample) || !previous.IsZero() && !sample.EvaluationAt.After(previous) {
			return false
		}
		previous = sample.EvaluationAt
	}
	return true
}

func validDailyCompliance(value api.SloDailyComplianceSeries, summary api.SloDailyComplianceSummary, slo api.Slo, start, end, effectiveAt time.Time, cadenceSeconds int) bool {
	if value.Kind != api.SloDailyComplianceSeriesKind("daily_compliance") || value.Days == nil || len(value.Days) > 365 ||
		summary.MetDays < 0 || summary.ViolatedDays < 0 || summary.EvaluableDays < 0 || summary.UnknownDays < 0 || summary.NotApplicableDays < 0 ||
		summary.EvaluableDays != summary.MetDays+summary.ViolatedDays || summary.MetDays+summary.ViolatedDays+summary.UnknownDays+summary.NotApplicableDays != len(value.Days) {
		return false
	}
	visibleEnd := end.UTC()
	completedThrough := time.Date(effectiveAt.UTC().Year(), effectiveAt.UTC().Month(), effectiveAt.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if visibleEnd.After(completedThrough) {
		visibleEnd = completedThrough
	}
	expected := make([]time.Time, 0, 365)
	dayEnd := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	for !dayEnd.After(visibleEnd) {
		expected = append(expected, dayEnd)
		dayEnd = dayEnd.Add(24 * time.Hour)
	}
	if len(expected) != len(value.Days) {
		return false
	}
	met, violated, unknown, notApplicable := 0, 0, 0, 0
	for index, item := range value.Days {
		dayStart := expected[index].Add(-24 * time.Hour)
		canonicalEnd := floorSloCadence(expected[index], cadenceSeconds)
		if item.SloId != slo.Id || item.DefinitionRevision != slo.DefinitionRevision || item.Target != slo.Target ||
			!item.DayStart.Equal(dayStart) || !item.DayEnd.Equal(expected[index]) || !item.RequestedAt.Equal(expected[index]) || !item.EffectiveAt.Equal(canonicalEnd) ||
			!item.UtcDate.Time.UTC().Equal(dayStart) || item.Explanation == "" || !validOperationalQuality(item.Quality) || !validOperationalPerformance(item.Performance) {
			return false
		}
		switch item.Status {
		case api.SloDailyComplianceDayStatusMet:
			met++
			if item.Quality.State != "complete" || item.Performance.State != "met" || item.Performance.Provisional {
				return false
			}
		case api.SloDailyComplianceDayStatusViolated:
			violated++
			if item.Quality.State != "complete" || item.Performance.State != "violated" || item.Performance.Provisional {
				return false
			}
		case api.SloDailyComplianceDayStatusUnknown:
			unknown++
			if item.Performance.State != "unknown" || !item.Performance.Provisional {
				return false
			}
		case api.SloDailyComplianceDayStatusNotApplicable:
			notApplicable++
		default:
			return false
		}
	}
	return summary.MetDays == met && summary.ViolatedDays == violated && summary.UnknownDays == unknown && summary.NotApplicableDays == notApplicable
}

func floorSloCadence(at time.Time, cadenceSeconds int) time.Time {
	cadenceMicros := int64(cadenceSeconds) * int64(time.Second/time.Microsecond)
	instant := at.UTC().UnixMicro()
	quotient := instant / cadenceMicros
	if instant < 0 && instant%cadenceMicros != 0 {
		quotient--
	}
	return time.UnixMicro(quotient * cadenceMicros).UTC()
}
func validOperationalDetail(data []byte, pathID api.SloId) bool {
	var value api.SloOperationalDetail
	if !fields(data, "evaluation_id", "requested_at", "effective_at", "calculation_version", "snapshot_kind", "objective", "context", "current", "urgency", "leading_reason", "reasons", "burn_1h", "burn_6h", "exhaustion", "range", "attainment_series", "budget_series", "burn_series", "compliance_series", "compliance_summary") || json.Unmarshal(data, &value) != nil || value.EvaluationId.String() == "00000000-0000-0000-0000-000000000000" || value.RequestedAt.IsZero() || value.EffectiveAt.IsZero() || value.CalculationVersion == "" || value.SnapshotKind != "current_operational_read" || value.Objective.Id != pathID || !validSlo(value.Objective) || !validOperationalValue(value.Current) || !validUrgency(value.LeadingReason) || value.Reasons == nil || !validExhaustion(value.Exhaustion) || !validBurn(value.Burn1h, 3600) || !validBurn(value.Burn6h, 21600) {
		return false
	}
	if value.Urgency != "needs_attention" && value.Urgency != "watch" && value.Urgency != "healthy" || len(value.Reasons) > 14 {
		return false
	}
	for _, reason := range value.Reasons {
		if !validUrgency(reason) {
			return false
		}
	}
	context := value.Context
	if context.SliId.String() == "00000000-0000-0000-0000-000000000000" || context.DatasourceId.String() == "00000000-0000-0000-0000-000000000000" || context.SliName == "" || context.SliDisplayName == "" || context.DatasourceDisplayName == "" || context.MeasurementRevision == "" || context.PollingIntervalSeconds <= 0 || context.SettlingDelaySeconds < 0 || context.GoodQuery == "" || context.TotalQuery == "" {
		return false
	}
	rangeValue := value.Range
	if rangeValue.RequestedStart.IsZero() || rangeValue.RequestedEnd.IsZero() || !rangeValue.RequestedEnd.After(rangeValue.RequestedStart) || rangeValue.EffectiveStart.IsZero() || rangeValue.EffectiveEnd.IsZero() || rangeValue.SourceCadenceSeconds <= 0 || rangeValue.ResolutionSeconds <= 0 || rangeValue.MaxSamplesPerSeries != 1500 || rangeValue.BucketBounds != "half_open" {
		return false
	}
	if rangeValue.EffectiveEnd.Before(rangeValue.EffectiveStart) {
		return false
	}
	validSeries := validOperationalSeries(value.AttainmentSeries, "attainment", nil) && validOperationalSeries(value.BudgetSeries, "rolling_budget", nil) && value.BurnSeries.BurnWindowSeconds != nil && validOperationalSeries(value.BurnSeries, "burn", value.BurnSeries.BurnWindowSeconds) && (*value.BurnSeries.BurnWindowSeconds == 3600 || *value.BurnSeries.BurnWindowSeconds == 21600) && validDailyCompliance(value.ComplianceSeries, value.ComplianceSummary, value.Objective, rangeValue.EffectiveStart, rangeValue.EffectiveEnd, value.EffectiveAt, rangeValue.SourceCadenceSeconds)
	if !validSeries {
		return false
	}
	for _, series := range [...]api.SloOperationalSeries{value.AttainmentSeries, value.BudgetSeries, value.BurnSeries} {
		for _, sample := range series.Samples {
			if sample.EvaluationAt.Before(rangeValue.EffectiveStart) || sample.EvaluationAt.After(rangeValue.EffectiveEnd) {
				return false
			}
		}
	}
	return true
}
func validResponse(data []byte, kind, action string, expectedID ...api.SloId) bool {
	if action == "overview" {
		return validOperationalOverview(data)
	}
	if action == "operational" {
		if len(expectedID) != 1 {
			return false
		}
		return validOperationalDetail(data, expectedID[0])
	}
	if action == "list" {
		var page struct {
			Items      []jsontext.Value `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		if !fields(data, "items", "next_cursor") || json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > 200 || len(page.NextCursor) > 512 {
			return false
		}
		for _, item := range page.Items {
			if !validResponse(item, kind, "get") {
				return false
			}
		}
		return true
	}
	if action != "evaluate" {
		if kind == "services" {
			var value api.Service
			return fields(data, "id", "name", "display_name", "version", "created_at", "updated_at") && json.Unmarshal(data, &value) == nil && value.Name != "" && value.DisplayName != "" && value.Version != "" && !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero()
		}
		var value api.Slo
		return fields(data, "id", "name", "display_name", "version", "definition_revision", "service_id", "service_display_name", "sli_id", "sli_display_name", "target", "rolling_window_seconds", "cadence_seconds", "activated_at", "evaluation_valid_from", "historical_requested_by", "historical_requested_at", "created_at", "updated_at") && json.Unmarshal(data, &value) == nil && value.Name != "" && value.DisplayName != "" && value.Version != "" && value.DefinitionRevision != "" && value.ServiceDisplayName != "" && value.SliDisplayName != "" && validDecimal(&value.Target) && value.CadenceSeconds > 0 && value.RollingWindowSeconds > 0 && !value.ActivatedAt.IsZero()
	}
	var value api.SloEvaluation
	var raw struct {
		Quality     jsontext.Value `json:"quality"`
		Performance jsontext.Value `json:"performance"`
		Timeline    jsontext.Value `json:"timeline"`
	}
	if !fields(data, "evaluation_id", "requested_at", "effective_end", "calculation_version", "snapshot_kind", "objective", "window", "quality", "performance", "timeline") || json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil ||
		!fields(raw.Quality, "state", "expected_intervals", "observed_intervals", "known_empty_intervals", "missing_intervals", "zero_traffic_intervals", "expired_intervals", "coverage", "first_observation_at", "last_observation_at", "latest_receipt_at", "freshness") ||
		!fields(raw.Performance, "state", "observed_state", "provisional", "reason", "good", "total", "bad", "attainment", "allowed_budget", "remaining_budget", "consumed_fraction", "remaining_fraction", "burn", "budget_unit") ||
		!fields(raw.Timeline, "start", "end", "cadence_seconds", "max_intervals", "items") {
		return false
	}
	q, p, t := value.Quality, value.Performance, value.Timeline
	if value.SnapshotKind != "current_evaluation" || value.RequestedAt.IsZero() || value.EffectiveEnd.IsZero() || value.Objective.Method != "occurrences" || value.Window.End.IsZero() || t.Start.IsZero() || t.End.IsZero() || t.CadenceSeconds <= 0 || t.MaxIntervals != 1500 || t.Items == nil || len(t.Items) > 1500 || q.ExpectedIntervals < 0 || q.ObservedIntervals < 0 || q.KnownEmptyIntervals < 0 || q.MissingIntervals < 0 || q.ZeroTrafficIntervals < 0 || q.ExpiredIntervals < 0 || !validDecimal(q.Coverage) || p.BudgetUnit != "events" {
		return false
	}
	for _, d := range []*string{p.Good, p.Total, p.Bad, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.ConsumedFraction, p.RemainingFraction, p.Burn} {
		if !validDecimal(d) {
			return false
		}
	}
	for _, item := range t.Items {
		if item.Start.IsZero() || item.End.IsZero() || !validDecimal(item.Good) || !validDecimal(item.Total) || !validDecimal(item.Attainment) || !validDecimal(item.Burn) {
			return false
		}
		switch item.State {
		case "observed", "zero_traffic", "known_empty", "missing", "expired", "not_applicable":
		default:
			return false
		}
	}
	return true
}
func failure(code string, status int, unknown bool) *datasourceclient.Failure {
	message := "The request could not be completed."
	if unknown {
		code = "outcome_unknown"
		message = "The command may have completed. Reconcile the record before retrying the same request."
	}
	return &datasourceclient.Failure{Code: code, Message: message, Status: status, Unknown: unknown}
}
