//go:build integration

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
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
	savedTest, savedTestRequest, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "7a111111-5377-4ad3-b9b3-44e75fedc211", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, ExpectedDatasourceVersion: 1,
		IdempotencyKey: "saved-connection-test", Operation: "connection_test", Secrets: secrets,
		RequestedAt: now, Now: func() time.Time { return now },
	})
	if err != nil || savedTest.Operation != "connection_test" || savedTest.DatasourceID != createdSource.ID ||
		savedTestRequest.Datasource.Endpoint != settings.Endpoint || savedTestRequest.Datasource.BearerToken != "first-source-token" {
		t.Fatalf("saved connection test did not use the committed settings and secret: record=%+v request=%+v err=%v", savedTest, savedTestRequest, err)
	}
	replay, replayRequest, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "7a111111-5377-4ad3-b9b3-44e75fedc212", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, ExpectedDatasourceVersion: 1,
		IdempotencyKey: "saved-connection-test", Operation: "connection_test", Secrets: secrets,
		RequestedAt: now.Add(time.Second), Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil || !replay.Replayed || replay.ID != savedTest.ID || replayRequest.DiagnosticID != "" {
		t.Fatalf("saved connection test idempotency replay = %+v request=%+v err=%v", replay, replayRequest, err)
	}
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "7a111111-5377-4ad3-b9b3-44e75fedc213", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, ExpectedDatasourceVersion: 2,
		IdempotencyKey: "saved-connection-test", Operation: "connection_test", Secrets: secrets,
		RequestedAt: now.Add(time.Second), Now: func() time.Time { return now.Add(time.Second) },
	}); !errors.Is(err, ErrDiagnosticConflict) {
		t.Fatalf("saved connection test idempotency key reuse error = %v", err)
	}
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: "7a111111-5377-4ad3-b9b3-44e75fedc214", AgentID: created.Agent.ID,
		AgentGeneration: 1, DatasourceID: createdSource.ID, ExpectedDatasourceVersion: 2,
		IdempotencyKey: "saved-connection-stale-version", Operation: "connection_test", Secrets: secrets,
		RequestedAt: now.Add(time.Second), Now: func() time.Time { return now.Add(time.Second) },
	}); !errors.Is(err, ErrDatasourceConflict) {
		t.Fatalf("saved connection test stale version error = %v", err)
	}
	if _, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, "7a111111-5377-4ad3-b9b3-44e75fedc214", func() time.Time { return now.Add(time.Second) }); !errors.Is(err, ErrDiagnosticNotFound) {
		t.Fatalf("stale saved connection test created a receipt: %v", err)
	}
	if canceled, err := CancelDatasourceDiagnostic(t.Context(), f.databaseURL, proof, savedTest.ID, func() time.Time { return now.Add(time.Second) }); err != nil || canceled.State != "canceled" {
		t.Fatalf("cancel saved connection test = %+v, %v", canceled, err)
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
	if err := verifyDiagnosticCancellationCompletionRace(t, f, issued.ID, issued.Verifier, created.Agent.ID, createdSource.ID, secrets, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := verifyDiagnosticTimeoutCompletionRace(t, f, issued.ID, issued.Verifier, created.Agent.ID, createdSource.ID, secrets, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
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
	if err := verifyConcurrentDatasourceRevisionWrites(t, f, created.Agent.ID, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
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

func verifyConcurrentDatasourceRevisionWrites(t *testing.T, f agentFixture, agentID string, now time.Time) error {
	t.Helper()
	secondVerifier := insertDiagnosticActor(t, f, "datasource-revision-second")
	proofs := [2]DatasourceProof{{SessionVerifier: f.verifier}, {SessionVerifier: secondVerifier}}
	controller := integrationConnect(t, f.databaseURL)
	controlTx, err := controller.Begin(t.Context())
	if err != nil {
		closeIntegrationConn(controller)
		return fmt.Errorf("begin datasource revision lock barrier: %w", err)
	}
	var lockedAgent string
	if err := controlTx.QueryRow(t.Context(), "SELECT id::text FROM public.almanac_agents WHERE id=$1 FOR UPDATE", agentID).Scan(&lockedAgent); err != nil {
		rollbackTransaction(controlTx)
		closeIntegrationConn(controller)
		return fmt.Errorf("lock agent for concurrent datasource writes: %w", err)
	}
	type result struct {
		record DatasourceRecord
		err    error
	}
	results := make(chan result, 2)
	for index, name := range []string{"revision-concurrent-a", "revision-concurrent-b"} {
		go func() {
			settings := datasource.Settings{Name: name, DisplayName: name, Provider: datasource.PrometheusProvider,
				ConfigurationFormat: datasource.PrometheusConfiguration, Endpoint: "https://metrics.example.test",
				AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: datasource.PrometheusSettlingDelay, AgentID: agentID}
			record, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proofs[index], Settings: settings,
				Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
				IdempotencyKey: "revision-write-" + name, RequestID: "revision-write-" + name, Now: func() time.Time { return now }})
			results <- result{record: record, err: err}
		}()
	}
	observer := integrationConnect(t, f.databaseURL)
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		var waiters int
		if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
			AND query ILIKE '%FROM public.almanac_agents%'`).Scan(&waiters); err != nil {
			rollbackTransaction(controlTx)
			closeIntegrationConn(observer)
			closeIntegrationConn(controller)
			return fmt.Errorf("observe concurrent datasource writers: %w", err)
		}
		if waiters >= 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			rollbackTransaction(controlTx)
			closeIntegrationConn(observer)
			closeIntegrationConn(controller)
			return fmt.Errorf("context ended observing concurrent datasource writers: %w", t.Context().Err())
		}
	}
	if time.Now().After(deadline) {
		rollbackTransaction(controlTx)
		closeIntegrationConn(observer)
		closeIntegrationConn(controller)
		return errors.New("concurrent datasource writes did not wait on the shared agent lock")
	}
	if err := controlTx.Commit(t.Context()); err != nil {
		closeIntegrationConn(observer)
		closeIntegrationConn(controller)
		return fmt.Errorf("release concurrent datasource writer barrier: %w", err)
	}
	closeIntegrationConn(controller)
	defer closeIntegrationConn(observer)
	revisions := make([]int64, 0, 2)
	for range 2 {
		select {
		case output := <-results:
			if output.err != nil {
				return fmt.Errorf("concurrent datasource write failed: %w", output.err)
			}
			revisions = append(revisions, output.record.ConfigurationRevision)
		case <-time.After(5 * time.Second):
			return errors.New("concurrent datasource write did not finish after releasing agent lock")
		}
	}
	if revisions[0] == revisions[1] || max(revisions[0], revisions[1])-min(revisions[0], revisions[1]) != 1 {
		return fmt.Errorf("concurrent datasource writes chose configuration revisions %v", revisions)
	}
	var latest int64
	if err := observer.QueryRow(t.Context(), "SELECT max(revision) FROM public.almanac_agent_configurations WHERE agent_id=$1", agentID).Scan(&latest); err != nil {
		return fmt.Errorf("read latest concurrent configuration revision: %w", err)
	}
	if latest != max(revisions[0], revisions[1]) {
		return fmt.Errorf("latest stored configuration revision=%d, write results=%v", latest, revisions)
	}
	return nil
}

func TestIntegrationDatasourceConfigurationSnapshotIsCoherentDuringEdit(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "datasource-config-coherence")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: datasource.PrometheusProvider,
		ConfigurationFormats: []string{datasource.PrometheusConfiguration}, Operations: []string{"connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	resumeVerifier := ownershipVerifier(ownershipBytes(32, 87))
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier,
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 1, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	settings := datasource.Settings{Name: "coherent-source", DisplayName: "Coherent source", Provider: datasource.PrometheusProvider,
		ConfigurationFormat: datasource.PrometheusConfiguration, Endpoint: "https://before.example.test", AuthenticationMode: "none",
		PollingIntervalSeconds: 60, SettlingDelaySeconds: 60, AgentID: created.Agent.ID}
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{
		Proof: DatasourceProof{SessionVerifier: f.verifier}, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		IdempotencyKey: "coherent-source", RequestID: "coherent-source", Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	renewedAt := grant.AcquiredAt.Add(agentMinimumRenewalInterval)
	renewed, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 0,
		ownershipNonce(88), func() time.Time { return renewedAt })
	if err != nil || renewed.ConfigurationRevision != source.ConfigurationRevision {
		t.Fatalf("renew datasource configuration grant = %+v, %v", renewed, err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1,
		renewed.ConfigurationRevision, func() time.Time { return renewedAt }); err != nil {
		t.Fatal(err)
	}
	runAt := renewedAt.Add(time.Second)
	settings.Endpoint = "https://after.example.test"
	type readResult struct {
		snapshot AgentConfigurationSnapshot
		err      error
	}
	type writeResult struct {
		record DatasourceRecord
		err    error
	}
	start := make(chan struct{})
	reads := make(chan readResult, 1)
	writes := make(chan writeResult, 1)
	go func() {
		<-start
		snapshot, err := ReadCurrentAgentConfiguration(t.Context(), f.databaseURL, issued.ID, issued.Verifier, func() time.Time { return runAt })
		reads <- readResult{snapshot: snapshot, err: err}
	}()
	go func() {
		<-start
		record, err := UpdateDatasource(t.Context(), f.databaseURL, nil, UpdateDatasourceInput{
			Proof: DatasourceProof{SessionVerifier: f.verifier}, DatasourceID: source.ID, ExpectedVersion: source.Version,
			Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
			RequestID: "coherent-source-edit", Now: func() time.Time { return runAt },
		})
		writes <- writeResult{record: record, err: err}
	}()
	close(start)
	read := <-reads
	write := <-writes
	if read.err != nil || write.err != nil {
		t.Fatalf("coherent configuration race read=%+v (%v), write=%+v (%v)", read.snapshot, read.err, write.record, write.err)
	}
	if write.record.ConfigurationRevision != source.ConfigurationRevision+1 {
		t.Fatalf("updated datasource configuration revision=%d, want %d", write.record.ConfigurationRevision, source.ConfigurationRevision+1)
	}
	if read.snapshot.Pending {
		if len(read.snapshot.Snapshot) != 0 || read.snapshot.Revision != write.record.ConfigurationRevision {
			t.Fatalf("post-edit stale grant response was not a no-snapshot pending result: %+v", read.snapshot)
		}
		return
	}
	if read.snapshot.Revision != source.ConfigurationRevision {
		t.Fatalf("configuration read observed an unrelated revision %d, want %d", read.snapshot.Revision, source.ConfigurationRevision)
	}
	snapshot, err := agentprotocol.DecodeConfigurationSnapshot(read.snapshot.Snapshot)
	if err != nil || len(snapshot.Assignments) != 1 || snapshot.Assignments[0].DatasourceVersion != "1" || snapshot.Assignments[0].Endpoint != "https://before.example.test" {
		t.Fatalf("configuration read mixed datasource edit state: snapshot=%+v, %v", snapshot, err)
	}
}

func verifyDiagnosticCancellationCompletionRace(t *testing.T, f agentFixture, credentialID string, credentialVerifier [32]byte, agentID, datasourceID string, secrets *sourcesecret.Adapter, now time.Time) error {
	t.Helper()
	proof := DatasourceProof{SessionVerifier: f.verifier}
	const diagnosticID = "5c000000-0000-4000-8000-000000000001"
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: diagnosticID, AgentID: agentID, AgentGeneration: 1, DatasourceID: datasourceID,
		Operation: "query_preview", Query: "vector(1)", Secrets: secrets, RequestedAt: now, Now: func() time.Time { return now },
	}); err != nil {
		return fmt.Errorf("begin cancellation/completion race receipt: %w", err)
	}
	start := make(chan struct{})
	type completion struct{ err error }
	type cancellation struct {
		record DiagnosticRecord
		err    error
	}
	completed := make(chan completion, 1)
	canceled := make(chan cancellation, 1)
	go func() {
		<-start
		result := agentprotocol.DiagnosticResult{DiagnosticID: diagnosticID, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
		completed <- completion{err: CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, credentialID, credentialVerifier, 1, result, now)}
	}()
	go func() {
		<-start
		record, err := CancelDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return now })
		canceled <- cancellation{record: record, err: err}
	}()
	close(start)
	var completeResult completion
	var cancelResult cancellation
	select {
	case completeResult = <-completed:
	case <-time.After(5 * time.Second):
		return errors.New("completion did not finish in diagnostic cancellation race")
	}
	select {
	case cancelResult = <-canceled:
	case <-time.After(5 * time.Second):
		return errors.New("cancellation did not finish in diagnostic cancellation race")
	}
	if cancelResult.err != nil {
		return fmt.Errorf("race cancellation failed: %w", cancelResult.err)
	}
	current, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		return fmt.Errorf("read cancellation/completion race receipt: %w", err)
	}
	switch current.State {
	case "canceled":
		if !errors.Is(completeResult.err, ErrDiagnosticConflict) || cancelResult.record.State != "canceled" {
			return fmt.Errorf("cancellation won but completion outcome was %v, returned cancellation=%s", completeResult.err, cancelResult.record.State)
		}
	case "succeeded":
		if completeResult.err != nil || cancelResult.record.State != "succeeded" {
			return fmt.Errorf("completion won but completion outcome was %v, returned cancellation=%s", completeResult.err, cancelResult.record.State)
		}
	default:
		return fmt.Errorf("race left diagnostic in nonterminal state %q", current.State)
	}
	return verifyTerminalDiagnosticTransitionWait(t, f, credentialID, credentialVerifier, agentID, datasourceID, secrets, now,
		"5c000000-0000-4000-8000-000000000003", false)
}

func verifyDiagnosticTimeoutCompletionRace(t *testing.T, f agentFixture, credentialID string, credentialVerifier [32]byte, agentID, datasourceID string, secrets *sourcesecret.Adapter, now time.Time) error {
	t.Helper()
	proof := DatasourceProof{SessionVerifier: f.verifier}
	const diagnosticID = "5c000000-0000-4000-8000-000000000002"
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: diagnosticID, AgentID: agentID, AgentGeneration: 1, DatasourceID: datasourceID,
		Operation: "query_preview", Query: "vector(1)", Secrets: secrets, RequestedAt: now, Now: func() time.Time { return now },
	}); err != nil {
		return fmt.Errorf("begin timeout/completion race receipt: %w", err)
	}
	deadline := now.Add(time.Second)
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_diagnostics SET deadline_at=$2 WHERE id=$1`, diagnosticID, timestamp(deadline)); err != nil {
		closeIntegrationConn(conn)
		return fmt.Errorf("set timeout/completion race deadline: %w", err)
	}
	closeIntegrationConn(conn)
	start := make(chan struct{})
	type completion struct{ err error }
	type timeout struct {
		record DiagnosticRecord
		err    error
	}
	completed := make(chan completion, 1)
	timedOut := make(chan timeout, 1)
	go func() {
		<-start
		result := agentprotocol.DiagnosticResult{DiagnosticID: diagnosticID, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
		completed <- completion{err: CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, credentialID, credentialVerifier, 1, result, deadline.Add(-time.Microsecond))}
	}()
	go func() {
		<-start
		record, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return deadline })
		timedOut <- timeout{record: record, err: err}
	}()
	close(start)
	var completeResult completion
	var timeoutResult timeout
	select {
	case completeResult = <-completed:
	case <-time.After(5 * time.Second):
		return errors.New("completion did not finish in diagnostic timeout race")
	}
	select {
	case timeoutResult = <-timedOut:
	case <-time.After(5 * time.Second):
		return errors.New("timeout read did not finish in diagnostic timeout race")
	}
	if timeoutResult.err != nil {
		return fmt.Errorf("race timeout read failed: %w", timeoutResult.err)
	}
	current, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return deadline.Add(time.Second) })
	if err != nil {
		return fmt.Errorf("read timeout/completion race receipt: %w", err)
	}
	switch current.State {
	case "timed_out":
		if !errors.Is(completeResult.err, ErrDiagnosticConflict) || timeoutResult.record.State != "timed_out" {
			return fmt.Errorf("timeout won but completion outcome was %v, returned timeout=%s", completeResult.err, timeoutResult.record.State)
		}
	case "succeeded":
		if completeResult.err != nil || timeoutResult.record.State != "succeeded" {
			return fmt.Errorf("completion won but completion outcome was %v, returned timeout=%s", completeResult.err, timeoutResult.record.State)
		}
	default:
		return fmt.Errorf("race left diagnostic in nonterminal state %q", current.State)
	}
	return verifyTerminalDiagnosticTransitionWait(t, f, credentialID, credentialVerifier, agentID, datasourceID, secrets, now,
		"5c000000-0000-4000-8000-000000000004", true)
}

// Force the stale-read interleaving: the controller commits a succeeded result
// while Cancel/Get has read pending and is blocked on its conditional UPDATE.
func verifyTerminalDiagnosticTransitionWait(t *testing.T, f agentFixture, credentialID string, credentialVerifier [32]byte, agentID, datasourceID string, secrets *sourcesecret.Adapter, now time.Time, diagnosticID string, timeout bool) error {
	t.Helper()
	proof := DatasourceProof{SessionVerifier: f.verifier}
	deadline := now.Add(30 * time.Second)
	// Use the admission's original deadline, visible to the stale MVCC read.
	// Get is invoked at that deadline so its conditional timeout update waits
	// behind the controller's uncommitted succeeded-state update.
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: proof, DiagnosticID: diagnosticID, AgentID: agentID, AgentGeneration: 1, DatasourceID: datasourceID,
		Operation: "query_preview", Query: "vector(1)", Secrets: secrets, RequestedAt: now, Now: func() time.Time { return now },
	}); err != nil {
		return fmt.Errorf("begin forced terminal-transition race: %w", err)
	}
	result := agentprotocol.DiagnosticResult{DiagnosticID: diagnosticID, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
	encoded, err := result.CanonicalJSON()
	if err != nil {
		return fmt.Errorf("encode forced terminal-transition result: %w", err)
	}
	digest := sha256.Sum256(encoded)
	controller := integrationConnect(t, f.databaseURL)
	tx, err := controller.Begin(t.Context())
	if err != nil {
		closeIntegrationConn(controller)
		return fmt.Errorf("begin terminal-transition controller: %w", err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_diagnostics SET state='succeeded', result=$2::jsonb,
		result_digest=$3, updated_at=$4 WHERE id=$1`, diagnosticID, encoded, digest[:], timestamp(now)); err != nil {
		rollbackTransaction(tx)
		closeIntegrationConn(controller)
		return fmt.Errorf("hold completed diagnostic row: %w", err)
	}
	type resultRecord struct {
		record DiagnosticRecord
		err    error
	}
	finished := make(chan resultRecord, 1)
	go func() {
		if timeout {
			record, readErr := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return deadline })
			finished <- resultRecord{record: record, err: readErr}
			return
		}
		record, cancelErr := CancelDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return now })
		finished <- resultRecord{record: record, err: cancelErr}
	}()
	observer := integrationConnect(t, f.databaseURL)
	if err := waitForDiagnosticUpdateLock(t, observer); err != nil {
		rollbackTransaction(tx)
		closeIntegrationConn(observer)
		closeIntegrationConn(controller)
		return err
	}
	if err := tx.Commit(t.Context()); err != nil {
		closeIntegrationConn(observer)
		closeIntegrationConn(controller)
		return fmt.Errorf("commit winning diagnostic completion: %w", err)
	}
	closeIntegrationConn(observer)
	closeIntegrationConn(controller)
	select {
	case outcome := <-finished:
		if outcome.err != nil || outcome.record.State != "succeeded" {
			return fmt.Errorf("stale terminal transition overwrote completion: record=%+v err=%v", outcome.record, outcome.err)
		}
	case <-time.After(5 * time.Second):
		return errors.New("terminal transition did not finish after completion commit")
	}
	return nil
}

func waitForDiagnosticUpdateLock(t *testing.T, observer *pgx.Conn) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		var waiters int
		if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
			AND query ILIKE '%UPDATE public.almanac_diagnostics SET state=%'`).Scan(&waiters); err != nil {
			return fmt.Errorf("observe diagnostic terminal-transition lock: %w", err)
		}
		if waiters > 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			return fmt.Errorf("context ended observing diagnostic transition: %w", t.Context().Err())
		}
	}
	return errors.New("diagnostic terminal transition did not wait on the held row")
}

func TestIntegrationDatasourceAssignmentCountLimitRollsBackOverLimitWrite(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "datasource-assignment-count")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: datasource.PrometheusProvider,
		ConfigurationFormats: []string{datasource.PrometheusConfiguration}, Operations: []string{"connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(ownershipBytes(32, 91)),
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	tls := `{"insecure_skip_verify":false,"ca_pem":""}`
	for index := range 255 {
		id := fmt.Sprintf("6c000000-0000-4000-8000-%012d", index+1)
		name := fmt.Sprintf("count-%03d", index+1)
		if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_datasources
			(id,name,display_name,version,provider,configuration_format,endpoint,tls,authentication_mode,
			authentication_username,password_envelope,password_revision,token_envelope,token_revision,
			polling_interval_seconds,settling_delay_seconds,agent_id,created_at,updated_at)
			VALUES ($1::uuid,$2,$3,1,'prometheus','prometheus-http-v1','https://metrics.example.test',$4::jsonb,'none',
			NULL,NULL,0,NULL,0,60,60,$5::uuid,$6,$6)`, id, name, name, tls, created.Agent.ID, now); err != nil {
			closeIntegrationConn(conn)
			t.Fatalf("seed datasource assignment %d: %v", index+1, err)
		}
	}
	closeIntegrationConn(conn)
	settings := datasource.Settings{Name: "count-final", DisplayName: "Count final", Provider: datasource.PrometheusProvider,
		ConfigurationFormat: datasource.PrometheusConfiguration, Endpoint: "https://metrics.example.test", AuthenticationMode: "none",
		PollingIntervalSeconds: 60, SettlingDelaySeconds: 60, AgentID: created.Agent.ID}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	final, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		IdempotencyKey: "count-limit-256", RequestID: "count-limit-256", Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatalf("exactly 256 assignments were rejected: %v", err)
	}
	agentID, _ := canonicalUUID(created.Agent.ID)
	conn = integrationConnect(t, f.databaseURL)
	var installationID string
	if err := conn.QueryRow(t.Context(), "SELECT installation_id::text FROM public.almanac_installation WHERE singleton=true").Scan(&installationID); err != nil {
		closeIntegrationConn(conn)
		t.Fatal(err)
	}
	snapshot, err := buildDatasourceConfigurationSnapshot(t.Context(), dbgen.New(conn), installationID, agentID, final.ConfigurationRevision, nil)
	closeIntegrationConn(conn)
	var decoded agentprotocol.ConfigurationSnapshot
	if err != nil || json.Unmarshal(snapshot, &decoded) != nil || len(decoded.Assignments) != 256 {
		t.Fatalf("256-assignment snapshot has %d assignments, error=%v", len(decoded.Assignments), err)
	}
	settings.Name, settings.DisplayName = "count-over", "Count over"
	if _, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		IdempotencyKey: "count-limit-257", RequestID: "count-limit-257", Now: func() time.Time { return now.Add(2 * time.Second) }}); !errors.Is(err, ErrDatasourceConfigurationTooLarge) {
		t.Fatalf("257th assignment write error = %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var datasourceCount, auditCount, receiptCount int
	var latestRevision int64
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_datasources WHERE agent_id=$1", created.Agent.ID).Scan(&datasourceCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT max(revision) FROM public.almanac_agent_configurations WHERE agent_id=$1", created.Agent.ID).Scan(&latestRevision); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE actor=$1 AND action='datasource.created'", f.userID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='datasource.create'", f.userID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if datasourceCount != 256 || latestRevision != final.ConfigurationRevision || auditCount != 1 || receiptCount != 1 {
		t.Fatalf("over-limit write was not atomic: sources=%d revision=%d audit=%d receipts=%d final_revision=%d", datasourceCount, latestRevision, auditCount, receiptCount, final.ConfigurationRevision)
	}
}

func TestIntegrationDatasourceSecretConfigurationByteLimitRollsBackOverLimitWrite(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "datasource-config-byte-limit")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: datasource.PrometheusProvider,
		ConfigurationFormats: []string{datasource.PrometheusConfiguration}, Operations: []string{"connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(ownershipBytes(32, 93)),
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	keysetID := "e79c723a-81a2-4e0f-ae09-f2a071b2ef45"
	keysetPath := filepath.Join(t.TempDir(), "source-keyset.json")
	if err := sourcesecret.Provision(keysetPath, keysetID); err != nil {
		t.Fatal(err)
	}
	secrets, err := sourcesecret.Load(keysetPath, keysetID)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.Repeat("s", 16<<10)
	proof := DatasourceProof{SessionVerifier: f.verifier}
	successfulWrites := 0
	for index := range 64 {
		name := fmt.Sprintf("large-token-%02d", index+1)
		settings := datasource.Settings{Name: name, DisplayName: name, Provider: datasource.PrometheusProvider,
			ConfigurationFormat: datasource.PrometheusConfiguration, Endpoint: "https://metrics.example.test",
			AuthenticationMode: "bearer", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60, AgentID: created.Agent.ID}
		_, err := CreateDatasource(t.Context(), f.databaseURL, secrets, CreateDatasourceInput{Proof: proof, Settings: settings,
			Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "replace", Value: password},
			IdempotencyKey: "large-token-" + fmt.Sprint(index+1), RequestID: "large-token-" + fmt.Sprint(index+1),
			Now: func() time.Time { return now.Add(time.Duration(index+1) * time.Second) }})
		if errors.Is(err, ErrDatasourceConfigurationTooLarge) {
			break
		}
		if err != nil {
			t.Fatalf("secret-bearing datasource write %d: %v", index+1, err)
		}
		successfulWrites++
	}
	if successfulWrites == 0 || successfulWrites >= 64 {
		t.Fatalf("expected a secret-bearing configuration to exceed 1 MiB before 64 writes; successful=%d", successfulWrites)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var sourceCount, auditCount, receiptCount int
	var latestRevision int64
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_datasources WHERE agent_id=$1", created.Agent.ID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT max(revision) FROM public.almanac_agent_configurations WHERE agent_id=$1", created.Agent.ID).Scan(&latestRevision); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE actor=$1 AND action='datasource.created'", f.userID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='datasource.create'", f.userID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	var installationID string
	if err := conn.QueryRow(t.Context(), "SELECT installation_id::text FROM public.almanac_installation WHERE singleton=true").Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	agentUUID, validAgent := canonicalUUID(created.Agent.ID)
	if !validAgent {
		t.Fatal("test agent ID was not canonical")
	}
	snapshotBytes, err := buildDatasourceConfigurationSnapshot(t.Context(), dbgen.New(conn), installationID, agentUUID, latestRevision, secrets)
	var snapshot agentprotocol.ConfigurationSnapshot
	if err != nil || json.Unmarshal(snapshotBytes, &snapshot) != nil || len(snapshot.Assignments) != successfulWrites ||
		!bytes.Contains(snapshotBytes, []byte(password)) || len(snapshotBytes) > agentprotocol.MaxConfigurationBytes {
		t.Fatalf("prospective secret-bearing configuration bytes=%d assignments=%d successful=%d err=%v", len(snapshotBytes), len(snapshot.Assignments), successfulWrites, err)
	}
	if sourceCount != successfulWrites || latestRevision != int64(successfulWrites+1) || auditCount != successfulWrites || receiptCount != successfulWrites {
		t.Fatalf("over-limit secret write was not atomic: sources=%d revision=%d audit=%d receipts=%d successful=%d", sourceCount, latestRevision, auditCount, receiptCount, successfulWrites)
	}
}

func TestIntegrationDatasourceDiagnosticCapacitySerializesAcrossActors(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "diagnostic-capacity")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"connection_test"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}
	resumeVerifier := ownershipVerifier(ownershipBytes(32, 76))
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, hello, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 1, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	secondVerifier := insertDiagnosticActor(t, f, "diagnostic-capacity-second")
	proofs := [2]DatasourceProof{{SessionVerifier: f.verifier}, {SessionVerifier: secondVerifier}}
	settings := datasource.Settings{
		Name: "capacity-source", DisplayName: "Capacity source", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1",
		Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 60,
		SettlingDelaySeconds: 60, AgentID: created.Agent.ID,
	}
	begin := func(proof DatasourceProof, id string, requestedAt time.Time) (DiagnosticRecord, agentprotocol.DiagnosticRequest, error) {
		return BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
			Proof: proof, DiagnosticID: id, AgentID: created.Agent.ID, AgentGeneration: 1,
			Operation: "connection_test", ConnectionSettings: settings,
			RequestedAt: requestedAt, Now: func() time.Time { return requestedAt },
		})
	}
	for i, id := range []string{
		"1a000000-0000-4000-8000-000000000001",
		"1a000000-0000-4000-8000-000000000002",
		"1a000000-0000-4000-8000-000000000003",
	} {
		if _, _, err := begin(proofs[0], id, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed pending diagnostic %d: %v", i+1, err)
		}
	}

	// Hold contenders at the INSERT boundary after each has performed its
	// capacity read. With the per-agent lock, contender two instead waits on
	// the agent row before it can read capacity; the observer detects either
	// state before releasing the barrier.
	const barrierClassID, barrierObjectID = 706661, 20
	barrier := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(barrier)
	barrierTx, err := barrier.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(barrierTx)
	if _, err := barrierTx.Exec(t.Context(), "SELECT pg_advisory_xact_lock($1, $2)", barrierClassID, barrierObjectID); err != nil {
		t.Fatal(err)
	}
	observer := integrationConnect(t, f.databaseURL)
	if _, err := observer.Exec(t.Context(), `CREATE FUNCTION public.almanac_test_diagnostic_capacity_barrier() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.id IN ('1a000000-0000-4000-8000-000000000004'::uuid, '1a000000-0000-4000-8000-000000000005'::uuid) THEN
		PERFORM pg_advisory_xact_lock(706661, 20);
		END IF;
		RETURN NEW;
		END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Exec(t.Context(), `CREATE TRIGGER almanac_test_diagnostic_capacity_barrier BEFORE INSERT ON public.almanac_diagnostics
		FOR EACH ROW EXECUTE FUNCTION public.almanac_test_diagnostic_capacity_barrier()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = observer.Exec(context.Background(), "DROP TRIGGER IF EXISTS almanac_test_diagnostic_capacity_barrier ON public.almanac_diagnostics")
		_, _ = observer.Exec(context.Background(), "DROP FUNCTION IF EXISTS public.almanac_test_diagnostic_capacity_barrier()")
		closeIntegrationConn(observer)
	})

	type outcome struct {
		id      string
		record  DiagnosticRecord
		request agentprotocol.DiagnosticRequest
		err     error
	}
	results := make(chan outcome, 2)
	contenderIDs := []string{"1a000000-0000-4000-8000-000000000004", "1a000000-0000-4000-8000-000000000005"}
	launch := func(index int) {
		go func() {
			record, request, beginErr := begin(proofs[index], contenderIDs[index], now.Add(4*time.Second))
			results <- outcome{id: contenderIDs[index], record: record, request: request, err: beginErr}
		}()
	}
	launch(0)
	waitForDiagnosticAdmissionBarrier(t, observer, barrierClassID, barrierObjectID, false)
	launch(1)
	waitForDiagnosticAdmissionBarrier(t, observer, barrierClassID, barrierObjectID, true)
	if err := barrierTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(barrier)
	var succeeded, conflicted int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			if result.record.ID != result.id || result.request.DiagnosticID != result.id {
				t.Fatalf("successful diagnostic dispatch does not match receipt: %+v", result)
			}
		case errors.Is(result.err, ErrDiagnosticConflict):
			conflicted++
			if result.request.DiagnosticID != "" {
				t.Fatalf("capacity loser produced an agent dispatch: %+v", result.request)
			}
		default:
			t.Fatalf("concurrent diagnostic admission failed unexpectedly: %v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("capacity outcomes: succeeded=%d conflicted=%d, want one each", succeeded, conflicted)
	}
	var active int
	if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_diagnostics WHERE agent_id=$1 AND state IN ('pending','running')`, created.Agent.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != maxActiveDatasourceDiagnostics {
		t.Fatalf("active diagnostic rows=%d, want cap %d", active, maxActiveDatasourceDiagnostics)
	}
}

func TestIntegrationDatasourceDiagnosticRetentionUsesReceiptExpiry(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "diagnostic-retention")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"connection_test"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}
	resumeVerifier := ownershipVerifier(ownershipBytes(32, 77))
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, hello, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 1, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	settings := datasource.Settings{
		Name: "retention-source", DisplayName: "Retention source", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1",
		Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 60,
		SettlingDelaySeconds: 60, AgentID: created.Agent.ID,
	}
	const diagnosticID = "2a000000-0000-4000-8000-000000000001"
	if _, _, err := BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
		Proof: DatasourceProof{SessionVerifier: f.verifier}, DiagnosticID: diagnosticID, AgentID: created.Agent.ID, AgentGeneration: 1,
		Operation: "connection_test", ConnectionSettings: settings, RequestedAt: now, Now: func() time.Time { return now },
	}); err != nil {
		t.Fatalf("begin diagnostic whose completion triggers receipt pruning: %v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	for index := range 257 {
		id := fmt.Sprintf("2b000000-0000-4000-8000-%012x", index+1)
		createdAt := now.Add(-time.Duration(index+2) * time.Hour)
		if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_diagnostics
			(id,actor_id,agent_id,session_generation,operation,state,request,result,created_at,updated_at,deadline_at,expires_at)
			VALUES ($1,$2,$3,1,'connection_test','succeeded','{}','{}',$4,$4,$5,$6)`,
			id, f.userID, created.Agent.ID, timestamp(createdAt), timestamp(createdAt.Add(30*time.Second)), timestamp(now.Add(24*time.Hour))); err != nil {
			closeIntegrationConn(conn)
			t.Fatalf("seed unexpired terminal receipt %d: %v", index+1, err)
		}
	}
	expiredID := "2c000000-0000-4000-8000-000000000001"
	expiredCreated := now.Add(-48 * time.Hour)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_diagnostics
		(id,actor_id,agent_id,session_generation,operation,state,request,result,created_at,updated_at,deadline_at,expires_at)
		VALUES ($1,$2,$3,1,'connection_test','succeeded','{}','{}',$4,$4,$5,$6)`,
		expiredID, f.userID, created.Agent.ID, timestamp(expiredCreated), timestamp(expiredCreated.Add(30*time.Second)), timestamp(now.Add(-time.Second))); err != nil {
		closeIntegrationConn(conn)
		t.Fatal(err)
	}
	closeIntegrationConn(conn)

	result := agentprotocol.DiagnosticResult{DiagnosticID: diagnosticID, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
	completedAt := now.Add(time.Second)
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, result, completedAt); err != nil {
		t.Fatalf("complete diagnostic and prune expired receipts: %v", err)
	}
	if err := CompleteDatasourceDiagnostic(t.Context(), f.databaseURL, issued.ID, issued.Verifier, 1, result, completedAt.Add(time.Second)); err != nil {
		t.Fatalf("exact terminal replay after pruning: %v", err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	preserved, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, "2b000000-0000-4000-8000-000000000001", func() time.Time { return completedAt.Add(2 * time.Second) })
	if err != nil || preserved.State != "succeeded" || !preserved.ExpiresAt.After(completedAt) {
		t.Fatalf("oldest unexpired terminal receipt = %+v, %v", preserved, err)
	}
	completed, err := GetDatasourceDiagnostic(t.Context(), f.databaseURL, proof, diagnosticID, func() time.Time { return completedAt.Add(2 * time.Second) })
	if err != nil || completed.State != "succeeded" || completed.ID != diagnosticID {
		t.Fatalf("completed diagnostic read after exact replay = %+v, %v", completed, err)
	}
	conn = integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var expiredRows, terminalRows int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_diagnostics WHERE id=$1", expiredID).Scan(&expiredRows); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_diagnostics WHERE agent_id=$1 AND state NOT IN ('pending','running')", created.Agent.ID).Scan(&terminalRows); err != nil {
		t.Fatal(err)
	}
	if expiredRows != 0 || terminalRows != 258 {
		t.Fatalf("receipt retention: expired=%d terminal=%d, want expired=0 and all 258 unexpired", expiredRows, terminalRows)
	}
}

func TestIntegrationConcurrentSavedPreviewDoesNotUpgradeInstallationLock(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "diagnostic-installation-lock")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := f.base.Add(4 * time.Second)
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test-agent", Capabilities: capabilities}
	resumeVerifier := ownershipVerifier(ownershipBytes(32, 78))
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, hello, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 1, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	settings := datasource.Settings{
		Name: "lock-source", DisplayName: "Lock source", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1",
		Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 60,
		SettlingDelaySeconds: 60, AgentID: created.Agent.ID,
	}
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{
		Proof: DatasourceProof{SessionVerifier: f.verifier}, Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"},
		IdempotencyKey: "lock-source-create", RequestID: "lock-source-create", Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatalf("create uncredentialed saved datasource: %v", err)
	}
	renewedAt := grant.AcquiredAt.Add(agentMinimumRenewalInterval)
	renewed, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, 0, ownershipNonce(78), func() time.Time { return renewedAt })
	if err != nil || renewed.ConfigurationRevision != source.ConfigurationRevision {
		t.Fatalf("renew datasource configuration grant = %+v, %v", renewed, err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resumeVerifier, 1, renewed.ConfigurationRevision, func() time.Time { return renewedAt }); err != nil {
		t.Fatal(err)
	}
	now = renewedAt.Add(time.Second)
	secondVerifier := insertDiagnosticActor(t, f, "diagnostic-installation-lock-second")
	proofs := [2]DatasourceProof{{SessionVerifier: f.verifier}, {SessionVerifier: secondVerifier}}
	begin := func(proof DatasourceProof, id string) (DiagnosticRecord, agentprotocol.DiagnosticRequest, error) {
		return BeginDatasourceDiagnostic(t.Context(), f.databaseURL, BeginDiagnosticInput{
			Proof: proof, DiagnosticID: id, AgentID: created.Agent.ID, AgentGeneration: 1,
			DatasourceID: source.ID, Operation: "query_preview", Query: "vector(1)",
			RequestedAt: now, Now: func() time.Time { return now },
		})
	}
	for index, id := range []string{
		"3a000000-0000-4000-8000-000000000001",
		"3a000000-0000-4000-8000-000000000002",
		"3a000000-0000-4000-8000-000000000003",
	} {
		if _, _, err := begin(proofs[0], id); err != nil {
			t.Fatalf("seed pending saved preview %d: %v", index+1, err)
		}
		now = now.Add(time.Second)
	}

	controller := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(controller)
	controlTx, err := controller.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(controlTx)
	var lockedAgent string
	if err := controlTx.QueryRow(t.Context(), "SELECT id::text FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID).Scan(&lockedAgent); err != nil {
		t.Fatal(err)
	}
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	type outcome struct {
		id      string
		record  DiagnosticRecord
		request agentprotocol.DiagnosticRequest
		err     error
	}
	results := make(chan outcome, 2)
	ids := []string{"3a000000-0000-4000-8000-000000000004", "3a000000-0000-4000-8000-000000000005"}
	for index := range 2 {
		go func() {
			record, request, beginErr := begin(proofs[index], ids[index])
			results <- outcome{id: ids[index], record: record, request: request, err: beginErr}
		}()
	}
	waitForDiagnosticAgentRowLockWaiters(t, observer, 2)
	if err := controlTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var succeeded, conflicted int
	for range 2 {
		select {
		case result := <-results:
			switch {
			case result.err == nil:
				succeeded++
				if result.record.ID != result.id || result.request.DiagnosticID != result.id {
					t.Fatalf("successful preview dispatch does not match receipt: %+v", result)
				}
			case errors.Is(result.err, ErrDiagnosticConflict):
				conflicted++
				if result.request.DiagnosticID != "" {
					t.Fatalf("capacity loser produced an agent dispatch: %+v", result.request)
				}
			default:
				t.Fatalf("concurrent saved preview failed unexpectedly: %v", result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("saved preview admission did not complete after releasing the agent row")
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("saved preview capacity outcomes: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	var active int
	if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_diagnostics WHERE agent_id=$1 AND state IN ('pending','running')`, created.Agent.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != maxActiveDatasourceDiagnostics {
		t.Fatalf("active saved preview rows=%d, want cap %d", active, maxActiveDatasourceDiagnostics)
	}
}

func insertDiagnosticActor(t *testing.T, f agentFixture, login string) [32]byte {
	t.Helper()
	conn := integrationConnect(t, f.databaseURL)
	var userID string
	if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users
		(login,password_verifier,role,enabled,must_change_password,credential_revision,created_at,updated_at)
		VALUES ($1,$2,'administrator',true,false,$3,$4,$4) RETURNING id::text`, login, testPasswordVerifier, f.revision, f.base).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	var verifier [32]byte
	verifier[0] = 117
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{
		UserID: userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision,
		Verifier: verifier, Now: f.base.Add(3 * time.Second), RequestID: "issue-" + login,
	}); err != nil {
		t.Fatal(err)
	}
	return verifier
}

func waitForDiagnosticAdmissionBarrier(t *testing.T, observer *pgx.Conn, classID, objectID int32, includeAgentRow bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		var advisoryWaiters, agentRowWaiters int
		if err := observer.QueryRow(t.Context(), `SELECT
			(SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND objsubid=2 AND NOT granted),
			(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event_type='Lock' AND query ILIKE '%FROM public.almanac_agents%')`, classID, objectID).Scan(&advisoryWaiters, &agentRowWaiters); err != nil {
			t.Fatal(err)
		}
		if (!includeAgentRow && advisoryWaiters >= 1) || (includeAgentRow && (advisoryWaiters >= 2 || agentRowWaiters >= 1)) {
			return
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatalf("context ended waiting for diagnostic admission barrier: %v", t.Context().Err())
		}
	}
	t.Fatal("concurrent diagnostic admissions did not reach the expected database barrier")
}

func waitForDiagnosticAgentRowLockWaiters(t *testing.T, observer *pgx.Conn, expected int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		var waiting int
		if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
			AND query ILIKE '%FROM public.almanac_agents%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= expected {
			return
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatalf("context ended waiting for agent row lock contenders: %v", t.Context().Err())
		}
	}
	t.Fatalf("observed fewer than %d saved preview agent row lock waiters", expected)
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
