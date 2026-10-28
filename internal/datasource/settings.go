package datasource

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/prometheus"
)

const (
	PrometheusProvider           = "prometheus"
	PrometheusConfiguration      = "prometheus-http-v1"
	DefaultPollingInterval       = 60
	MinimumPollingInterval       = 15
	MaximumPollingInterval       = 86400
	PrometheusSettlingDelay      = 60
	MaximumEndpointBytes         = 2048
	MaximumCertificateBundleSize = 64 << 10
)

var (
	ErrInvalidSettings = errors.New("datasource: invalid settings")
	datasourceNameRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

type TLSSettings struct {
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	CAPEM              string `json:"ca_pem"`
}

type Settings struct {
	Name                   string      `json:"name"`
	DisplayName            string      `json:"display_name"`
	Provider               string      `json:"provider"`
	ConfigurationFormat    string      `json:"configuration_format"`
	Endpoint               string      `json:"endpoint"`
	TLS                    TLSSettings `json:"tls"`
	AuthenticationMode     string      `json:"authentication_mode"`
	AuthenticationUser     string      `json:"authentication_username,omitempty"`
	PollingIntervalSeconds int         `json:"polling_interval_seconds"`
	SettlingDelaySeconds   int         `json:"settling_delay_seconds"`
	AgentID                string      `json:"agent_id"`
}

func DefaultSettings(name, displayName, endpoint, agentID string) Settings {
	return Settings{
		Name: name, DisplayName: displayName, Provider: PrometheusProvider,
		ConfigurationFormat: PrometheusConfiguration, Endpoint: endpoint,
		AuthenticationMode: "none", PollingIntervalSeconds: DefaultPollingInterval,
		SettlingDelaySeconds: PrometheusSettlingDelay, AgentID: agentID,
	}
}

func (s Settings) Validate() error {
	if !datasourceNameRE.MatchString(s.Name) || strings.TrimSpace(s.DisplayName) == "" || utf8.RuneCountInString(s.DisplayName) == 0 || len(s.DisplayName) > 256 ||
		s.Provider != PrometheusProvider || s.ConfigurationFormat != PrometheusConfiguration ||
		len(s.Endpoint) == 0 || len(s.Endpoint) > MaximumEndpointBytes || prometheus.ValidateEndpoint(s.Endpoint) != nil ||
		s.PollingIntervalSeconds < MinimumPollingInterval || s.PollingIntervalSeconds > MaximumPollingInterval ||
		s.SettlingDelaySeconds != PrometheusSettlingDelay || !canonicalUUID(s.AgentID) ||
		!validAuthenticationMode(s.AuthenticationMode) {
		return ErrInvalidSettings
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || (u.Scheme == "http" && (s.TLS.InsecureSkipVerify || s.TLS.CAPEM != "")) || len(s.TLS.CAPEM) > MaximumCertificateBundleSize {
		return ErrInvalidSettings
	}
	if s.AuthenticationMode == "basic" {
		if s.AuthenticationUser == "" || len(s.AuthenticationUser) > 256 {
			return ErrInvalidSettings
		}
	} else if s.AuthenticationUser != "" {
		return ErrInvalidSettings
	}
	if s.TLS.CAPEM != "" && !validCABundle(s.TLS.CAPEM) {
		return ErrInvalidSettings
	}
	return nil
}

func (s Settings) ClientTLSConfig() (*tls.Config, error) {
	if err := s.Validate(); err != nil {
		return nil, ErrInvalidSettings
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.TLS.InsecureSkipVerify} // #nosec G402 -- the setting is explicit, persisted, and returned visibly.
	if s.TLS.CAPEM == "" {
		return config, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM([]byte(s.TLS.CAPEM)) {
		return nil, ErrInvalidSettings
	}
	config.RootCAs = roots
	return config, nil
}

// SupportsOperation reports an explicitly advertised provider operation.
// It deliberately does not infer support from a provider name or software version.
func SupportsOperation(capabilities []agentprotocol.Capability, operation string) bool {
	if operation != "collect" && operation != "connection_test" && operation != "query_preview" {
		return false
	}
	for _, capability := range capabilities {
		if capability.Provider != PrometheusProvider ||
			!slices.Contains(capability.ConfigurationFormats, PrometheusConfiguration) ||
			!slices.Contains(capability.Operations, operation) {
			continue
		}
		return true
	}
	return false
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validAuthenticationMode(value string) bool {
	return value == "none" || value == "basic" || value == "bearer"
}

func validCABundle(value string) bool {
	remaining := bytes.TrimSpace([]byte(value))
	count := 0
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return false
		}
		certificates, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(certificates) != 1 || !certificates[0].IsCA {
			return false
		}
		count++
		remaining = bytes.TrimSpace(rest)
	}
	return count > 0 && strings.TrimSpace(value) != ""
}
