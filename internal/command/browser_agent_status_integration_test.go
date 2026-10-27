//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/v2"
	"encoding/pem"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertConnectedAgentBrowser checks the actual authenticated UI and management
// API. It creates no response mocks and retains only status-page screenshots.
func assertConnectedAgentBrowser(t *testing.T, baseURL string, certificate []byte, login, password, agentID string) string {
	t.Helper()
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Hostname() != "localhost" || parsedURL.Path != "" && parsedURL.Path != "/" || login == "" || password == "" || len(agentID) != 36 || len(certificate) > 16*1024 {
		t.Fatal("connected-agent browser fixture is invalid")
	}
	block, _ := pem.Decode(certificate)
	if block == nil {
		t.Fatal("decode connected-agent browser certificate failed")
	}
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(parsedCertificate.RawSubjectPublicKeyInfo) == 0 {
		t.Fatal("parse connected-agent browser certificate failed")
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve repository failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "browser-agent-status-")
	if err != nil {
		t.Fatal("create connected-agent browser artifact directory failed")
	}
	spki := sha256.Sum256(parsedCertificate.RawSubjectPublicKeyInfo)
	fixture := struct {
		URL         string `json:"url"`
		SPKI        string `json:"spki"`
		Certificate string `json:"certificate"`
		Login       string `json:"login"`
		Password    string `json:"password"`
		AgentID     string `json:"agent_id"`
		Artifacts   string `json:"artifacts"`
	}{
		URL: baseURL, SPKI: base64.StdEncoding.EncodeToString(spki[:]),
		Certificate: string(certificate), Login: login, Password: password,
		AgentID: agentID, Artifacts: artifacts,
	}
	input, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal("encode connected-agent browser fixture failed")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "scripts/check-agent-status-browser.mjs")
	browser.Dir = filepath.Join(root, "web")
	browser.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	browser.Stdout, browser.Stderr = &output, &output
	if err := browser.Run(); err != nil {
		if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
			t.Fatal("connected-agent browser fixture leaked a credential in output")
		}
		t.Fatalf("connected-agent browser acceptance failed; inspect sanitized artifacts in %s", artifacts)
	}
	if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
		t.Fatal("connected-agent browser fixture leaked a credential in output")
	}
	if strings.TrimSpace(output.String()) != "PASS browser-agent-status" {
		t.Fatalf("connected-agent browser acceptance did not produce its exact completion marker; inspect sanitized artifacts in %s", artifacts)
	}
	return artifacts
}
