//go:build integration

package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
)

func TestIntegrationDatasourceRecoveryDoesNotWaitForSource(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	sourceCredential, sourceAgent := createOwnershipAgent(t, f, "recovery-source")
	targetCredential, targetAgent := createOwnershipAgent(t, f, "recovery-target")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["collect","connection_test","query_preview"]}]`)}
	sourceProof := ownershipVerifier(ownershipBytes(32, 121))
	targetProof := ownershipVerifier(ownershipBytes(32, 122))
	sourceGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceProof, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	targetGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	settings := datasource.DefaultSettings("recovery-metrics", "Recovery metrics", "https://metrics.example.test", sourceAgent.Agent.ID)
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "recovery-source", RequestID: "recovery-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: "recovery-sli", DisplayName: "Recovery SLI", DatasourceID: source.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "recovery-sli", RequestID: "recovery-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceProof, sourceGrant.Generation, measurement.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	sourceGrant, err = RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceProof, sourceGrant.Generation, 0, ownershipNonce(125), &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: sourceGrant.LeaseID, AppliedConfigurationRevision: measurement.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	oldItem := sli.ObservationItem{DeliveryID: uuid.New().String(), ExecutionID: uuid.New().String(), CreatedAt: now, LeaseID: sourceGrant.LeaseID, ConfigurationRevision: fmt.Sprint(sourceGrant.ConfigurationRevision), SessionGeneration: fmt.Sprint(sourceGrant.Generation), SLIID: measurement.ID, MeasurementRevision: "1", IntervalStart: f.base.Add(-2 * time.Minute), IntervalEnd: f.base.Add(-time.Minute), Outcome: sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, oldItem, clock); err != nil || result.Status != "accepted" {
		t.Fatalf("original observation: %+v err=%v", result, err)
	}
	if !now.Before(sourceGrant.ExpiresAt) {
		t.Fatal("fixture must retain the lost agent's unexpired lease")
	}
	settings.AgentID = targetAgent.Agent.ID
	moved, err := UpdateDatasource(t.Context(), f.databaseURL, nil, UpdateDatasourceInput{Proof: proof, DatasourceID: source.ID, ExpectedVersion: source.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "recovery-move", Now: clock})
	if err != nil || moved.Settings.AgentID != targetAgent.Agent.ID || moved.Reassignment == nil {
		t.Fatalf("replacement must be assigned before old lease expiry: agent=%s pending=%+v err=%v", moved.Settings.AgentID, moved.Reassignment, err)
	}
	if moved.PreviousAgentID != sourceAgent.Agent.ID || moved.ConfigurationRevision <= targetGrant.ConfigurationRevision || moved.PreviousConfigurationRevision <= sourceGrant.ConfigurationRevision {
		t.Fatal("both configurations must advance atomically")
	}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, oldItem, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("old receipt replay must be fenced: %+v err=%v", result, err)
	}
	oldItem.DeliveryID, oldItem.ExecutionID = uuid.New().String(), uuid.New().String()
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, oldItem, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("old buffered interval must be fenced: %+v err=%v", result, err)
	}
	sourceConfig, err := ReadCurrentAgentConfiguration(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, clock)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := agentprotocol.DecodeConfigurationSnapshot(sourceConfig.Snapshot)
	if err != nil || len(decoded.Assignments) != 0 || len(decoded.SLIs) != 0 {
		t.Fatalf("source reconnect configuration retains datasource: %+v err=%v", decoded, err)
	}
	epochConn := integrationConnect(t, f.databaseURL)
	var startsAt time.Time
	err = epochConn.QueryRow(t.Context(), `SELECT starts_at FROM public.almanac_sli_ownership_epochs WHERE sli_id=$1 AND ends_at IS NULL`, measurement.ID).Scan(&startsAt)
	closeIntegrationConn(epochConn)
	if err != nil || !startsAt.Equal(f.base.Add(time.Minute)) {
		t.Fatalf("replacement starts at the next boundary: %v err=%v", startsAt, err)
	}
	// The lost source never applies or acknowledges its removal configuration.
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, targetGrant.Generation, moved.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	acknowledged, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || acknowledged.Reassignment != nil || acknowledged.Version != moved.Version {
		t.Fatalf("ACK must complete immediately without another configuration version: %+v err=%v", acknowledged, err)
	}
	now = now.Add(10 * time.Second)
	targetGrant, err = RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, targetGrant.Generation, 0, ownershipNonce(126), &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: targetGrant.LeaseID, AppliedConfigurationRevision: moved.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil {
		t.Fatal(err)
	}
	pending, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || pending.Reassignment != nil {
		t.Fatalf("configuration ACK completes before collection: %+v err=%v", pending.Reassignment, err)
	}
	now = f.base.Add(3 * time.Minute)
	renewed, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, targetGrant.Generation, 1, ownershipNonce(123), &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: targetGrant.LeaseID, AppliedConfigurationRevision: moved.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}, clock)
	if err != nil || !hasMeasurementGrantJSON(renewed.AllowedMeasurements, measurement.ID) {
		t.Fatalf("replacement grant: %+v err=%v", renewed, err)
	}
	item := sli.ObservationItem{DeliveryID: uuid.New().String(), ExecutionID: uuid.New().String(), CreatedAt: now, LeaseID: renewed.LeaseID, ConfigurationRevision: fmt.Sprint(moved.ConfigurationRevision), SessionGeneration: fmt.Sprint(renewed.Generation), SLIID: measurement.ID, MeasurementRevision: "1", IntervalStart: f.base.Add(time.Minute), IntervalEnd: f.base.Add(2 * time.Minute), Outcome: sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}}
	failed := item
	failed.DeliveryID, failed.ExecutionID = uuid.New().String(), uuid.New().String()
	failed.Outcome = sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: "query"}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, failed, clock); err != nil || result.Status != "accepted" {
		t.Fatalf("failed attempt: %+v err=%v", result, err)
	}
	stillPending, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || stillPending.Reassignment != nil {
		t.Fatalf("collection failure must not reopen configuration update: %+v err=%v", stillPending, err)
	}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, item, clock); err != nil || result.Status != "accepted" {
		t.Fatalf("replacement result: %+v err=%v", result, err)
	}
	active, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || active.Reassignment != nil || active.Version != pending.Version {
		t.Fatalf("collection must preserve completed configuration: %+v err=%v", active.Reassignment, err)
	}
	item.LeaseID, item.ConfigurationRevision, item.SessionGeneration = sourceGrant.LeaseID, fmt.Sprint(sourceGrant.ConfigurationRevision), fmt.Sprint(sourceGrant.Generation)
	item.IntervalStart, item.IntervalEnd = f.base.Add(-time.Minute), f.base
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, item, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("old buffered delivery must be fenced: %+v err=%v", result, err)
	}
	now = now.Add(time.Second)
	setIntegrationUserRole(t, f, "editor")
	if _, err := ListAgents(t.Context(), f.databaseURL, ListAgentsInput{SessionVerifier: f.verifier, Limit: 20, Now: clock}); err != nil {
		t.Fatalf("editor agent choices: %v", err)
	}
	if _, err := ListAgentCredentials(t.Context(), f.databaseURL, ListAgentCredentialsInput{SessionVerifier: f.verifier, AgentID: sourceAgent.Agent.ID, Limit: 20, Now: clock}); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("editor credential read: %v", err)
	}
	settings.AgentID = sourceAgent.Agent.ID
	update := UpdateDatasourceInput{Proof: proof, DatasourceID: source.ID, ExpectedVersion: active.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "recovery-return", Now: clock}
	bad := update
	bad.Settings.Endpoint = "https://unauthorized.example.test"
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, bad); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("editor connection edit: %v", err)
	}
	returned, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update)
	if err != nil || returned.Settings.AgentID != sourceAgent.Agent.ID {
		t.Fatalf("editor return assignment: %+v err=%v", returned, err)
	}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, oldItem, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("returning to an agent must not revive old grants: %+v err=%v", result, err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceProof, sourceGrant.Generation, returned.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	returnGrant, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceProof, sourceGrant.Generation, 1, ownershipNonce(128), &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: sourceGrant.LeaseID, AppliedConfigurationRevision: returned.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	rebound := oldItem
	rebound.DeliveryID, rebound.ExecutionID = uuid.New().String(), uuid.New().String()
	rebound.CreatedAt, rebound.LeaseID, rebound.ConfigurationRevision = now, returnGrant.LeaseID, fmt.Sprint(returnGrant.ConfigurationRevision)
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, rebound, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("fresh grant must not admit a prior ownership interval or falsely complete recovery: %+v err=%v", result, err)
	}
	update.ExpectedVersion, update.Settings.AgentID, update.RequestID = returned.Version, targetAgent.Agent.ID, "recovery-return-again"
	setIntegrationUserRole(t, f, "viewer")
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer assignment: %v", err)
	}
	setIntegrationUserRole(t, f, "editor")
	again, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update)
	if err != nil || again.Settings.AgentID != targetAgent.Agent.ID {
		t.Fatalf("second move before the boundary: %+v err=%v", again, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agents SET enabled=false WHERE id=$1`, targetAgent.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil {
		t.Fatal(err)
	}
	unavailable, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || unavailable.Reassignment == nil || unavailable.Reassignment.State != "replacement_unavailable" {
		t.Fatalf("failed replacement: %+v err=%v", unavailable, err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agents SET enabled=true WHERE id=$1`, targetAgent.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, targetGrant.Generation, again.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	now = f.base.Add(6 * time.Minute)
	grant, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, targetGrant.Generation, 2, ownershipNonce(127), &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: renewed.LeaseID, AppliedConfigurationRevision: again.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	item.DeliveryID, item.ExecutionID = uuid.New().String(), uuid.New().String()
	item.CreatedAt, item.LeaseID, item.ConfigurationRevision = now, grant.LeaseID, fmt.Sprint(grant.ConfigurationRevision)
	item.IntervalStart, item.IntervalEnd = f.base.Add(4*time.Minute), f.base.Add(5*time.Minute)
	item.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "0", TotalCount: "0"}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, item, clock); err != nil || result.Status != "accepted" {
		t.Fatalf("zero traffic proves replacement collection: %+v err=%v", result, err)
	}
	completed, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || completed.Reassignment != nil {
		t.Fatalf("recovered replacement: %+v err=%v", completed, err)
	}
	var observations, revision int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_coverage WHERE sli_id=$1`, measurement.ID).Scan(&observations); err != nil || observations != 3 {
		t.Fatalf("preserved coverage: %d err=%v", observations, err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT measurement_revision FROM public.almanac_slis WHERE id=$1`, measurement.ID).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("measurement revision: %d err=%v", revision, err)
	}
	setIntegrationUserRole(t, f, "administrator")
	adminProof := f.input("unused", "unused").Proof
	adminProof.VerifiedAt = now
	if _, err := DeleteAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: adminProof, AgentID: sourceAgent.Agent.ID, ExpectedVersion: sourceAgent.Agent.Version, IdempotencyKey: "delete-recovered-source", RequestID: "delete-recovered-source", Now: clock}); err != nil {
		t.Fatalf("old unassigned agent must not remain reserved by move history: %v", err)
	}

}

func TestIntegrationPendingDatasourceMoveCancelsWithoutReversePendingMove(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	sourceCredential, sourceAgent := createOwnershipAgent(t, f, "cancel-source")
	targetCredential, targetAgent := createOwnershipAgent(t, f, "cancel-target")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["connection_test","query_preview"]}]`)}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, ownershipVerifier(ownershipBytes(32, 141)), hello, clock); err != nil {
		t.Fatal(err)
	}
	targetGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, ownershipVerifier(ownershipBytes(32, 142)), hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	settings := datasource.DefaultSettings("cancel-metrics", "Cancel metrics", "https://metrics.example.test", sourceAgent.Agent.ID)
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "cancel-metrics", RequestID: "cancel-metrics", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	// Both agents are offline and neither can acknowledge a new configuration.
	now = now.Add(time.Minute)
	settings.AgentID = targetAgent.Agent.ID
	input := UpdateDatasourceInput{Proof: proof, DatasourceID: source.ID, ExpectedVersion: source.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "cancel-move", Now: clock}
	moved, err := UpdateDatasource(t.Context(), f.databaseURL, nil, input)
	if err != nil || moved.Reassignment == nil {
		t.Fatalf("pending move: %+v err=%v", moved, err)
	}
	input.ExpectedVersion, input.Settings.AgentID, input.RequestID = moved.Version, sourceAgent.Agent.ID, "cancel-return"
	input.CancelReassignment = true
	canceled, err := UpdateDatasource(t.Context(), f.databaseURL, nil, input)
	if err != nil || canceled.Settings.AgentID != sourceAgent.Agent.ID || canceled.Reassignment != nil {
		t.Fatalf("cancellation must finish without a reverse pending operation: %+v err=%v", canceled, err)
	}
	if canceled.Version != moved.Version+1 || canceled.ConfigurationRevision <= moved.PreviousConfigurationRevision || canceled.PreviousConfigurationRevision <= moved.ConfigurationRevision {
		t.Fatalf("cancellation must publish fresh configurations for both agents: %+v", canceled)
	}
	if _, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil {
		t.Fatal(err)
	}
	current, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || current.Reassignment != nil {
		t.Fatalf("offline restored agent must not reopen cancellation: %+v err=%v", current, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var floor, audits int64
	if err := conn.QueryRow(t.Context(), `SELECT assignment_configuration_revision FROM almanac_datasources WHERE id=$1`, source.ID).Scan(&floor); err != nil || floor != canceled.ConfigurationRevision {
		t.Fatalf("restored assignment must keep its fresh authorization floor: %d err=%v", floor, err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_audit_events WHERE subject_id=$1 AND action='datasource.reassignment.canceled'`, source.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("cancellation audit: %d err=%v", audits, err)
	}
	// An ACK can finish the move before the page learns it; cancellation must not turn into a new move.
	input.ExpectedVersion, input.Settings.AgentID, input.CancelReassignment = canceled.Version, targetAgent.Agent.ID, false
	completed, err := UpdateDatasource(t.Context(), f.databaseURL, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, ownershipVerifier(ownershipBytes(32, 142)), targetGrant.Generation, completed.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	input.ExpectedVersion, input.Settings.AgentID, input.CancelReassignment = completed.Version, sourceAgent.Agent.ID, true
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, input); !errors.Is(err, ErrDatasourceConflict) {
		t.Fatalf("stale cancellation must conflict without a reverse move: %v", err)
	}
	unchanged, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || unchanged.Version != completed.Version || unchanged.Settings.AgentID != targetAgent.Agent.ID || unchanged.Reassignment != nil {
		t.Fatalf("stale cancellation changed the completed assignment: %+v err=%v", unchanged, err)
	}

}

func TestIntegrationEmptyDatasourceMoveCompletesOnReplacementAcknowledgement(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	sourceCredential, sourceAgent := createOwnershipAgent(t, f, "empty-source")
	targetCredential, targetAgent := createOwnershipAgent(t, f, "empty-target")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["connection_test","query_preview"]}]`)}
	sourceProof, targetProof := ownershipVerifier(ownershipBytes(32, 131)), ownershipVerifier(ownershipBytes(32, 132))
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceProof, hello, clock); err != nil {
		t.Fatal(err)
	}
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	settings := datasource.DefaultSettings("empty-metrics", "Empty metrics", "https://metrics.example.test", sourceAgent.Agent.ID)
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "empty-metrics", RequestID: "empty-metrics", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	settings.AgentID = targetAgent.Agent.ID
	moved, err := UpdateDatasource(t.Context(), f.databaseURL, nil, UpdateDatasourceInput{Proof: proof, DatasourceID: source.ID, ExpectedVersion: source.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "move-empty", Now: clock})
	if err != nil || moved.Reassignment == nil || moved.Settings.AgentID != targetAgent.Agent.ID {
		t.Fatalf("empty move: %+v err=%v", moved, err)
	}
	if _, err := DeleteAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: sourceAgent.Agent.ID, ExpectedVersion: sourceAgent.Agent.Version, IdempotencyKey: "delete-empty-source", RequestID: "delete-empty-source", Now: clock}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil {
		t.Fatal(err)
	}
	pending, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || pending.Reassignment == nil {
		t.Fatal("publishing configuration must not complete empty move")
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, grant.Generation, moved.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if _, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetProof, grant.Generation, 0, ownershipNonce(133), &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: grant.LeaseID, AppliedConfigurationRevision: moved.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}, clock); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil {
		t.Fatal(err)
	}
	complete, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || complete.Reassignment != nil {
		t.Fatalf("acknowledged empty move: %+v err=%v", complete, err)
	}
}

func TestIntegrationInitialConfigurationRefreshDoesNotWaitForHeartbeat(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	credential, agent := createOwnershipAgent(t, f, "starting-agent")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	resume := ownershipVerifier(ownershipBytes(32, 141))
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["connection_test","query_preview"]}]`)}
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, credential.ID, credential.Verifier, resume, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	heartbeat := &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: grant.LeaseID, SchedulerHealth: "starting", CollectionProgress: "not_started"}
	if _, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, credential.ID, credential.Verifier, resume, grant.Generation, 0, ownershipNonce(142), heartbeat, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("unchanged initial revision renewed early: %v", err)
	}
	// Publish configuration after Welcome and before the initial configuration fetch.
	proof := DatasourceProof{SessionVerifier: f.verifier}
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: datasource.DefaultSettings("startup-metrics", "Startup metrics", "https://metrics.example.test", agent.Agent.ID), Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "startup-metrics", RequestID: "startup-metrics", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	config, err := ReadCurrentAgentConfiguration(t.Context(), f.databaseURL, credential.ID, credential.Verifier, clock)
	if err != nil || !config.Pending {
		t.Fatalf("new configuration must await its matching grant: %+v err=%v", config, err)
	}
	refreshed, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, credential.ID, credential.Verifier, resume, grant.Generation, 0, ownershipNonce(142), heartbeat, clock)
	if err != nil || refreshed.ConfigurationRevision != source.ConfigurationRevision {
		t.Fatalf("initial refresh must not wait ten seconds: %+v err=%v", refreshed, err)
	}
	config, err = ReadCurrentAgentConfiguration(t.Context(), f.databaseURL, credential.ID, credential.Verifier, clock)
	if err != nil || config.Pending || config.Revision != refreshed.ConfigurationRevision {
		t.Fatalf("refreshed configuration: %+v err=%v", config, err)
	}
	replay, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, credential.ID, credential.Verifier, resume, grant.Generation, 0, ownershipNonce(142), heartbeat, clock)
	if err != nil || replay.LeaseID != refreshed.LeaseID {
		t.Fatalf("refresh replay: %+v err=%v", replay, err)
	}
	heartbeat.LeaseID = refreshed.LeaseID
	if _, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, credential.ID, credential.Verifier, resume, grant.Generation, 1, ownershipNonce(143), heartbeat, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("same revision repeatedly renewed early: %v", err)
	}
}
