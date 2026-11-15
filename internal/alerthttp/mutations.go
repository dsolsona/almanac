package alerthttp

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
)

var policyLimits = alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}

func (h *Handler) commandInput(r *http.Request) storage.AlertCommandInput {
	info := requestInfo(r)
	return storage.AlertCommandInput{Runtime: h.options.Runtime, Proof: info.proof, IdempotencyKey: r.Header.Get("Idempotency-Key"), RequestID: info.id, Now: h.options.Now, ReceiptLifetime: 24 * time.Hour}
}

func mutationHeaders(r *http.Request, kind, id string) (int64, bool) {
	if !printableHeader(r, "Idempotency-Key") || len(r.Header.Values("If-None-Match")) != 0 || len(requestInfo(r).query) != 0 {
		return 0, false
	}
	if id == "" {
		return 0, len(r.Header.Values("If-Match")) == 0
	}
	return parseETag(r.Header.Values("If-Match"), kind, id)
}

func readConditions(values []api.AlertCondition) ([]alert.Condition, string) {
	conditions := make([]alert.Condition, 0, len(values))
	for _, value := range values {
		// Check before multiplying untrusted int64 values into time.Duration.
		if value.ShortWindowSeconds < 60 || value.ShortWindowSeconds > 2592000 || value.LongWindowSeconds < 60 || value.LongWindowSeconds > 2592000 {
			return nil, "conditions"
		}
		conditions = append(conditions, alert.Condition{ID: value.Id, Short: time.Duration(value.ShortWindowSeconds) * time.Second, Long: time.Duration(value.LongWindowSeconds) * time.Second, Threshold: value.Threshold})
	}
	return conditions, validateConditions(conditions)
}

func (h *Handler) createPolicy(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	if _, valid := mutationHeaders(r, "", ""); !valid {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	var body api.AlertPolicyWrite
	if status := decodeBody(r, &body); status != 0 {
		problem(w, status, "invalid_request", info.id)
		return
	}
	if !validName(body.Name) {
		fieldProblem(w, info.id, "name", "name_invalid", "Enter a policy name of at most 256 characters without control characters.")
		return
	}
	severity := "medium"
	if body.Severity != nil {
		severity = string(*body.Severity)
	}
	if !slices.Contains([]string{"low", "medium", "high"}, severity) {
		fieldProblem(w, info.id, "severity", "severity_invalid", "Choose low, medium or high severity.")
		return
	}
	conditions, field := readConditions(body.Conditions)
	if field != "" {
		fieldProblem(w, info.id, field, "condition_invalid", "Use positive exact thresholds and paired whole-minute windows, with one to eight unique conditions.")
		return
	}
	result, err := storage.CreateAlertPolicy(r.Context(), h.databaseURL, storage.CreateAlertPolicyInput{AlertCommandInput: h.commandInput(r), Name: body.Name, SLOID: body.SloId.String(), Severity: severity, Enabled: body.Enabled, Conditions: conditions, Limits: policyLimits, ConfirmNon30DayPresets: body.ConfirmNon30DayPresets != nil && *body.ConfirmNon30DayPresets})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	w.Header().Set("Location", "/api/v1/alert-policies/"+result.Policy.ID)
	h.writeCommand(w, 201, result)
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	id := r.PathValue("policy_id")
	if !validID(id) {
		problem(w, 404, "not_found", info.id)
		return
	}
	version, valid := mutationHeaders(r, "alert-policy", id)
	if !valid {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	var body api.AlertPolicyPatch
	if status := decodeBody(r, &body); status != 0 {
		problem(w, status, "invalid_request", info.id)
		return
	}
	if body.Name == nil && body.SloId == nil && body.Severity == nil && body.Conditions == nil {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	input := storage.UpdateAlertPolicyInput{AlertCommandInput: h.commandInput(r), ID: id, ExpectedVersion: version, Name: body.Name, Limits: policyLimits, Reason: "Policy updated", ConfirmNon30DayPresets: body.ConfirmNon30DayPresets != nil && *body.ConfirmNon30DayPresets}
	if body.Name != nil && !validName(*body.Name) {
		fieldProblem(w, info.id, "name", "name_invalid", "Enter a policy name of at most 256 characters without control characters.")
		return
	}
	if body.Severity != nil {
		if !slices.Contains([]string{"low", "medium", "high"}, string(*body.Severity)) {
			fieldProblem(w, info.id, "severity", "severity_invalid", "Choose low, medium or high severity.")
			return
		}
		input.Severity = new(string(*body.Severity))
	}
	if body.SloId != nil {
		input.SLOID = new(body.SloId.String())
	}
	if body.Reason != nil {
		input.Reason = cmp.Or(strings.TrimSpace(*body.Reason), input.Reason)
	}
	if body.Conditions != nil {
		conditions, field := readConditions(*body.Conditions)
		if field != "" {
			fieldProblem(w, info.id, field, "condition_invalid", "Use positive exact thresholds and paired whole-minute windows, with one to eight unique conditions.")
			return
		}
		input.Conditions = &conditions
	}
	result, err := storage.UpdateAlertPolicy(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	h.writeCommand(w, 200, result)
}

func (h *Handler) policyCommand(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	id := r.PathValue("policy_id")
	if !validID(id) {
		problem(w, 404, "not_found", info.id)
		return
	}
	version, valid := mutationHeaders(r, "alert-policy", id)
	if !valid {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	var body api.AlertPolicyCommand
	if status := decodeBody(r, &body); status != 0 {
		problem(w, status, "invalid_request", info.id)
		return
	}
	reason := ""
	if body.Reason != nil {
		reason = strings.TrimSpace(*body.Reason)
	}
	var result storage.AlertCommandResult
	var err error
	switch r.PathValue("action") {
	case "enable":
		result, err = storage.EnableAlertPolicy(r.Context(), h.databaseURL, storage.EnableAlertPolicyInput{AlertCommandInput: h.commandInput(r), ID: id, ExpectedVersion: version, Reason: cmp.Or(reason, "Policy enabled"), Limits: policyLimits})
	case "disable":
		result, err = storage.DisableAlertPolicy(r.Context(), h.databaseURL, storage.DisableAlertPolicyInput{AlertCommandInput: h.commandInput(r), ID: id, ExpectedVersion: version, Reason: cmp.Or(reason, "Policy disabled")})
	case "retry":
		result, err = storage.RetryAlertPolicy(r.Context(), h.databaseURL, storage.RetryAlertPolicyInput{AlertCommandInput: h.commandInput(r), ID: id, ExpectedVersion: version})
	default:
		problem(w, 404, "not_found", info.id)
		return
	}
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	h.writeCommand(w, 200, result)
}

func (h *Handler) acknowledge(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	id := r.PathValue("occurrence_id")
	if !validID(id) {
		problem(w, 404, "not_found", info.id)
		return
	}
	version, valid := mutationHeaders(r, "alert-occurrence", id)
	if !valid {
		problem(w, 400, "invalid_request", info.id)
		return
	}
	var body api.AlertAcknowledge
	if status := decodeBody(r, &body); status != 0 {
		problem(w, status, "invalid_request", info.id)
		return
	}
	result, err := storage.AcknowledgeAlert(r.Context(), h.databaseURL, storage.AcknowledgeAlertInput{AlertCommandInput: h.commandInput(r), ID: id, ExpectedVersion: version})
	if err != nil {
		storageError(w, err, info.id)
		return
	}
	h.writeCommand(w, 200, result)
}

func (h *Handler) writeCommand(w http.ResponseWriter, status int, result storage.AlertCommandResult) {
	if result.Policy != nil {
		w.Header().Set("ETag", etag("alert-policy", result.Policy.ID, result.Policy.Version))
	} else if result.Alert != nil {
		w.Header().Set("ETag", etag("alert-occurrence", result.Alert.Occurrence.ID, result.Alert.Version))
	}
	writeJSON(w, status, commandDTO(result))
}
