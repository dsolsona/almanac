package setupclient

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/dsolsona/almanac/internal/api"
)

// SessionOptions binds a protected credential to one HTTPS backend and CA bundle.
type SessionOptions struct {
	BackendURL string
	CABundle   []byte
	Session    string `json:"-"`
}

// SessionClient performs explicit authentication operations without retrying
// mutations. It is used by one operation at a time, under the profile lock.
type SessionClient struct {
	client  *Client
	session string
	preauth string
}

func NewSession(options SessionOptions) (*SessionClient, error) {
	if options.Session != "" && !validCookieValue(options.Session) {
		return nil, &Failure{Kind: KindInvalid, Code: "invalid_configuration", Message: "stored session is invalid", SessionState: "unavailable"}
	}
	c, err := newClient(Options{BackendURL: options.BackendURL, CABundle: options.CABundle}, false)
	if err != nil {
		return nil, err
	}
	s := &SessionClient{client: c}
	s.restore(options.Session)
	return s, nil
}

func (s *SessionClient) Close() { s.client.Close() }

// Session returns the credential only for protected persistence, never output.
func (s *SessionClient) Session() string { return s.session }

// AgentOperations is the generated agent-management surface available to an
// authenticated administrator session. It deliberately excludes arbitrary
// HTTP requests and the cookie jar.
type AgentOperations interface {
	GetAgents(context.Context, *api.GetAgentsParams, ...api.RequestEditorFn) (*http.Response, error)
	PostAgents(context.Context, *api.PostAgentsParams, api.PostAgentsJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetAgent(context.Context, api.AgentId, ...api.RequestEditorFn) (*http.Response, error)
	GetAgentCredentials(context.Context, api.AgentId, *api.GetAgentCredentialsParams, ...api.RequestEditorFn) (*http.Response, error)
	PostAgentCredentialRotate(context.Context, api.AgentId, *api.PostAgentCredentialRotateParams, api.PostAgentCredentialRotateJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	PostAgentCredentialRevoke(context.Context, api.AgentId, api.AgentId, *api.PostAgentCredentialRevokeParams, api.PostAgentCredentialRevokeJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	PostAgentDisable(context.Context, api.AgentId, *api.PostAgentDisableParams, api.PostAgentDisableJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
}

// DatasourceOperations exposes only the generated datasource and diagnostic
// routes to callers that have refreshed their human session.
type DatasourceOperations interface {
	GetDatasources(context.Context, *api.GetDatasourcesParams, ...api.RequestEditorFn) (*http.Response, error)
	GetProviders(context.Context, *api.GetProvidersParams, ...api.RequestEditorFn) (*http.Response, error)
	GetDatasource(context.Context, api.DatasourceId, ...api.RequestEditorFn) (*http.Response, error)
	PostDatasources(context.Context, *api.PostDatasourcesParams, api.PostDatasourcesJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	PutDatasource(context.Context, api.DatasourceId, *api.PutDatasourceParams, api.PutDatasourceJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	PostDatasourceConnectionTest(context.Context, api.PostDatasourceConnectionTestJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	PostSavedDatasourceConnectionTest(context.Context, api.DatasourceId, *api.PostSavedDatasourceConnectionTestParams, api.PostSavedDatasourceConnectionTestJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	PostDatasourceQueryPreview(context.Context, api.DatasourceId, api.PostDatasourceQueryPreviewJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetDiagnostic(context.Context, api.DatasourceId, ...api.RequestEditorFn) (*http.Response, error)
	PostDiagnosticCancel(context.Context, api.DatasourceId, api.PostDiagnosticCancelJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	ListSlis(context.Context, *api.ListSlisParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateSli(context.Context, *api.CreateSliParams, api.CreateSliJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetSli(context.Context, api.SliId, ...api.RequestEditorFn) (*http.Response, error)
	PatchSliMetadata(context.Context, api.SliId, *api.PatchSliMetadataParams, api.PatchSliMetadataJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	ListSliObservations(context.Context, api.SliId, *api.ListSliObservationsParams, ...api.RequestEditorFn) (*http.Response, error)
}

// AgentContext refreshes authority and CSRF state before exposing the narrow
// generated operation set. The caller must hold the profile lock throughout.
func (s *SessionClient) AgentContext(ctx context.Context) (AgentOperations, api.RequestEditorFn, error) {
	auth, err := s.context(ctx)
	if err != nil {
		return nil, nil, err
	}
	if auth.Session == nil {
		return nil, nil, &Failure{Kind: KindAuthentication, Code: "unauthenticated", Message: "sign in before managing agents", SessionState: "anonymous"}
	}
	if auth.Session.Restricted || auth.Session.Role != "administrator" {
		return nil, nil, &Failure{Kind: KindAuthentication, Code: "forbidden", Message: "an unrestricted administrator session is required", SessionState: sessionState(auth.Session)}
	}
	return s.client.api, s.client.headers(auth.CsrfToken), nil
}

// DatasourceContext rechecks the session on every command before exposing the
// bounded generated API surface. Server authorization remains authoritative.
func (s *SessionClient) DatasourceContext(ctx context.Context) (DatasourceOperations, api.RequestEditorFn, *api.SafeIdentity, error) {
	auth, err := s.context(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if auth.Session == nil {
		return nil, nil, nil, &Failure{Kind: KindAuthentication, Code: "unauthenticated", Message: "sign in before managing datasources", SessionState: "anonymous"}
	}
	if auth.Session.Restricted {
		return nil, nil, auth.Session, &Failure{Kind: KindAuthentication, Code: "forbidden", Message: "an unrestricted session is required", SessionState: sessionState(auth.Session)}
	}
	return s.client.api, s.client.headers(auth.CsrfToken), auth.Session, nil
}

func (s *SessionClient) restore(value string) {
	s.session = value
	cookie := &http.Cookie{Name: "__Host-almanac-session", Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if value == "" {
		cookie.MaxAge = -1
	}
	s.client.jar.SetCookies(s.client.origin, []*http.Cookie{cookie})
}

func (s *SessionClient) Context(ctx context.Context) (*api.SafeIdentity, error) {
	auth, err := s.context(ctx)
	return auth.Session, err
}

func (s *SessionClient) context(ctx context.Context) (api.AuthResponse, error) {
	if ctx.Err() != nil {
		return api.AuthResponse{}, sessionCancelled(false)
	}
	preauth := ""
	for _, cookie := range s.client.jar.Cookies(s.client.origin) {
		if cookie.Name == "__Host-almanac-preauth" && cookie.Value == s.preauth {
			preauth = s.preauth
		}
	}
	r, err := s.client.api.PostAuthContext(ctx, api.PostAuthContextJSONRequestBody{}, s.client.headers(""))
	// Context cannot rotate credentials. Restore the trusted value even if a
	// malformed response made the HTTP cookie jar accept a different cookie.
	s.restore(s.session)
	if err != nil {
		return api.AuthResponse{}, sessionReadFailure(ctx)
	}
	body, err := s.client.read(r)
	if err != nil {
		return api.AuthResponse{}, sessionReadFailure(ctx)
	}
	if ctx.Err() != nil {
		return api.AuthResponse{}, sessionCancelled(false)
	}
	if r.StatusCode != http.StatusOK {
		if f := sessionResponseFailure(r, body, false); f != nil {
			return api.AuthResponse{}, f
		}
		return api.AuthResponse{}, protocolFailure("unavailable", false)
	}
	auth, err := decodeAuth(body)
	if err != nil {
		return api.AuthResponse{}, protocolFailure("unavailable", false)
	}
	if auth.Session == nil {
		supplied := false
		for _, raw := range r.Header.Values("Set-Cookie") {
			if strings.HasPrefix(strings.TrimSpace(raw), "__Host-almanac-preauth=") {
				supplied = true
			}
		}
		if supplied {
			cookie, valid := findCookie(r, "__Host-almanac-preauth")
			if !valid {
				s.preauth = ""
				return api.AuthResponse{}, protocolFailure("unavailable", false)
			}
			preauth = cookie.Value
		}
		if preauth == "" {
			return api.AuthResponse{}, protocolFailure("unavailable", false)
		}
		s.preauth = preauth
		s.restore("")
	} else if s.session == "" {
		return api.AuthResponse{}, protocolFailure("unavailable", false)
	}
	return auth, nil
}

func (s *SessionClient) Login(ctx context.Context, login, password string) (*api.SafeIdentity, error) {
	if login == "" || len(login) > 256 || !utf8Valid(login) || password == "" || len(password) > 512 || !utf8Valid(password) {
		return nil, &Failure{Kind: KindInvalid, Code: "invalid_credentials", Message: "login or password is invalid", SessionState: "unchecked"}
	}
	auth, err := s.context(ctx)
	if err != nil {
		return nil, err
	}
	if auth.Session != nil {
		return nil, &Failure{Kind: KindConflict, Code: "session_present", Message: "profile already has an active session; log out explicitly before signing in", SessionState: sessionState(auth.Session)}
	}
	if ctx.Err() != nil {
		return nil, sessionCancelled(false)
	}
	r, err := s.client.api.PostAuthLogin(ctx, api.LoginRequest{Login: login, Password: password}, s.client.headers(auth.CsrfToken))
	if err != nil {
		s.restore("")
		return nil, s.uncertainLogin(ctx)
	}
	// Validate attributes before exposing a jar credential to persistence. A
	// valid header remains useful when the response body is lost or malformed.
	cookie, valid := findCookie(r, "__Host-almanac-session")
	if valid && r.StatusCode == http.StatusOK {
		s.restore(cookie.Value)
	} else {
		s.restore("")
	}
	body, err := s.client.read(r)
	if err != nil {
		return nil, s.uncertainLogin(ctx)
	}
	if r.StatusCode != http.StatusOK {
		if f := sessionResponseFailure(r, body, true); f != nil {
			return nil, f
		}
		return nil, s.uncertainLogin(ctx)
	}
	auth, err = decodeAuth(body)
	if err != nil || auth.Session == nil || !valid {
		return nil, s.uncertainLogin(ctx)
	}
	if ctx.Err() != nil {
		return nil, s.uncertainLogin(ctx)
	}
	return auth.Session, nil
}

func (s *SessionClient) Logout(ctx context.Context) error {
	auth, err := s.context(ctx)
	if err != nil {
		return err
	}
	if auth.Session == nil {
		return nil
	}
	if ctx.Err() != nil {
		return sessionCancelled(false)
	}
	original := s.session
	r, err := s.client.api.PostAuthLogout(ctx, api.PostAuthLogoutJSONRequestBody{}, s.client.headers(auth.CsrfToken))
	s.restore(original)
	if err != nil {
		return sessionUnknown(ctx)
	}
	body, err := s.client.read(r)
	if err != nil {
		return sessionUnknown(ctx)
	}
	if r.StatusCode == http.StatusNoContent && len(body) == 0 {
		s.restore("")
		return nil
	}
	if f := sessionResponseFailure(r, body, false); f != nil {
		return f
	}
	return sessionUnknown(ctx)
}

func (s *SessionClient) uncertainLogin(ctx context.Context) *Failure {
	f := sessionUnknown(ctx)
	if ctx.Err() == nil {
		auth, err := s.context(ctx)
		if err == nil {
			f.SessionState = sessionState(auth.Session)
		}
	}
	if ctx.Err() != nil {
		f.Kind = KindCancelled
		f.SessionState = "unavailable"
	}
	return f
}

func sessionState(identity *api.SafeIdentity) string {
	if identity == nil {
		return "anonymous"
	}
	if identity.Restricted {
		return "restricted"
	}
	return "authenticated"
}

func sessionUnknown(ctx context.Context) *Failure {
	f := &Failure{Kind: KindRemote, Code: "outcome_unknown", Message: "authentication outcome is unknown; check session state before deliberately signing in or out again", OutcomeUnknown: true, SessionState: "unchecked"}
	if ctx.Err() != nil {
		f.Kind = KindCancelled
		f.SessionState = "unavailable"
	}
	return f
}

func sessionCancelled(unknown bool) *Failure {
	return &Failure{Kind: KindCancelled, Code: "cancelled", Message: "authentication was cancelled or timed out", OutcomeUnknown: unknown, SessionState: "unavailable"}
}

func sessionReadFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return sessionCancelled(false)
	}
	return &Failure{Kind: KindRemote, Code: "connection_failed", Message: "authentication context is unavailable", SessionState: "unavailable"}
}

func sessionResponseFailure(r *http.Response, body []byte, login bool) *Failure {
	var problem api.Problem
	if decodeProblem(body, &problem) != nil || r.StatusCode == http.StatusUnprocessableEntity {
		return nil
	}
	f := mapProblem(r.StatusCode, problem, "anonymous", login)
	if f == nil {
		return nil
	}
	f.SessionState = "unchecked"
	switch f.Code {
	case "setup_already_complete":
		f.Code = "session_present"
		f.Message = "an active session already exists"
	case "unauthenticated":
		f.Message = "authentication failed"
	case "request_timeout":
		f.Message = "server timed out processing authentication"
	}
	if requestIDPattern.MatchString(problem.RequestId) {
		f.RequestID = problem.RequestId
	}
	if retry, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil && retry > 0 && retry <= 86400 {
		f.RetryAfterSeconds = retry
	}
	return f
}
