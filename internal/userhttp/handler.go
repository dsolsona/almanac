// Package userhttp exposes current-account and administrator user operations.
package userhttp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/telemetry"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"uuid"
)

type Handler struct {
	databaseURL, origin string
	auth                *authhttp.Handler
	service             *authentication.Service
	now                 func() time.Time
	preflight           func(context.Context) error
}

func New(databaseURL, origin string, auth *authhttp.Handler, service *authentication.Service, now func() time.Time, preflight func(context.Context) error) *Handler {
	return &Handler{databaseURL: databaseURL, origin: origin, auth: auth, service: service, now: now, preflight: preflight}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	id := uuid.New().String()
	w.Header().Set("X-Request-ID", id)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.URL.Path != r.URL.EscapedPath() || strings.ContainsAny(r.URL.Path, "%\\") || strings.Contains(r.URL.Path, "/../") || strings.Contains(r.URL.Path, "/./") {
		h.problem(w, 404, "not_found", id)
		return
	}
	if h.preflight != nil {
		if err := h.preflight(ctx); err != nil {
			h.failure(w, err, id)
			return
		}
	}
	if r.URL.Path == "/api/v1/account/activation" || r.URL.Path == "/api/v1/account/activation/context" {
		h.redeem(w, r, id)
		return
	}
	authorized, err := h.auth.ValidateSessionRequest(r)
	if err != nil {
		if errors.Is(err, authhttp.ErrMissingSession) || errors.Is(err, authhttp.ErrSessionRequest) {
			h.problem(w, 401, "unauthorized", id)
		} else {
			h.problem(w, 403, "forbidden", id)
		}
		return
	}
	verifier, err := identity.SessionSecretVerifier(authorized.SessionSecret)
	if err != nil {
		h.problem(w, 401, "unauthorized", id)
		return
	}
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	path := r.URL.Path
	switch {
	case path == "/api/v1/account":
		if r.Method == http.MethodGet {
			user, err := storage.GetAccount(ctx, h.databaseURL, proof, h.now)
			h.user(w, user, err, id)
			return
		}
		if r.Method == http.MethodPut {
			user, err := storage.GetAccount(ctx, h.databaseURL, proof, h.now)
			if err != nil {
				h.failure(w, err, id)
				return
			}
			var body api.AccountUpdateRequest
			if !h.decode(w, r, &body, id) {
				return
			}
			h.command(w, r, id, storage.UserCommandInput{Proof: proof, Operation: "profile", Version: version(r, user.ID), Name: body.FullName})
			return
		}
	case path == "/api/v1/users":
		if r.Method == http.MethodGet {
			h.list(w, r, id, proof)
			return
		}
		if r.Method == http.MethodPost {
			var body api.UserWriteRequest
			if !h.decode(w, r, &body, id) {
				return
			}
			h.command(w, r, id, storage.UserCommandInput{Proof: proof, Operation: "create", Name: body.FullName, Email: string(body.Email), Role: string(body.Role)})
			return
		}
	case strings.HasPrefix(path, "/api/v1/users/"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/v1/users/"), "/")
		parsed, err := uuid.Parse(parts[0])
		if err != nil || parsed.String() != parts[0] {
			h.problem(w, 404, "not_found", id)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			user, err := storage.GetUser(ctx, h.databaseURL, proof, parts[0], h.now)
			h.user(w, user, err, id)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodPut {
			var body api.UserWriteRequest
			if !h.decode(w, r, &body, id) {
				return
			}
			h.command(w, r, id, storage.UserCommandInput{Proof: proof, UserID: parts[0], Operation: "update", Version: version(r, parts[0]), Name: body.FullName, Email: string(body.Email), Role: string(body.Role)})
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			var body struct{}
			if !h.decode(w, r, &body, id) {
				return
			}
			h.command(w, r, id, storage.UserCommandInput{Proof: proof, UserID: parts[0], Operation: parts[1], Version: version(r, parts[0])})
			return
		}
		h.problem(w, 404, "not_found", id)
		return
	default:
		h.problem(w, 404, "not_found", id)
		return
	}
	w.Header().Set("Allow", "GET, PUT, POST")
	h.problem(w, 405, "method_not_allowed", id)
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request, id string, proof storage.DatasourceProof) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	for key, list := range values {
		if len(list) != 1 || (key != "q" && key != "role" && key != "status" && key != "limit" && key != "cursor") {
			err = storage.ErrUserInvalid
		}
	}
	limit := 50
	if raw := values.Get("limit"); raw != "" {
		var parseErr error
		limit, parseErr = strconv.Atoi(raw)
		if parseErr != nil {
			err = parseErr
		}
	}
	if err != nil {
		h.problem(w, 400, "invalid_request", id)
		return
	}
	page, err := storage.ListUsers(r.Context(), h.databaseURL, storage.UserListInput{Proof: proof, Search: values.Get("q"), Role: values.Get("role"), Status: values.Get("status"), Limit: limit, Cursor: values.Get("cursor"), Now: h.now})
	if err != nil {
		h.failure(w, err, id)
		return
	}
	h.json(w, 200, page)
}
func (h *Handler) command(w http.ResponseWriter, r *http.Request, id string, in storage.UserCommandInput) {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		h.problem(w, 400, "invalid_request", id)
		return
	}
	in.Key = r.Header.Get("Idempotency-Key")
	in.RequestID = id
	in.Now = h.now
	result, err := storage.ChangeUser(r.Context(), h.databaseURL, in)
	if err != nil {
		h.failure(w, err, id)
		return
	}
	response := struct {
		storage.UserCommandResult
		Link string `json:"link,omitempty"`
	}{UserCommandResult: result}
	if result.Secret != "" {
		response.Link = h.origin + "/activate#" + result.Secret
	}
	w.Header().Set("ETag", etag(result.User))
	status := 200
	if in.Operation == "create" {
		status = 201
	}
	h.json(w, status, response)
}
func (h *Handler) user(w http.ResponseWriter, user storage.UserRecord, err error, id string) {
	if err != nil {
		h.failure(w, err, id)
		return
	}
	w.Header().Set("ETag", etag(user))
	h.json(w, 200, user)
}
func (h *Handler) redeem(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		h.problem(w, 405, "method_not_allowed", id)
		return
	}
	source, err := h.auth.ValidateChallengeRequest(r)
	if err != nil {
		h.problem(w, 403, "forbidden", id)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 0 {
		h.problem(w, 400, "invalid_request", id)
		return
	}
	if r.URL.Path == "/api/v1/account/activation/context" {
		var body struct {
			Token string `json:"token"`
		}
		if !h.decode(w, r, &body, id) {
			return
		}
		value, err := storage.InspectUserChallenge(r.Context(), h.databaseURL, body.Token, h.now)
		if err != nil {
			h.failure(w, err, id)
			return
		}
		h.json(w, 200, value)
		return
	}
	var body api.UserRedemptionRequest
	if !h.decode(w, r, &body, id) {
		return
	}
	if body.Token == nil || body.Password == nil {
		h.problem(w, 400, "invalid_request", id)
		return
	}
	if err := h.service.RedeemUserChallenge(r.Context(), *body.Token, *body.Password, source, id); err != nil {
		h.failure(w, err, id)
		return
	}
	w.WriteHeader(204)
}
func version(r *http.Request, userID string) string {
	if len(r.Header.Values("If-Match")) != 1 {
		return ""
	}
	value, ok := strings.CutPrefix(r.Header.Get("If-Match"), `"user-`+userID+`-`)
	if !ok {
		return ""
	}
	value, ok = strings.CutSuffix(value, `"`)
	if !ok {
		return ""
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return ""
	}
	return value
}
func etag(user storage.UserRecord) string { return `"user-` + user.ID + `-` + user.Version + `"` }
func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any, id string) bool {
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		h.problem(w, 415, "unsupported_media_type", id)
		return false
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		h.problem(w, 415, "unsupported_media_type", id)
		return false
	}
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.problem(w, 503, "user_unavailable", id)
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.problem(w, 413, "request_too_large", id)
		} else {
			h.problem(w, 400, "invalid_request", id)
		}
		return false
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "{") || json.Unmarshal(body, target, json.RejectUnknownMembers(true)) != nil {
		h.problem(w, 400, "invalid_request", id)
		return false
	}
	return true
}
func (h *Handler) failure(w http.ResponseWriter, err error, id string) {
	status, code := 503, "user_unavailable"
	switch {
	case errors.Is(err, storage.ErrInvalidSession), errors.Is(err, authentication.ErrUnauthenticated):
		status, code = 401, "unauthorized"
	case errors.Is(err, storage.ErrAgentForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, storage.ErrUserNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, storage.ErrUserInvalid), errors.Is(err, authentication.ErrInvalidInput):
		status, code = 400, "invalid_request"
	case errors.Is(err, storage.ErrUserConflict):
		status, code = 409, "conflict"
	case errors.Is(err, storage.ErrUserEmail):
		status, code = 409, "email_in_use"
	case errors.Is(err, storage.ErrLastAdmin):
		status, code = 409, "last_administrator"
	case errors.Is(err, storage.ErrUserChallenge):
		status, code = 422, "link_unavailable"
	case errors.Is(err, identity.ErrInvalidPassword), errors.Is(err, identity.ErrCommonPassword):
		status, code = 422, "password_policy"
	}
	if limited, ok := errors.AsType[*authentication.RateLimitError](err); ok {
		status, code = 429, "rate_limited"
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(limited.RetryAfter.Seconds())))))
	}
	h.problem(w, status, code, id)
}
func (h *Handler) problem(w http.ResponseWriter, status int, code, id string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: "Account request could not be completed", RequestId: id, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}})
}
func (h *Handler) json(w http.ResponseWriter, status int, body any) {
	defer telemetry.ResponseEncoding(w)()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, body)
}
