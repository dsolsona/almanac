package alerthttp

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
)

func parseSeriesQuery(query url.Values) (storage.ReadAlertSeriesInput, bool) {
	input := storage.ReadAlertSeriesInput{ConditionID: query.Get("condition_id"), Limit: 120}
	_, _, ok := pageQuery(query, "condition_id", "start", "end")
	if !ok || query.Has("cursor") || strings.TrimSpace(input.ConditionID) == "" || len(input.ConditionID) > 64 {
		return input, false
	}
	for _, r := range input.ConditionID {
		if r < 32 || r > 126 {
			return input, false
		}
	}
	if query.Has("limit") {
		input.Limit, _ = strconv.Atoi(query.Get("limit"))
	}
	if input.Limit < 2 || input.Limit > 120 {
		return input, false
	}
	for key, target := range map[string]*time.Time{"start": &input.Start, "end": &input.End} {
		if !query.Has(key) {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, query.Get(key))
		if err != nil || value.IsZero() || !strings.HasSuffix(query.Get(key), "Z") || value.Year() < 1 || value.Year() > 9999 {
			return input, false
		}
		*target = value
	}
	if !input.Start.IsZero() && !input.End.IsZero() && (input.Start.After(input.End) || input.End.Sub(input.Start) > 24*time.Hour) {
		return input, false
	}
	return input, true
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	read, ok := h.readInput(r, "occurrence_id")
	if !ok {
		problem(w, 404, "not_found", info.id)
		return
	}
	input, ok := parseSeriesQuery(info.query)
	if !ok || !noReadBody(r) {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	input.ReadAlertInput = read
	value, err := storage.ReadAlertSeries(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, seriesDTO(value))
}

func seriesDTO(value storage.AlertSeriesRecord) api.AlertSeries {
	points := make([]api.AlertSeriesPoint, 0, len(value.Points))
	for _, point := range value.Points {
		points = append(points, api.AlertSeriesPoint{EvaluationId: wireID(point.EvaluationID), Snapshot: snapshotDTO(point.Snapshot), ScheduledAt: point.ScheduledAt.UTC(), CommittedAt: point.CommittedAt.UTC(), Condition: conditionResultsDTO([]alert.ConditionResult{point.Condition}, value.Objective.Target)[0]})
	}
	gaps := make([]api.AlertSeriesGap, 0, len(value.Gaps))
	for _, gap := range value.Gaps {
		gaps = append(gaps, api.AlertSeriesGap{Start: gap.Start.UTC(), End: gap.End.UTC(), StartInclusive: gap.StartInclusive, EndInclusive: gap.EndInclusive, Basis: api.AlertSeriesGapBasis(gap.Basis), Kind: api.AlertSeriesGapKind(gap.Kind), Reason: gap.Reason, RangeId: optionalID(gap.RangeID)})
	}
	return api.AlertSeries{PolicyId: wireID(value.PolicyID), EvaluationRevision: strconv.FormatInt(value.Revision, 10), Objective: api.AlertObjective{Target: value.Objective.Target, CadenceSeconds: int64(value.Objective.Cadence / time.Second), DefinitionValidFrom: value.Objective.DefinitionValidFrom.UTC(), MeasurementValidFrom: value.Objective.MeasurementValidFrom.UTC(), RetentionStart: value.Objective.RetentionStart.UTC()}, Condition: conditionDTO(value.Condition), Start: value.Start.UTC(), End: value.End.UTC(), Points: points, Gaps: gaps, Sampling: api.AlertSeriesSampling{Total: value.Sampling.Total, Returned: value.Sampling.Returned, Reduced: value.Sampling.Reduced, OmittedExtrema: value.Sampling.OmittedExtrema, Method: api.AlertSeriesSamplingMethod(value.Sampling.Method)}}
}
