package uihttp

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/config"
	webassets "github.com/dsolsona/almanac/web"
)

var testHTTPClient = &http.Client{Timeout: 2 * time.Second}
var longTestHTTPClient = &http.Client{Timeout: 10 * time.Second}

func TestOperationalAndEmbeddedRoutes(t *testing.T) {
	backend := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()
	server := testServer(t, backend.URL)
	frontend := newTestServer(t, server.http.Handler)
	defer frontend.Close()

	for _, tc := range []struct{ path, wantType string }{
		{"/", "text/html"},
		{"/agents?agent=example&agents_cursor=next&credentials_cursor=history", "text/html"},
		{"/datasources", "text/html"},
		{"/datasources/8ed93d7e-51e3-4a3e-9222-3e8a612de20a", "text/html"},
	} {
		response, err := testHTTPClient.Get(frontend.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), tc.wantType) || !strings.Contains(string(body), "Almanac") {
			t.Fatalf("unexpected embedded root: %d %s %s", response.StatusCode, response.Header.Get("Content-Type"), body)
		}
		if response.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("index cache control = %q", response.Header.Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/design-system", "/missing.js", "/assets/missing.js", "/agents/extra"} {
		response, err := testHTTPClient.Get(frontend.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d", path, response.StatusCode)
		}
	}
	assetName := fingerprintedAssetPath(t)
	asset, err := testHTTPClient.Get(frontend.URL + "/" + assetName)
	if err != nil {
		t.Fatal(err)
	}
	asset.Body.Close()
	if asset.StatusCode != http.StatusOK || asset.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("fingerprinted asset headers = %d %q", asset.StatusCode, asset.Header.Get("Cache-Control"))
	}
	for _, path := range []string{"/livez", "/readyz"} {
		response, err := testHTTPClient.Get(frontend.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
			t.Errorf("%s = %d %s", path, response.StatusCode, body)
		}
	}
	request, _ := http.NewRequest(http.MethodPost, frontend.URL+"/livez", nil)
	response, err := testHTTPClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /livez = %d", response.StatusCode)
	}
}

func TestNewAssetHandlerServesEmbeddedRoot(t *testing.T) {
	handler, err := NewAssetHandler()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(response.Body.String(), "Almanac") {
		t.Fatalf("embedded root = %d %q", response.Code, response.Body.String())
	}
	for _, target := range []string{"/design-system", "/missing.js", "/%2e%2e/index.html", "/agents/extra"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s returned %d", target, response.Code)
		}
	}
}

func TestProxyFixedOriginAndRestrictions(t *testing.T) {
	got := make(chan *http.Request, 1)
	backend := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Clone(context.Background())
		w.WriteHeader(http.StatusNoContent)
	}))
	server := testServer(t, backend.URL)
	frontend := newTestServer(t, server.http.Handler)
	request, _ := http.NewRequest(http.MethodPost, frontend.URL+"/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/disable?view=history", strings.NewReader("body"))
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("Cookie", "session=test")
	request.Header.Set("If-Match", `"agent:abcdefab-cdef-4abc-8def-abcdefabcdee:7"`)
	request.Header.Set("Idempotency-Key", "proxy,receipt")
	request.Header.Set("X-Forwarded-Host", "attacker.example")
	request.Header.Set("X-Real-Ip", "192.0.2.12")
	response, err := testHTTPClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}
	forwarded := awaitValue(t, got, "proxied request")
	if forwarded.URL.Path != "/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/disable" || forwarded.URL.RawQuery != "view=history" {
		t.Errorf("forwarded URL = %s", forwarded.URL)
	}
	if forwarded.Header.Get("Authorization") != "Bearer test" || forwarded.Header.Get("Cookie") != "session=test" {
		t.Error("auth/cookie headers were not forwarded")
	}
	if forwarded.Header.Get("If-Match") != `"agent:abcdefab-cdef-4abc-8def-abcdefabcdee:7"` || forwarded.Header.Get("Idempotency-Key") != "proxy,receipt" {
		t.Error("management conditional/idempotency headers were not forwarded")
	}
	if forwarded.Header.Get("X-Forwarded-Host") != "" || forwarded.Header.Get("X-Forwarded-For") != "" || forwarded.Header.Get("X-Real-Ip") != "" {
		t.Error("untrusted forwarding headers were retained")
	}
	noRedirect := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/api/v1/../internal/v1/work", "/api/v1/%2e%2e/internal/v1/work", "/api/v1/%252e%252e/internal/v1/work", "/api/agent/v1/heartbeat", "/internal/v1/work"} {
		resp, err := noRedirect.Get(frontend.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d", path, resp.StatusCode)
		}
	}
	trace, _ := http.NewRequest(http.MethodTrace, frontend.URL+"/api/v1/slis", nil)
	resp, err := testHTTPClient.Do(trace)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("TRACE status = %d", resp.StatusCode)
	}
}

func TestGeneratedAuthenticationOperationsDelegateToFixedBackendProxy(t *testing.T) {
	got := make(chan *http.Request, 4)
	backend := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Clone(context.Background())
		w.WriteHeader(http.StatusNoContent)
	}))
	server := testServer(t, backend.URL)
	frontend := newTestServer(t, server.http.Handler)
	for _, path := range []string{"context", "login", "password", "logout"} {
		t.Run(path, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, frontend.URL+"/api/v1/auth/"+path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-Forwarded-For", "198.51.100.9")
			response, err := testHTTPClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("proxy status=%d", response.StatusCode)
			}
			forwarded := awaitValue(t, got, "delegated auth request")
			if forwarded.URL.Path != "/api/v1/auth/"+path || forwarded.Header.Get("X-Forwarded-For") != "" {
				t.Fatalf("forwarded auth request path=%q headers=%v", forwarded.URL.Path, forwarded.Header)
			}
		})
	}
}

func TestSafeAPIPathRejectsDecodedTraversal(t *testing.T) {
	for _, target := range []string{"/api/v1/%2e%2e/internal", "/api/v1/%252e%252e/internal", "/api/v1/%5c..%5cinternal"} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if safeAPIPath(r) {
			t.Errorf("accepted traversal path %q", target)
		}
	}
	for _, target := range []string{"/api/v1/slis", "/api/v1/services/7/labels"} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if !safeAPIPath(r) {
			t.Errorf("rejected management path %q", target)
		}
	}
}

func TestGracefulShutdownDrainsAndClosesListener(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	backend := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	server := testServer(t, backend.URL)
	t.Cleanup(func() { _ = server.Close() })
	t.Cleanup(releaseHandler)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	requestCtx, cancelRequest := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listener.Addr().String()+"/api/v1/slis", nil)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := testHTTPClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		requestDone <- err
	}()
	awaitSignal(t, started, "upstream request start")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(ctx) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown completed before in-flight request drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseHandler()
	if err := awaitValue(t, shutdownDone, "graceful shutdown"); err != nil {
		t.Fatal(err)
	}
	if err := awaitValue(t, requestDone, "in-flight request completion"); err != nil {
		t.Fatal(err)
	}
	if err := awaitValue(t, serveDone, "listener shutdown"); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve returned %v", err)
	}
}

func TestProxyPropagatesRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	backend := newTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	server := testServer(t, backend.URL)
	frontend := newTestServer(t, server.http.Handler)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, timeoutCancel := context.WithTimeout(ctx, 10*time.Second)
	defer timeoutCancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, frontend.URL+"/api/v1/slis", nil)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		response, _ := longTestHTTPClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		close(requestDone)
	}()
	awaitSignal(t, started, "upstream request start")
	cancel()
	awaitSignalWithin(t, canceled, "upstream cancellation", 500*time.Millisecond)
	awaitSignalWithin(t, requestDone, "cancelled client request completion", time.Second)
}

func TestShutdownDeadlineForcesInFlightRequestClosed(t *testing.T) {
	started := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	backend := newTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	server := testServer(t, backend.URL)
	t.Cleanup(func() { _ = server.Close() })
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	requestCtx, cancelRequest := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listener.Addr().String()+"/api/v1/slis", nil)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		response, _ := longTestHTTPClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		close(requestDone)
	}()
	awaitSignal(t, started, "upstream request start")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	awaitSignalWithin(t, upstreamCanceled, "forced-close cancellation", 500*time.Millisecond)
	awaitSignalWithin(t, requestDone, "forced-close client completion", time.Second)
	if err := awaitValue(t, serveDone, "listener close"); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve returned %v", err)
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	awaitSignalWithin(t, signal, description, 2*time.Second)
}

func awaitSignalWithin(t *testing.T, signal <-chan struct{}, description string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitValue[T any](t *testing.T, result <-chan T, description string) T {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case value := <-result:
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func fingerprintedAssetPath(t *testing.T) string {
	t.Helper()
	assets, err := webassets.Assets()
	if err != nil {
		t.Fatal(err)
	}
	var found string
	err = fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && fingerprintedAsset.MatchString(path.Base(name)) {
			found = name
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatal("no fingerprinted production asset found")
	}
	return found
}

func TestReadinessOutageAndRedirectAreUnavailable(t *testing.T) {
	for _, handler := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://elsewhere.invalid/readyz", http.StatusFound)
		},
	} {
		backend := newTestServer(t, handler)
		server := testServer(t, backend.URL)
		frontend := newTestServer(t, server.http.Handler)
		response, err := testHTTPClient.Get(frontend.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"status":"unavailable"`) {
			t.Errorf("readiness = %d %s", response.StatusCode, body)
		}
		frontend.Close()
		backend.Close()
	}
	server := testServer(t, "http://127.0.0.1:1")
	frontend := newTestServer(t, server.http.Handler)
	defer frontend.Close()
	response, err := testHTTPClient.Get(frontend.URL + "/api/v1/slis")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"code":"backend_unavailable"`) || !strings.Contains(string(body), `"field_errors":[]`) || !strings.Contains(string(body), `"request_id":"`) || strings.Contains(string(body), "127.0.0.1:1") {
		t.Errorf("proxy problem = %d %s", response.StatusCode, body)
	}
}

func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func testServer(t *testing.T, backendURL string) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.Components = "ui"
	cfg.BackendURL = backendURL
	server, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return server
}
