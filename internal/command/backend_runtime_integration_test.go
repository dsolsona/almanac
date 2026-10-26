//go:build integration

package command

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
	webassets "github.com/dsolsona/almanac/web"
)

func TestIntegrationExecutableBackendTLSOwnerLifecycleAndRotation(t *testing.T) {
	testExecutableBackendTLSOwnerLifecycleAndRotation(t, "backend")
}

func TestIntegrationExecutableCombinedBackendUITLSOwnerLifecycleAndRotation(t *testing.T) {
	testExecutableBackendTLSOwnerLifecycleAndRotation(t, "backend,ui")
}

func testExecutableBackendTLSOwnerLifecycleAndRotation(t *testing.T, components string) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve ephemeral test port failed")
	}
	address := portListener.Addr().String()
	portListener.Close()
	dir := t.TempDir()
	databaseFile, passwordFile := filepath.Join(dir, "database-url"), filepath.Join(dir, "initial-password")
	certFile, keyFile := filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "private-key.pem")
	initialPassword, permanentPassword := "Executable bootstrap phrase 934!", "Permanent executable phrase 638!"
	for path, value := range map[string][]byte{databaseFile: []byte(databaseURL), passwordFile: []byte(initialPassword)} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal("write protected test configuration failed")
		}
	}
	certificate, key, err := runtimeTestCertificate()
	if err != nil {
		t.Fatal("create test TLS certificate failed")
	}
	if err := os.WriteFile(certFile, certificate, 0o644); err != nil {
		t.Fatal("write test certificate failed")
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal("write test private key failed")
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse test certificate failed")
	}
	roots.AddCert(parsedCertificate)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create cookie jar failed")
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	baseURL := "https://localhost:" + strings.Split(address, ":")[1]
	publicOrigin := baseURL
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve built executable path failed")
	}
	args := []string{"serve", "--components=" + components, "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=owner", "--initial-admin-password-file=" + passwordFile, "--public-origin=" + publicOrigin, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile, "--shutdown-timeout=5s"}
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "ALMANAC_TEST_DATABASE_URL=")
	stderr, stdout := newRuntimeLog(), &strings.Builder{}
	cmd.Stderr, cmd.Stdout = stderr, stdout
	if err := cmd.Start(); err != nil {
		t.Fatal("start built backend executable failed")
	}
	wait := make(chan error, 1)
	waitDone := make(chan struct{})
	go func() { wait <- cmd.Wait(); close(waitDone) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			t.Error("backend test process did not exit")
		}
	})
	started := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-wait:
			t.Fatalf("backend exited before serving (exit-error=%t)", err != nil)
		default:
		}
		response, err := client.Get(baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				started = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !started {
		_ = cmd.Process.Kill()
		<-wait
		t.Fatal("backend liveness did not become available")
	}
	if strings.Contains(components, "ui") {
		assets, err := webassets.Assets()
		if err != nil {
			t.Fatal("load expected embedded UI assets failed")
		}
		response, err := client.Get(baseURL + "/")
		if err != nil {
			t.Fatal("read combined embedded UI failed")
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		index, indexErr := fs.ReadFile(assets, "index.html")
		if readErr != nil || indexErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/html") || !slices.Equal(body, index) || response.Header.Get("Cache-Control") != "no-cache" {
			t.Fatal("combined runtime did not serve the embedded UI root")
		}
		assetURLs := make([]string, 0, 6)
		foundJS, foundCSS := false, false
		for _, match := range regexp.MustCompile(`(?:src|href)="([^"]+\.(?:js|css))"`).FindAllStringSubmatch(string(body), -1) {
			assetURLs = append(assetURLs, match[1])
			foundJS = foundJS || strings.HasSuffix(match[1], ".js")
			foundCSS = foundCSS || strings.HasSuffix(match[1], ".css")
		}
		if !foundJS || !foundCSS {
			t.Fatal("combined root did not reference both JavaScript and CSS assets")
		}
		assetURLs = append(assetURLs, "/fonts/Bevan-Regular.ttf", "/fonts/Bevan-OFL.txt", "/licenses/echarts/LICENSE")
		for _, asset := range assetURLs {
			response, err := client.Get(baseURL + asset)
			if err != nil {
				t.Fatalf("read combined asset %s failed", asset)
			}
			assetBody, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			expected, expectedErr := fs.ReadFile(assets, strings.TrimPrefix(asset, "/"))
			wantCache := ""
			if strings.HasPrefix(asset, "/assets/") {
				wantCache = "public, max-age=31536000, immutable"
			}
			contentType := response.Header.Get("Content-Type")
			if readErr != nil || expectedErr != nil || response.StatusCode != http.StatusOK || !slices.Equal(assetBody, expected) || contentType == "" || response.Header.Get("Cache-Control") != wantCache {
				t.Fatalf("combined asset %s was unavailable", asset)
			}
			head, err := client.Head(baseURL + asset)
			if err != nil {
				t.Fatalf("HEAD combined asset %s failed", asset)
			}
			headBody, headReadErr := io.ReadAll(head.Body)
			head.Body.Close()
			if headReadErr != nil || head.StatusCode != http.StatusOK || len(headBody) != 0 || head.Header.Get("Content-Type") != contentType || head.Header.Get("Cache-Control") != wantCache {
				t.Fatalf("HEAD combined asset %s returned wrong body or headers", asset)
			}
		}
		for _, target := range []string{"/design-system", "/missing.js", "/api/v1/unknown", "/api/v1/%2e%2e/readyz"} {
			response, err := client.Get(baseURL + target)
			if err != nil {
				t.Fatalf("request combined path %s failed", target)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Errorf("combined path %s returned %d", target, response.StatusCode)
			}
		}
		post, err := client.Post(baseURL+"/", "text/plain", strings.NewReader("blocked"))
		if err != nil {
			t.Fatal("post combined asset root failed")
		}
		post.Body.Close()
		if post.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST combined root returned %d", post.StatusCode)
		}
		head, err := client.Head(baseURL + "/")
		if err != nil {
			t.Fatal("HEAD combined root failed")
		}
		indexHeadBody, headReadErr := io.ReadAll(head.Body)
		head.Body.Close()
		if headReadErr != nil || head.StatusCode != http.StatusOK || len(indexHeadBody) != 0 || !strings.Contains(head.Header.Get("Content-Type"), "text/html") || head.Header.Get("Cache-Control") != "no-cache" {
			t.Fatal("HEAD combined root returned wrong body or cache header")
		}
	}
	response, err := client.Get(baseURL + "/readyz")
	if err != nil {
		t.Fatal("read backend readiness failed")
	}
	var health api.HealthResponse
	if err := json.UnmarshalRead(response.Body, &health); err != nil {
		response.Body.Close()
		t.Fatal("decode readiness failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || health.Component != api.Backend || health.Status != api.Ok {
		t.Fatal("backend did not report ready after provisioning")
	}
	duplicateCtx, cancelDuplicate := context.WithTimeout(t.Context(), 5*time.Second)
	duplicate := exec.CommandContext(duplicateCtx, binary, args...)
	duplicateErr := duplicate.Run()
	cancelDuplicate()
	if exitErr, ok := errors.AsType[*exec.ExitError](duplicateErr); !ok || exitErr.ExitCode() != 1 {
		t.Fatal("duplicate backend executable did not fail with a nonzero ownership error")
	}
	call := func(path, body, csrf string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("create authentication request failed")
		}
		req.Header.Set("Origin", publicOrigin)
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("send authentication request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("read authentication response failed")
		}
		return response.StatusCode, data, response.Header
	}
	status, body, _ := call("/api/v1/auth/context", `{}`, "")
	var authContext api.AuthResponse
	if status != http.StatusOK || json.Unmarshal(body, &authContext) != nil || authContext.CsrfToken == "" || authContext.Session != nil {
		t.Fatal("anonymous authentication context was not usable")
	}
	status, body, _ = call("/api/v1/auth/login", `{"login":"owner","password":"`+initialPassword+`"}`, authContext.CsrfToken)
	var restricted api.AuthResponse
	if status != http.StatusOK || json.Unmarshal(body, &restricted) != nil || restricted.Session == nil || !restricted.Session.Restricted {
		t.Fatal("initial login did not create a restricted session")
	}
	status, body, _ = call("/api/v1/auth/context", `{}`, "")
	if status != http.StatusOK || json.Unmarshal(body, &authContext) != nil {
		t.Fatal("restricted context request failed")
	}
	status, body, _ = call("/api/v1/auth/password", `{"current_password":"`+initialPassword+`","new_password":"`+permanentPassword+`"}`, authContext.CsrfToken)
	var normal api.AuthResponse
	if status != http.StatusOK || json.Unmarshal(body, &normal) != nil || normal.Session == nil || normal.Session.Restricted {
		t.Fatal("password rotation did not unlock the normal session")
	}
	lockConn := testpostgres.Connect(t, databaseURL)
	lockTx, err := lockConn.Begin(t.Context())
	if err != nil {
		t.Fatal("begin disposable readiness-blocking transaction failed")
	}
	defer lockTx.Rollback(context.Background())
	if _, err := lockTx.Exec(t.Context(), "LOCK TABLE public.almanac_installation IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal("lock installation reads in disposable database failed")
	}
	startedAt := time.Now()
	blockedReadiness, err := client.Get(baseURL + "/readyz")
	if err != nil {
		t.Fatal("readiness did not return during installation lock")
	}
	blockedReadiness.Body.Close()
	if blockedReadiness.StatusCode != http.StatusServiceUnavailable || time.Since(startedAt) > 4*time.Second {
		t.Fatal("readiness did not fail within its bounded preflight while database reads were blocked")
	}
	status, _, headers := call("/api/v1/auth/context", `{}`, "")
	if status != http.StatusServiceUnavailable || len(headers.Values("Set-Cookie")) != 0 || len(headers.Get("X-Request-ID")) != 32 {
		t.Fatal("authentication preflight did not fail safely while database reads were blocked")
	}
	if err := lockTx.Commit(t.Context()); err != nil {
		t.Fatal("release installation lock failed")
	}
	recovered, err := client.Get(baseURL + "/readyz")
	if err != nil {
		t.Fatal("readiness did not recover after database reads resumed")
	}
	recovered.Body.Close()
	if recovered.StatusCode != http.StatusOK {
		t.Fatal("readiness stayed unavailable after the database lock was released")
	}
	status, _, _ = call("/api/v1/auth/context", `{}`, "")
	if status != http.StatusOK {
		t.Fatal("authentication stayed unavailable after the database lock was released")
	}
	if err := os.Remove(passwordFile); err != nil {
		t.Fatal("remove initial password fixture failed")
	}
	// Close idle client connections before server-side graceful drain.
	client.CloseIdleConnections()

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	drainConn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal("connect for in-flight drain request failed")
	}
	defer drainConn.Close()
	if _, err := io.WriteString(drainConn, "POST /api/v1/auth/context HTTP/1.1\r\nHost: localhost:"+strings.Split(address, ":")[1]+"\r\nOrigin: "+publicOrigin+"\r\nContent-Type: application/json\r\nContent-Length: 2\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal("write in-flight request headers failed")
	}
	reader := bufio.NewReader(drainConn)
	continueResponse, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal("read server-side request-body barrier failed")
	}
	continueResponse.Body.Close()
	if continueResponse.StatusCode != http.StatusContinue {
		t.Fatal("server did not confirm that the authentication handler is reading the request body")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal graceful shutdown failed")
	}
	waitForDrain(t, stderr, wait)
	postgres := testpostgres.Connect(t, databaseURL)
	var heldPID int32
	if err := postgres.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE application_name='almanac-backend-owner' AND datname=current_database()").Scan(&heldPID); err != nil || heldPID <= 0 {
		t.Fatal("observe owner session during graceful drain failed")
	}
	var heldLocks int
	if err := postgres.QueryRow(t.Context(), "SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted", heldPID).Scan(&heldLocks); err != nil || heldLocks != 1 {
		t.Fatal("backend released ownership before request drain completed")
	}
	if _, err := io.WriteString(drainConn, "{}"); err != nil {
		t.Fatal("finish request body during graceful drain failed")
	}
	finalResponse, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal("read drained authentication response failed")
	}
	finalResponse.Body.Close()
	if finalResponse.StatusCode != http.StatusOK {
		t.Fatal("normal shutdown cancelled an in-flight request")
	}
	select {
	case err := <-wait:
		if err != nil {
			logs := stderr.String()
			t.Fatalf("backend graceful shutdown returned failure: shutdown-failed=%t ownership-lost=%t cleanup-failed=%t", strings.Contains(logs, "backend shutdown failed"), strings.Contains(logs, "backend ownership lost"), strings.Contains(logs, "backend ownership cleanup failed"))
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-wait
		t.Fatal("backend did not drain and exit")
	}
	if strings.Contains(stderr.String()+stdout.String(), databaseURL) || strings.Contains(stderr.String()+stdout.String(), initialPassword) || strings.Contains(stderr.String()+stdout.String(), permanentPassword) {
		t.Fatal("backend logs disclosed protected runtime values")
	}
	restartArgs := replaceFlagValue(slices.Clone(args), "--initial-admin-login", "changed-owner")
	restart := exec.Command(binary, restartArgs...)
	restart.Stderr, restart.Stdout = io.Discard, io.Discard
	if err := restart.Start(); err != nil {
		t.Fatal("restart built backend executable failed")
	}
	restartWait := make(chan error, 1)
	restartDone := make(chan struct{})
	go func() { restartWait <- restart.Wait(); close(restartDone) }()
	t.Cleanup(func() {
		_ = restart.Process.Kill()
		select {
		case <-restartDone:
		case <-time.After(5 * time.Second):
			t.Error("restarted backend process did not exit")
		}
	})
	readyAfterRestart := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-restartWait:
			t.Fatalf("restarted backend exited before serving (failure=%t)", err != nil)
		default:
		}
		response, err := client.Get(baseURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				readyAfterRestart = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !readyAfterRestart {
		_ = restart.Process.Kill()
		<-restartWait
		t.Fatal("restarted backend was not ready with the removed initial password file")
	}
	status, body, _ = call("/api/v1/auth/context", `{}`, "")
	if status != http.StatusOK || json.Unmarshal(body, &authContext) != nil || authContext.Session == nil || authContext.Session.Restricted {
		t.Fatalf("persisted normal session did not survive backend restart: status=%d session-present=%t", status, authContext.Session != nil)
	}
	freshJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create fresh post-restart cookie jar failed")
	}
	freshClient := &http.Client{Jar: freshJar, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(freshClient.CloseIdleConnections)
	freshCall := func(target, body, csrf string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+target, strings.NewReader(body))
		if err != nil {
			t.Fatal("create fresh post-restart request failed")
		}
		request.Header.Set("Origin", publicOrigin)
		request.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := freshClient.Do(request)
		if err != nil {
			t.Fatal("send fresh post-restart request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("read fresh post-restart response failed")
		}
		return response.StatusCode, data
	}
	status, body = freshCall("/api/v1/auth/context", `{}`, "")
	var freshContext api.AuthResponse
	if status != http.StatusOK || json.Unmarshal(body, &freshContext) != nil || freshContext.Session != nil {
		t.Fatalf("fresh post-restart context was not anonymous: status=%d session-present=%t", status, freshContext.Session != nil)
	}
	status, body = freshCall("/api/v1/auth/login", `{"login":"owner","password":"`+permanentPassword+`"}`, freshContext.CsrfToken)
	if status != http.StatusOK || json.Unmarshal(body, &normal) != nil || normal.Session == nil || normal.Session.Restricted {
		t.Fatalf("rotated password did not authenticate after restart: status=%d normal-session=%t", status, normal.Session != nil && !normal.Session.Restricted)
	}
	if err := restart.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal restarted backend shutdown failed")
	}
	select {
	case err := <-restartWait:
		if err != nil {
			t.Fatal("restarted backend shutdown returned failure")
		}
	case <-time.After(10 * time.Second):
		_ = restart.Process.Kill()
		<-restartWait
		t.Fatal("restarted backend did not drain and exit")
	}
	proxyArgs := []string{"serve", "--components=backend", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=changed-owner", "--initial-admin-password-file=" + passwordFile, "--public-origin=" + publicOrigin, "--trusted-proxy-cidrs=127.0.0.1/32"}
	proxyProcess := exec.Command(binary, proxyArgs...)
	proxyProcess.Stdout, proxyProcess.Stderr = io.Discard, io.Discard
	if err := proxyProcess.Start(); err != nil {
		t.Fatal("start trusted termination backend failed")
	}
	proxyWait := make(chan error, 1)
	proxyDone := make(chan struct{})
	go func() { proxyWait <- proxyProcess.Wait(); close(proxyDone) }()
	t.Cleanup(func() {
		_ = proxyProcess.Process.Kill()
		select {
		case <-proxyDone:
		case <-time.After(5 * time.Second):
			t.Error("trusted termination backend did not exit")
		}
	})
	proxyURL := "http://127.0.0.1:" + strings.Split(address, ":")[1]
	proxyClient := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(proxyClient.CloseIdleConnections)
	proxyReady := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-proxyWait:
			t.Fatalf("trusted termination backend exited before serving (failure=%t)", err != nil)
		default:
		}
		response, err := proxyClient.Get(proxyURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				proxyReady = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !proxyReady {
		_ = proxyProcess.Process.Kill()
		<-proxyWait
		t.Fatal("trusted termination backend did not become ready")
	}
	proxyCall := func(target, body string, forwarded bool) (*http.Response, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxyURL+target, strings.NewReader(body))
		if err != nil {
			t.Fatal("create trusted termination request failed")
		}
		request.Header.Set("Origin", publicOrigin)
		request.Header.Set("Content-Type", "application/json")
		if forwarded {
			request.Header.Set("X-Forwarded-Proto", "https")
			request.Header.Set("X-Forwarded-For", "198.51.100.47")
		}
		response, err := proxyClient.Do(request)
		if err != nil {
			t.Fatal("send trusted termination request failed")
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal("read trusted termination response failed")
		}
		return response, data
	}
	proxyResponse, body := proxyCall("/api/v1/auth/context", `{}`, true)
	var proxyContext api.AuthResponse
	if proxyResponse.StatusCode != http.StatusOK || json.Unmarshal(body, &proxyContext) != nil || proxyContext.CsrfToken == "" {
		t.Fatal("trusted TLS termination did not reach backend authentication context")
	}
	var preauth *http.Cookie
	for _, cookie := range proxyResponse.Cookies() {
		if cookie.Name == "__Host-almanac-preauth" {
			preauth = cookie
		}
	}
	if preauth == nil {
		t.Fatal("trusted authentication context omitted pre-auth cookie")
	}
	proxyLogin, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxyURL+"/api/v1/auth/login", strings.NewReader(`{"login":"owner","password":"`+permanentPassword+`"}`))
	if err != nil {
		t.Fatal("create trusted login request failed")
	}
	proxyLogin.Header.Set("Origin", publicOrigin)
	proxyLogin.Header.Set("Content-Type", "application/json")
	proxyLogin.Header.Set("X-Forwarded-Proto", "https")
	proxyLogin.Header.Set("X-Forwarded-For", "198.51.100.47")
	proxyLogin.Header.Set("X-CSRF-Token", proxyContext.CsrfToken)
	proxyLogin.AddCookie(preauth)
	loginResponse, err := proxyClient.Do(proxyLogin)
	if err != nil {
		t.Fatal("send trusted login request failed")
	}
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK {
		t.Fatal("trusted TLS termination did not reach the same login handler")
	}
	spoofed, _ := proxyCall("/api/v1/auth/context", `{}`, false)
	if spoofed.StatusCode != http.StatusForbidden {
		t.Fatal("plain HTTP request bypassed trusted forwarding checks")
	}
	if err := proxyProcess.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal trusted termination backend failed")
	}
	select {
	case err := <-proxyWait:
		if err != nil {
			t.Fatal("trusted termination backend shutdown failed")
		}
	case <-time.After(10 * time.Second):
		_ = proxyProcess.Process.Kill()
		<-proxyWait
		t.Fatal("trusted termination backend did not drain")
	}
	reacquired, err := storage.AcquireBackendOwner(t.Context(), databaseURL)
	if err != nil {
		t.Fatal("backend ownership was not released after graceful drain")
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal("close verification owner failed")
	}
	ownerLoss := exec.Command(binary, args...)
	ownerLossLog := newRuntimeLog()
	ownerLoss.Stdout, ownerLoss.Stderr = io.Discard, ownerLossLog
	if err := ownerLoss.Start(); err != nil {
		t.Fatal("start owner-loss process failed")
	}
	ownerLossWait := make(chan error, 1)
	ownerLossDone := make(chan struct{})
	go func() { ownerLossWait <- ownerLoss.Wait(); close(ownerLossDone) }()
	t.Cleanup(func() {
		_ = ownerLoss.Process.Kill()
		select {
		case <-ownerLossDone:
		case <-time.After(5 * time.Second):
			t.Error("owner-loss backend did not exit")
		}
	})
	ownerPID := int32(0)
	ownerAdmin := testpostgres.Connect(t, databaseURL)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-ownerLossWait:
			t.Fatalf("owner-loss backend exited before probe (failure=%t)", err != nil)
		default:
		}
		if err := ownerAdmin.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE application_name='almanac-backend-owner' AND datname=current_database()").Scan(&ownerPID); err == nil && ownerPID > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ownerPID <= 0 {
		_ = ownerLoss.Process.Kill()
		<-ownerLossWait
		t.Fatal("observe dedicated owner PID failed")
	}
	lossServerStarted := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-ownerLossWait:
			t.Fatalf("owner-loss backend exited before serving (failure=%t)", err != nil)
		default:
		}
		response, err := client.Get(baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				lossServerStarted = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !lossServerStarted {
		_ = ownerLoss.Process.Kill()
		<-ownerLossWait
		t.Fatal("owner-loss backend did not begin serving")
	}
	lossRequest, lossReader := openHeldAuthContext(t, address, publicOrigin, roots)
	defer lossRequest.Close()
	if err := ownerLoss.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal owner-loss backend graceful drain failed")
	}
	waitForDrain(t, ownerLossLog, ownerLossWait)
	var ownerDrainLocks int
	if err := ownerAdmin.QueryRow(t.Context(), "SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted", ownerPID).Scan(&ownerDrainLocks); err != nil || ownerDrainLocks != 1 {
		t.Fatal("owner lock was released before drain completed")
	}
	var terminated bool
	if err := ownerAdmin.QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", ownerPID).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate observed backend owner failed")
	}
	select {
	case err := <-ownerLossWait:
		if processErr, ok := errors.AsType[*exec.ExitError](err); !ok || processErr.ExitCode() == 0 {
			t.Fatal("backend ownership loss did not produce nonzero process exit")
		}
	case <-time.After(6 * time.Second):
		_ = ownerLoss.Process.Kill()
		<-ownerLossWait
		t.Fatal("backend did not fail-stop after ownership loss")
	}
	_ = lossRequest.SetReadDeadline(time.Now().Add(2 * time.Second))
	interrupted, readErr := http.ReadResponse(lossReader, &http.Request{Method: http.MethodPost})
	if readErr == nil {
		interrupted.Body.Close()
		if interrupted.StatusCode == http.StatusOK {
			t.Fatal("owner-loss shutdown completed the held request successfully")
		}
	}
}

func openHeldAuthContext(t *testing.T, address, publicOrigin string, roots *x509.CertPool) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal("connect for held authentication request failed")
	}
	if _, err := io.WriteString(conn, "POST /api/v1/auth/context HTTP/1.1\r\nHost: localhost:"+strings.Split(address, ":")[1]+"\r\nOrigin: "+publicOrigin+"\r\nContent-Type: application/json\r\nContent-Length: 2\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n"); err != nil {
		_ = conn.Close()
		t.Fatal("write held authentication request failed")
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		_ = conn.Close()
		t.Fatal("read held-request body barrier failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusContinue {
		_ = conn.Close()
		t.Fatal("authentication handler did not enter request-body read")
	}
	return conn, reader
}

type runtimeLog struct {
	mu       sync.Mutex
	text     strings.Builder
	draining chan struct{}
	once     sync.Once
}

func newRuntimeLog() *runtimeLog { return &runtimeLog{draining: make(chan struct{})} }

func (l *runtimeLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.text.Write(data)
	if strings.Contains(l.text.String(), `"msg":"component draining"`) {
		l.once.Do(func() { close(l.draining) })
	}
	return n, err
}

func (l *runtimeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

func waitForDrain(t *testing.T, logs *runtimeLog, exited <-chan error) {
	t.Helper()
	select {
	case <-logs.draining:
	case processErr := <-exited:
		t.Fatalf("process exited before listener drain confirmation (failure=%t)", processErr != nil)
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not report listener closure after SIGTERM")
	}
}

func replaceFlagValue(args []string, name, value string) []string {
	prefix := name + "="
	for i, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			args[i] = prefix + value
			return args
		}
	}
	return args
}

func TestIntegrationExecutableMissingInitialCredentialsStaysUnavailable(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve ephemeral test port failed")
	}
	address := reserved.Addr().String()
	_ = reserved.Close()
	dir := t.TempDir()
	databaseFile, certFile, keyFile := filepath.Join(dir, "database-url"), filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "private-key.pem")
	if err := os.WriteFile(databaseFile, []byte(databaseURL), 0o600); err != nil {
		t.Fatal("write protected database URL failed")
	}
	certificate, key, err := runtimeTestCertificate()
	if err != nil {
		t.Fatal("create test TLS certificate failed")
	}
	if err := os.WriteFile(certFile, certificate, 0o644); err != nil {
		t.Fatal("write TLS certificate failed")
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal("write private key failed")
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse TLS certificate failed")
	}
	roots.AddCert(parsed)
	port := strings.Split(address, ":")[1]
	baseURL := "https://localhost:" + port
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve built executable path failed")
	}
	args := []string{"serve", "--components=backend", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=owner", "--initial-admin-password-file=" + filepath.Join(dir, "missing-password"), "--public-origin=" + baseURL, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile}
	process := exec.Command(binary, args...)
	process.Stdout, process.Stderr = io.Discard, io.Discard
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
			t.Error("backend without credentials did not exit")
		}
	})
	started := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-wait:
			t.Fatalf("backend without credentials exited early (failure=%t)", err != nil)
		default:
		}
		response, err := client.Get(baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				started = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !started {
		_ = process.Process.Kill()
		<-wait
		t.Fatal("liveness was unavailable without initial credentials")
	}
	response, err := client.Get(baseURL + "/readyz")
	if err != nil {
		t.Fatal("read setup readiness failed")
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		response.Body.Close()
		t.Fatal("backend without initial credentials reported ready")
	}
	response.Body.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/v1/auth/context", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal("create unavailable authentication request failed")
	}
	request.Header.Set("Origin", baseURL)
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal("request unavailable authentication context failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || len(response.Header.Values("Set-Cookie")) != 0 || len(response.Header.Get("X-Request-ID")) != 32 {
		t.Fatal("unprovisioned authentication API did not fail safely")
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("stop unavailable backend failed")
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatal("unavailable backend graceful shutdown failed")
		}
	case <-time.After(10 * time.Second):
		_ = process.Process.Kill()
		<-wait
		t.Fatal("unavailable backend did not drain")
	}
}

func TestIntegrationExecutableWeakAndUnsafeCredentialsFailClosed(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve ephemeral test port failed")
	}
	address := reserved.Addr().String()
	_ = reserved.Close()
	dir := t.TempDir()
	databaseFile, certFile, keyFile := filepath.Join(dir, "database-url"), filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "private-key.pem")
	if err := os.WriteFile(databaseFile, []byte(databaseURL), 0o600); err != nil {
		t.Fatal("write protected database URL failed")
	}
	certificate, key, err := runtimeTestCertificate()
	if err != nil {
		t.Fatal("create TLS certificate failed")
	}
	if err := os.WriteFile(certFile, certificate, 0o644); err != nil {
		t.Fatal("write certificate failed")
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal("write private key failed")
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse TLS certificate failed")
	}
	roots.AddCert(parsed)
	baseURL := "https://localhost:" + strings.Split(address, ":")[1]
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve built executable path failed")
	}
	for _, credential := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{{"weak", []byte("short"), 0o600}, {"unsafe", []byte("Valid initial password phrase 628!"), 0o644}} {
		t.Run(credential.name, func(t *testing.T) {
			passwordFile := filepath.Join(dir, credential.name+"-password")
			if err := os.WriteFile(passwordFile, credential.content, credential.mode); err != nil {
				t.Fatal("write initial credential fixture failed")
			}
			if err := os.Chmod(passwordFile, credential.mode); err != nil {
				t.Fatal("set initial credential fixture permissions failed")
			}
			args := []string{"serve", "--components=backend", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=owner", "--initial-admin-password-file=" + passwordFile, "--public-origin=" + baseURL, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile}
			process := exec.Command(binary, args...)
			process.Stdout, process.Stderr = io.Discard, io.Discard
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
					t.Error("backend with invalid initial credentials did not exit")
				}
			})
			if !waitForStatus(t, client, baseURL+"/livez", http.StatusOK, wait) {
				_ = process.Process.Kill()
				t.Fatal("liveness failed for invalid initial credentials")
			}
			if !waitForStatus(t, client, baseURL+"/readyz", http.StatusServiceUnavailable, nil) {
				t.Fatal("invalid initial credentials did not leave readiness unavailable")
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/v1/auth/context", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal("create authentication preflight request failed")
			}
			request.Header.Set("Origin", baseURL)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("request authentication preflight failed")
			}
			response.Body.Close()
			if response.StatusCode != http.StatusServiceUnavailable || len(response.Header.Values("Set-Cookie")) != 0 || len(response.Header.Get("X-Request-ID")) != 32 {
				t.Fatal("invalid initial credentials did not fail authentication safely")
			}
			state, err := storage.ReadInitialOwnerState(t.Context(), databaseURL)
			if err != nil || state.Provisioned {
				t.Fatal("invalid credentials partially provisioned the initial owner")
			}
			if err := process.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal("stop invalid-credential backend failed")
			}
			select {
			case err := <-wait:
				if err != nil {
					t.Fatal("invalid-credential backend shutdown failed")
				}
			case <-time.After(10 * time.Second):
				_ = process.Process.Kill()
				<-wait
				t.Fatal("invalid-credential backend did not drain")
			}
		})
	}
	correctedFile := filepath.Join(dir, "corrected-password")
	if err := os.WriteFile(correctedFile, []byte("Corrected initial password phrase 482!"), 0o600); err != nil {
		t.Fatal("write corrected credential file failed")
	}
	correctedArgs := []string{"serve", "--components=backend", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=owner", "--initial-admin-password-file=" + correctedFile, "--public-origin=" + baseURL, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile}
	corrected := exec.Command(binary, correctedArgs...)
	corrected.Stdout, corrected.Stderr = io.Discard, io.Discard
	if err := corrected.Start(); err != nil {
		t.Fatal("restart backend with corrected credentials failed")
	}
	correctedWait := make(chan error, 1)
	correctedDone := make(chan struct{})
	go func() { correctedWait <- corrected.Wait(); close(correctedDone) }()
	t.Cleanup(func() {
		_ = corrected.Process.Kill()
		select {
		case <-correctedDone:
		case <-time.After(5 * time.Second):
			t.Error("corrected backend did not exit")
		}
	})
	if !waitForStatus(t, client, baseURL+"/readyz", http.StatusOK, correctedWait) {
		_ = corrected.Process.Kill()
		t.Fatal("corrected initial credentials did not provision successfully on restart")
	}
	state, err := storage.ReadInitialOwnerState(t.Context(), databaseURL)
	if err != nil || !state.Provisioned {
		t.Fatal("corrected restart did not provision the initial owner")
	}
	if err := corrected.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("stop corrected backend failed")
	}
	select {
	case err := <-correctedWait:
		if err != nil {
			t.Fatal("corrected backend shutdown failed")
		}
	case <-time.After(10 * time.Second):
		_ = corrected.Process.Kill()
		<-correctedWait
		t.Fatal("corrected backend did not drain")
	}
}

func waitForStatus(t *testing.T, client *http.Client, target string, want int, exited <-chan error) bool {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if exited != nil {
			select {
			case <-exited:
				return false
			default:
			}
		}
		response, err := client.Get(target)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == want {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func runtimeTestCertificate() ([]byte, []byte, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2026), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), nil
}
