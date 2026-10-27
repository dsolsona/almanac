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

// checkDatasourceBrowser exercises the real authenticated datasource UI against
// the live backend and assigned agent, keeping browser output and screenshots
// free of credential values.
func checkDatasourceBrowser(t *testing.T, baseURL string, certificatePEM []byte, login, password, agentID, datasourceID, query, caPEM string) {
	t.Helper()
	parsedURL, err := url.Parse(baseURL)
	caPool := x509.NewCertPool()
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Hostname() != "localhost" || parsedURL.Path != "" && parsedURL.Path != "/" || login == "" || password == "" || !validCLIUUID(agentID) || !validCLIUUID(datasourceID) || query != "vector(1)" || len(certificatePEM) > 16*1024 || len(caPEM) == 0 || len(caPEM) > 16*1024 || !caPool.AppendCertsFromPEM([]byte(caPEM)) {
		t.Fatal("datasource browser fixture is invalid")
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		t.Fatal("decode datasource browser certificate failed")
	}
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(parsedCertificate.RawSubjectPublicKeyInfo) == 0 {
		t.Fatal("parse datasource browser certificate failed")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve repository failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "browser-datasources-")
	if err != nil {
		t.Fatal("create datasource browser artifact directory failed")
	}
	spki := sha256.Sum256(parsedCertificate.RawSubjectPublicKeyInfo)
	fixture := struct {
		URL          string `json:"url"`
		SPKI         string `json:"spki"`
		Certificate  string `json:"certificate"`
		Login        string `json:"login"`
		Password     string `json:"password"`
		AgentID      string `json:"agent_id"`
		DatasourceID string `json:"datasource_id"`
		Query        string `json:"query"`
		CAPEM        string `json:"ca_pem"`
		Artifacts    string `json:"artifacts"`
	}{
		URL: baseURL, SPKI: base64.StdEncoding.EncodeToString(spki[:]), Certificate: string(certificatePEM),
		Login: login, Password: password, AgentID: agentID, DatasourceID: datasourceID, Query: query, CAPEM: caPEM, Artifacts: artifacts,
	}
	input, err := json.Marshal(fixture)
	if err != nil || len(input) > 48*1024 {
		t.Fatal("encode datasource browser fixture failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "scripts/check-datasource-browser.mjs")
	browser.Dir = filepath.Join(root, "web")
	browser.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	browser.Stdout, browser.Stderr = &output, &output
	if err := browser.Run(); err != nil {
		if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
			t.Fatal("datasource browser fixture leaked a credential in output")
		}
		t.Fatalf("datasource browser acceptance failed; inspect sanitized artifacts in %s", artifacts)
	}
	if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
		t.Fatal("datasource browser fixture leaked a credential in output")
	}
	if strings.TrimSpace(output.String()) != "PASS browser-datasources" {
		t.Fatalf("datasource browser acceptance did not produce its exact completion marker; inspect sanitized artifacts in %s", artifacts)
	}
}
