// Package slohttp exposes service and occurrence objective management.
package slohttp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/google/uuid"
)

type Handler struct {
	runtime     *storage.Runtime
	databaseURL string
	auth        *authhttp.Handler
	now         func() time.Time
	preflight   func(context.Context) error
}

func New(databaseURL string, auth *authhttp.Handler, now func() time.Time, preflight func(context.Context) error, runtimes ...*storage.Runtime) (*Handler, error) {
	if databaseURL == "" || auth == nil || now == nil {
		return nil, errors.New("slohttp: invalid options")
	}
	if len(runtimes) > 1 {
		return nil, errors.New("slohttp: invalid runtime options")
	}
	var runtime *storage.Runtime
	if len(runtimes) == 1 {
		runtime = runtimes[0]
	}
	return &Handler{runtime: runtime, databaseURL: databaseURL, auth: auth, now: now, preflight: preflight}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	requestID, err := uuid.NewRandom()
	if err != nil {
		problem(w, 503, "unavailable", "")
		return
	}
	reqID := requestID.String()
	w.Header().Set("X-Request-ID", reqID)
	if r.URL.EscapedPath() != r.URL.Path || strings.ContainsAny(r.URL.Path, "%\\") {
		problem(w, 404, "not_found", reqID)
		return
	}
	if h.preflight != nil {
		if err = h.preflight(r.Context()); err != nil {
			problem(w, 503, "unavailable", reqID)
			return
		}
	}
	authorized, err := h.auth.ValidateSessionRequest(r)
	if err != nil {
		if errors.Is(err, authhttp.ErrMissingSession) || errors.Is(err, authhttp.ErrSessionRequest) {
			problem(w, 401, "unauthorized", reqID)
		} else {
			problem(w, 403, "forbidden", reqID)
		}
		return
	}
	verifier, err := identity.SessionSecretVerifier(authorized.SessionSecret)
	if err != nil {
		problem(w, 401, "unauthorized", reqID)
		return
	}
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery {
		problem(w, 400, "invalid_request", reqID)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	if len(parts) == 0 || (parts[0] != "services" && parts[0] != "slos") {
		problem(w, 404, "not_found", reqID)
		return
	}
	isSlo := parts[0] == "slos"
	if len(parts) == 2 && isSlo && parts[1] == "overview" {
		h.operationalOverview(w, r, reqID, proof, query)
		return
	}
	if len(parts) == 1 {
		h.collection(w, r, reqID, proof, query, isSlo)
		return
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || id.String() != parts[1] || id == uuid.Nil {
		problem(w, 404, "not_found", reqID)
		return
	}
	if len(parts) == 3 && isSlo && parts[2] == "evaluation" {
		h.evaluate(w, r, reqID, proof, parts[1], query)
		return
	}
	if len(parts) == 3 && isSlo && parts[2] == "status" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			problem(w, 405, "method_not_allowed", reqID)
			return
		}
		if len(query) != 0 || hasBody(r) || hasConditional(r) {
			problem(w, 400, "invalid_request", reqID)
			return
		}
		value, err := storage.ReadCurrentSloStatus(r.Context(), h.databaseURL, storage.CurrentSloInput{Runtime: h.runtime, Proof: proof, ID: parts[1], Now: h.now})
		if err != nil {
			storageError(w, err, reqID, parts[1])
			return
		}
		write(w, 200, value)
		return
	}
	if len(parts) == 3 && isSlo && parts[2] == "preparation" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			problem(w, 405, "method_not_allowed", reqID)
			return
		}
		if len(query) != 0 || hasBody(r) || hasConditional(r) {
			problem(w, 400, "invalid_request", reqID)
			return
		}
		value, err := storage.ReadSloPreparation(r.Context(), h.databaseURL, storage.CurrentSloInput{Runtime: h.runtime, Proof: proof, ID: parts[1], Now: h.now})
		if err != nil {
			storageError(w, err, reqID)
			return
		}
		write(w, 200, value)
		return
	}
	if len(parts) == 3 && isSlo && parts[2] == "operational" {
		h.operationalDetail(w, r, reqID, proof, parts[1], query)
		return
	}
	if len(parts) == 5 && isSlo && parts[2] == "history" && parts[4] == "values" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			problem(w, 405, "method_not_allowed", reqID)
			return
		}
		if hasBody(r) || hasConditional(r) {
			problem(w, 400, "invalid_request", reqID)
			return
		}
		for key, values := range query {
			if (key != "lane" && key != "offset" && key != "limit") || len(values) != 1 {
				problem(w, 400, "invalid_request", reqID)
				return
			}
		}
		offset, limit := 0, 50
		for key, target := range map[string]*int{"offset": &offset, "limit": &limit} {
			if query.Has(key) {
				value, err := strconv.Atoi(query.Get(key))
				if err != nil {
					problem(w, 400, "invalid_request", reqID)
					return
				}
				*target = value
			}
		}
		page, err := storage.ReadSloHistoryValues(r.Context(), h.databaseURL, storage.SloHistoryValuesInput{Runtime: h.runtime,
			Proof: proof, ID: parts[1], GenerationID: parts[3], Lane: query.Get("lane"), Offset: offset, Limit: limit, Now: h.now})
		if err != nil {
			storageError(w, err, reqID)
			return
		}
		write(w, 200, page)
		return
	}
	if len(parts) == 5 && isSlo && parts[2] == "preparation" && parts[4] == "retry" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			problem(w, 405, "method_not_allowed", reqID)
			return
		}
		if len(query) != 0 || hasBody(r) || hasConditional(r) {
			problem(w, 400, "invalid_request", reqID)
			return
		}
		err := storage.RetrySloPreparation(r.Context(), h.databaseURL, storage.RetrySloPreparationInput{
			Runtime: h.runtime, Proof: proof, SloID: parts[1], JobID: parts[3], RequestID: reqID, Now: h.now,
		})
		if err != nil {
			storageError(w, err, reqID)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if len(parts) != 2 {
		problem(w, 404, "not_found", reqID)
		return
	}
	h.detail(w, r, reqID, proof, parts[1], query, isSlo)
}

func (h *Handler) operationalOverview(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, q url.Values) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	if hasBody(r) || hasConditional(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	input := storage.OperationalOverviewInput{Runtime: h.runtime, Proof: proof, Now: h.now}
	for key, values := range q {
		if len(values) != 1 {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		value := values[0]
		switch key {
		case "q":
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Query = value
		case "service_id":
			input.ServiceID = value
		case "urgency":
			input.Urgency = value
		case "limit":
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 200 || strconv.Itoa(limit) != value {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Limit = limit
		case "cursor":
			if value == "" || len(value) > 512 {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Cursor = value
		default:
			problem(w, 400, "invalid_request", requestID)
			return
		}
	}
	result, err := storage.ReadSloOperationalOverview(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, requestID)
		return
	}
	write(w, 200, result)
}

func (h *Handler) operationalDetail(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, id string, q url.Values) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	if hasBody(r) || hasConditional(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	input := storage.OperationalDetailInput{Runtime: h.runtime, Proof: proof, ID: id, Now: h.now, BurnWindow: 3600}
	seen := make(map[string]bool, len(q))
	for key, values := range q {
		if len(values) != 1 || values[0] == "" || seen[key] {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		seen[key] = true
		value := values[0]
		switch key {
		case "at", "start", "end":
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				fieldProblem(w, requestID, key, "time_invalid", "Use a UTC RFC3339 time with microsecond precision.")
				return
			}
			switch key {
			case "at":
				input.At = &parsed
			case "start":
				input.Start = &parsed
			case "end":
				input.End = &parsed
			}
		case "burn_window_seconds":
			seconds, err := strconv.Atoi(value)
			if err != nil || strconv.Itoa(seconds) != value || seconds != 3600 && seconds != 21600 {
				fieldProblem(w, requestID, key, "burn_window_invalid", "Choose 3600 or 21600 seconds.")
				return
			}
			input.BurnWindow = seconds
		default:
			problem(w, 400, "invalid_request", requestID)
			return
		}
	}
	result, err := storage.ReadSloOperationalDetail(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, requestID, id)
		return
	}
	write(w, 200, result)
}

func (h *Handler) collection(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, q url.Values, isSlo bool) {
	switch r.Method {
	case http.MethodGet:
		if hasBody(r) || hasConditional(r) {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		limit, cursor, filter, ok := pageQuery(q, isSlo)
		if !ok {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		page, err := storage.ListObjectives(r.Context(), h.databaseURL, proof, limit, cursor, filter, h.now, isSlo)
		if err != nil {
			storageError(w, err, requestID)
			return
		}
		if isSlo {
			items := make([]api.Slo, 0, len(page.Slos))
			for _, item := range page.Slos {
				items = append(items, sloDTO(item))
			}
			write(w, 200, api.SloPage{Items: items, NextCursor: page.NextCursor})
		} else {
			items := make([]api.Service, 0, len(page.Services))
			for _, item := range page.Services {
				items = append(items, serviceDTO(item))
			}
			write(w, 200, api.ServicePage{Items: items, NextCursor: page.NextCursor})
		}
	case http.MethodPost:
		if len(q) != 0 || len(r.Header.Values("If-Match")) != 0 || len(r.Header.Values("If-None-Match")) != 0 || !printableHeader(r.Header, "Idempotency-Key") {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		body, status := readBody(w, r)
		if status != 0 {
			problem(w, status, "invalid_request", requestID)
			return
		}
		input := storage.CreateObjectiveInput{Proof: proof, IdempotencyKey: r.Header.Get("Idempotency-Key"), RequestID: requestID, Now: h.now}
		if isSlo {
			var req api.SloCreateRequest
			if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Name = req.Name
			input.DisplayName = req.DisplayName
			input.ServiceID = req.ServiceId.String()
			input.SliID = req.SliId.String()
			input.Target = req.Target
			input.RollingWindowSeconds = req.RollingWindowSeconds
			input.EvaluationValidFrom = req.EvaluationValidFrom
		} else {
			var req api.ServiceCreateRequest
			if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Name = req.Name
			input.DisplayName = req.DisplayName
		}
		service, objective, err := storage.CreateObjective(r.Context(), h.databaseURL, input, isSlo)
		if err != nil {
			storageError(w, err, requestID)
			return
		}
		if isSlo {
			w.Header().Set("Location", "/api/v1/slos/"+objective.ID)
			w.Header().Set("ETag", etag("slo", objective.ID, objective.Version))
			write(w, 201, sloDTO(objective))
		} else {
			w.Header().Set("Location", "/api/v1/services/"+service.ID)
			w.Header().Set("ETag", etag("service", service.ID, service.Version))
			write(w, 201, serviceDTO(service))
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		problem(w, 405, "method_not_allowed", requestID)
	}
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, id string, q url.Values, isSlo bool) {
	if len(q) != 0 {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	kind := "service"
	if isSlo {
		kind = "slo"
	}
	var service storage.ServiceRecord
	var objective storage.SloRecord
	var err error
	switch r.Method {
	case http.MethodGet:
		if hasBody(r) || hasConditional(r) {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		service, objective, err = storage.ReadObjective(r.Context(), h.databaseURL, proof, id, h.now, isSlo)
	case http.MethodPatch:
		version, valid := parseETag(r.Header.Values("If-Match"), kind, id)
		if !valid || len(r.Header.Values("If-None-Match")) != 0 || len(r.Header.Values("Idempotency-Key")) != 0 {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		body, status := readBody(w, r)
		if status != 0 {
			problem(w, status, "invalid_request", requestID)
			return
		}
		var req api.ResourceMetadataWrite
		if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		service, objective, err = storage.UpdateObjectiveMetadata(r.Context(), h.databaseURL, proof, id, req.DisplayName, requestID, version, h.now, isSlo)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	if err != nil {
		storageError(w, err, requestID)
		return
	}
	if isSlo {
		w.Header().Set("ETag", etag(kind, id, objective.Version))
		write(w, 200, sloDTO(objective))
	} else {
		w.Header().Set("ETag", etag(kind, id, service.Version))
		write(w, 200, serviceDTO(service))
	}
}

func (h *Handler) evaluate(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, id string, q url.Values) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	if hasBody(r) || hasConditional(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	input := storage.EvaluateSloInput{Proof: proof, ID: id, Now: h.now}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		t, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil {
			if key == "at" {
				fieldProblem(w, requestID, "at", "evaluation_time_invalid", "Use a UTC microsecond time no later than now.")
			} else if key == "timeline_start" || key == "timeline_end" {
				fieldProblem(w, requestID, "timeline_start", "timeline_range_invalid", "Supply both aligned bounds within the window, at most 1500 intervals.")
			} else {
				problem(w, 400, "invalid_request", requestID)
			}
			return
		}
		switch key {
		case "at":
			input.At = &t
		case "timeline_start":
			input.TimelineStart = &t
		case "timeline_end":
			input.TimelineEnd = &t
		default:
			problem(w, 400, "invalid_request", requestID)
			return
		}
	}
	result, err := storage.EvaluateSlo(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, requestID)
		return
	}
	write(w, 200, result)
}

func serviceDTO(v storage.ServiceRecord) api.Service {
	return api.Service{Id: uuid.MustParse(v.ID), Name: v.Name, DisplayName: v.DisplayName, Version: strconv.FormatInt(v.Version, 10), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func sloDTO(v storage.SloRecord) api.Slo {
	r := api.Slo{Id: uuid.MustParse(v.ID), Name: v.Name, DisplayName: v.DisplayName, Version: strconv.FormatInt(v.Version, 10), DefinitionRevision: strconv.FormatInt(v.DefinitionRevision, 10), ServiceId: uuid.MustParse(v.ServiceID), ServiceDisplayName: v.ServiceDisplayName, SliId: uuid.MustParse(v.SliID), SliDisplayName: v.SliDisplayName, Target: v.Target, RollingWindowSeconds: v.RollingWindowSeconds, CadenceSeconds: int(v.CadenceSeconds), ActivatedAt: v.ActivatedAt, EvaluationValidFrom: v.EvaluationValidFrom, HistoricalRequestedAt: v.HistoricalRequestedAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	if v.HistoricalRequestedBy != nil {
		r.HistoricalRequestedBy = new(uuid.MustParse(*v.HistoricalRequestedBy))
	}
	return r
}
func pageQuery(q url.Values, isSlo bool) (int, string, string, bool) {
	limit := 0
	cursor, filter := "", ""
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return 0, "", "", false
		}
		switch key {
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 200 || strconv.Itoa(n) != values[0] {
				return 0, "", "", false
			}
			limit = n
		case "cursor":
			cursor = values[0]
		case "service_id":
			if !isSlo {
				return 0, "", "", false
			}
			filter = values[0]
		default:
			return 0, "", "", false
		}
	}
	return limit, cursor, filter, true
}
func hasBody(r *http.Request) bool { return r.ContentLength > 0 || len(r.TransferEncoding) > 0 }
func hasConditional(r *http.Request) bool {
	return len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("If-None-Match")) > 0 || len(r.Header.Values("Idempotency-Key")) > 0
}
func printableHeader(h http.Header, key string) bool {
	v := h.Values(key)
	if len(v) != 1 || len(v[0]) < 1 || len(v[0]) > 128 {
		return false
	}
	for _, c := range v[0] {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func etag(kind, id string, version int64) string {
	return `"` + kind + ":" + id + ":" + strconv.FormatInt(version, 10) + `"`
}
func parseETag(values []string, kind, id string) (int64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	prefix := `"` + kind + ":" + id + ":"
	if !strings.HasPrefix(values[0], prefix) || !strings.HasSuffix(values[0], `"`) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(values[0], prefix), `"`)
	version, err := strconv.ParseInt(raw, 10, 64)
	return version, err == nil && version > 0 && strconv.FormatInt(version, 10) == raw
}
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		return nil, 415
	}
	media, params, err := mime.ParseMediaType(values[0])
	if err != nil || media != "application/json" || !(len(params) == 0 || len(params) == 1 && strings.EqualFold(params["charset"], "utf-8")) {
		return nil, 415
	}
	controller := http.NewResponseController(w)
	if err = controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, 503
	}
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil {
		return nil, 400
	}
	if len(body) > 64<<10 {
		return nil, 413
	}
	return body, 0
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, value)
}
func problem(w http.ResponseWriter, status int, code, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: "Service or SLO request failed", RequestId: requestID, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}})
}
func fieldProblem(w http.ResponseWriter, requestID, field, code, message string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.MarshalWrite(w, api.Problem{Code: "invalid_request", Message: message, RequestId: requestID, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{{Code: code, Field: field, Message: message}}})
}
func storageError(w http.ResponseWriter, err error, id string, objectiveIDs ...string) {
	if errors.Is(err, storage.ErrDerivedPending) || errors.Is(err, storage.ErrDerivedFailed) {
		state, code, message := "pending", "derived_data_pending", "SLO results are being prepared."
		retry := 2
		if errors.Is(err, storage.ErrDerivedFailed) {
			state, code, message, retry = "failed", "derived_data_failed", "SLO preparation failed. An administrator or editor can retry the preparation.", 0
		} else {
			w.Header().Set("Retry-After", "2")
		}
		preparation := api.DerivedPreparation{State: api.DerivedPreparationState(state), RetryAfterSeconds: retry}
		if len(objectiveIDs) == 1 {
			path := "/api/v1/slos/" + objectiveIDs[0] + "/preparation"
			preparation.DetailsUrl = &path
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(503)
		_ = json.MarshalWrite(w, api.Problem{Code: code, Message: message, RequestId: id, Preparation: &preparation, FieldErrors: []struct {
			Code    string `json:"code"`
			Field   string `json:"field"`
			Message string `json:"message"`
		}{}})
		return
	}
	switch {
	case errors.Is(err, storage.ErrOperationalSnapshot):
		problem(w, 400, "invalid_request", id)
	case errors.Is(err, storage.ErrOperationalSnapshotOld):
		problemMessage(w, 409, "snapshot_expired", "This operational overview snapshot expired. Restart pagination with the current filters.", id)
	case errors.Is(err, storage.ErrOperationalResource):
		problemMessage(w, 503, "resource_limit", "The operational read exceeded its bounded resource limit. Narrow the name or service filter and retry.", id)
	case errors.Is(err, storage.ErrOperationalRangeDense):
		problemMessage(w, 422, "range_too_dense", "This history range has too many gaps or extrema to represent safely. Request a narrower range.", id)
	case errors.Is(err, storage.ErrOperationalRange):
		fieldProblem(w, id, "start", "operational_range_invalid", "Use paired UTC cadence-aligned bounds spanning no more than 365 days, ending no later than at.")
	case errors.Is(err, storage.ErrObjectiveTarget):
		fieldProblem(w, id, "target", "target_invalid", "Enter a target from 0 up to, but not including, 1.")
	case errors.Is(err, storage.ErrObjectiveWindow):
		fieldProblem(w, id, "rolling_window_seconds", "window_invalid", "Use a positive window no greater than 2147483647 seconds and divisible by the SLI cadence.")
	case errors.Is(err, storage.ErrObjectiveHistoricalStart):
		fieldProblem(w, id, "evaluation_valid_from", "historical_start_invalid", "Choose an aligned start within retained SLI history, no later than activation.")
	case errors.Is(err, storage.ErrObjectiveTimelineRange):
		fieldProblem(w, id, "timeline_start", "timeline_range_invalid", "Supply both aligned bounds within the window, at most 1500 intervals.")
	case errors.Is(err, storage.ErrObjectiveEvaluationTime):
		fieldProblem(w, id, "at", "evaluation_time_invalid", "Use a UTC microsecond time no later than now.")
	case errors.Is(err, storage.ErrInvalidSession):
		problem(w, 401, "unauthorized", id)
	case errors.Is(err, storage.ErrAgentForbidden):
		problem(w, 403, "forbidden", id)
	case errors.Is(err, storage.ErrObjectiveNotFound):
		problem(w, 404, "not_found", id)
	case errors.Is(err, storage.ErrObjectiveConflict):
		problem(w, 409, "conflict", id)
	case errors.Is(err, storage.ErrObjectiveInvalid), errors.Is(err, storage.ErrInvalidAgentInput):
		problem(w, 400, "invalid_request", id)
	default:
		problem(w, 503, "unavailable", id)
	}
}

func problemMessage(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: message, RequestId: requestID, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}})
}
