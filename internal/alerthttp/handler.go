// Package alerthttp exposes authenticated alert policy and occurrence management.
package alerthttp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

type Options struct {
	Runtime   *storage.Runtime
	Now       func() time.Time
	Preflight func(context.Context) error
}

type Handler struct {
	databaseURL string
	auth        *authhttp.Handler
	options     Options
	mux         *http.ServeMux
}

type requestContext struct {
	id    string
	proof storage.DatasourceProof
	query url.Values
}

type requestKey struct{}

func New(databaseURL string, auth *authhttp.Handler, options Options) (*Handler, error) {
	if databaseURL == "" || auth == nil || options.Now == nil {
		return nil, errors.New("alerthttp: invalid options")
	}
	h := &Handler{databaseURL: databaseURL, auth: auth, options: options, mux: http.NewServeMux()}
	h.registerRoutes()
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id := strings.ReplaceAll(uuid.New().String(), "-", "")
	w.Header().Set("X-Request-ID", id)
	if r.URL.EscapedPath() != r.URL.Path || strings.ContainsAny(r.URL.Path, "%\\") || path.Clean(r.URL.Path) != r.URL.Path {
		problem(w, 404, "not_found", id)
		return
	}
	if h.options.Preflight != nil {
		if err := h.options.Preflight(r.Context()); err != nil {
			problem(w, 503, "unavailable", id)
			return
		}
	}
	authorized, err := h.auth.ValidateSessionRequest(r)
	if err != nil {
		if errors.Is(err, authhttp.ErrMissingSession) || errors.Is(err, authhttp.ErrSessionRequest) {
			problem(w, 401, "unauthorized", id)
		} else {
			problem(w, 403, "forbidden", id)
		}
		return
	}
	verifier, err := identity.SessionSecretVerifier(authorized.SessionSecret)
	if err != nil {
		problem(w, 401, "unauthorized", id)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery {
		problem(w, 400, "invalid_request", id)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			problem(w, 503, "unavailable", id)
			return
		}
		defer controller.SetReadDeadline(time.Time{})
	}
	request := requestContext{id: id, proof: storage.DatasourceProof{SessionVerifier: verifier}, query: query}
	r = r.WithContext(context.WithValue(r.Context(), requestKey{}, request))
	_, pattern := h.mux.Handler(r)
	if pattern == "" {
		var allowed []string
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPatch} {
			probe := r.Clone(r.Context())
			probe.Method = method
			if _, match := h.mux.Handler(probe); match != "" {
				allowed = append(allowed, method)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			problem(w, 405, "method_not_allowed", id)
			return
		}
		problem(w, 404, "not_found", id)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func requestInfo(r *http.Request) requestContext {
	return r.Context().Value(requestKey{}).(requestContext)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		problem(w, 503, "unavailable", w.Header().Get("X-Request-ID"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func problem(w http.ResponseWriter, status int, code, id string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: "Alert request failed.", RequestId: id})
}

func fieldProblem(w http.ResponseWriter, id, field, code, message string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.MarshalWrite(w, api.Problem{Code: "invalid_request", Message: message, RequestId: id, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{{Code: code, Field: field, Message: message}}})
}

func storageError(w http.ResponseWriter, err error, id string) {
	switch {
	case errors.Is(err, storage.ErrAlertPresetConfirmation):
		fieldProblem(w, id, "confirm_non_30_day_presets", "preset_confirmation_required", "These preset values use a 30-day reference period. Confirm them explicitly for this SLO period, or choose custom conditions.")
	case errors.Is(err, alert.ErrPolicy):
		fieldProblem(w, id, "conditions", "conditions_invalid", "Use windows aligned to the SLI cadence and a reachable positive burn threshold.")
	case errors.Is(err, storage.ErrInvalidSession):
		problem(w, 401, "unauthorized", id)
	case errors.Is(err, storage.ErrAgentForbidden):
		problem(w, 403, "forbidden", id)
	case errors.Is(err, storage.ErrAlertNotFound):
		problem(w, 404, "not_found", id)
	case errors.Is(err, storage.ErrAlertStale):
		problem(w, 409, "version_conflict", id)
	case errors.Is(err, storage.ErrAlertNameConflict):
		problem(w, 409, "name_conflict", id)
	case errors.Is(err, storage.ErrAlertKeyConflict):
		problem(w, 409, "command_body_conflict", id)
	case errors.Is(err, storage.ErrAlertReceiptExpired):
		problem(w, 410, "command_expired", id)
	case errors.Is(err, storage.ErrAlertInvalid):
		problem(w, 400, "invalid_request", id)
	default:
		problem(w, 503, "unavailable", id)
	}
}
