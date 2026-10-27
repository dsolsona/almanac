package storage

import (
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
)

func TestDatasourceConfigurationSnapshotEnforcesExactCountAndByteLimits(t *testing.T) {
	base := agentprotocol.DatasourceAssignment{DatasourceID: "00000000-0000-4000-8000-000000000001", DatasourceVersion: "1",
		Name: "metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test",
		AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
	assignments := make([]agentprotocol.DatasourceAssignment, 256)
	for i := range assignments {
		assignments[i] = base
	}
	if _, err := encodeDatasourceConfigurationSnapshot("00000000-0000-4000-8000-000000000002", 1, assignments); err != nil {
		t.Fatalf("exact assignment count rejected: %v", err)
	}
	if _, err := encodeDatasourceConfigurationSnapshot("00000000-0000-4000-8000-000000000002", 1, append(assignments, base)); !errors.Is(err, ErrDatasourceConfigurationTooLarge) {
		t.Fatalf("one assignment above the limit error = %v", err)
	}

	assignments = []agentprotocol.DatasourceAssignment{base}
	encoded, err := encodeDatasourceConfigurationSnapshot("00000000-0000-4000-8000-000000000002", 1, assignments)
	if err != nil {
		t.Fatal(err)
	}
	assignments[0].TLS.CAPEM = strings.Repeat("x", agentprotocol.MaxConfigurationBytes-len(encoded))
	encoded, err = encodeDatasourceConfigurationSnapshot("00000000-0000-4000-8000-000000000002", 1, assignments)
	if err != nil || len(encoded) != agentprotocol.MaxConfigurationBytes {
		t.Fatalf("exact serialized byte limit length=%d, error=%v", len(encoded), err)
	}
	assignments[0].TLS.CAPEM += "x"
	if _, err := encodeDatasourceConfigurationSnapshot("00000000-0000-4000-8000-000000000002", 1, assignments); !errors.Is(err, ErrDatasourceConfigurationTooLarge) {
		t.Fatalf("one serialized byte above limit error = %v", err)
	}
}

func TestReleaseDatasourceAssignmentDecryptsOnlyWithExpectedBinding(t *testing.T) {
	const (
		installationID = "7d1b861f-21b4-49b5-a891-3d3ae72db2e1"
		datasourceID   = "a55332e8-b2b9-4a2b-ae89-141a326bfe70"
		keysetID       = "c79c723a-81a2-4e0f-ae09-f2a071b2ef45"
	)
	keysetPath := filepath.Join(t.TempDir(), "keyset.json")
	if err := sourcesecret.Provision(keysetPath, keysetID); err != nil {
		t.Fatal(err)
	}
	adapter, err := sourcesecret.Load(keysetPath, keysetID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("bearer-secret-for-agent")
	envelope, err := adapter.Encrypt(sourcesecret.Binding{InstallationID: installationID, ResourceKind: "datasource", ResourceID: datasourceID, Field: "auth.token", Revision: 1}, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	storedEnvelope, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decodedEnvelope sourcesecret.Envelope
	if err := json.Unmarshal(storedEnvelope, &decodedEnvelope); err != nil {
		t.Fatal(err)
	}
	decrypted, err := adapter.Decrypt(sourcesecret.Binding{InstallationID: installationID, ResourceKind: "datasource", ResourceID: datasourceID, Field: "auth.token", Revision: 1}, decodedEnvelope)
	if err != nil || string(decrypted) != string(plaintext) {
		t.Fatalf("stored envelope round-trip = %q, %v", decrypted, err)
	}
	clear(decrypted)
	if string(storedEnvelope) == string(plaintext) {
		t.Fatal("stored secret envelope is plaintext")
	}
	id, err := uuid.Parse(datasourceID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := uuid.Parse("67e65384-0f4b-4af9-a42a-02a6f8eeef20")
	if err != nil {
		t.Fatal(err)
	}
	tls, err := json.Marshal(datasource.TLSSettings{})
	if err != nil {
		t.Fatal(err)
	}
	row := dbgen.AlmanacDatasource{ID: uuidToPG(id), Name: "metrics-east", DisplayName: "Metrics East", Version: 1,
		Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test",
		Tls: tls, AuthenticationMode: "bearer", TokenEnvelope: storedEnvelope, TokenRevision: 1,
		PollingIntervalSeconds: 60, SettlingDelaySeconds: 60, AgentID: uuidToPG(agentID)}
	if validationErr := (agentprotocol.DatasourceAssignment{DatasourceID: row.ID.String(), DatasourceVersion: "1", Name: row.Name, Provider: row.Provider, ConfigurationFormat: row.ConfigurationFormat, Endpoint: row.Endpoint, AuthenticationMode: "bearer", BearerToken: string(plaintext), PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}).Validate(); validationErr != nil {
		t.Fatalf("fixture assignment validation: %v (id %q)", validationErr, row.ID.String())
	}
	assignment, err := releaseDatasourceAssignment(installationID, row, adapter)
	if err != nil || assignment.BearerToken != string(plaintext) {
		t.Fatalf("releaseDatasourceAssignment() = %+v, %v", assignment, err)
	}
	clear(plaintext)
	if _, err := releaseDatasourceAssignment("00000000-0000-4000-8000-000000000001", row, adapter); err == nil {
		t.Fatal("release accepted a substituted installation binding")
	}
	if _, err := releaseDatasourceAssignment(installationID, row, nil); err == nil {
		t.Fatal("release accepted missing key material")
	}
}
