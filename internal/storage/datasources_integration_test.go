//go:build integration

package storage

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
)

func TestIntegrationDatasourceCRUDSecretRotationAndConfigurationRevision(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "datasource-crud")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}
	resumeVerifier := ownershipVerifier(ownershipBytes(32, 63))
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, hello, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 1, func() time.Time { return now }); err != nil {
		t.Fatalf("record initial empty datasource configuration application: %v", err)
	}
	agentPage, err := ListAgents(t.Context(), f.databaseURL, ListAgentsInput{SessionVerifier: f.verifier, Limit: 50, Now: func() time.Time { return now }})
	if err != nil || len(agentPage.Items) != 1 || agentPage.Items[0].Runtime == nil || len(agentPage.Items[0].Runtime.Capabilities) != 1 ||
		agentPage.Items[0].Runtime.Capabilities[0].Provider != "prometheus" || len(agentPage.Items[0].Runtime.Capabilities[0].Operations) != 2 {
		t.Fatalf("agent list datasource capability metadata = %+v, %v", agentPage.Items, err)
	}
	otherCredential, _ := createOwnershipAgent(t, f, "datasource-cross-agent")
	keysetID := "c79c723a-81a2-4e0f-ae09-f2a071b2ef45"
	keysetPath := filepath.Join(t.TempDir(), "source-keyset.json")
	if err := sourcesecret.Provision(keysetPath, keysetID); err != nil {
		t.Fatal(err)
	}
	secrets, err := sourcesecret.Load(keysetPath, keysetID)
	if err != nil {
		t.Fatal(err)
	}
	settings := datasource.Settings{Name: "metrics-east", DisplayName: "Metrics East", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test", AuthenticationMode: "bearer", TLS: datasource.TLSSettings{}, PollingIntervalSeconds: 60, SettlingDelaySeconds: 60, AgentID: created.Agent.ID}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	now = now.Add(time.Second)
	createdSource, err := CreateDatasource(t.Context(), f.databaseURL, secrets, CreateDatasourceInput{
		Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "replace", Value: "first-source-token"},
		IdempotencyKey: "datasource-create", RequestID: "datasource-create", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdSource.Version != 1 || !createdSource.TokenConfigured || createdSource.PasswordConfigured || createdSource.ConfigurationRevision != 2 {
		t.Fatalf("created datasource metadata = %+v", createdSource)
	}
	capability, err := GetDatasourceAgentCapability(t.Context(), f.databaseURL, proof, created.Agent.ID, func() time.Time { return now })
	if err != nil || !capability.Enabled || !capability.Reported || capability.DesiredConfigurationRevision == nil || *capability.DesiredConfigurationRevision != 2 || capability.AppliedConfigurationRevision == nil || *capability.AppliedConfigurationRevision != 1 || capability.ConfigurationFailureCode != nil {
		t.Fatalf("provider readiness during assignment reconciliation = %+v, %v", capability, err)
	}
	pending, err := ReadCurrentAgentConfiguration(t.Context(), f.databaseURL, issued.ID, issued.Verifier, func() time.Time { return now }, secrets)
	if err != nil || !pending.Pending || pending.Revision != createdSource.ConfigurationRevision || len(pending.Snapshot) != 0 {
		t.Fatalf("stale execution grant configuration response = %+v, %v", pending, err)
	}
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "4bb806a6-bfeb-4f3d-9c9f-1f63a6b4ad85", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, Operation: "query_preview", Query: "up", Secrets: secrets, RequestedAt: now, Now: func() time.Time { return now },
	}); !errors.Is(err, ErrDiagnosticConfigurationPending) {
		t.Fatalf("diagnostic admission before assignment apply = %v", err)
	}
	if _, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, "4bb806a6-bfeb-4f3d-9c9f-1f63a6b4ad85", func() time.Time { return now }); !errors.Is(err, ErrDiagnosticNotFound) {
		t.Fatalf("configuration-pending diagnostic unexpectedly created a receipt: %v", err)
	}
	now = now.Add(9 * time.Second)
	renewed, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 0, ownershipNonce(64), func() time.Time { return now })
	if err != nil || renewed.ConfigurationRevision != createdSource.ConfigurationRevision {
		t.Fatalf("configuration-bound heartbeat grant = %+v, %v", renewed, err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, renewed.ConfigurationRevision, func() time.Time { return now }); err != nil {
		t.Fatalf("record datasource configuration application: %v", err)
	}
	capability, err = GetDatasourceAgentCapability(t.Context(), f.databaseURL, proof, created.Agent.ID, func() time.Time { return now })
	if err != nil || capability.DesiredConfigurationRevision == nil || *capability.DesiredConfigurationRevision != 2 || capability.AppliedConfigurationRevision == nil || *capability.AppliedConfigurationRevision != 2 {
		t.Fatalf("provider readiness after assignment application = %+v, %v", capability, err)
	}
	current, err := ReadCurrentAgentConfiguration(t.Context(), f.databaseURL, issued.ID, issued.Verifier, func() time.Time { return now }, secrets)
	if err != nil || current.Pending || current.Revision != renewed.ConfigurationRevision || !bytes.Contains(current.Snapshot, []byte("first-source-token")) {
		t.Fatalf("matching grant configuration release = %+v, %v", current, err)
	}
	now = now.Add(time.Second)
	diagnostic, diagnosticRequest, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "afa762d3-37c1-48b1-8ec1-e8b5f9336f94", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, Operation: "query_preview", Query: "up", Secrets: secrets, RequestedAt: now, Now: func() time.Time { return now },
	})
	if err != nil || diagnostic.State != "pending" || diagnosticRequest.Datasource.BearerToken != "first-source-token" {
		t.Fatalf("saved diagnostic dispatch material = %+v request=%+v err=%v", diagnostic, diagnosticRequest, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var storedRequest []byte
	if err := conn.QueryRow(t.Context(), `SELECT request FROM public.almanac_diagnostics WHERE id=$1`, diagnostic.ID).Scan(&storedRequest); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if bytes.Contains(storedRequest, []byte("first-source-token")) {
		t.Fatal("diagnostic request stored its source credential")
	}
	now = now.Add(time.Second)
	canceledDiagnostic, err := CancelDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnostic.ID, func() time.Time { return now })
	if err != nil || canceledDiagnostic.State != "canceled" {
		t.Fatalf("diagnostic cancellation = %+v, %v", canceledDiagnostic, err)
	}
	late := agentprotocol.DiagnosticResult{DiagnosticID: diagnostic.ID, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, late, now.Add(time.Second)); !errors.Is(err, ErrDiagnosticConflict) {
		t.Fatalf("late result for canceled diagnostic error = %v", err)
	}
	status, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnostic.ID, func() time.Time { return now.Add(time.Second) })
	if err != nil || status.State != "canceled" {
		t.Fatalf("late diagnostic result replaced cancellation = %+v, %v", status, err)
	}
	now = now.Add(time.Second)
	read, err := GetDatasource(t.Context(), f.databaseURL, createdSource.ID, proof, func() time.Time { return now })
	if err != nil || !read.TokenConfigured || read.Settings.AuthenticationUser != "" {
		t.Fatalf("redacted datasource read = %+v, %v", read, err)
	}
	page, err := ListDatasources(t.Context(), f.databaseURL, proof, 10, "", func() time.Time { return now })
	if err != nil || len(page.Items) != 1 || !page.Items[0].TokenConfigured {
		t.Fatalf("redacted datasource list = %+v, %v", page, err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var envelope []byte
	if err := conn.QueryRow(t.Context(), `SELECT token_envelope FROM public.almanac_datasources WHERE id=$1`, createdSource.ID).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if string(envelope) == "first-source-token" || bytes.Contains(envelope, []byte("first-source-token")) {
		t.Fatal("datasource token was not encrypted at rest")
	}
	now = now.Add(time.Second)
	replayed, err := CreateDatasource(t.Context(), f.databaseURL, secrets, CreateDatasourceInput{
		Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "replace", Value: "first-source-token"},
		IdempotencyKey: "datasource-create", RequestID: "datasource-create-replay", Now: func() time.Time { return now },
	})
	if err != nil || replayed.ID != createdSource.ID || replayed.ConfigurationRevision != createdSource.ConfigurationRevision {
		t.Fatalf("same-key create replay = %+v, %v", replayed, err)
	}
	now = now.Add(time.Second)
	settings.DisplayName = "Metrics East Updated"
	updated, err := UpdateDatasource(t.Context(), f.databaseURL, secrets, UpdateDatasourceInput{
		Proof: proof, DatasourceID: createdSource.ID, ExpectedVersion: 1, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "retain"},
		RequestID: "datasource-update-retain", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || !updated.TokenConfigured || updated.ConfigurationRevision != 3 {
		t.Fatalf("retained datasource update = %+v", updated)
	}
	now = now.Add(time.Second)
	settings.AuthenticationMode = "none"
	updated, err = UpdateDatasource(t.Context(), f.databaseURL, secrets, UpdateDatasourceInput{
		Proof: proof, DatasourceID: createdSource.ID, ExpectedVersion: 2, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		RequestID: "datasource-update-clear", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 3 || updated.TokenConfigured || updated.ConfigurationRevision != 4 {
		t.Fatalf("cleared datasource update = %+v", updated)
	}
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, secrets, UpdateDatasourceInput{
		Proof: proof, DatasourceID: createdSource.ID, ExpectedVersion: 2, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		RequestID: "datasource-stale-update", Now: func() time.Time { return now.Add(time.Second) },
	}); err != ErrDatasourceConflict {
		t.Fatalf("stale datasource update error = %v", err)
	}
	setIntegrationUserRole(t, f, "viewer")
	now = now.Add(time.Second)
	viewerRead, err := GetDatasource(t.Context(), f.databaseURL, createdSource.ID, proof, func() time.Time { return now })
	if err != nil || viewerRead.TokenConfigured || viewerRead.Version != 3 {
		t.Fatalf("viewer redacted datasource read = %+v, %v", viewerRead, err)
	}
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, secrets, UpdateDatasourceInput{
		Proof: proof, DatasourceID: createdSource.ID, ExpectedVersion: 3, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		RequestID: "datasource-viewer-update", Now: func() time.Time { return now.Add(time.Second) },
	}); err != ErrAgentForbidden {
		t.Fatalf("viewer datasource update error = %v", err)
	}
	setIntegrationUserRole(t, f, "editor")
	now = renewed.IssuedAt.Add(agentMinimumRenewalInterval)
	renewed, err = RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 1, ownershipNonce(65), func() time.Time { return now })
	if err != nil || renewed.ConfigurationRevision != updated.ConfigurationRevision {
		t.Fatalf("updated datasource heartbeat grant = %+v, %v", renewed, err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, renewed.ConfigurationRevision, func() time.Time { return now }); err != nil {
		t.Fatalf("record updated datasource configuration application: %v", err)
	}
	preview, previewRequest, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "0a781dc5-7c3e-4fbd-8744-1af8f26c7c11", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, Operation: "query_preview", Query: "vector(1)", Secrets: secrets,
		RequestedAt: now, Now: func() time.Time { return now },
	})
	if err != nil || preview.Operation != "query_preview" || previewRequest.Datasource.AuthenticationMode != "none" {
		t.Fatalf("editor saved-source query preview = %+v request=%+v err=%v", preview, previewRequest, err)
	}
	now = now.Add(time.Second)
	previewResult := agentprotocol.DiagnosticResult{DiagnosticID: preview.ID, State: "succeeded", ResultClass: "complete",
		Series: largeDiagnosticSeries()}
	encodedPreview, err := previewResult.CanonicalJSON()
	if err != nil || len(encodedPreview) <= agentprotocol.MaxInlineDiagnosticResultBytes || len(encodedPreview) > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		t.Fatalf("large diagnostic transfer payload size = %d, err=%v", len(encodedPreview), err)
	}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 2, previewResult, now); !errors.Is(err, ErrDiagnosticConflict) {
		t.Fatalf("stale generation diagnostic result error = %v", err)
	}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, otherCredential.ID, otherCredential.Verifier, 1, previewResult, now); !errors.Is(err, ErrDiagnosticNotFound) {
		t.Fatalf("cross-agent diagnostic result error = %v", err)
	}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, previewResult, now); err != nil {
		t.Fatalf("first diagnostic result = %v", err)
	}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, previewResult, now.Add(time.Second)); err != nil {
		t.Fatalf("exact diagnostic result replay = %v", err)
	}
	changedResult := previewResult
	changedResult.Series = []agentprotocol.DiagnosticSeries{{Labels: map[string]string{"service": "api"}, Time: "1793102400.000", Value: "2"}}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, changedResult, now.Add(2*time.Second)); !errors.Is(err, ErrDiagnosticConflict) {
		t.Fatalf("changed terminal result replay error = %v", err)
	}
	completed, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, preview.ID, func() time.Time { return now.Add(2 * time.Second) })
	var storedResult agentprotocol.DiagnosticResult
	decodeErr := json.Unmarshal(completed.Result, &storedResult)
	if err != nil || decodeErr != nil || completed.State != "succeeded" || len(completed.Result) <= agentprotocol.MaxInlineDiagnosticResultBytes || len(storedResult.Series) != 20 || storedResult.Series[0].Value != "1" {
		t.Fatalf("completed diagnostic receipt = %+v, %v", completed, err)
	}
	deadlineDiagnostic, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "2c111dd1-5377-4ad3-b9b3-44e75fedc214", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, Operation: "query_preview", Query: "vector(1)", Secrets: secrets,
		RequestedAt: now.Add(time.Second), Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatalf("begin diagnostic for deadline fencing: %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_diagnostics SET deadline_at=$2 WHERE id=$1`, deadlineDiagnostic.ID, timestamp(now.Add(3*time.Second))); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	deadlineResult := agentprotocol.DiagnosticResult{DiagnosticID: deadlineDiagnostic.ID, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, deadlineResult, now.Add(4*time.Second)); !errors.Is(err, ErrDiagnosticConflict) {
		t.Fatalf("late diagnostic result error = %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var deadlineState string
	var deadlineStored []byte
	if err := conn.QueryRow(t.Context(), `SELECT state, result FROM public.almanac_diagnostics WHERE id=$1`, deadlineDiagnostic.ID).Scan(&deadlineState, &deadlineStored); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if deadlineState != "pending" || len(deadlineStored) != 0 {
		t.Fatalf("late result mutated diagnostic receipt: state=%s bytes=%d", deadlineState, len(deadlineStored))
	}
	setIntegrationUserRole(t, f, "administrator")
	revoked, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{
		Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, CredentialID: issued.ID,
		ExpectedVersion: created.Agent.Version, IdempotencyKey: "diagnostic-result-revoke", RequestID: "diagnostic-result-revoke",
		Now: func() time.Time { return now.Add(5 * time.Second) },
	})
	if err != nil || revoked.Credential == nil || revoked.Credential.RevokedAt == nil {
		t.Fatalf("revoke result-transfer credential: %+v %v", revoked, err)
	}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, previewResult, now.Add(6*time.Second)); !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatalf("result retry using revoked credential = %v", err)
	}
}

func largeDiagnosticSeries() []agentprotocol.DiagnosticSeries {
	series := make([]agentprotocol.DiagnosticSeries, 20)
	for index := range series {
		labels := make(map[string]string, 32)
		for label := range 32 {
			labels[fmt.Sprintf("label_%02d", label)] = string(bytes.Repeat([]byte{'x'}, 256))
		}
		series[index] = agentprotocol.DiagnosticSeries{Labels: labels, Time: "1793102400.000", Value: "1"}
	}
	return series
}

func setIntegrationUserRole(t *testing.T, f agentFixture, role string) {
	t.Helper()
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_users SET role=$1 WHERE id=$2`, role, f.userID); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
}
