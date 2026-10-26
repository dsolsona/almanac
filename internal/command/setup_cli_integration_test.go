//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
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

func TestIntegrationExecutableSetupCLICompletesPersistsAndRefusesReplay(t *testing.T) {
	testExecutableSetupCLICompletesPersistsAndRefusesReplay(t, "backend")
}

func TestIntegrationExecutableCombinedSetupCLICompletesPersistsAndRefusesReplay(t *testing.T) {
	testExecutableSetupCLICompletesPersistsAndRefusesReplay(t, "ui,backend")
}

func testExecutableSetupCLICompletesPersistsAndRefusesReplay(t *testing.T, components string) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve backend test port failed")
	}
	address := listener.Addr().String()
	listener.Close()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal("parse backend test port failed")
	}
	dir := t.TempDir()
	databaseFile := filepath.Join(dir, "database-url")
	initialFile := filepath.Join(dir, "initial-password")
	permanentFile := filepath.Join(dir, "permanent-password")
	secondFile := filepath.Join(dir, "second-password")
	certificateFile := filepath.Join(dir, "certificate.pem")
	keyFile := filepath.Join(dir, "private-key.pem")
	initialPassword := "Executable bootstrap phrase 934!"
	permanentPassword := "Permanent executable phrase 638!"
	secondPassword := "Second executable phrase 851!"
	for path, value := range map[string]string{databaseFile: databaseURL, initialFile: initialPassword, permanentFile: permanentPassword, secondFile: secondPassword} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal("write protected setup fixture failed")
		}
	}
	certificate, key, err := runtimeTestCertificate()
	if err != nil {
		t.Fatal("create setup test TLS certificate failed")
	}
	if err := os.WriteFile(certificateFile, certificate, 0o644); err != nil {
		t.Fatal("write setup test certificate failed")
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal("write setup test private key failed")
	}
	block, _ := pem.Decode(certificate)
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse setup test certificate failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsedCertificate)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create setup test cookie jar failed")
	}
	apiClient := &http.Client{Jar: jar, Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(apiClient.CloseIdleConnections)
	baseURL := "https://localhost:" + port
	publicOrigin := baseURL
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve built executable path failed")
	}
	serveArgs := []string{"serve", "--components=" + components, "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=owner", "--initial-admin-password-file=" + initialFile, "--public-origin=" + publicOrigin, "--tls-cert-file=" + certificateFile, "--tls-key-file=" + keyFile, "--shutdown-timeout=5s"}
	var process *exec.Cmd
	var processDone chan struct{}
	var processErr error
	startBackend := func() {
		t.Helper()
		process = exec.Command(binary, serveArgs...)
		logs := newRuntimeLog()
		process.Stdout, process.Stderr = io.Discard, logs
		if err := process.Start(); err != nil {
			t.Fatal("start real backend executable failed")
		}
		processDone = make(chan struct{})
		processErr = nil
		go func() { processErr = process.Wait(); close(processDone) }()
		ready := false
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
			select {
			case <-processDone:
				t.Fatalf("backend exited before readiness (failed=%t)", processErr != nil)
			default:
			}
			response, requestErr := apiClient.Get(baseURL + "/readyz")
			if requestErr == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					ready = true
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			t.Fatal("real backend did not become ready")
		}
	}
	stopBackend := func() {
		t.Helper()
		if process == nil {
			return
		}
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case <-processDone:
		case <-time.After(8 * time.Second):
			_ = process.Process.Kill()
			<-processDone
			t.Fatal("backend did not stop within the test deadline")
		}
		process = nil
	}
	t.Cleanup(func() {
		if process != nil {
			_ = process.Process.Kill()
			<-processDone
		}
	})
	startBackend()
	assertExecutableAuthProfile(t, binary, baseURL, initialFile, certificateFile, true)
	assertSetupDatabaseState(t, databaseURL, true, false, 0)

	runSetup := func(passwordFile, nextPasswordFile string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
		defer cancel()
		args := []string{"setup", "--backend-url=" + baseURL, "--initial-admin-login=owner", "--initial-admin-password-file=" + passwordFile, "--new-password-file=" + nextPasswordFile, "--ca-file=" + certificateFile, "--output=json"}
		cmd := exec.CommandContext(ctx, binary, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err == nil {
			return 0, stdout.String(), stderr.String()
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode(), stdout.String(), stderr.String()
		}
		t.Fatal("run setup executable failed")
		return 1, stdout.String(), stderr.String()
	}
	status, output, diagnostics := runSetup(initialFile, permanentFile)
	wantSuccess := "{\"schema_version\":1,\"operation\":\"setup\",\"completed\":true,\"session_cleanup\":\"complete\"}\n"
	if status != 0 || output != wantSuccess || diagnostics != "" {
		t.Fatal("setup CLI did not return the exact safe success document")
	}
	assertSetupDatabaseState(t, databaseURL, false, true, 0)
	assertExecutableAuthProfile(t, binary, baseURL, permanentFile, certificateFile, false)
	assertSetupDatabaseState(t, databaseURL, false, true, 0)

	verifyCredentials := func(password string, wantStatus int) {
		t.Helper()
		clientJar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			t.Fatal("create credential-check cookie jar failed")
		}
		client := &http.Client{Jar: clientJar, Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
		t.Cleanup(client.CloseIdleConnections)
		call := func(path, body, csrf string) (int, []byte) {
			t.Helper()
			req, requestErr := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, strings.NewReader(body))
			if requestErr != nil {
				t.Fatal("create authentication request failed")
			}
			req.Header.Set("Origin", publicOrigin)
			req.Header.Set("Content-Type", "application/json")
			if csrf != "" {
				req.Header.Set("X-CSRF-Token", csrf)
			}
			response, requestErr := client.Do(req)
			if requestErr != nil {
				t.Fatal("send authentication request failed")
			}
			defer response.Body.Close()
			data, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				t.Fatal("read authentication response failed")
			}
			return response.StatusCode, data
		}
		code, body := call("/api/v1/auth/context", `{}`, "")
		var auth api.AuthResponse
		if code != http.StatusOK || json.Unmarshal(body, &auth) != nil || auth.Session != nil || auth.CsrfToken == "" {
			t.Fatal("anonymous context could not establish credential check")
		}
		code, body = call("/api/v1/auth/login", `{"login":"owner","password":"`+password+`"}`, auth.CsrfToken)
		if code != wantStatus {
			t.Fatalf("login status=%d; want %d", code, wantStatus)
		}
		logoutCSRF := auth.CsrfToken
		if code == http.StatusOK {
			if json.Unmarshal(body, &auth) != nil || auth.Session == nil || auth.Session.Restricted {
				t.Fatal("permanent password did not create a normal administrator session")
			}
			logoutCSRF = auth.CsrfToken
		}
		logoutCode, _ := call("/api/v1/auth/logout", `{}`, logoutCSRF)
		if logoutCode != http.StatusNoContent {
			t.Fatal("credential-check session/pre-auth cleanup failed")
		}
	}
	verifyCredentials(permanentPassword, http.StatusOK)
	verifyCredentials(initialPassword, http.StatusUnauthorized)

	status, output, diagnostics = runSetup(permanentFile, secondFile)
	if status != 5 || !strings.Contains(output, `"code":"setup_already_complete"`) || strings.Contains(output+diagnostics, permanentPassword) || strings.Contains(output+diagnostics, baseURL) {
		t.Fatal("second setup did not safely refuse ordinary administrator credentials")
	}
	verifyCredentials(permanentPassword, http.StatusOK)
	verifyCredentials(secondPassword, http.StatusUnauthorized)
	assertSetupDatabaseState(t, databaseURL, false, true, 0)

	stopBackend()
	startBackend()
	verifyCredentials(permanentPassword, http.StatusOK)
	verifyCredentials(initialPassword, http.StatusUnauthorized)
	assertSetupDatabaseState(t, databaseURL, false, true, 0)
}

func assertSetupDatabaseState(t *testing.T, databaseURL string, mustChange, completed bool, sessions int) {
	t.Helper()
	conn := testpostgres.Connect(t, databaseURL)
	var actualMustChange, actualCompleted bool
	var actualSessions int
	err := conn.QueryRow(t.Context(), `SELECT u.must_change_password, i.onboarding_completed_at IS NOT NULL FROM public.almanac_users u CROSS JOIN public.almanac_installation i WHERE u.login = 'owner' AND i.singleton`).Scan(&actualMustChange, &actualCompleted)
	if err != nil {
		t.Fatal("read setup completion state failed")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sessions`).Scan(&actualSessions); err != nil {
		t.Fatal("count setup sessions failed")
	}
	if actualMustChange != mustChange || actualCompleted != completed || actualSessions != sessions {
		t.Fatalf("database setup state must_change/completed/sessions = %t/%t/%d", actualMustChange, actualCompleted, actualSessions)
	}
}
