package telemetry

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestBothExportersUseServerOwnedHeadersPrivateCAAndClientCertificate(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "collector client"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(clientPEM)
	received := make(chan string, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.Header.Get("Authorization") != "Bearer collector-secret+value" || len(r.TLS.PeerCertificates) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	write := func(name string, body []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", server.URL+"/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=wrong-general-value")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "Authorization=Bearer%20collector-secret%2Bvalue")
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE", write("client.pem", clientPEM))
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY", write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})))
	provider, err := Start(t.Context(), "almanac-backend")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		otel.SetTracerProvider(noop.NewTracerProvider())
	})
	_, span := Tracer().Start(t.Context(), "fixture")
	span.End()
	if err := provider.browser.enqueue([]byte(`{"resourceSpans":[]}`)); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case contentType := <-received:
			seen[contentType] = true
		case <-time.After(3 * time.Second):
			t.Fatal("both authenticated mutual-TLS export paths did not reach collector")
		}
	}
	if !seen["application/x-protobuf"] || !seen["application/json"] {
		t.Fatal("backend and browser export paths were not both exercised")
	}
}
