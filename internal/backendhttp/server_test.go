package backendhttp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
