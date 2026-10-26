//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json/v2"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

// Each scenario owns its database and process. Browser assertions are kept out of
// the ordinary unit lane and run against the same executable built for this lane.
func TestIntegrationExecutableBrowserAuthentication(t *testing.T) {
	for _, scenario := range []string{"onboarding", "lost-login", "lost-password"} {
		t.Run(scenario, func(t *testing.T) { testBrowserAuthentication(t, scenario) })
	}
}

func testBrowserAuthentication(t *testing.T, scenario string) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade browser fixture failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve browser backend port failed")
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal("release reserved browser port failed")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal("parse browser backend address failed")
	}
	baseURL := "https://localhost:" + port
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve repository failed")
	}
	dir := t.TempDir()
	initialPassword := "Browser initial phrase 493827!"
	permanentPassword := "Browser permanent phrase 681534!"
	certificate, key, err := browserTestCertificate()
	if err != nil {
		t.Fatal("create browser fixture certificate failed")
	}
	for name, contents := range map[string][]byte{
		"database": []byte(databaseURL), "initial": []byte(initialPassword),
		"certificate.pem": certificate, "key.pem": key,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal("write protected browser fixture failed")
		}
	}
	block, _ := pem.Decode(certificate)
	if block == nil {
		t.Fatal("decode browser certificate failed")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse browser certificate failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	t.Cleanup(client.CloseIdleConnections)
	backend := exec.Command(filepath.Join(root, "bin", "almanac"), "serve",
		"--components=backend,ui", "--listen="+address, "--public-origin="+baseURL,
		"--database-url-file="+filepath.Join(dir, "database"),
		"--initial-admin-login=owner", "--initial-admin-password-file="+filepath.Join(dir, "initial"),
		"--tls-cert-file="+filepath.Join(dir, "certificate.pem"),
		"--tls-key-file="+filepath.Join(dir, "key.pem"), "--shutdown-timeout=5s")
	logs := newRuntimeLog()
	backend.Stdout, backend.Stderr = io.Discard, logs
	if err := backend.Start(); err != nil {
		t.Fatal("start browser backend failed")
	}
	done := make(chan error, 1)
	go func() { done <- backend.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = backend.Process.Kill()
			<-done
		}
	})
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		select {
		case <-done:
			stopped = true
			t.Fatal("browser backend exited before readiness")
		default:
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			t.Fatal("create browser readiness request failed")
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("browser backend readiness deadline exceeded")
	}
	client.CloseIdleConnections()
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "browser-auth-"+scenario+"-")
	if err != nil {
		t.Fatal("create browser artifact directory failed")
	}
	hash := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	input, err := json.Marshal(map[string]string{
		"url": baseURL, "spki": base64.StdEncoding.EncodeToString(hash[:]),
		"certificate": string(certificate), "login": "owner", "initial": initialPassword,
		"permanent": permanentPassword, "scenario": scenario, "artifacts": artifacts,
	})
	if err != nil {
		t.Fatal("encode browser fixture failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "scripts/check-auth-browser.mjs")
	browser.Dir = filepath.Join(root, "web")
	browser.Stdin = bytes.NewReader(input)
	// The script emits stage labels only, never Playwright errors, request bodies,
	// cookies or traces. Do not print captured subprocess output on failure.
	var output bytes.Buffer
	browser.Stdout, browser.Stderr = &output, &output
	if err := browser.Run(); err != nil {
		t.Fatalf("browser acceptance failed; inspect sanitized artifacts in %s", artifacts)
	}
	if strings.Contains(output.String(), initialPassword) || strings.Contains(output.String(), permanentPassword) {
		t.Fatal("browser fixture leaked a credential in output")
	}
	if strings.TrimSpace(output.String()) != "PASS browser-auth "+scenario {
		t.Fatal("browser acceptance did not produce its exact completion marker")
	}
	assertSetupDatabaseState(t, databaseURL, false, true, 0)
	if err := backend.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal browser backend shutdown failed")
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal("browser backend shutdown failed")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("browser backend shutdown deadline exceeded")
	}
	if strings.Contains(logs.String(), initialPassword) || strings.Contains(logs.String(), permanentPassword) {
		t.Fatal("browser backend leaked a credential in logs")
	}
	t.Logf("real browser scenario %s passed; sanitized artifacts: %s", scenario, artifacts)
}

// Chromium does not negotiate the Ed25519 certificate used by Go-only fixtures.
// ECDSA P-256 exercises the browser's actual TLS path without disabling TLS.
func browserTestCertificate() ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2026), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), nil
}
