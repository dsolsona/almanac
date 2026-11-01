package telemetry

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Both export paths use the same server-owned credentials and TLS transport.
// A signal-specific value takes precedence over the general OTLP setting.
func exportHTTPConfig() (*http.Client, map[string]string, error) {
	setting := func(name string) string {
		if value := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_" + name); value != "" {
			return value
		}
		return os.Getenv("OTEL_EXPORTER_OTLP_" + name)
	}
	headers := make(map[string]string)
	for field := range strings.SplitSeq(setting("HEADERS"), ",") {
		if strings.TrimSpace(field) == "" {
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		value, err := url.PathUnescape(strings.TrimSpace(value))
		if !ok || strings.TrimSpace(key) == "" || err != nil || strings.ContainsAny(key+value, "\r\n") {
			return nil, nil, errors.New("telemetry: invalid exporter headers")
		}
		headers[strings.TrimSpace(key)] = value
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost = 2, 2
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if path := setting("CERTIFICATE"); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, errors.New("telemetry: exporter CA unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(body) {
			return nil, nil, errors.New("telemetry: invalid exporter CA")
		}
		tlsConfig.RootCAs = roots
	}
	cert, key := setting("CLIENT_CERTIFICATE"), setting("CLIENT_KEY")
	if cert != "" || key != "" {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, nil, errors.New("telemetry: exporter client certificate unavailable")
		}
		tlsConfig.Certificates = []tls.Certificate{pair}
	}
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, headers, nil
}
