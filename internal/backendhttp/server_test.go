package backendhttp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/config"
)

func TestNewRejectsUnsupportedComponentsAndAssetMismatches(t *testing.T) {
	assets := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	service := &authentication.Service{}
	for _, tc := range []struct {
		name       string
		components string
		assets     http.Handler
	}{
		{name: "combined missing assets", components: "backend,ui"},
		{name: "backend unexpected assets", components: "backend", assets: assets},
		{name: "alerts unsupported", components: "backend,ui,alerts", assets: assets},
		{name: "unsupported pair", components: "backend,alerts", assets: assets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Components: tc.components}
			if server, err := New(context.Background(), cfg, service, "postgres://unused", nil, tc.assets); err == nil || server != nil {
				t.Fatal("invalid component/asset combination was accepted")
			}
		})
	}
}

func TestNewCombinedRegistersAssetFallback(t *testing.T) {
	cfg := config.Config{Components: "ui,backend", PublicOrigin: "https://example.test"}
	assets := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	databaseURL := "postgres://almanac:password@127.0.0.1:5432/almanac?sslmode=disable"
	service, err := authentication.New(t.Context(), databaseURL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(context.Background(), cfg, service, databaseURL, nil, assets)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("combined root status = %d", response.Code)
	}
}

func TestShutdownClosesTLSHandshakeThatHasNotStartedARequest(t *testing.T) {
	server, _ := newTLSBackendServer(t)
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, 1)
	priorConnState := server.http.ConnState
	server.http.ConnState = func(conn net.Conn, state http.ConnState) {
		if priorConnState != nil {
			priorConnState(conn, state)
		}
		if state == http.StateNew {
			accepted <- struct{}{}
		}
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	clientConn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not accept the TLS connection")
	}
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown waited for an unstarted TLS request: %v", err)
	}
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned %v after shutdown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not exit after shutdown")
	}
}

func TestShutdownWaitsForActiveRequestToFinish(t *testing.T) {
	server, roots := newTLSBackendServer(t)
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(releaseHandler) })
		_ = server.Close()
	}()
	handlerStarted := make(chan struct{})
	server.http.Handler = server.rejectAfterShutdown(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerStarted)
		<-releaseHandler
		w.WriteHeader(http.StatusNoContent)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"}}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	requestResult := make(chan struct {
		status int
		err    error
	}, 1)
	url := "https://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	go func() {
		response, err := client.Get(url)
		if err != nil {
			requestResult <- struct {
				status int
				err    error
			}{err: err}
			return
		}
		response.Body.Close()
		requestResult <- struct {
			status int
			err    error
		}{status: response.StatusCode}
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not enter the active handler")
	}
	shutdownStarted := make(chan struct{})
	server.RegisterOnShutdown(func() { close(shutdownStarted) })
	shutdownCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- server.Shutdown(shutdownCtx) }()
	select {
	case <-shutdownStarted:
	case <-time.After(time.Second):
		t.Fatal("server shutdown did not start")
	}
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before the active handler drained: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseHandler) })
	select {
	case result := <-requestResult:
		if result.err != nil || result.status != http.StatusNoContent {
			t.Fatalf("active request was interrupted during shutdown: status=%d err=%v", result.status, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("active request did not finish after release")
	}
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("shutdown failed after active request finished: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after active request drained")
	}
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned %v after shutdown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not exit after shutdown")
	}
}

func TestConnectionAcceptedDuringShutdownIsClosed(t *testing.T) {
	server, _ := newTLSBackendServer(t)
	server.closeNewConnections()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	server.connectionState(serverConn, http.StateNew)
	readResult := make(chan error, 1)
	go func() {
		var data [1]byte
		_, err := clientConn.Read(data[:])
		readResult <- err
	}()
	select {
	case err := <-readResult:
		if err == nil {
			t.Fatal("connection remained open after being accepted during shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("connection accepted during shutdown was not closed")
	}
}

func TestShutdownDoesNotDispatchARequestTransitioningFromStateNew(t *testing.T) {
	server, roots := newTLSBackendServer(t)
	defer server.Close()
	var handlerCalls atomic.Int32
	server.http.Handler = server.rejectAfterShutdown(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	stateActive := make(chan struct{})
	releaseState := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseState) })
	priorConnState := server.http.ConnState
	server.http.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateActive {
			close(stateActive)
			<-releaseState
		}
		priorConnState(conn, state)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"}}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	requestDone := make(chan struct{})
	url := "https://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	go func() {
		defer close(requestDone)
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-stateActive:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the StateActive transition")
	}
	shutdownStarted := make(chan struct{})
	server.RegisterOnShutdown(func() { close(shutdownStarted) })
	shutdownCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- server.Shutdown(shutdownCtx) }()
	select {
	case <-shutdownStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start")
	}
	releaseOnce.Do(func() { close(releaseState) })
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("shutdown failed while rejecting a request not yet dispatched: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after the pre-request connection closed")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("request did not finish after shutdown")
	}
	if handlerCalls.Load() != 0 {
		t.Fatal("request handler ran after its StateNew connection was closed for shutdown")
	}
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned %v after shutdown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not exit after shutdown")
	}
}

func TestRejectAfterShutdownDoesNotDispatchHandler(t *testing.T) {
	server, _ := newTLSBackendServer(t)
	var handlerCalls atomic.Int32
	handler := server.rejectAfterShutdown(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/livez", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || handlerCalls.Load() != 1 {
		t.Fatalf("request before shutdown dispatched=%d status=%d", handlerCalls.Load(), response.Code)
	}
	server.closeNewConnections()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || handlerCalls.Load() != 1 {
		t.Fatalf("request after shutdown dispatched=%d status=%d", handlerCalls.Load(), response.Code)
	}
}

func newTLSBackendServer(t *testing.T) (*Server, *x509.CertPool) {
	t.Helper()
	databaseURL := "postgres://almanac:password@127.0.0.1:5432/almanac?sslmode=disable"
	service, err := authentication.New(t.Context(), databaseURL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	certificate, key := testCertificate(t)
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certificate)
	if block == nil {
		t.Fatal("test certificate is not PEM encoded")
	}
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsedCertificate)
	server, err := New(context.Background(), config.Config{Components: "backend", PublicOrigin: "https://localhost"}, service, databaseURL,
		&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, roots
}

func TestLoadTLSConfigRequiresProtectedMatchingPair(t *testing.T) {
	if cfg, err := LoadTLSConfig("", ""); err != nil || cfg != nil {
		t.Fatalf("empty pair = %v, %v", cfg, err)
	}
	if _, err := LoadTLSConfig("cert", ""); err == nil {
		t.Fatal("partial TLS pair accepted")
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cert, key := testCertificate(t)
	if err := os.WriteFile(certPath, cert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadTLSConfig(certPath, keyPath)
	if err != nil || cfg == nil || cfg.MinVersion != 0x0303 {
		t.Fatalf("valid TLS pair accepted=%t err=%v", cfg != nil, err)
	}
	if err := os.Chmod(keyPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLSConfig(certPath, keyPath); err == nil {
		t.Fatal("group-readable private key accepted")
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	_, mismatchedKey := testCertificate(t)
	if err := os.WriteFile(keyPath, mismatchedKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLSConfig(certPath, keyPath); err == nil {
		t.Fatal("mismatched pair accepted")
	}
}

func TestStrictPathsNeverRedirectCleanedRoutes(t *testing.T) {
	handler := strictPaths(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, target := range []string{"http://example.test//readyz", "http://example.test/api/v1/../livez", "http://example.test/api/v1/%2e%2e/livez"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("cleaned path %q returned %d", target, response.Code)
		}
	}
}

func testCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}
