//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runPerformanceBrowser(t *testing.T, ctx context.Context, fleet performanceFleet) (string, error) {
	t.Helper()
	f := fleet.fixture
	if len(fleet.objectives) != 100 {
		return "", errors.New("browser performance requires the complete controlled fleet")
	}
	block, rest := pem.Decode(f.certificate)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return "", errors.New("invalid browser performance certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", errors.New("invalid browser performance certificate")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return "", errors.New("resolve browser performance workspace failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "backend-performance-browser-")
	if err != nil {
		return "", errors.New("create browser performance evidence directory failed")
	}
	file, err := os.Open(f.binary)
	if err != nil {
		return artifacts, errors.New("open candidate executable for browser evidence failed")
	}
	digest := sha256.New()
	_, hashErr := io.Copy(digest, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		return artifacts, errors.New("identify candidate executable for browser evidence failed")
	}
	spki := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	input, err := json.Marshal(struct {
		URL       string `json:"url"`
		SPKI      string `json:"spki"`
		Login     string `json:"login"`
		Password  string `json:"password"`
		SloID     string `json:"slo_id"`
		Artifacts string `json:"artifacts"`
		Candidate string `json:"candidate_binary_sha256"`
	}{f.baseURL, base64.StdEncoding.EncodeToString(spki[:]), "sli-owner", f.password, fleet.objectives[0], artifacts, hex.EncodeToString(digest.Sum(nil))})
	if err != nil || len(input) > 16*1024 {
		return artifacts, errors.New("encode protected browser performance input failed")
	}
	defer clear(input)
	command := exec.CommandContext(ctx, "node", "scripts/measure-backend-browser.mjs")
	command.Dir = filepath.Join(root, "web")
	command.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	runErr := command.Run()
	if strings.Contains(output.String(), f.password) {
		return artifacts, errors.New("browser performance output contained a credential")
	}
	// Do not print raw Playwright or process errors. The protected report retains
	// static failure stages and all completed samples, including the first failure.
	if runErr != nil {
		return artifacts, errors.New("browser performance failed; inspect the retained browser-performance.json report")
	}
	reportFile, err := os.Open(filepath.Join(artifacts, "browser-performance.json"))
	if err != nil {
		return artifacts, errors.New("browser performance omitted its evidence report")
	}
	reportBody, readErr := io.ReadAll(io.LimitReader(reportFile, (8<<20)+1))
	reportCloseErr := reportFile.Close()
	var report struct {
		Result       string           `json:"result"`
		Candidate    string           `json:"candidate_binary_sha256"`
		ColdLoads    []jsontext.Value `json:"cold_loads"`
		Interactions []jsontext.Value `json:"interactions"`
		Failures     []string         `json:"failures"`
	}
	if readErr != nil || reportCloseErr != nil || len(reportBody) > 8<<20 || json.Unmarshal(reportBody, &report) != nil ||
		report.Result != "passed" || report.Candidate != hex.EncodeToString(digest.Sum(nil)) ||
		len(report.ColdLoads) != 60 || len(report.Interactions) != 50 || len(report.Failures) != 0 {
		return artifacts, errors.New("browser performance report is incomplete, failed or from another executable")
	}
	return artifacts, nil
}
