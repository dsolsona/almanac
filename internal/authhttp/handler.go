// Package authhttp exposes the local authentication service through a bounded,
// same-origin HTTP boundary.
package authhttp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

const (
	sessionCookie = "__Host-almanac-session"
	preauthCookie = "__Host-almanac-preauth"
	bodyLimit     = 4096
)

var (
	ErrSessionRequest = errors.New("authhttp: invalid session request")
	ErrAuthorization  = errors.New("authhttp: authorization header is not supported")
	ErrMissingSession = errors.New("authhttp: session is missing or malformed")
	ErrRequestMethod  = errors.New("authhttp: method is not allowed")
	ErrRequestOrigin  = errors.New("authhttp: origin is not allowed")
	ErrRequestTLS     = errors.New("authhttp: secure transport is required")
	ErrRequestCSRF    = errors.New("authhttp: csrf token is invalid")
)

type SessionRequest struct {
	SessionSecret string `json:"-"`
	Source        netip.Addr
}

type Options struct {
	PublicOrigin      string
	TrustedProxyCIDRs []netip.Prefix
	Now               func() time.Time
	Preflight         func(context.Context) error
}

type Handler struct {
	service   *authentication.Service
	origin    string
	proxies   []netip.Prefix
	preflight func(context.Context) error
	now       func() time.Time
	clockMu   sync.Mutex
	key       [32]byte
}

func New(service *authentication.Service, options Options) (*Handler, error) {
	if service == nil || options.Now == nil || !canonicalOrigin(options.PublicOrigin) {
		return nil, errors.New("authhttp: invalid options")
	}
	proxies := make([]netip.Prefix, len(options.TrustedProxyCIDRs))
	for i, prefix := range options.TrustedProxyCIDRs {
		if !prefix.IsValid() || prefix.Addr().Zone() != "" {
			return nil, errors.New("authhttp: invalid trusted proxy prefix")
		}
		proxies[i] = prefix.Masked()
	}
	h := &Handler{service: service, origin: options.PublicOrigin, proxies: proxies, preflight: options.Preflight, now: options.Now}
	if _, err := rand.Read(h.key[:]); err != nil {
		return nil, errors.New("authhttp: initialization failed")
	}
	return h, nil
}

// ValidateSessionRequest applies the same cookie, origin, transport and CSRF
// rules used by authentication endpoints. It deliberately does not inspect or
// touch session authority; guarded services do that for every operation.
func (h *Handler) ValidateSessionRequest(r *http.Request) (SessionRequest, error) {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch) {
		return SessionRequest{}, ErrRequestMethod
	}
	unsafeMethod := r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch
	if values := r.Header.Values("Authorization"); len(values) != 0 {
		return SessionRequest{}, ErrAuthorization
	}
	if unsafeMethod {
		if !singleHeaderEquals(r.Header, "Origin", h.origin) {
			return SessionRequest{}, ErrRequestOrigin
		}
	} else if values := r.Header.Values("Origin"); len(values) != 0 && !singleHeaderEquals(r.Header, "Origin", h.origin) {
		return SessionRequest{}, ErrRequestOrigin
	}
	source, ok := h.requestSource(r)
	if !ok {
		return SessionRequest{}, ErrRequestTLS
	}
	session, _, invalid := cookieValues(r)
	if invalid {
		return SessionRequest{}, ErrSessionRequest
	}
	if session == "" {
		return SessionRequest{}, ErrMissingSession
	}
	if _, err := identity.SessionSecretVerifier(session); err != nil {
		return SessionRequest{}, ErrMissingSession
	}
	if unsafeMethod && !h.validCSRF(r, "session", session) {
		return SessionRequest{}, ErrRequestCSRF
	}
	return SessionRequest{SessionSecret: session, Source: source}, nil
}

// ValidateTLSRequest applies direct-TLS or trusted-proxy transport validation
// without imposing cookie, origin, or session-authentication requirements.
func (h *Handler) ValidateTLSRequest(r *http.Request) error {
	if h == nil || r == nil {
		return ErrRequestTLS
	}
	if _, ok := h.requestSource(r); !ok {
		return ErrRequestTLS
	}
	return nil
}

func canonicalOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Contains(u.Host, "*") {
		return false
	}
	host := u.Hostname()
	if host == "" || strings.ToLower(host) != host {
		return false
	}
	if strings.Contains(host, ":") {
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Is6() || address.Zone() != "" || address.String() != host {
			return false
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || n == 443 || strconv.Itoa(n) != port {
			return false
		}
	}
	return u.String() == raw
}

// ValidPublicOrigin reports whether raw is the canonical HTTPS origin accepted by authentication.
func ValidPublicOrigin(raw string) bool { return canonicalOrigin(raw) }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, err := requestID()
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is unavailable", "", "", nil)
		return
	}
	w.Header().Set("X-Request-ID", id)
	path := r.URL.EscapedPath()
	if path != r.URL.Path || strings.Contains(path, "%") || strings.Contains(path, "\\") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") || strings.Contains(path, "/./") || strings.HasSuffix(path, "/.") {
		h.problem(w, http.StatusNotFound, "not_found", "Route not found", id, "", nil)
		return
	}
	switch path {
	case "/api/v1/auth/context", "/api/v1/auth/login", "/api/v1/auth/password", "/api/v1/auth/logout":
	default:
		h.problem(w, http.StatusNotFound, "not_found", "Route not found", id, "", nil)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported", id, "", nil)
		return
	}
	if !singleHeaderEquals(r.Header, "Origin", h.origin) {
		h.problem(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed", id, "", nil)
		return
	}
	source, ok := h.requestSource(r)
	if !ok {
		h.problem(w, http.StatusForbidden, "secure_transport_required", "A secure request is required", id, "", nil)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 0 {
		h.problem(w, http.StatusNotImplemented, "capability_unavailable", "Idempotent authentication requests are not available", id, "", nil)
		return
	}
	if values := r.Header.Values("Content-Encoding"); len(values) != 0 {
		h.problem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content encoding is not supported", id, "", nil)
		return
	}
	if !validContentType(r.Header.Values("Content-Type")) {
		h.problem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", id, "", nil)
		return
	}
	if h.preflight != nil {
		if err := h.preflight(r.Context()); err != nil {
			h.problem(w, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
			return
		}
	}
	body, status := h.readBody(w, r)
	if status != 0 {
		code := "invalid_request"
		message := "Request body is invalid"
		if status == http.StatusRequestTimeout {
			code, message = "request_timeout", "Request body timed out"
		}
		if status == http.StatusRequestEntityTooLarge {
			code, message = "request_too_large", "Request body is too large"
		}
		if status == http.StatusServiceUnavailable {
			code, message = "authentication_unavailable", "Authentication is unavailable"
		}
		h.problem(w, status, code, message, id, "", nil)
		return
	}
	switch path {
	case "/api/v1/auth/context":
		h.context(w, r, body, id)
	case "/api/v1/auth/login":
		h.login(w, r, body, source, id)
	case "/api/v1/auth/password":
		h.password(w, r, body, source, id)
	case "/api/v1/auth/logout":
		h.logout(w, r, body, id)
	}
}

func (h *Handler) requestSource(r *http.Request) (netip.Addr, bool) {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer, err := netip.ParseAddr(remote)
	if err != nil || peer.Zone() != "" {
		return netip.Addr{}, false
	}
	peer = peer.Unmap()
	trusted := false
	for _, prefix := range h.proxies {
		if prefix.Contains(peer) {
			trusted = true
			break
		}
	}
	if trusted {
		if !singleHeaderEquals(r.Header, "X-Forwarded-Proto", "https") {
			return netip.Addr{}, false
		}
		values := r.Header.Values("X-Forwarded-For")
		if len(values) != 1 || strings.Contains(values[0], ",") || strings.TrimSpace(values[0]) != values[0] {
			return netip.Addr{}, false
		}
		addr, err := netip.ParseAddr(values[0])
		if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() || addr.String() != values[0] {
			return netip.Addr{}, false
		}
		return addr.Unmap(), true
	}
	if r.TLS == nil || peer.IsUnspecified() || peer.IsMulticast() {
		return netip.Addr{}, false
	}
	return peer, true
}

func singleHeaderEquals(header http.Header, name, expected string) bool {
	values := header.Values(name)
	return len(values) == 1 && values[0] == expected
}

func validContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" || len(params) > 1 {
		return false
	}
	charset, ok := params["charset"]
	return len(params) == 0 || (ok && strings.EqualFold(charset, "utf-8"))
}

func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(5 * time.Second)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if r.ContentLength > bodyLimit {
		return nil, http.StatusRequestEntityTooLarge
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, http.StatusRequestEntityTooLarge
		}
		if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
			return nil, http.StatusRequestTimeout
		}
		return nil, http.StatusBadRequest
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		return nil, http.StatusServiceUnavailable
	}
	return data, 0
}

func decodeObject(data []byte, target any, members ...string) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return false
	}
	if err := json.Unmarshal(trimmed, target, json.RejectUnknownMembers(true)); err != nil {
		return false
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return false
	}
	for _, member := range members {
		value, ok := object[member]
		if !ok || value == nil {
			return false
		}
	}
	return len(object) == len(members)
}

func (h *Handler) context(w http.ResponseWriter, r *http.Request, body []byte, id string) {
	var request api.EmptyAuthRequest
	if !decodeObject(body, &request) {
		h.problem(w, 400, "invalid_request", "Request body is invalid", id, "", nil)
		return
	}
	session, preauth, invalid := cookieValues(r)
	if invalid {
		h.problem(w, 400, "invalid_request", "Cookies are ambiguous", id, "", nil)
		return
	}
	response := api.AuthResponse{}
	if session != "" {
		access, err := h.service.Inspect(r.Context(), session)
		if err == nil {
			response.Session = identityView(access)
		} else if errors.Is(err, authentication.ErrUnauthenticated) {
			clearCookie(w, sessionCookie)
			session = ""
		} else {
			h.mapError(w, err, id, "")
			return
		}
	}
	if response.Session == nil {
		if !validPreauth(preauth) {
			var err error
			preauth, err = randomPreauth()
			if err != nil {
				h.problem(w, 503, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
				return
			}
			setCookie(w, preauthCookie, preauth, h.nowTime().Add(10*time.Minute), 600)
		}
		nonce, err := csrfNonce()
		if err != nil {
			h.problem(w, 503, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
			return
		}
		response.CsrfToken = h.csrf("login", preauth, nonce)
	} else {
		nonce, err := csrfNonce()
		if err != nil {
			h.problem(w, 503, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
			return
		}
		response.CsrfToken = h.csrf("session", session, nonce)
	}
	writeJSON(w, 200, response)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request, body []byte, source netip.Addr, id string) {
	var request api.LoginRequest
	if !decodeObject(body, &request, "login", "password") {
		h.problem(w, 400, "invalid_request", "Request body is invalid", id, "", nil)
		return
	}
	session, preauth, invalid := cookieValues(r)
	if invalid {
		h.problem(w, 400, "invalid_request", "Cookies are ambiguous", id, "", nil)
		return
	}
	if preauth == "" || !h.validCSRF(r, "login", preauth) {
		h.problem(w, 403, "csrf_invalid", "CSRF token is invalid", id, "", nil)
		return
	}
	if session != "" {
		h.problem(w, 409, "session_present", "Sign out before signing in as another user", id, "", nil)
		return
	}
	nonce, err := csrfNonce()
	if err != nil {
		h.problem(w, 503, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
		return
	}
	result, err := h.service.Login(r.Context(), authentication.LoginInput{Login: request.Login, Password: request.Password, Source: source, RequestID: id})
	if err != nil {
		h.mapError(w, err, id, "")
		return
	}
	h.setSession(w, result)
	clearCookie(w, preauthCookie)
	writeJSON(w, 200, api.AuthResponse{CsrfToken: h.csrf("session", result.Secret, nonce), Session: identityView(result.Access)})
}

func (h *Handler) password(w http.ResponseWriter, r *http.Request, body []byte, source netip.Addr, id string) {
	var request api.PasswordRequest
	if !decodeObject(body, &request, "current_password", "new_password") {
		h.problem(w, 400, "invalid_request", "Request body is invalid", id, "", nil)
		return
	}
	session, _, invalid := cookieValues(r)
	if invalid {
		h.problem(w, 400, "invalid_request", "Cookies are ambiguous", id, "", nil)
		return
	}
	if session == "" || !h.validCSRF(r, "session", session) {
		h.problem(w, 403, "csrf_invalid", "CSRF token is invalid", id, "", nil)
		return
	}
	nonce, err := csrfNonce()
	if err != nil {
		h.problem(w, 503, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
		return
	}
	result, err := h.service.ChangePassword(r.Context(), authentication.ChangePasswordInput{SessionSecret: session, CurrentPassword: request.CurrentPassword, NewPassword: request.NewPassword, Source: source, RequestID: id})
	if err != nil {
		h.mapError(w, err, id, "new_password")
		return
	}
	h.setSession(w, result)
	writeJSON(w, 200, api.AuthResponse{CsrfToken: h.csrf("session", result.Secret, nonce), Session: identityView(result.Access)})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, body []byte, id string) {
	var request api.EmptyAuthRequest
	if !decodeObject(body, &request) {
		h.problem(w, 400, "invalid_request", "Request body is invalid", id, "", nil)
		return
	}
	session, preauth, invalid := cookieValues(r)
	if invalid {
		h.problem(w, 400, "invalid_request", "Cookies are ambiguous", id, "", nil)
		return
	}
	purpose, bound := "login", preauth
	if session != "" {
		purpose, bound = "session", session
	}
	if bound == "" || !h.validCSRF(r, purpose, bound) {
		h.problem(w, 403, "csrf_invalid", "CSRF token is invalid", id, "", nil)
		return
	}
	if session != "" {
		if _, err := h.service.Logout(r.Context(), session, id); err != nil {
			h.mapError(w, err, id, "")
			return
		}
	}
	clearCookie(w, sessionCookie)
	clearCookie(w, preauthCookie)
	w.WriteHeader(http.StatusNoContent)
}

func cookieValues(r *http.Request) (session, preauth string, invalid bool) {
	sessionCount, preauthCount := 0, 0
	for _, cookie := range r.Cookies() {
		switch cookie.Name {
		case sessionCookie:
			sessionCount++
			session = cookie.Value
		case preauthCookie:
			preauthCount++
			preauth = cookie.Value
		}
	}
	return session, preauth, sessionCount > 1 || preauthCount > 1
}

func validPreauth(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func randomPreauth() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func identityView(access storage.SessionAccess) *api.SafeIdentity {
	return &api.SafeIdentity{UserId: access.UserID, Login: access.Login, Role: access.Role, Restricted: access.Restricted}
}

func (h *Handler) nowTime() time.Time {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	return h.now()
}

func csrfNonce() ([32]byte, error) {
	var nonce [32]byte
	_, err := rand.Read(nonce[:])
	return nonce, err
}

func (h *Handler) csrf(purpose, cookie string, nonce [32]byte) string {
	mac := hmac.New(sha256.New, h.key[:])
	writeFrame(mac, "almanac-csrf-v1")
	writeFrame(mac, purpose)
	writeFrame(mac, cookie)
	writeFrame(mac, string(nonce[:]))
	return base64.RawURLEncoding.EncodeToString(append(nonce[:], mac.Sum(nil)...))
}

func writeFrame(w io.Writer, value string) {
	var length [4]byte
	length[0], length[1], length[2], length[3] = byte(len(value)>>24), byte(len(value)>>16), byte(len(value)>>8), byte(len(value))
	_, _ = w.Write(length[:])
	_, _ = io.WriteString(w, value)
}

func (h *Handler) validCSRF(r *http.Request, purpose, cookie string) bool {
	values := r.Header.Values("X-CSRF-Token")
	if len(values) != 1 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || len(decoded) != 64 || base64.RawURLEncoding.EncodeToString(decoded) != values[0] {
		return false
	}
	mac := hmac.New(sha256.New, h.key[:])
	writeFrame(mac, "almanac-csrf-v1")
	writeFrame(mac, purpose)
	writeFrame(mac, cookie)
	writeFrame(mac, string(decoded[:32]))
	return hmac.Equal(decoded[32:], mac.Sum(nil))
}

func (h *Handler) setSession(w http.ResponseWriter, result authentication.SessionResult) {
	maxAge := int(result.Access.AbsoluteExpiresAt.Sub(h.nowTime()).Seconds())
	if maxAge > 43200 {
		maxAge = 43200
	}
	if maxAge < 1 {
		maxAge = 1
	}
	setCookie(w, sessionCookie, result.Secret, result.Access.AbsoluteExpiresAt, maxAge)
}

func setCookie(w http.ResponseWriter, name, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Expires: expires.UTC(), MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

type fieldError struct {
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

func writeProblem(w http.ResponseWriter, status int, code, message, id, _ string, fields []fieldError) {
	if fields == nil {
		fields = []fieldError{}
	}
	apiFields := make([]struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}, len(fields))
	for i, value := range fields {
		apiFields[i] = struct {
			Code    string `json:"code"`
			Field   string `json:"field"`
			Message string `json:"message"`
		}{Code: value.Code, Field: value.Field, Message: value.Message}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: message, RequestId: id, FieldErrors: apiFields})
}

func (h *Handler) problem(w http.ResponseWriter, status int, code, message, id, _ string, fields []fieldError) {
	writeProblem(w, status, code, message, id, "", fields)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, value)
}

func requestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (h *Handler) mapError(w http.ResponseWriter, err error, id, field string) {
	if limit, ok := errors.AsType[*authentication.RateLimitError](err); ok {
		retry := max(1, int((limit.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		h.problem(w, 429, "rate_limited", "Too many authentication attempts", id, "", nil)
		return
	}
	switch {
	case errors.Is(err, authentication.ErrUnauthenticated), errors.Is(err, authentication.ErrAttemptExpired):
		h.problem(w, 401, "unauthenticated", "Credentials could not be verified", id, "", nil)
	case errors.Is(err, authentication.ErrPasswordUnchanged):
		h.problem(w, 422, "password_unchanged", "The new password must differ from the current password", id, "", nil)
	case errors.Is(err, identity.ErrInvalidPassword), errors.Is(err, identity.ErrCommonPassword):
		h.problem(w, 422, "password_policy", "The new password does not meet the password policy", id, field, []fieldError{{Field: field, Code: "password_policy", Message: "Choose a different password that meets the password policy"}})
	case errors.Is(err, authentication.ErrInvalidInput):
		h.problem(w, 400, "invalid_request", "Request fields are invalid", id, "", nil)
	default:
		h.problem(w, 503, "authentication_unavailable", "Authentication is unavailable", id, "", nil)
	}
}
