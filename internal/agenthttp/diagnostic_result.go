package agenthttp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/agentaccess"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/google/uuid"
)

const minSeparateDiagnosticResultBytes = 64 << 10

type DiagnosticResultCompleter interface {
	CompleteDiagnostic(context.Context, string, int64, agentprotocol.DiagnosticResult) error
}

type DiagnosticResultHandler struct {
	completer DiagnosticResultCompleter
	auth      *authhttp.Handler
	clock     func() time.Time
}

func NewDiagnosticResultHandler(completer DiagnosticResultCompleter, auth *authhttp.Handler, clock func() time.Time) (*DiagnosticResultHandler, error) {
	if completer == nil || auth == nil || clock == nil {
		return nil, errors.New("agenthttp: invalid diagnostic result handler")
	}
	return &DiagnosticResultHandler{completer: completer, auth: auth, clock: clock}, nil
}

func (h *DiagnosticResultHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	requestID, err := newRequestID()
	if err != nil {
		writeDiagnosticResultProblem(w, http.StatusServiceUnavailable, "diagnostic_result_unavailable", "")
		return
	}
	w.Header().Set("X-Request-ID", requestID)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDiagnosticResultProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	diagnosticID, ok := diagnosticResultPathID(r.URL.Path)
	if !ok || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != r.URL.Path ||
		h.auth.ValidateTLSRequest(r) != nil || len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Cookie")) != 0 {
		writeDiagnosticResultProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	mediaType, _, contentTypeErr := mime.ParseMediaType(firstHeader(contentTypes))
	if len(contentTypes) != 1 || contentTypeErr != nil || mediaType != "application/json" {
		writeDiagnosticResultProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", requestID)
		return
	}
	authorization := r.Header.Values("Authorization")
	if len(authorization) != 1 || len(authorization[0]) > 2048 {
		writeDiagnosticResultProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		return
	}
	scheme, token, bearer := strings.Cut(authorization[0], " ")
	if !bearer || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		writeDiagnosticResultProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		return
	}
	generation, validGeneration := parseAgentSessionGeneration(r.Header.Values("X-Agent-Session-Generation"))
	if !validGeneration {
		writeDiagnosticResultProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	if r.ContentLength > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		writeDiagnosticResultProblem(w, http.StatusRequestEntityTooLarge, "result_too_large", requestID)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, agentprotocol.MaxHTTPSDiagnosticResultBytes+1))
	if err != nil {
		writeDiagnosticResultProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	if len(body) > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		writeDiagnosticResultProblem(w, http.StatusRequestEntityTooLarge, "result_too_large", requestID)
		return
	}
	if len(body) <= minSeparateDiagnosticResultBytes {
		writeDiagnosticResultProblem(w, http.StatusBadRequest, "result_transport_not_required", requestID)
		return
	}
	var result agentprotocol.DiagnosticResult
	if json.Unmarshal(body, &result, json.RejectUnknownMembers(true)) != nil || result.DiagnosticID != diagnosticID || result.ValidateHTTPS() != nil {
		writeDiagnosticResultProblem(w, http.StatusBadRequest, "invalid_result", requestID)
		return
	}
	if err := h.completer.CompleteDiagnostic(r.Context(), token, generation, result); err != nil {
		h.writeCompletionError(w, err, requestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DiagnosticResultHandler) writeCompletionError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, agentaccess.ErrInvalidCredential):
		writeDiagnosticResultProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
	case errors.Is(err, storage.ErrDiagnosticNotFound):
		writeDiagnosticResultProblem(w, http.StatusNotFound, "not_found", requestID)
	case errors.Is(err, storage.ErrDiagnosticConflict):
		writeDiagnosticResultProblem(w, http.StatusConflict, "conflict", requestID)
	case errors.Is(err, storage.ErrInvalidAgentInput):
		writeDiagnosticResultProblem(w, http.StatusBadRequest, "invalid_result", requestID)
	case errors.Is(err, agentaccess.ErrCancelled):
		writeDiagnosticResultProblem(w, http.StatusServiceUnavailable, "diagnostic_result_unavailable", requestID)
	default:
		writeDiagnosticResultProblem(w, http.StatusServiceUnavailable, "diagnostic_result_unavailable", requestID)
	}
}

func diagnosticResultPathID(path string) (string, bool) {
	const prefix = "/api/agent/v1/diagnostics/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/result") {
		return "", false
	}
	rawID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/result")
	id, err := uuid.Parse(rawID)
	return rawID, err == nil && id.String() == rawID
}

func parseAgentSessionGeneration(values []string) (int64, bool) {
	if len(values) != 1 || values[0] == "" {
		return 0, false
	}
	generation, err := strconv.ParseInt(values[0], 10, 64)
	return generation, err == nil && generation > 0 && strconv.FormatInt(generation, 10) == values[0]
}

func writeDiagnosticResultProblem(w http.ResponseWriter, status int, code, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: "Agent diagnostic result request failed", RequestId: requestID})
}

func firstHeader(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
