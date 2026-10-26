//go:build integration

package command

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func TestIntegrationExecutableAgentManagementRouteAndSessionLifecycle(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade isolated database failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve runtime port failed")
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dir := t.TempDir()
	databaseFile, passwordFile := filepath.Join(dir, "database-url"), filepath.Join(dir, "initial-password")
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	initialPassword, permanentPassword := "Runtime agent initial password 346!", "Runtime agent permanent password 571!"
	for path, value := range map[string][]byte{databaseFile: []byte(databaseURL), passwordFile: []byte(initialPassword)} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal("write protected runtime file failed")
		}
	}
	certificate, key, err := runtimeTestCertificate()
	if err != nil {
		t.Fatal("create runtime TLS certificate failed")
	}
	if err := os.WriteFile(certFile, certificate, 0o644); err != nil {
		t.Fatal("write runtime certificate failed")
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal("write runtime key failed")
	}
	block, _ := pem.Decode(certificate)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse runtime certificate failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create cookie jar failed")
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	baseURL := "https://localhost:" + strings.Split(address, ":")[1]
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve executable failed")
	}
	args := []string{"serve", "--components=backend", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=agent-owner", "--initial-admin-password-file=" + passwordFile, "--public-origin=" + baseURL, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile, "--shutdown-timeout=5s"}
	process := exec.Command(binary, args...)
	stderr := newRuntimeLog()
	process.Stderr = stderr
	process.Stdout = io.Discard
	if err := process.Start(); err != nil {
		t.Fatal("start backend executable failed")
	}
	wait := make(chan error, 1)
	done := make(chan struct{})
	go func() { wait <- process.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = process.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("backend did not exit")
		}
	})
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-wait:
			t.Fatalf("backend exited before readiness (failed=%t)", err != nil)
		default:
		}
		response, err := client.Get(baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		_ = process.Process.Kill()
		<-wait
		t.Fatal("backend did not become live")
	}
	callAuth := func(path, body, csrf string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("create auth request failed")
		}
		request.Header.Set("Origin", baseURL)
		request.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("send auth request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("read auth response failed")
		}
		return response.StatusCode, data
	}
	status, body := callAuth("/api/v1/auth/context", `{}`, "")
	var authContext api.AuthResponse
	if status != 200 || json.Unmarshal(body, &authContext) != nil || authContext.Session != nil || authContext.CsrfToken == "" {
		t.Fatal("initial auth context unavailable")
	}
	status, body = callAuth("/api/v1/auth/login", `{"login":"agent-owner","password":"`+initialPassword+`"}`, authContext.CsrfToken)
	var session api.AuthResponse
	if status != 200 || json.Unmarshal(body, &session) != nil || session.Session == nil || !session.Session.Restricted {
		t.Fatal("initial owner session was not restricted")
	}
	managementGET := func() (int, []byte, http.Header) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents", nil)
		if err != nil {
			t.Fatal("create management request failed")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("send management request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("read management response failed")
		}
		return response.StatusCode, body, response.Header
	}
	status, body, headers := managementGET()
	if status != 403 || !strings.Contains(string(body), `"forbidden"`) || headers.Get("X-Request-ID") == "" {
		t.Fatalf("restricted management GET status=%d body=%s headers=%v", status, body, headers)
	}
	status, body = callAuth("/api/v1/auth/context", `{}`, "")
	if status != 200 || json.Unmarshal(body, &authContext) != nil {
		t.Fatal("restricted session context failed")
	}
	status, body = callAuth("/api/v1/auth/password", `{"current_password":"`+initialPassword+`","new_password":"`+permanentPassword+`"}`, authContext.CsrfToken)
	if status != 200 || json.Unmarshal(body, &session) != nil || session.Session == nil || session.Session.Restricted {
		t.Fatal("owner password rotation did not complete")
	}
	status, body, headers = managementGET()
	if status != 200 {
		t.Fatalf("normal administrator GET status=%d body=%s", status, body)
	}
	var page api.AgentPage
	if err := json.Unmarshal(body, &page); err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("initial agent page=%s err=%v", body, err)
	}
	if headers.Get("X-Request-ID") == "" || headers.Get("Cache-Control") != "no-store" || headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("management response headers=%v", headers)
	}
	malformedPath, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents/%2e%2e/readyz", nil)
	if err != nil {
		t.Fatal("create malformed management path request failed")
	}
	malformedResponse, err := client.Do(malformedPath)
	if err != nil {
		t.Fatal("send malformed management path request failed")
	}
	malformedResponse.Body.Close()
	if malformedResponse.StatusCode != http.StatusNotFound || malformedResponse.Header.Get("Cache-Control") != "no-store" || malformedResponse.Header.Get("X-Content-Type-Options") != "nosniff" || malformedResponse.Header.Get("X-Request-ID") == "" {
		t.Fatalf("malformed management path boundary status=%d headers=%v", malformedResponse.StatusCode, malformedResponse.Header)
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal backend shutdown failed")
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("backend shutdown failed (stderr-present=%t)", stderr.String() != "")
		}
	case <-time.After(10 * time.Second):
		_ = process.Process.Kill()
		<-wait
		t.Fatal("backend shutdown timed out")
	}
}
