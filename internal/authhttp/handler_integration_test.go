//go:build integration

package authhttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func TestIntegrationTLSAuthenticationJourney(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var clockMu sync.Mutex
	nowFunc := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); now = now.Add(time.Microsecond); return now }
	currentTime := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	service, err := authentication.New(t.Context(), databaseURL, nowFunc)
	if err != nil {
		t.Fatal(err)
	}
	initialPassword := "Initial protected password 934!"
	passwordFile := filepath.Join(t.TempDir(), "initial-password")
	if err := os.WriteFile(passwordFile, []byte(initialPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(passwordFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnsureInitialOwner(t.Context(), "owner", passwordFile, "integration-bootstrap"); err != nil {
		t.Fatal(err)
	}
	handler, err := New(service, Options{PublicOrigin: "https://almanac.example", Now: nowFunc})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(func() { client.CloseIdleConnections() })
	call := func(path, body, csrf string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://almanac.example")
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data, response.Header
	}
	var contextHeaders http.Header
	readContext := func() api.AuthResponse {
		t.Helper()
		status, body, headers := call("/api/v1/auth/context", `{}`, "")
		if status != http.StatusOK {
			t.Fatalf("context status=%d", status)
		}
		contextHeaders = headers
		var response api.AuthResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	context := readContext()
	if context.Session != nil || context.CsrfToken == "" {
		t.Fatal("anonymous context did not contain an empty session and CSRF token")
	}
	preauth := integrationCookie(contextHeaders, preauthCookie)
	if preauth == nil || preauth.Value == "" || !preauth.Secure || !preauth.HttpOnly || preauth.SameSite != http.SameSiteLaxMode || preauth.Path != "/" || preauth.Domain != "" || preauth.MaxAge != 600 || !preauth.Expires.Equal(currentTime().Add(10*time.Minute).Truncate(time.Second)) {
		t.Fatal("context pre-authentication cookie attributes or exact expiry were invalid")
	}
	status, body, headers := call("/api/v1/auth/login", `{"login":"missing","password":"wrong password"}`, context.CsrfToken)
	if status != http.StatusUnauthorized || len((&http.Response{Header: headers}).Cookies()) != 0 {
		t.Fatalf("unknown login status=%d; session issuance=%t", status, len((&http.Response{Header: headers}).Cookies()) != 0)
	}
	status, body, headers = call("/api/v1/auth/login", `{"login":"owner","password":"incorrect password"}`, context.CsrfToken)
	if status != http.StatusUnauthorized || len((&http.Response{Header: headers}).Cookies()) != 0 {
		t.Fatalf("wrong-password login status=%d; session issuance=%t", status, len((&http.Response{Header: headers}).Cookies()) != 0)
	}
	status, _, headers = call("/api/v1/auth/login", `{"login":"owner","password":"short"}`, context.CsrfToken)
	if status != http.StatusUnauthorized || len((&http.Response{Header: headers}).Cookies()) != 0 {
		t.Fatalf("short-password login status=%d; session issuance=%t", status, len((&http.Response{Header: headers}).Cookies()) != 0)
	}
	status, body, headers = call("/api/v1/auth/login", `{"login":"owner","password":"`+initialPassword+`"}`, context.CsrfToken)
	if status != http.StatusOK {
		t.Fatalf("restricted login status=%d", status)
	}
	if len(headers.Values("Set-Cookie")) < 2 {
		t.Fatal("login did not set the session and clear the pre-authentication cookie")
	}
	loginSessionCookie, loginPreauthCookie := integrationCookie(headers, sessionCookie), integrationCookie(headers, preauthCookie)
	if !secureSessionCookie(loginSessionCookie) || loginSessionCookie.MaxAge < 1 || loginSessionCookie.MaxAge > 43200 || !deletedCookie(loginPreauthCookie) {
		t.Fatal("login cookie issuance or pre-authentication deletion attributes were invalid")
	}
	loginAccess, err := service.Inspect(t.Context(), loginSessionCookie.Value)
	if err != nil || !loginSessionCookie.Expires.Equal(loginAccess.AbsoluteExpiresAt.Truncate(time.Second)) {
		t.Fatal("login cookie expiry did not match the persisted absolute session expiry")
	}
	var loggedIn api.AuthResponse
	if err := json.Unmarshal(body, &loggedIn); err != nil {
		t.Fatal("login response did not match the generated authentication contract")
	}
	if loggedIn.Session == nil || !loggedIn.Session.Restricted || strings.Contains(string(body), "secret") || strings.Contains(string(body), "verifier") || strings.Contains(string(body), "session_id") {
		t.Fatal("restricted login response contained credential or unsafe identity data")
	}
	if headers.Get("Cache-Control") != "no-store" || headers.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("login response cache or CORS headers violated the security contract")
	}
	var issuedCookie *http.Cookie
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		if cookie.Name == sessionCookie {
			issuedCookie = cookie
		}
	}
	if issuedCookie == nil || !issuedCookie.Secure || !issuedCookie.HttpOnly || issuedCookie.SameSite != http.SameSiteLaxMode || issuedCookie.Path != "/" || issuedCookie.Domain != "" || issuedCookie.MaxAge < 1 || issuedCookie.MaxAge > 43200 {
		t.Fatalf("session cookie security attributes invalid: present=%t secure=%t httpOnly=%t sameSite=%t path=%t noDomain=%t maxAgeValid=%t", issuedCookie != nil, issuedCookie != nil && issuedCookie.Secure, issuedCookie != nil && issuedCookie.HttpOnly, issuedCookie != nil && issuedCookie.SameSite == http.SameSiteLaxMode, issuedCookie != nil && issuedCookie.Path == "/", issuedCookie != nil && issuedCookie.Domain == "", issuedCookie != nil && issuedCookie.MaxAge >= 1 && issuedCookie.MaxAge <= 43200)
	}
	firstTab, secondTab := readContext(), readContext()
	if firstTab.CsrfToken == secondTab.CsrfToken || firstTab.Session == nil || secondTab.Session == nil {
		t.Fatal("context did not issue independent valid tab tokens for the current session")
	}
	for _, token := range []string{firstTab.CsrfToken, secondTab.CsrfToken} {
		status, _, _ := call("/api/v1/auth/password", `{"new_password":"too short"}`, token)
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("valid tab token password policy status=%d", status)
		}
	}
	status, body, headers = call("/api/v1/auth/password", `{"new_password":"too short"}`, firstTab.CsrfToken)
	var policyProblem api.Problem
	if status != http.StatusUnprocessableEntity || json.Unmarshal(body, &policyProblem) != nil || policyProblem.Code != "password_policy" || len(policyProblem.FieldErrors) != 1 || policyProblem.FieldErrors[0].Field != "new_password" || len(headers.Values("Set-Cookie")) != 0 {
		t.Fatalf("new-password policy response status=%d code=%q field-error-count=%d cookie-issued=%t", status, policyProblem.Code, len(policyProblem.FieldErrors), len(headers.Values("Set-Cookie")) != 0)
	}
	context = readContext()
	if context.Session == nil || !context.Session.Restricted {
		t.Fatal("password policy rejection replaced the restricted session")
	}
	oldSession := ""
	for _, cookie := range jar.Cookies(mustURL(t, server.URL)) {
		if cookie.Name == sessionCookie {
			oldSession = cookie.Value
		}
	}
	if oldSession == "" {
		t.Fatal("login did not place the session cookie in the jar")
	}
	permanent := "Permanent password phrase 467!"
	status, body, headers = call("/api/v1/auth/password", `{"new_password":"`+permanent+`"}`, loggedIn.CsrfToken)
	if status != http.StatusOK {
		t.Fatalf("password rotation status=%d", status)
	}
	replacementCookie := integrationCookie(headers, sessionCookie)
	if !secureSessionCookie(replacementCookie) {
		t.Fatal("password rotation did not issue a protected replacement session cookie")
	}
	replacementAccess, inspectErr := service.Inspect(t.Context(), replacementCookie.Value)
	if inspectErr != nil || !replacementCookie.Expires.Equal(replacementAccess.AbsoluteExpiresAt.Truncate(time.Second)) {
		t.Fatal("password rotation cookie expiry did not match the persisted absolute session expiry")
	}
	var rotated api.AuthResponse
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatal("password rotation response did not match the generated authentication contract")
	}
	if rotated.Session == nil || rotated.Session.Restricted {
		t.Fatal("password rotation did not issue a normal session")
	}
	status, body, _ = call("/api/v1/auth/logout", `{}`, loggedIn.CsrfToken)
	if status != http.StatusForbidden {
		t.Fatalf("pre-rotation CSRF status=%d", status)
	}
	if _, err := service.Authenticate(t.Context(), oldSession); err == nil {
		t.Fatal("old restricted session remained valid after password rotation")
	}
	restarted, err := New(service, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewTLSServer(restarted)
	t.Cleanup(server.Close)
	client.CloseIdleConnections()
	roots = x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client = &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(func() { client.CloseIdleConnections() })
	status, _, _ = call("/api/v1/auth/logout", `{}`, rotated.CsrfToken)
	if status != http.StatusForbidden {
		t.Fatalf("restart did not invalidate previous CSRF token: status=%d", status)
	}
	context = readContext()
	if context.Session == nil || context.Session.Restricted {
		t.Fatal("normal authentication context was absent or restricted")
	}
	currentSession := ""
	for _, cookie := range jar.Cookies(mustURL(t, server.URL)) {
		if cookie.Name == sessionCookie {
			currentSession = cookie.Value
		}
	}
	if currentSession == "" {
		t.Fatal("normal session cookie was absent after handler restart")
	}
	digest, err := identity.SessionSecretVerifier(currentSession)
	if err != nil {
		t.Fatal("normal session cookie was invalid")
	}
	pg := testpostgres.Connect(t, databaseURL)
	var beforeInspect time.Time
	if err := pg.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", digest[:]).Scan(&beforeInspect); err != nil {
		t.Fatal("read session activity failed")
	}
	for range 3 {
		_ = readContext()
	}
	var afterInspect time.Time
	if err := pg.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", digest[:]).Scan(&afterInspect); err != nil {
		t.Fatal("read session activity after context failed")
	}
	if !afterInspect.Equal(beforeInspect) {
		t.Fatal("repeated authentication context requests extended idle session activity")
	}
	status, body, headers = call("/api/v1/auth/logout", `{}`, context.CsrfToken)
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("logout status=%d body-empty=%t", status, len(body) == 0)
	}
	if !deletedCookie(integrationCookie(headers, sessionCookie)) || !deletedCookie(integrationCookie(headers, preauthCookie)) {
		t.Fatal("logout did not delete both protected authentication cookies")
	}
	context = readContext()
	if context.Session != nil {
		t.Fatal("logout left an authenticated session in context")
	}
	status, _, headers = call("/api/v1/auth/login", `{"login":"owner","password":"`+initialPassword+`"}`, context.CsrfToken)
	if status != http.StatusUnauthorized || len((&http.Response{Header: headers}).Cookies()) != 0 {
		t.Fatalf("old password status=%d; session issuance=%t", status, len((&http.Response{Header: headers}).Cookies()) != 0)
	}
}

type integrationStalledBody struct {
	prefix  []byte
	read    int
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (b *integrationStalledBody) Read(p []byte) (int, error) {
	if b.read < len(b.prefix) {
		n := copy(p, b.prefix[b.read:])
		b.read += n
		return n, nil
	}
	<-b.release
	close(b.done)
	return 0, io.EOF
}
func (b *integrationStalledBody) Close() error {
	b.once.Do(func() { close(b.release) })
	return nil
}

func TestIntegrationTLSBoundedChunkedBodyAndCleanup(t *testing.T) {
	handler, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	closedConnections := make(chan struct{}, 2)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closedConnections <- struct{}{}:
			default:
			}
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	for _, tc := range []struct {
		name   string
		prefix []byte
		want   int
	}{
		{name: "stalled", prefix: []byte(`{"`), want: http.StatusRequestTimeout},
		{name: "oversized", prefix: []byte(strings.Repeat("x", bodyLimit+1)), want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			target.Path = "/api/v1/auth/context"
			body := &integrationStalledBody{prefix: tc.prefix, release: make(chan struct{}), done: make(chan struct{})}
			defer body.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", "https://almanac.example")
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatalf("bounded request failed: %v", err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, tc.want)
			}
			select {
			case <-closedConnections:
			case <-time.After(time.Second):
				t.Fatal("server connection remained open after bounded body rejection")
			}
			body.Close()
			select {
			case <-body.done:
			case <-time.After(time.Second):
				t.Fatal("test client request body reader did not unblock during cleanup")
			}
		})
	}
}

func TestIntegrationLoginBudgetsUseOnlyConfiguredProxySource(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	service, err := authentication.New(t.Context(), databaseURL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler, err := New(service, Options{PublicOrigin: "https://almanac.example", TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewTLSServer(proxyHandler)
	t.Cleanup(proxyServer.Close)
	proxyClient := tlsClientFor(t, proxyServer)
	trustedPreauth, err := randomPreauth()
	if err != nil {
		t.Fatal(err)
	}
	trustedNonce, err := csrfNonce()
	if err != nil {
		t.Fatal(err)
	}
	trustedToken := proxyHandler.csrf("login", trustedPreauth, trustedNonce)
	trustedAttempt := func(login, source string) (int, string) {
		return postFailedLogin(t, proxyClient, proxyServer.URL, login, trustedPreauth, trustedToken, true, source)
	}
	for i := range 20 {
		status, _ := trustedAttempt("trusted-a-"+strconv.Itoa(i), "198.51.100.1")
		if status != http.StatusUnauthorized {
			t.Fatalf("trusted source first budget status=%d at attempt=%d", status, i+1)
		}
	}
	if status, _ := trustedAttempt("trusted-b", "203.0.113.2"); status != http.StatusUnauthorized {
		t.Fatalf("second trusted source shared the first source budget: status=%d", status)
	}
	status, retryAfter := trustedAttempt("trusted-a-over", "198.51.100.1")
	if status != http.StatusTooManyRequests || !positiveInteger(retryAfter) {
		t.Fatalf("trusted source throttle status=%d retry-after-positive=%t", status, positiveInteger(retryAfter))
	}

	plainHandler, err := New(service, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	plainServer := httptest.NewTLSServer(plainHandler)
	t.Cleanup(plainServer.Close)
	plainClient := tlsClientFor(t, plainServer)
	plainPreauth, err := randomPreauth()
	if err != nil {
		t.Fatal(err)
	}
	plainNonce, err := csrfNonce()
	if err != nil {
		t.Fatal(err)
	}
	plainToken := plainHandler.csrf("login", plainPreauth, plainNonce)
	for i := range 20 {
		status, _ := postFailedLogin(t, plainClient, plainServer.URL, "plain-"+strconv.Itoa(i), plainPreauth, plainToken, false, "198.51.100."+strconv.Itoa(i+1))
		if status != http.StatusUnauthorized {
			t.Fatalf("untrusted forwarded address changed shared source budget: status=%d attempt=%d", status, i+1)
		}
	}
	status, retryAfter = postFailedLogin(t, plainClient, plainServer.URL, "plain-over", plainPreauth, plainToken, false, "203.0.113.8")
	if status != http.StatusTooManyRequests || !positiveInteger(retryAfter) {
		t.Fatalf("untrusted forwarding evaded source throttle: status=%d retry-after-positive=%t", status, positiveInteger(retryAfter))
	}
}

func tlsClientFor(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func postFailedLogin(t *testing.T, client *http.Client, serverURL, login, preauth, csrf string, trusted bool, source string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL+"/api/v1/auth/login", strings.NewReader(`{"login":"`+login+`","password":"A wrong password phrase 218!"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://almanac.example")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(&http.Cookie{Name: preauthCookie, Value: preauth})
	if trusted {
		request.Header.Set("X-Forwarded-Proto", "https")
	}
	request.Header.Set("X-Forwarded-For", source)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("authentication request failed: %v", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode, response.Header.Get("Retry-After")
}

func positiveInteger(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

func integrationDatabase(t *testing.T) string { t.Helper(); return testpostgres.Database(t) }

func integrationCookie(headers http.Header, name string) *http.Cookie {
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func secureSessionCookie(cookie *http.Cookie) bool {
	return cookie != nil && cookie.Value != "" && cookie.Secure && cookie.HttpOnly && cookie.SameSite == http.SameSiteLaxMode && cookie.Path == "/" && cookie.Domain == "" && !cookie.Expires.IsZero()
}

func deletedCookie(cookie *http.Cookie) bool {
	return cookie != nil && cookie.Value == "" && cookie.MaxAge < 0 && cookie.Secure && cookie.HttpOnly && cookie.SameSite == http.SameSiteLaxMode && cookie.Path == "/" && cookie.Domain == ""
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
