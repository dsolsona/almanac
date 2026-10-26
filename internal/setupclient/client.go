// Package setupclient implements HTTPS authentication for administrator setup and
// reusable CLI sessions.
package setupclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	stdjson "encoding/json"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
)

const maxResponseBytes = 64 << 10
const uncertainOutcomeMessage = "setup outcome is unknown; the initial or new password may already be active. If you do not have a usable session, deliberately sign in through the authentication API with the initial password if login was uncertain; after an uncertain password change, use the new password and, if that fails, the initial password. Do not automatically rerun setup; a valid session does not prove the password change completed."

type Kind string

const (
	KindInvalid        Kind = "invalid"
	KindAuthentication Kind = "authentication"
	KindConflict       Kind = "conflict"
	KindCancelled      Kind = "cancelled"
	KindRemote         Kind = "remote"
	KindNotFound       Kind = "not_found"
)

type Failure struct {
	Kind              Kind
	Code              string
	Message           string
	OutcomeUnknown    bool
	SessionState      string
	RequestID         string
	RetryAfterSeconds int
}

func (f *Failure) Error() string { return f.Message }

type Options struct {
	BackendURL, Login string
	CABundle          []byte
}
type Result struct {
	Completed      bool
	SessionCleanup string
}

type Client struct {
	origin *url.URL
	login  string
	http   *http.Client
	api    api.ClientInterface
	jar    http.CookieJar
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func New(options Options) (*Client, error) {
	return newClient(options, true)
}

func newClient(options Options, requireLogin bool) (*Client, error) {
	u, err := url.Parse(options.BackendURL)
	if err != nil || !authhttp.ValidPublicOrigin(options.BackendURL) || u.User != nil || (requireLogin && options.Login == "") || len(options.Login) > 256 || !utf8Valid(options.Login) || len(options.CABundle) > 64<<10 {
		return nil, &Failure{Kind: KindInvalid, Code: "invalid_configuration", Message: "backend URL or administrator login is invalid", SessionState: "anonymous"}
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		return nil, &Failure{Kind: KindRemote, Code: "client_initialization_failed", Message: "system certificate roots are unavailable", SessionState: "anonymous"}
	}
	if len(options.CABundle) > 0 && !pool.AppendCertsFromPEM(options.CABundle) {
		return nil, &Failure{Kind: KindInvalid, Code: "invalid_ca_bundle", Message: "CA bundle is invalid", SessionState: "anonymous"}
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, &Failure{Kind: KindRemote, Code: "client_initialization_failed", Message: "setup client initialization failed", SessionState: "anonymous"}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool},
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
	}
	hc := &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	generated, err := api.NewClient(u.String(), api.WithHTTPClient(hc))
	if err != nil {
		transport.CloseIdleConnections()
		return nil, &Failure{Kind: KindInvalid, Code: "invalid_configuration", Message: "backend URL is invalid", SessionState: "anonymous"}
	}
	return &Client{origin: u, login: options.Login, http: hc, api: generated, jar: jar}, nil
}

func (c *Client) Close() {
	if tr, ok := c.http.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

func (c *Client) Complete(ctx context.Context, initialPassword, newPassword string) (Result, error) {
	if len(initialPassword) > 512 || !utf8Valid(initialPassword) || len(newPassword) > 512 || !utf8Valid(newPassword) || identity.ValidateNewPassword(newPassword) != nil || initialPassword == newPassword {
		return Result{}, &Failure{Kind: KindInvalid, Code: "invalid_password", Message: "new password is invalid or unchanged", SessionState: "anonymous"}
	}
	ctxBody := api.PostAuthContextJSONRequestBody{}
	contextResponse, err := c.api.PostAuthContext(ctx, ctxBody, c.headers(""))
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, cancelledFailure(false, "unavailable")
		}
		return Result{}, networkFailure(err, false, "anonymous")
	}
	contextData, err := c.read(contextResponse)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, cancelledFailure(false, "unavailable")
		}
		return Result{}, networkFailure(err, false, "anonymous")
	}
	if contextResponse.StatusCode != http.StatusOK {
		return Result{}, c.responseFailure(ctx, contextResponse, contextData, false, "anonymous")
	}
	var auth api.AuthResponse
	if auth, err = decodeAuth(contextData); err != nil || auth.Session != nil || auth.CsrfToken == "" {
		return Result{}, protocolFailure("anonymous", false)
	}
	_, ok := findCookie(contextResponse, "__Host-almanac-preauth")
	if !ok {
		return Result{}, protocolFailure("anonymous", false)
	}
	loginBody := api.LoginRequest{Login: cLogin(c), Password: initialPassword}
	loginResponse, err := c.api.PostAuthLogin(ctx, loginBody, c.headers(auth.CsrfToken))
	if err != nil {
		return Result{}, c.uncertain(ctx, err, "unavailable")
	}
	data, err := c.read(loginResponse)
	if err != nil {
		return Result{}, c.uncertain(ctx, err, "unavailable")
	}
	if loginResponse.StatusCode != http.StatusOK {
		failure := c.responseFailure(ctx, loginResponse, data, true, "anonymous")
		if definitive, ok := errors.AsType[*Failure](failure); ok && !definitive.OutcomeUnknown {
			if c.logout(ctx, auth.CsrfToken) == nil {
				definitive.SessionState = "anonymous"
			}
		}
		return Result{}, failure
	}
	if err = requireCookie(loginResponse, "__Host-almanac-session"); err != nil {
		return Result{}, c.uncertain(ctx, err, "unavailable")
	}
	if auth, err = decodeAuth(data); err != nil || auth.Session == nil || auth.CsrfToken == "" || auth.Session.UserId == "" || auth.Session.Login == "" || auth.Session.Role == "" {
		return Result{}, c.uncertain(ctx, protocolFailure("restricted", true), "restricted")
	}
	if !auth.Session.Restricted {
		state := "authenticated"
		if c.logout(ctx, auth.CsrfToken) == nil {
			state = "anonymous"
		}
		return Result{}, &Failure{Kind: KindConflict, Code: "setup_already_complete", Message: "setup is already complete; ordinary administrator password was not changed", SessionState: state}
	}
	if auth.Session.Role != "administrator" {
		return Result{}, c.rejectAndLogout(ctx, &Failure{Kind: KindAuthentication, Code: "setup_identity_invalid", Message: "initial account is not the expected administrator", SessionState: "restricted"}, auth.CsrfToken)
	}
	userID := auth.Session.UserId
	passwordResponse, err := c.api.PostAuthPassword(ctx, api.PasswordRequest{CurrentPassword: initialPassword, NewPassword: newPassword}, c.headers(auth.CsrfToken))
	if err != nil {
		return Result{}, c.uncertain(ctx, err, "unavailable")
	}
	data, err = c.read(passwordResponse)
	if err != nil {
		return Result{}, c.uncertain(ctx, err, "unavailable")
	}
	if passwordResponse.StatusCode != http.StatusOK {
		failure := c.responseFailure(ctx, passwordResponse, data, true, "restricted")
		if definitive, ok := errors.AsType[*Failure](failure); ok && !definitive.OutcomeUnknown {
			if c.logout(ctx, auth.CsrfToken) == nil {
				definitive.SessionState = "anonymous"
			}
		}
		return Result{}, failure
	}
	if auth, err = decodeAuth(data); err != nil || auth.Session == nil || auth.CsrfToken == "" || auth.Session.UserId != userID || auth.Session.Restricted || auth.Session.Role != "administrator" || auth.Session.Login == "" {
		return Result{}, c.uncertain(ctx, protocolFailure("authenticated", true), "authenticated")
	}
	if err = requireCookie(passwordResponse, "__Host-almanac-session"); err != nil {
		return Result{}, c.uncertain(ctx, protocolFailure("authenticated", true), "authenticated")
	}
	logoutErr := c.logout(ctx, auth.CsrfToken)
	if logoutErr != nil {
		return Result{Completed: true, SessionCleanup: "unknown"}, nil
	}
	return Result{Completed: true, SessionCleanup: "complete"}, nil
}

func (c *Client) logout(ctx context.Context, csrf string) error {
	r, err := c.api.PostAuthLogout(ctx, api.PostAuthLogoutJSONRequestBody{}, c.headers(csrf))
	if err != nil {
		return err
	}
	return c.requireNoBodyStatus(r, http.StatusNoContent)
}

func decodeAuth(data []byte) (api.AuthResponse, error) {
	var fields map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(data, &fields); err != nil {
		return api.AuthResponse{}, err
	}
	csrf, hasCSRF := fields["csrf_token"]
	session, hasSession := fields["session"]
	if !hasCSRF || !hasSession {
		return api.AuthResponse{}, errors.New("required authentication fields missing")
	}
	var token string
	if string(csrf) == "null" || json.Unmarshal(csrf, &token) != nil || token == "" {
		return api.AuthResponse{}, errors.New("csrf token missing")
	}
	if string(session) != "null" {
		var identityFields map[string]stdjson.RawMessage
		if err := json.Unmarshal(session, &identityFields); err != nil {
			return api.AuthResponse{}, err
		}
		for _, key := range []string{"user_id", "login", "role", "restricted"} {
			value, ok := identityFields[key]
			if !ok || string(value) == "null" {
				return api.AuthResponse{}, errors.New("required identity field missing")
			}
		}
		for _, key := range []string{"user_id", "login", "role"} {
			var value string
			if err := json.Unmarshal(identityFields[key], &value); err != nil || value == "" {
				return api.AuthResponse{}, errors.New("identity field invalid")
			}
		}
		var restricted bool
		if string(identityFields["restricted"]) != "true" && string(identityFields["restricted"]) != "false" {
			return api.AuthResponse{}, errors.New("restricted state invalid")
		}
		if err := json.Unmarshal(identityFields["restricted"], &restricted); err != nil {
			return api.AuthResponse{}, errors.New("restricted state invalid")
		}
	}
	var result api.AuthResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return api.AuthResponse{}, err
	}
	return result, nil
}

func (c *Client) headers(csrf string) api.RequestEditorFn {
	return func(_ context.Context, r *http.Request) error {
		r.Header.Set("Origin", c.origin.String())
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		r.Header.Set("Content-Type", "application/json")
		return nil
	}
}
func (c *Client) read(r *http.Response) ([]byte, error) {
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, maxResponseBytes+1))
	if e != nil || len(b) > maxResponseBytes {
		return nil, errors.New("response body unavailable")
	}
	return b, nil
}
func (c *Client) requireNoBodyStatus(r *http.Response, status int) error {
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, 1))
	if e != nil || len(b) > 0 || r.StatusCode != status {
		return errors.New("cleanup response invalid")
	}
	return nil
}
func (c *Client) rejectAndLogout(ctx context.Context, original *Failure, csrf string) error {
	r, e := c.api.PostAuthLogout(ctx, api.PostAuthLogoutJSONRequestBody{}, c.headers(csrf))
	if e == nil && c.requireNoBodyStatus(r, http.StatusNoContent) == nil {
		original.SessionState = "anonymous"
	}
	return original
}
func (c *Client) uncertain(ctx context.Context, cause error, state string) error {
	f := &Failure{Kind: KindRemote, Code: "outcome_unknown", Message: uncertainOutcomeMessage, OutcomeUnknown: true, SessionState: "unchecked"}
	if cause != nil && ctx.Err() == nil {
		r, e := c.api.PostAuthContext(ctx, api.PostAuthContextJSONRequestBody{}, c.headers(""))
		if e == nil {
			b, re := c.read(r)
			if re == nil && r.StatusCode == http.StatusOK {
				if a, decodeErr := decodeAuth(b); decodeErr == nil {
					if a.Session == nil {
						f.SessionState = "anonymous"
					} else if a.Session.UserId != "" && a.Session.Login != "" && a.Session.Role != "" && c.hasSessionCookie() && a.Session.Restricted {
						f.SessionState = "restricted"
					} else if a.Session.UserId != "" && a.Session.Login != "" && a.Session.Role != "" && c.hasSessionCookie() {
						f.SessionState = "authenticated"
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		f.Kind = KindCancelled
		f.SessionState = "unavailable"
	}
	_ = state
	return f
}
func (c *Client) responseFailure(ctx context.Context, r *http.Response, body []byte, mutation bool, state string) error {
	var p api.Problem
	if err := decodeProblem(body, &p); err != nil {
		if mutation {
			return c.uncertain(ctx, err, state)
		}
		return protocolFailure(state, false)
	}
	f := mapProblem(r.StatusCode, p, state, mutation)
	if f == nil {
		if mutation {
			unknown := c.uncertain(ctx, errors.New("unexpected response"), state)
			if failure, ok := errors.AsType[*Failure](unknown); ok {
				if requestIDPattern.MatchString(p.RequestId) {
					failure.RequestID = p.RequestId
				}
				if retry, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil && retry > 0 {
					failure.RetryAfterSeconds = retry
				}
			}
			return unknown
		}
		return protocolFailure(state, false)
	}
	if f.Code != "setup_already_complete" {
		f.SessionState = state
	} else {
		// A login conflict does not prove which session the server observed. The
		// later logout attempt can establish anonymous state if it succeeds.
		f.SessionState = "unchecked"
	}
	f.RequestID = p.RequestId
	if !requestIDPattern.MatchString(f.RequestID) {
		f.RequestID = ""
	}
	if retry := r.Header.Get("Retry-After"); retry != "" {
		if n, e := strconv.Atoi(retry); e == nil && n > 0 {
			f.RetryAfterSeconds = n
		}
	}
	return f
}

func decodeProblem(data []byte, result *api.Problem) error {
	var fields map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"code", "message", "request_id", "field_errors"} {
		if _, ok := fields[key]; !ok {
			return errors.New("required problem field missing")
		}
	}
	if err := json.Unmarshal(data, result); err != nil {
		return err
	}
	if result.Code == "" || result.Message == "" {
		return errors.New("required problem field empty")
	}
	var fieldErrors []stdjson.RawMessage
	if err := stdjson.Unmarshal(fields["field_errors"], &fieldErrors); err != nil || fieldErrors == nil {
		return errors.New("problem field errors invalid")
	}
	return nil
}

type rejection struct {
	kind          Kind
	code, message string
}

var definitiveRejections = map[int]map[string]rejection{
	400: {"invalid_request": {KindInvalid, "invalid_request", "request was rejected"}},
	401: {"unauthenticated": {KindAuthentication, "unauthenticated", "administrator authentication failed"}},
	403: {
		"origin_rejected":           {KindAuthentication, "origin_rejected", "request origin was rejected"},
		"secure_transport_required": {KindAuthentication, "secure_transport_required", "secure transport is required"},
		"csrf_invalid":              {KindAuthentication, "csrf_invalid", "authentication proof was rejected"},
	},
	405: {"method_not_allowed": {KindRemote, "method_not_allowed", "authentication operation is unavailable"}},
	408: {"request_timeout": {KindCancelled, "request_timeout", "server timed out processing setup"}},
	409: {"session_present": {KindConflict, "setup_already_complete", "setup is already complete"}},
	413: {"request_too_large": {KindInvalid, "request_too_large", "request was rejected as too large"}},
	415: {"unsupported_media_type": {KindInvalid, "unsupported_media_type", "request media type was rejected"}},
	422: {
		"password_unchanged": {KindInvalid, "password_unchanged", "new password must differ from the initial password"},
		"password_policy":    {KindInvalid, "password_policy", "new password does not meet server policy"},
	},
	429: {"rate_limited": {KindAuthentication, "rate_limited", "authentication is rate limited"}},
	501: {"capability_unavailable": {KindRemote, "capability_unavailable", "authentication operation is unavailable"}},
}

func mapProblem(status int, p api.Problem, state string, mutation bool) *Failure {
	if status == http.StatusConflict && (!mutation || state != "anonymous") {
		return nil
	}
	if status == http.StatusUnprocessableEntity && (!mutation || state != "restricted") {
		return nil
	}
	reason, ok := definitiveRejections[status][p.Code]
	if !ok {
		return nil
	}
	return &Failure{Kind: reason.kind, Code: reason.code, Message: reason.message}
}
func networkFailure(err error, mutation bool, state string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		message := "setup was cancelled or timed out"
		if mutation {
			message = uncertainOutcomeMessage
		}
		return &Failure{Kind: KindCancelled, Code: "cancelled", Message: message, OutcomeUnknown: mutation, SessionState: "unavailable"}
	}
	message := "could not contact the authentication backend"
	if mutation {
		message = uncertainOutcomeMessage
	}
	return &Failure{Kind: KindRemote, Code: map[bool]string{true: "outcome_unknown", false: "connection_failed"}[mutation], Message: message, OutcomeUnknown: mutation, SessionState: map[bool]string{true: "unchecked", false: state}[mutation]}
}
func cancelledFailure(unknown bool, state string) *Failure {
	if unknown {
		state = "unavailable"
	}
	message := "setup was cancelled or timed out"
	if unknown {
		message = uncertainOutcomeMessage
	}
	return &Failure{Kind: KindCancelled, Code: "cancelled", Message: message, OutcomeUnknown: unknown, SessionState: state}
}
func protocolFailure(state string, unknown bool) error {
	code, msg := "protocol_failure", "authentication response was invalid"
	if unknown {
		code = "outcome_unknown"
		msg = uncertainOutcomeMessage
	}
	return &Failure{Kind: KindRemote, Code: code, Message: msg, OutcomeUnknown: unknown, SessionState: state}
}
func requireCookie(r *http.Response, name string) error {
	_, ok := findCookie(r, name)
	if !ok {
		return errors.New("cookie invalid")
	}
	return nil
}
func findCookie(r *http.Response, name string) (*http.Cookie, bool) {
	var found *http.Cookie
	rawMatches := 0
	rawValue := ""
	for _, raw := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(raw, name+"=") {
			rawMatches++
			rawValue, _, _ = strings.Cut(strings.TrimPrefix(raw, name+"="), ";")
			first := true
			for attr := range strings.SplitSeq(raw, ";") {
				if first {
					first = false
					continue
				}
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attr)), "domain") {
					return nil, false
				}
			}
		}
	}
	for _, c := range r.Cookies() {
		if c.Name == name {
			if found != nil || c.Value != rawValue || !validCookieValue(c.Value) || c.MaxAge < 1 || (!c.Expires.IsZero() && !c.Expires.After(time.Now())) || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
				return nil, false
			}
			found = c
		}
	}
	return found, found != nil && rawMatches == 1
}

func validCookieValue(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func (c *Client) hasSessionCookie() bool {
	for _, cookie := range c.jar.Cookies(c.origin) {
		if cookie.Name == "__Host-almanac-session" && validCookieValue(cookie.Value) {
			return true
		}
	}
	return false
}
func cLogin(c *Client) string { return c.login }
func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }
