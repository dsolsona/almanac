package alerthttp

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
)

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value && value != "00000000-0000-0000-0000-000000000000"
}

func validQuery(query url.Values, allowed ...string) bool {
	for key, values := range query {
		if !slices.Contains(allowed, key) || len(values) != 1 || values[0] == "" {
			return false
		}
	}
	return true
}

func (h *Handler) readInput(r *http.Request, key string) (storage.ReadAlertInput, bool) {
	info := requestInfo(r)
	id := r.PathValue(key)
	if !validID(id) {
		return storage.ReadAlertInput{}, false
	}
	return storage.ReadAlertInput{Runtime: h.options.Runtime, Proof: info.proof, ID: id, Now: h.options.Now}, true
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	limit, cursor, valid := pageQuery(info.query, "q", "slo_id", "severity", "enabled")
	if !valid || !noReadBody(r) {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	input := storage.ListAlertPoliciesInput{Runtime: h.options.Runtime, Proof: info.proof, Now: h.options.Now, Limit: limit, Cursor: cursor, Search: info.query.Get("q"), SLOID: info.query.Get("slo_id"), Severity: info.query.Get("severity")}
	if info.query.Has("enabled") {
		value := info.query.Get("enabled")
		if value != "true" && value != "false" {
			problem(w, 400, "invalid_request", info.id)
			return
		}
		input.Enabled = new(value == "true")
	}
	page, err := storage.ListAlertPolicies(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, policyPageDTO(page))
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	input, valid := h.readInput(r, "policy_id")
	if !valid {
		problem(w, 404, "not_found", info.id)
		return
	}
	if !noReadBody(r) || len(info.query) != 0 {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	value, err := storage.ReadAlertPolicy(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	w.Header().Set("ETag", etag("alert-policy", value.ID, value.Version))
	writeJSON(w, 200, policyDTO(value))
}

func (h *Handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	limit, cursor, valid := pageQuery(info.query, "q", "slo_id", "service_id", "severity", "state", "scope", "group_by", "sort")
	if !valid || !noReadBody(r) {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	input := storage.ListAlertsInput{Runtime: h.options.Runtime, Proof: info.proof, Now: h.options.Now, Limit: limit, Cursor: cursor, GroupBy: cmp.Or(info.query.Get("group_by"), "none"), Sort: cmp.Or(info.query.Get("sort"), "newest"), Filter: storage.AlertFilter{Scope: cmp.Or(info.query.Get("scope"), "active"), SLOID: info.query.Get("slo_id"), ServiceID: info.query.Get("service_id"), Search: info.query.Get("q")}}
	if info.query.Has("severity") {
		input.Filter.Severities = strings.Split(info.query.Get("severity"), ",")
	}
	if info.query.Has("state") {
		input.Filter.States = strings.Split(info.query.Get("state"), ",")
	}
	page, err := storage.ListAlerts(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, alertPageDTO(page))
}

func (h *Handler) getAlert(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	input, valid := h.readInput(r, "occurrence_id")
	if !valid {
		problem(w, 404, "not_found", info.id)
		return
	}
	if !noReadBody(r) || len(info.query) != 0 {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	value, err := storage.ReadAlert(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	w.Header().Set("ETag", etag("alert-occurrence", input.ID, value.Version))
	writeJSON(w, 200, recordDTO(value))
}

func (h *Handler) evidence(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	input, valid := h.readInput(r, "occurrence_id")
	if !valid {
		problem(w, 404, "not_found", info.id)
		return
	}
	if !noReadBody(r) || !validQuery(info.query, "selection") {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	value, err := storage.ReadAlertEvidence(r.Context(), h.databaseURL, storage.ReadAlertEvidenceInput{ReadAlertInput: input, Selection: cmp.Or(info.query.Get("selection"), "latest")})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, evidenceDTO(value))
}

func (h *Handler) transitions(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	input, valid := h.readInput(r, "occurrence_id")
	if !valid {
		problem(w, 404, "not_found", info.id)
		return
	}
	limit, cursor, valid := pageQuery(info.query)
	if !noReadBody(r) || !valid {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	page, err := storage.ListAlertTransitions(r.Context(), h.databaseURL, storage.ListAlertTransitionsInput{ReadAlertInput: input, Limit: limit, Cursor: cursor})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, transitionPageDTO(page))
}

func (h *Handler) ranges(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	input, valid := h.readInput(r, "policy_id")
	if !valid {
		problem(w, 404, "not_found", info.id)
		return
	}
	limit, cursor, valid := pageQuery(info.query, "revision")
	revision, err := strconv.ParseInt(info.query.Get("revision"), 10, 64)
	if !noReadBody(r) || !valid || err != nil || revision < 1 || strconv.FormatInt(revision, 10) != info.query.Get("revision") {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	page, err := storage.ListAlertHistoryRanges(r.Context(), h.databaseURL, storage.ListAlertRangesInput{ReadAlertInput: input, Revision: revision, Limit: limit, Cursor: cursor})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, rangePageDTO(page))
}

func (h *Handler) processing(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	if !noReadBody(r) || !validQuery(info.query, "policy_id") || !validID(info.query.Get("policy_id")) {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	value, err := storage.ReadAlertProcessing(r.Context(), h.databaseURL, storage.ReadAlertInput{Runtime: h.options.Runtime, Proof: info.proof, ID: info.query.Get("policy_id"), Now: h.options.Now})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	writeJSON(w, 200, processingDTO(value))
}

func (h *Handler) commandResult(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	if !noReadBody(r) || !validQuery(info.query, "operation", "idempotency_key") {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	value, err := storage.ReadAlertCommand(r.Context(), h.databaseURL, storage.ReadAlertCommandInput{Runtime: h.options.Runtime, Proof: info.proof, Operation: info.query.Get("operation"), IdempotencyKey: info.query.Get("idempotency_key"), Now: h.options.Now})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	result := api.AlertCommandLookup{Status: "unknown"}
	if value.Found {
		result.Status = "expired"
		if !value.Expired && value.Result != nil {
			result.Status = "committed"
			result.Result = new(commandDTO(*value.Result))
		}
	}
	writeJSON(w, 200, result)
}
