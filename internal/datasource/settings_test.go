package datasource

import (
	"crypto/tls"
	"testing"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

func TestDefaultSettingsUsePrometheusCadenceAndSettlingDelay(t *testing.T) {
	settings := DefaultSettings("victoria", "Victoria", "https://metrics.example.test", "00000000-0000-4000-8000-000000000001")
	if settings.Provider != "prometheus" || settings.ConfigurationFormat != "prometheus-http-v1" ||
		settings.PollingIntervalSeconds != 60 || settings.SettlingDelaySeconds != 60 {
		t.Fatalf("default settings = %#v", settings)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("valid default settings: %v", err)
	}
	tlsConfig, err := settings.ClientTLSConfig()
	if err != nil || tlsConfig.MinVersion < tls.VersionTLS12 || tlsConfig.InsecureSkipVerify {
		t.Fatalf("default TLS config = %#v, error = %v", tlsConfig, err)
	}
}

func TestSettingsRejectInvalidEndpointAndCadence(t *testing.T) {
	base := DefaultSettings("victoria", "Victoria", "http://10.0.0.9:8428", "00000000-0000-4000-8000-000000000001")
	tests := []struct {
		name string
		edit func(*Settings)
	}{
		{name: "embedded credentials", edit: func(value *Settings) { value.Endpoint = "https://user:secret@metrics.example.test" }},
		{name: "fragment", edit: func(value *Settings) { value.Endpoint += "#fragment" }},
		{name: "too-fast polling", edit: func(value *Settings) { value.PollingIntervalSeconds = 14 }},
		{name: "mutable settling delay", edit: func(value *Settings) { value.SettlingDelaySeconds = 61 }},
		{name: "invalid CA", edit: func(value *Settings) { value.TLS.CAPEM = "not a certificate" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := base
			tt.edit(&settings)
			if err := settings.Validate(); err == nil {
				t.Fatal("Validate accepted invalid settings")
			}
		})
	}
}

func TestCapabilityRequiresExactProviderFormatAndDiagnosticOperation(t *testing.T) {
	tests := []struct {
		name string
		caps []agentprotocol.Capability
		want bool
	}{
		{name: "exact tuple", caps: []agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"connection_test", "query_preview"}}}, want: true},
		{name: "wrong format", caps: []agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v2"}, Operations: []string{"connection_test"}}}},
		{name: "wrong operation", caps: []agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"live_query"}}}},
		{name: "wrong provider", caps: []agentprotocol.Capability{{Provider: "tempo", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"connection_test"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsOperation(tt.caps, "connection_test"); got != tt.want {
				t.Fatalf("SupportsOperation() = %v, want %v", got, tt.want)
			}
		})
	}
	if SupportsOperation([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"live_query"}}}, "live_query") {
		t.Fatal("live_query was accepted before collection is implemented")
	}
}

func TestCollectionRequiresExplicitProviderFormatAndOperation(t *testing.T) {
	capability := agentprotocol.Capability{Provider: PrometheusProvider,
		ConfigurationFormats: []string{PrometheusConfiguration}, Operations: []string{"collect"}}
	if !SupportsOperation([]agentprotocol.Capability{capability}, "collect") {
		t.Fatal("advertised Prometheus collection capability was rejected")
	}
	capability.Operations = []string{"connection_test"}
	if SupportsOperation([]agentprotocol.Capability{capability}, "collect") {
		t.Fatal("diagnostic capability implicitly enabled collection")
	}
}
