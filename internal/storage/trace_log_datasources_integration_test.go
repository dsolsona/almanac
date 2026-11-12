//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
)

// TestIntegrationDatasourceProviderAdmissionAndImmutability exercises provider capability isolation, persisted settings and immutable provider identity against PostgreSQL.
func TestIntegrationDatasourceProviderAdmissionAndImmutability(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	now := f.base.Add(4 * time.Second)
	oldCredential, oldAgent := createOwnershipAgent(t, f, "old-provider-agent")
	newCredential, newAgent := createOwnershipAgent(t, f, "new-provider-agent")
	oldCaps, _ := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	newCaps, _ := json.Marshal(datasource.Capabilities())
	resumeOld := ownershipVerifier(ownershipBytes(32, 63))
	resumeNew := ownershipVerifier(ownershipBytes(32, 64))
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, oldCredential.ID, oldCredential.Verifier, resumeOld, &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-old", Capabilities: oldCaps}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier, resumeNew, &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-new", Capabilities: newCaps}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	for _, provider := range []string{"victoriatraces", "tempo", "loki"} {
		settings := datasource.DefaultSettings(provider, "Source "+provider, "http://source:3100", oldAgent.Agent.ID)
		settings.Provider, settings.ConfigurationFormat = provider, provider+"-http-v1"
		input := CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "create-" + provider, RequestID: "create-" + provider, Now: func() time.Time { return now }}
		if _, err := CreateDatasource(t.Context(), f.databaseURL, nil, input); !errors.Is(err, ErrAgentConflict) {
			t.Fatalf("%s admitted on old agent: %v", provider, err)
		}
		input.Settings.AgentID = newAgent.Agent.ID
		saved, err := CreateDatasource(t.Context(), f.databaseURL, nil, input)
		if err != nil {
			t.Fatalf("%s create: %v", provider, err)
		}
		if saved.Settings.Provider != provider {
			t.Fatal("provider lost in storage")
		}
		changed := input.Settings
		changed.Provider, changed.ConfigurationFormat = "prometheus", "prometheus-http-v1"
		if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, UpdateDatasourceInput{Proof: proof, DatasourceID: saved.ID, ExpectedVersion: saved.Version, Settings: changed, Password: input.Password, Token: input.Token, RequestID: "change-" + provider, Now: input.Now}); !errors.Is(err, ErrInvalidAgentInput) {
			t.Fatalf("provider mutation: %v", err)
		}
	}
}
