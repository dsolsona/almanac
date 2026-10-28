// Package slihttp exposes session-authenticated SLI management and
// credential-authenticated agent observation ingestion.
package slihttp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/agentaccess"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/google/uuid"
)

const (
	managementBodyLimit     = 64 << 10
	observationsBodyLimit   = 4 << 20
	maxObservationItemBytes = 1 << 20
	maxObservationItems     = 1000
)

type Options struct {
	Now       func() time.Time
	Preflight func(context.Context) error
}

type Handler struct {
	databaseURL string
	secrets     *sourcesecret.Adapter
	auth        *authhttp.Handler
	agents      *agentaccess.Service
	now         func() time.Time
	preflight   func(context.Context) error
}

func New(databaseURL string, secrets *sourcesecret.Adapter, auth *authhttp.Handler, agents *agentaccess.Service, options Options) (*Handler, error) {
	if databaseURL == "" || auth == nil || agents == nil || options.Now == nil {
		return nil, errors.New("slihttp: invalid options")
	}
	return &Handler{databaseURL: databaseURL, secrets: secrets, auth: auth, agents: agents, now: options.Now, preflight: options.Preflight}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	requestID, err := uuid.NewRandom()
	if err != nil {
		h.writeProblem(w, http.StatusServiceUnavailable, "sli_unavailable", "")
		return
	}
	id := requestID.String()
	w.Header().Set("X-Request-ID", id)
	path := r.URL.EscapedPath()
	if path != r.URL.Path || strings.ContainsAny(path, "%\\") || strings.Contains(path, "/../") || strings.Contains(path, "/./") {
		h.writeProblem(w, http.StatusNotFound, "not_found", id)
		return
	}
	if path == "/api/agent/v1/observations" {
		h.ingest(w, r, id)
		return
	}
	if h.preflight != nil {
		if err := h.preflight(r.Context()); err != nil {
			h.writeProblem(w, http.StatusServiceUnavailable, "sli_unavailable", id)
			return
		}
	}
	authorized, err := h.auth.ValidateSessionRequest(r)
	if err != nil {
		h.writeAuthError(w, err, id)
		return
	}
	verifier, err := identity.SessionSecretVerifier(authorized.SessionSecret)
	if err != nil {
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", id)
		return
	}
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", id)
		return
	}
	if path == "/api/v1/slis" {
		h.collection(w, r, id, proof, query)
		return
	}
	const prefix = "/api/v1/slis/"
	if !strings.HasPrefix(path, prefix) {
		h.writeProblem(w, http.StatusNotFound, "not_found", id)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 1 && canonicalID(parts[0]) {
		h.detail(w, r, id, proof, parts[0])
		return
	}
	if len(parts) == 2 && canonicalID(parts[0]) && parts[1] == "observations" {
		h.history(w, r, id, proof, parts[0], query)
		return
	}
	h.writeProblem(w, http.StatusNotFound, "not_found", id)
}

func (h *Handler) collection(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, query url.Values) {
	switch r.Method {
	case http.MethodGet:
		limit, cursor, valid := pageQuery(query)
		if !valid || hasBody(r) || hasMutationHeaders(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		page, err := storage.ListSlis(r.Context(), h.databaseURL, proof, limit, cursor, h.now)
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		items := make([]api.Sli, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, sliDTO(item))
		}
		h.writeJSON(w, http.StatusOK, api.SliPage{Items: items, NextCursor: page.NextCursor})
	case http.MethodPost:
		if len(query) != 0 || r.URL.ForceQuery || len(r.Header.Values("If-Match")) != 0 || len(r.Header.Values("If-None-Match")) != 0 ||
			!singlePrintableHeader(r.Header, "Idempotency-Key", 1, 128) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		body, status := readBody(w, r, managementBodyLimit)
		if status != 0 {
			h.bodyError(w, status, requestID)
			return
		}
		var request api.SliCreateRequest
		if json.Unmarshal(body, &request, json.RejectUnknownMembers(true)) != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		result, err := storage.CreateSli(r.Context(), h.databaseURL, storage.CreateSliInput{
			Proof: proof, Secrets: h.secrets, Name: request.Name, DisplayName: request.DisplayName,
			DatasourceID: request.DatasourceId.String(), MeasurementKind: sli.MeasurementKind(request.MeasurementKind),
			ComponentMode: sli.ComponentMode(request.ComponentMode), GoodQuery: request.GoodQuery,
			TotalQuery: request.TotalQuery, IdempotencyKey: r.Header.Get("Idempotency-Key"), RequestID: requestID, Now: h.now,
		})
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.Header().Set("Location", "/api/v1/slis/"+result.ID)
		w.Header().Set("ETag", sliETag(result.ID, result.Version))
		h.writeJSON(w, http.StatusCreated, sliDTO(result))
	default:
		w.Header().Set("Allow", "GET, POST")
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
	}
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, sliID string) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if hasBody(r) || hasConditionalHeaders(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		value, err := storage.GetSli(r.Context(), h.databaseURL, proof, sliID, h.now)
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.Header().Set("ETag", sliETag(value.ID, value.Version))
		h.writeJSON(w, http.StatusOK, sliDTO(value))
	case http.MethodPatch:
		version, valid := parseSliETag(r.Header.Values("If-Match"), sliID)
		if !valid || len(r.Header.Values("If-None-Match")) != 0 || len(r.Header.Values("Idempotency-Key")) != 0 || !jsonContent(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		body, status := readBody(w, r, managementBodyLimit)
		if status != 0 {
			h.bodyError(w, status, requestID)
			return
		}
		var request api.SliMetadataWrite
		if json.Unmarshal(body, &request, json.RejectUnknownMembers(true)) != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		value, err := storage.UpdateSliMetadata(r.Context(), h.databaseURL, storage.UpdateSliMetadataInput{
			Proof: proof, SliID: sliID, ExpectedVersion: version, DisplayName: request.DisplayName, RequestID: requestID, Now: h.now,
		})
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.Header().Set("ETag", sliETag(value.ID, value.Version))
		h.writeJSON(w, http.StatusOK, sliDTO(value))
	default:
		w.Header().Set("Allow", "GET, PATCH")
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
	}
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, sliID string, query url.Values) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	limit, cursor, valid := pageQuery(query)
	if !valid || r.URL.ForceQuery || hasBody(r) || hasConditionalHeaders(r) {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	page, err := storage.ListSliObservations(r.Context(), h.databaseURL, proof, sliID, limit, cursor, h.now)
	if err != nil {
		h.writeStorageError(w, err, requestID)
		return
	}
	items := make([]api.SliObservation, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, observationDTO(item))
	}
	missing := make([]api.SliMissingInterval, 0, len(page.MissingIntervals))
	for _, interval := range page.MissingIntervals {
		missing = append(missing, api.SliMissingInterval{IntervalStart: interval.IntervalStart, IntervalEnd: interval.IntervalEnd})
	}
	h.writeJSON(w, http.StatusOK, api.SliObservationPage{Items: items, NextCursor: page.NextCursor,
		EarliestQueryableIntervalStart: page.EarliestQueryableIntervalStart, InspectedStart: page.InspectedStart, InspectedEnd: page.InspectedEnd, MissingIntervals: missing})
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != r.URL.Path || len(r.Header.Values("Cookie")) != 0 ||
		len(r.Header.Values("Origin")) != 0 || h.auth.ValidateTLSRequest(r) != nil {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	if !bearerHeader(r.Header.Values("Authorization")) {
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		return
	}
	if _, err := h.agents.Authenticate(r.Context(), bearerToken(r.Header.Get("Authorization"))); err != nil {
		if errors.Is(err, agentaccess.ErrInvalidCredential) {
			h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		} else {
			h.writeProblem(w, http.StatusServiceUnavailable, "observation_unavailable", requestID)
		}
		return
	}
	body, status := readBody(w, r, observationsBodyLimit)
	if status != 0 {
		h.bodyError(w, status, requestID)
		return
	}
	var envelope struct {
		Items []jsontext.Value `json:"items"`
	}
	if json.Unmarshal(body, &envelope, json.RejectUnknownMembers(true)) != nil || len(envelope.Items) == 0 || len(envelope.Items) > maxObservationItems {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	incomingItems := make([]api.ObservationBatchItem, len(envelope.Items))
	for index, rawItem := range envelope.Items {
		if len(rawItem) > maxObservationItemBytes || json.Unmarshal(rawItem, &incomingItems[index], json.RejectUnknownMembers(true)) != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
	}
	results := make([]api.ObservationItemResult, 0, len(incomingItems))
	token := bearerToken(r.Header.Get("Authorization"))
	for _, incoming := range incomingItems {
		item := incomingObservation(incoming)
		var result storage.SliIngestResult
		if item.DeliveryID == "" {
			result = storage.SliIngestResult{DeliveryID: incoming.DeliveryId.String(), Status: "rejected", ErrorCode: "invalid_observation"}
		} else if incomingErr := validateOutcome(incoming.Outcome); incomingErr != nil {
			result = storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected", ErrorCode: "invalid_observation"}
		} else {
			ingested, ingestErr := h.agents.IngestObservation(r.Context(), token, item)
			result = ingested
			if errors.Is(ingestErr, agentaccess.ErrInvalidCredential) {
				result = storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected", ErrorCode: "invalid_credential"}
			} else if ingestErr != nil {
				result = storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "retryable", ErrorCode: "temporarily_unavailable"}
			}
		}
		results = append(results, ingestResultDTO(result))
	}
	h.writeJSON(w, http.StatusOK, api.ObservationBatchResponse{Items: results})
}

func incomingObservation(value api.ObservationBatchItem) sli.ObservationItem {
	item := sli.ObservationItem{DeliveryID: value.DeliveryId.String(), ExecutionID: value.ExecutionId.String(), CreatedAt: value.CreatedAt,
		LeaseID: value.LeaseId, ConfigurationRevision: value.ConfigurationRevision, SessionGeneration: value.SessionGeneration,
		SLIID: value.SliId.String(), MeasurementRevision: value.MeasurementRevision,
		IntervalStart: value.IntervalStart, IntervalEnd: value.IntervalEnd}
	item.Outcome = decodeOutcome(value.Outcome)
	return item
}

func decodeOutcome(value api.ObservationExecutionOutcome) sli.ExecutionOutcome {
	discriminator, err := value.Discriminator()
	if err != nil {
		return sli.ExecutionOutcome{}
	}
	raw, err := value.MarshalJSON()
	if err != nil {
		return sli.ExecutionOutcome{}
	}
	switch discriminator {
	case "observed":
		var outcome api.ObservedOutcome
		if json.Unmarshal(raw, &outcome, json.RejectUnknownMembers(true)) != nil || outcome.State != api.ObservedOutcomeStateObserved {
			return sli.ExecutionOutcome{}
		}
		return sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: outcome.GoodCount, TotalCount: outcome.TotalCount}
	case "complete_empty":
		var outcome api.CompleteEmptyOutcome
		if json.Unmarshal(raw, &outcome, json.RejectUnknownMembers(true)) != nil || outcome.State != api.CompleteEmptyOutcomeStateCompleteEmpty {
			return sli.ExecutionOutcome{}
		}
		return sli.ExecutionOutcome{State: sli.OutcomeCompleteEmpty}
	case "incomplete":
		var outcome api.IncompleteOutcome
		if json.Unmarshal(raw, &outcome, json.RejectUnknownMembers(true)) != nil || outcome.State != api.IncompleteOutcomeStateIncomplete {
			return sli.ExecutionOutcome{}
		}
		missing := make([]string, 0, len(outcome.MissingComponents))
		for _, component := range outcome.MissingComponents {
			missing = append(missing, string(component))
		}
		return sli.ExecutionOutcome{State: sli.OutcomeIncomplete, MissingComponents: missing}
	case "failed":
		var outcome api.FailedOutcome
		if json.Unmarshal(raw, &outcome, json.RejectUnknownMembers(true)) != nil || outcome.State != api.FailedOutcomeStateFailed {
			return sli.ExecutionOutcome{}
		}
		return sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: string(outcome.ErrorCode)}
	default:
		return sli.ExecutionOutcome{}
	}
}

func validateOutcome(value api.ObservationExecutionOutcome) error {
	return decodeOutcome(value).Validate()
}

func ingestResultDTO(value storage.SliIngestResult) api.ObservationItemResult {
	result := api.ObservationItemResult{Status: api.ObservationItemResultStatus(value.Status)}
	if id, err := uuid.Parse(value.DeliveryID); err == nil {
		result.DeliveryId = id
	}
	if value.ErrorCode != "" {
		code := value.ErrorCode
		result.ErrorCode = &code
	}
	if value.ObservationID != "" {
		if id, err := uuid.Parse(value.ObservationID); err == nil {
			result.ObservationId = &id
		}
	}
	if value.Status == "accepted" || value.Status == "duplicate" || value.ObservationID != "" {
		age := value.ReplayAgeSeconds
		result.ReplayAgeSeconds = &age
	}
	return result
}

func sliDTO(value storage.SliRecord) api.Sli {
	return api.Sli{Id: uuid.MustParse(value.ID), Name: value.Name, DisplayName: value.DisplayName,
		Version: strconv.FormatInt(value.Version, 10), MeasurementRevision: strconv.FormatInt(value.MeasurementRevision, 10),
		DatasourceId: uuid.MustParse(value.DatasourceID), DatasourceVersion: strconv.FormatInt(value.DatasourceVersion, 10),
		AgentId: uuid.MustParse(value.AgentID), AgentReachability: api.SliAgentReachability(value.AgentReachability),
		CollectionProgress: api.SliCollectionProgress(value.CollectionProgress), MeasurementKind: value.MeasurementKind,
		ComponentMode: value.ComponentMode, GoodQuery: value.GoodQuery, TotalQuery: value.TotalQuery,
		PollingIntervalSeconds: value.PollingIntervalSeconds, SettlingDelaySeconds: value.SettlingDelaySeconds,
		EffectiveFrom: value.EffectiveFrom, ActivationState: api.SliActivationState(value.ActivationState), Freshness: api.SliFreshness(value.Freshness),
		LatestObservation: optionalObservation(value.LatestObservation), LastAttemptAt: value.LastAttemptAt,
		LastObservationAt: value.LastObservationAt, NextExpectedExecutionAt: value.NextExpectedExecutionAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func optionalObservation(value *storage.SliObservationRecord) *api.SliObservation {
	if value == nil {
		return nil
	}
	result := observationDTO(*value)
	return &result
}

func observationDTO(value storage.SliObservationRecord) api.SliObservation {
	result := api.SliObservation{ObservationId: uuid.MustParse(value.ObservationID), MeasurementRevision: strconv.FormatInt(value.MeasurementRevision, 10),
		IntervalStart: value.IntervalStart, IntervalEnd: value.IntervalEnd, Outcome: api.SliObservationOutcome(value.Outcome),
		GoodCount: value.GoodCount, TotalCount: value.TotalCount, MissingComponents: make([]api.SliObservationMissingComponents, 0, len(value.MissingComponents)), RecordedAt: value.RecordedAt}
	for _, component := range value.MissingComponents {
		result.MissingComponents = append(result.MissingComponents, api.SliObservationMissingComponents(component))
	}
	if value.ErrorCode != nil {
		code := api.SliObservationErrorCode(*value.ErrorCode)
		result.ErrorCode = &code
	}
	return result
}

func pageQuery(query url.Values) (int, string, bool) {
	for key := range query {
		if key != "limit" && key != "cursor" {
			return 0, "", false
		}
	}
	limit := 50
	if raw, exists := query["limit"]; exists {
		if len(raw) != 1 || raw[0] == "" {
			return 0, "", false
		}
		parsed, err := strconv.Atoi(raw[0])
		if err != nil || strconv.Itoa(parsed) != raw[0] || parsed < 1 || parsed > 200 {
			return 0, "", false
		}
		limit = parsed
	}
	cursor := ""
	if raw, exists := query["cursor"]; exists {
		if len(raw) != 1 || len(raw[0]) > 512 {
			return 0, "", false
		}
		cursor = raw[0]
	}
	return limit, cursor, true
}

func (h *Handler) writeStorageError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, storage.ErrInvalidSession):
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
	case errors.Is(err, storage.ErrAgentForbidden):
		h.writeProblem(w, http.StatusForbidden, "forbidden", requestID)
	case errors.Is(err, storage.ErrSliNotFound), errors.Is(err, storage.ErrDatasourceNotFound):
		h.writeProblem(w, http.StatusNotFound, "not_found", requestID)
	case errors.Is(err, storage.ErrSliConflict), errors.Is(err, storage.ErrDatasourceConflict), errors.Is(err, storage.ErrDatasourceMeasurementConflict):
		h.writeProblem(w, http.StatusConflict, "conflict", requestID)
	case errors.Is(err, storage.ErrSliUnsupported):
		h.writeProblem(w, http.StatusUnprocessableEntity, "unsupported_measurement", requestID)
	case errors.Is(err, storage.ErrSliAgentUnavailable):
		h.writeProblem(w, http.StatusUnprocessableEntity, "agent_unavailable", requestID)
	case errors.Is(err, storage.ErrSliInvalid), errors.Is(err, storage.ErrInvalidAgentInput):
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
	case errors.Is(err, storage.ErrSourceSecretUnavailable):
		h.writeProblem(w, http.StatusServiceUnavailable, "source_secret_unavailable", requestID)
	default:
		h.writeProblem(w, http.StatusServiceUnavailable, "sli_unavailable", requestID)
	}
}

func (h *Handler) writeAuthError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, authhttp.ErrMissingSession), errors.Is(err, authhttp.ErrSessionRequest):
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
	case errors.Is(err, authhttp.ErrAuthorization):
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
	default:
		h.writeProblem(w, http.StatusForbidden, "forbidden", requestID)
	}
}

func (h *Handler) bodyError(w http.ResponseWriter, status int, requestID string) {
	code := "invalid_request"
	if status == http.StatusUnsupportedMediaType {
		code = "unsupported_media_type"
	} else if status == http.StatusRequestEntityTooLarge {
		code = "request_too_large"
	} else if status == http.StatusRequestTimeout {
		code = "request_timeout"
	} else if status == http.StatusServiceUnavailable {
		code = "sli_unavailable"
	}
	h.writeProblem(w, status, code, requestID)
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, value)
}

func (h *Handler) writeProblem(w http.ResponseWriter, status int, code, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}, Message: "SLI request failed", RequestId: requestID})
}

func readBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, int) {
	if len(r.Header.Values("Content-Encoding")) != 0 || !jsonContent(r) {
		return nil, http.StatusUnsupportedMediaType
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, http.StatusRequestTimeout
		}
		return nil, http.StatusBadRequest
	}
	if len(body) > int(maxBytes) {
		return nil, http.StatusRequestEntityTooLarge
	}
	return body, 0
}

func jsonContent(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	media, params, err := mime.ParseMediaType(values[0])
	return err == nil && media == "application/json" && (len(params) == 0 || len(params) == 1 && strings.EqualFold(params["charset"], "utf-8"))
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func hasBody(r *http.Request) bool { return r.ContentLength > 0 || len(r.TransferEncoding) != 0 }

func hasConditionalHeaders(r *http.Request) bool {
	return len(r.Header.Values("If-Match")) != 0 || len(r.Header.Values("If-None-Match")) != 0 || len(r.Header.Values("Idempotency-Key")) != 0
}

func hasMutationHeaders(r *http.Request) bool { return hasConditionalHeaders(r) }

func singlePrintableHeader(header http.Header, name string, min, max int) bool {
	values := header.Values(name)
	if len(values) != 1 || len(values[0]) < min || len(values[0]) > max {
		return false
	}
	for _, value := range values[0] {
		if value < 33 || value > 126 {
			return false
		}
	}
	return true
}

func parseSliETag(values []string, id string) (int64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	value := values[0]
	prefix := `"sli:` + id + ":"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	version, err := strconv.ParseInt(raw, 10, 64)
	return version, err == nil && version > 0 && strconv.FormatInt(version, 10) == raw
}

func sliETag(id string, version int64) string {
	return `"sli:` + id + ":" + strconv.FormatInt(version, 10) + `"`
}

func bearerHeader(values []string) bool {
	if len(values) != 1 || len(values[0]) > 2048 {
		return false
	}
	scheme, token, found := strings.Cut(values[0], " ")
	return found && strings.EqualFold(scheme, "Bearer") && token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func bearerToken(value string) string {
	_, token, _ := strings.Cut(value, " ")
	return token
}
