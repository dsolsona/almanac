//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
)

func TestIntegrationReassignmentRetryPaginationDoesNotStarveLaterMoves(t *testing.T) {
	f := newAgentFixture(t)
	source, err := CreateAgent(t.Context(), f.databaseURL, f.input("pagination-source", "pagination-source"))
	if err != nil {
		t.Fatal(err)
	}
	targetInput := f.input("pagination-target", "pagination-target")
	targetInput.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdeb"
	targetInput.CredentialVerifier = [32]byte{92}
	target, err := CreateAgent(t.Context(), f.databaseURL, targetInput)
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	now := f.base.Add(5 * time.Second)
	for i := range 51 {
		name := fmt.Sprintf("pagination-source-%d", i)
		_, err := conn.Exec(t.Context(), `WITH source AS (INSERT INTO public.almanac_datasources (id,name,display_name,provider,configuration_format,endpoint,tls,authentication_mode,polling_interval_seconds,settling_delay_seconds,agent_id,created_at,updated_at) VALUES (gen_random_uuid(),$1,$1,'prometheus','prometheus-http-v1','https://metrics.example.test','{}','none',60,60,$2,$3,$3) RETURNING id) INSERT INTO public.almanac_datasource_reassignments (datasource_id,source_agent_id,target_agent_id,requested_at,authority_expires_at,removal_revision,source_config_version,uses_v2_removal,retry_at,state,actor_id,request_id) SELECT id,$2,$4,$3,$3,1,1,false,$3,'waiting_for_previous_agent',$5,$1 FROM source`, name, source.Agent.ID, now, target.Agent.ID, f.userID)
		if err != nil {
			t.Fatal(err)
		}
	}
	q := dbgen.New(conn)
	first, err := q.ListReadyDatasourceReassignments(t.Context(), timestamp(now))
	if err != nil || len(first) != 50 {
		t.Fatalf("first page=%d err=%v", len(first), err)
	}
	for _, row := range first {
		if _, err := q.SetDatasourceReassignmentUnavailable(t.Context(), dbgen.SetDatasourceReassignmentUnavailableParams{DatasourceID: row.DatasourceID, RetryAt: timestamp(now.Add(10 * time.Second))}); err != nil {
			t.Fatal(err)
		}
	}
	second, err := q.ListReadyDatasourceReassignments(t.Context(), timestamp(now))
	if err != nil || len(second) != 1 {
		t.Fatalf("next page=%d err=%v", len(second), err)
	}
	for _, row := range first {
		if row.DatasourceID == second[0].DatasourceID {
			t.Fatal("unavailable retry starved later move")
		}
	}
}

func TestIntegrationDatasourceReassignmentWaitsForV2DrainAndPreservesEpochs(t *testing.T) {
	f := newAgentFixture(t)
	sourceCredential, sourceAgent := createOwnershipAgent(t, f, "handoff-source")
	targetCredential, targetAgent := createOwnershipAgent(t, f, "handoff-target")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	sourceResume := ownershipVerifier(ownershipBytes(32, 31))
	targetResume := ownershipVerifier(ownershipBytes(32, 32))
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: capabilities}
	sourceGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceResume, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	targetGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, 1, 1, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	targetSettings := datasource.DefaultSettings("target-metrics", "Target metrics", "https://target.example.test", targetAgent.Agent.ID)
	targetSource, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: targetSettings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "target-source", RequestID: "target-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	targetMeasurement, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: "target-sli", DisplayName: "Target SLI", DatasourceID: targetSource.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "target-sli", RequestID: "target-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, 1, targetMeasurement.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	settings := datasource.DefaultSettings("handoff-metrics", "Handoff metrics", "https://metrics.example.test", sourceAgent.Agent.ID)
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "handoff-source", RequestID: "handoff-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: "handoff-sli", DisplayName: "Handoff SLI", DatasourceID: source.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "handoff-sli", RequestID: "handoff-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	secondSettings := datasource.DefaultSettings("second-metrics", "Second metrics", "https://second.example.test", sourceAgent.Agent.ID)
	secondSource, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: secondSettings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "second-source", RequestID: "second-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	secondMeasurement, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: "second-sli", DisplayName: "Second SLI", DatasourceID: secondSource.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "second-sli", RequestID: "second-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceResume, 1, secondMeasurement.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$1, activated_at=$2 WHERE sli_id=$3`, f.base.Add(-time.Minute), now, measurement.ID); err != nil {
		t.Fatal(err)
	}
	allowed, err := json.Marshal([]agentprotocol.MeasurementGrant{{SLIID: measurement.ID, MeasurementRevision: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_execution_grants SET allowed_measurements=$1 WHERE lease_id=$2`, allowed, sourceGrant.LeaseID); err != nil {
		t.Fatal(err)
	}
	settings.AgentID = targetAgent.Agent.ID
	update := UpdateDatasourceInput{Proof: proof, DatasourceID: source.ID, ExpectedVersion: source.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "handoff-request", Now: clock}
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update); !errors.Is(err, ErrDiagnosticConfigurationPending) {
		t.Fatalf("move with source lease behind applied configuration: %v", err)
	}
	now = now.Add(10 * time.Second)
	sourceHeartbeat := &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: sourceGrant.LeaseID, AppliedConfigurationRevision: secondMeasurement.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}
	sourceGrant, err = RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceResume, sourceGrant.Generation, 0, ownershipNonce(84), sourceHeartbeat, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_execution_grants SET allowed_measurements=$1 WHERE lease_id=$2`, allowed, sourceGrant.LeaseID); err != nil {
		t.Fatal(err)
	}
	pending, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update)
	if err != nil || pending.Reassignment == nil || pending.Settings.AgentID != sourceAgent.Agent.ID {
		t.Fatalf("pending=%+v err=%v", pending.Reassignment, err)
	}
	secondSettings.AgentID = targetAgent.Agent.ID
	secondUpdate := UpdateDatasourceInput{Proof: proof, DatasourceID: secondSource.ID, ExpectedVersion: secondSource.Version, Settings: secondSettings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "second-handoff", Now: clock}
	secondPending, err := UpdateDatasource(t.Context(), f.databaseURL, nil, secondUpdate)
	if err != nil || secondPending.Reassignment == nil {
		t.Fatalf("second pending=%+v err=%v", secondPending.Reassignment, err)
	}
	secondUpdate.ExpectedVersion = secondPending.Version
	secondUpdate.Settings.AgentID = sourceAgent.Agent.ID
	secondUpdate.RequestID = "cancel-second-handoff"
	canceled, err := UpdateDatasource(t.Context(), f.databaseURL, nil, secondUpdate)
	if err != nil || canceled.Reassignment != nil || canceled.Settings.AgentID != sourceAgent.Agent.ID || canceled.Version <= secondPending.Version {
		t.Fatalf("canceled=%+v err=%v", canceled, err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceResume, sourceGrant.Generation, secondPending.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatalf("overtaken ACK: %v", err)
	}
	for _, agent := range []AgentRecord{sourceAgent.Agent, targetAgent.Agent} {
		_, err := DeleteAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: agent.ID, ExpectedVersion: agent.Version, IdempotencyKey: "delete-" + agent.Name, RequestID: "delete-" + agent.Name, Now: clock})
		if !errors.Is(err, ErrAgentInUse) {
			t.Fatalf("pending participant delete=%v", err)
		}
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil || len(notices) != 0 {
		t.Fatalf("premature completion=%v err=%v", notices, err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceResume, sourceGrant.Generation, canceled.ConfigurationRevision, agentprotocol.ProtocolV1, clock); err != nil {
		t.Fatal(err)
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil || len(notices) != 0 {
		t.Fatalf("legacy ACK completed move=%v err=%v", notices, err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, sourceResume, sourceGrant.Generation, canceled.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil || len(notices) != 0 {
		t.Fatalf("target lease behind applied configuration completed move=%v err=%v", notices, err)
	}
	now = now.Add(10 * time.Second)
	targetHeartbeat := &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: targetGrant.LeaseID, AppliedConfigurationRevision: targetMeasurement.ConfigurationRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}
	targetGrant, err = RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, targetGrant.Generation, 0, ownershipNonce(85), targetHeartbeat, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agents SET enabled=false WHERE id=$1`, targetAgent.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil || len(notices) != 0 {
		t.Fatalf("unavailable target completion=%v err=%v", notices, err)
	}
	unavailable, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || unavailable.Reassignment == nil || unavailable.Reassignment.State != "replacement_unavailable" {
		t.Fatalf("unavailable=%+v err=%v", unavailable.Reassignment, err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agents SET enabled=true WHERE id=$1`, targetAgent.Agent.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock)
	if err != nil || len(notices) != 2 {
		t.Fatalf("drained completion=%v err=%v", notices, err)
	}
	active, err := GetDatasource(t.Context(), f.databaseURL, source.ID, proof, clock)
	if err != nil || active.Reassignment != nil || active.Settings.AgentID != targetAgent.Agent.ID {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	var originalAgent string
	var count int
	if err := conn.QueryRow(t.Context(), `SELECT agent_id::text FROM public.almanac_sli_measurements WHERE sli_id=$1`, measurement.ID).Scan(&originalAgent); err != nil || originalAgent != sourceAgent.Agent.ID {
		t.Fatal("measurement identity was rewritten")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_ownership_epochs WHERE sli_id=$1`, measurement.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("ownership epochs=%d err=%v", count, err)
	}
	grants, err := dbgen.New(conn).ListAuthorizedMeasurementGrantsForAgent(t.Context(), dbgen.ListAuthorizedMeasurementGrantsForAgentParams{AgentID: mustPGUUID(targetAgent.Agent.ID), ConfigurationRevision: targetMeasurement.ConfigurationRevision})
	if err != nil || len(grants) != 1 || grants[0].SliID != mustPGUUID(targetMeasurement.ID) {
		t.Fatalf("old target grant must retain unrelated SLI only: %+v err=%v", grants, err)
	}
	var cutoff time.Time
	if err := conn.QueryRow(t.Context(), `SELECT ends_at FROM public.almanac_sli_ownership_epochs WHERE sli_id=$1 AND agent_id=$2`, measurement.ID, sourceAgent.Agent.ID).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	now = cutoff.Add(2 * time.Minute)
	item := sli.ObservationItem{DeliveryID: uuid.NewString(), ExecutionID: uuid.NewString(), CreatedAt: now, LeaseID: sourceGrant.LeaseID, ConfigurationRevision: fmt.Sprint(sourceGrant.ConfigurationRevision), SessionGeneration: "1", SLIID: measurement.ID, MeasurementRevision: "1", IntervalStart: f.base.Add(-time.Minute), IntervalEnd: f.base, Outcome: sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: "query"}}
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, item, clock); err != nil || result.Status != "accepted" {
		t.Fatalf("buffered pre-cutoff observation: %+v err=%v", result, err)
	}
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.IntervalStart, item.IntervalEnd = cutoff, cutoff.Add(time.Minute)
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, item, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("old producer after cutoff: %+v err=%v", result, err)
	}
	var targetRevision int64
	if err := conn.QueryRow(t.Context(), `SELECT max(revision) FROM public.almanac_agent_configurations WHERE agent_id=$1`, targetAgent.Agent.ID).Scan(&targetRevision); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, targetGrant.Generation, hello, clock)
	if err != nil || resumed.ConfigurationRevision != targetGrant.ConfigurationRevision {
		t.Fatalf("target resumed old grant=%+v err=%v", resumed, err)
	}
	var desired int64
	if err := conn.QueryRow(t.Context(), `SELECT desired_configuration_revision FROM public.almanac_agent_runtime_status WHERE agent_id=$1`, targetAgent.Agent.ID).Scan(&desired); err != nil || desired != targetRevision {
		t.Fatalf("desired revision after reconnect=%d want=%d err=%v", desired, targetRevision, err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, targetGrant.Generation, targetRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatalf("target configuration ACK: %v", err)
	}
	heartbeat := &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: targetGrant.LeaseID, AppliedConfigurationRevision: targetRevision, SchedulerHealth: "ready", CollectionProgress: "idle"}
	renewedTarget, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, targetGrant.Generation, 1, ownershipNonce(83), heartbeat, clock)
	if err != nil || renewedTarget.ConfigurationRevision != targetRevision || !hasMeasurementGrantJSON(renewedTarget.AllowedMeasurements, measurement.ID) {
		t.Fatalf("target renewed grant=%+v err=%v", renewedTarget, err)
	}
	item.LeaseID = renewedTarget.LeaseID
	item.ConfigurationRevision = fmt.Sprint(targetRevision)
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.IntervalStart, item.IntervalEnd = f.base.Add(-time.Minute), f.base
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, item, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("replacement producer before cutoff: %+v err=%v", result, err)
	}
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.IntervalStart, item.IntervalEnd = cutoff, cutoff.Add(time.Minute)
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, item, clock); err != nil || result.Status != "accepted" {
		t.Fatalf("replacement producer at cutoff: %+v err=%v", result, err)
	}
}

func hasMeasurementGrantJSON(encoded []byte, id string) bool {
	var grants []agentprotocol.MeasurementGrant
	if json.Unmarshal(encoded, &grants) != nil {
		return false
	}
	for _, grant := range grants {
		if grant.SLIID == id && grant.MeasurementRevision == "1" {
			return true
		}
	}
	return false
}

func TestIntegrationLegacyReassignmentWaitsForRecordedExpiry(t *testing.T) {
	f := newAgentFixture(t)
	_, source := createOwnershipAgent(t, f, "legacy-source")
	targetCredential, target := createOwnershipAgent(t, f, "legacy-target")
	base := f.base.Add(5 * time.Second)
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, ownershipVerifier(ownershipBytes(32, 62)), &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "legacy", Capabilities: capabilities}, func() time.Time { return base }); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, ownershipVerifier(ownershipBytes(32, 62)), 1, 1, func() time.Time { return base }); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	datasourceID := "a2345678-1234-4123-8123-123456789012"
	sliID := "a2345678-1234-4123-8123-123456789013"
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_datasources (id,name,display_name,provider,configuration_format,endpoint,tls,authentication_mode,polling_interval_seconds,settling_delay_seconds,agent_id,created_at,updated_at) VALUES ($1,'legacy-source-metrics','Legacy source','prometheus','prometheus-http-v1','https://metrics.example.test','{}','none',60,60,$2,$3,$3)`, datasourceID, source.Agent.ID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slis (id,name,display_name,created_at,updated_at) VALUES ($1,'legacy-sli','Legacy SLI',$2,$2)`, sliID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_sli_measurements (sli_id,revision,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,activated_at,created_at) VALUES ($1,1,$2,$3,'event_counts','interval_count','good','total',$4,$5,$5)`, sliID, datasourceID, source.Agent.ID, base.Add(-time.Minute), base); err != nil {
		t.Fatal(err)
	}
	deadline := base.Add(time.Hour)
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_datasource_reassignments (datasource_id,source_agent_id,target_agent_id,requested_at,authority_expires_at,removal_revision,source_config_version,uses_v2_removal,retry_at,state,actor_id,request_id) VALUES ($1,$2,$3,$4,$5,1,1,false,$4,'waiting_for_previous_agent',$6,'legacy-handoff')`, datasourceID, source.Agent.ID, target.Agent.ID, base, deadline, f.userID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, func() time.Time { return deadline.Add(-time.Microsecond) }); err != nil || len(notices) != 0 {
		t.Fatalf("early legacy handoff=%v err=%v", notices, err)
	}
	notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, func() time.Time { return deadline })
	if err != nil || len(notices) != 2 {
		t.Fatalf("expired legacy handoff=%v err=%v", notices, err)
	}
	var agentID string
	if err := conn.QueryRow(t.Context(), `SELECT agent_id::text FROM public.almanac_datasources WHERE id=$1`, datasourceID).Scan(&agentID); err != nil || agentID != target.Agent.ID {
		t.Fatalf("assignment=%s err=%v", agentID, err)
	}
}

func TestIntegrationPendingTargetAndDeleteSerialize(t *testing.T) {
	f := newAgentFixture(t)
	sourceCredential, source := createOwnershipAgent(t, f, "race-source")
	targetCredential, target := createOwnershipAgent(t, f, "race-target")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, ownershipVerifier(ownershipBytes(32, 81)), &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, clock); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, ownershipVerifier(ownershipBytes(32, 82)), &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, clock); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	settings := datasource.DefaultSettings("race-metrics", "Race metrics", "https://metrics.example.test", source.Agent.ID)
	record, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "race-source", RequestID: "race-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: "race-sli", DisplayName: "Race SLI", DatasourceID: record.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "race-sli", RequestID: "race-sli", Now: clock}); err != nil {
		t.Fatal(err)
	}
	settings.AgentID = target.Agent.ID
	update := UpdateDatasourceInput{Proof: proof, DatasourceID: record.ID, ExpectedVersion: record.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "race-move", Now: clock}
	deletion := DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: target.Agent.ID, ExpectedVersion: target.Agent.Version, IdempotencyKey: "race-delete", RequestID: "race-delete", Now: clock}
	start, moved, deleted := make(chan struct{}), make(chan error, 1), make(chan error, 1)
	go func() { <-start; _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update); moved <- err }()
	go func() { <-start; _, err := DeleteAgent(t.Context(), f.databaseURL, deletion); deleted <- err }()
	close(start)
	moveErr, deleteErr := <-moved, <-deleted
	if !((moveErr == nil && errors.Is(deleteErr, ErrAgentInUse)) || (deleteErr == nil && errors.Is(moveErr, ErrAgentNotFound))) {
		t.Fatalf("move=%v delete=%v", moveErr, deleteErr)
	}
}

func TestIntegrationOfflineV2WithUnappliedConfigurationUsesExpiryHandoff(t *testing.T) {
	f := newAgentFixture(t)
	sourceCredential, source := createOwnershipAgent(t, f, "offline-v2-source")
	targetCredential, target := createOwnershipAgent(t, f, "offline-v2-target")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	capabilities := []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["collect","connection_test","query_preview"]}]`)
	sourceGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, sourceCredential.ID, sourceCredential.Verifier, ownershipVerifier(ownershipBytes(32, 86)), &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: capabilities}, clock)
	if err != nil {
		t.Fatal(err)
	}
	targetResume := ownershipVerifier(ownershipBytes(32, 87))
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, clock); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, 1, 1, clock); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	settings := datasource.DefaultSettings("offline-v2-metrics", "Offline metrics", "https://metrics.example.test", source.Agent.ID)
	record, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "offline-v2-source", RequestID: "offline-v2-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: "offline-v2-sli", DisplayName: "Offline SLI", DatasourceID: record.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "offline-v2-sli", RequestID: "offline-v2-sli", Now: clock}); err != nil {
		t.Fatal(err)
	}
	settings.AgentID = target.Agent.ID
	update := UpdateDatasourceInput{Proof: proof, DatasourceID: record.ID, ExpectedVersion: record.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "offline-v2-move", Now: clock}
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update); !errors.Is(err, ErrDiagnosticConfigurationPending) {
		t.Fatalf("live unapplied configuration must wait: %v", err)
	}
	now = sourceGrant.ExpiresAt.Add(time.Second)
	if _, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("idle session after lease expiry: %v", err)
	}
	var freshVerifier [32]byte
	freshVerifier[0] = 88
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: freshVerifier, Now: now, RequestID: "offline-v2-sign-in"}); err != nil {
		t.Fatal(err)
	}
	proof.SessionVerifier = freshVerifier
	update.Proof = proof
	pending, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update)
	if err != nil || pending.Reassignment == nil {
		t.Fatalf("offline move=%+v err=%v", pending.Reassignment, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var usesV2 bool
	if err := conn.QueryRow(t.Context(), `SELECT uses_v2_removal FROM public.almanac_datasource_reassignments WHERE datasource_id=$1`, record.ID).Scan(&usesV2); err != nil || usesV2 {
		t.Fatalf("offline removal must use expiry path: v2=%v err=%v", usesV2, err)
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil || len(notices) != 2 {
		t.Fatalf("offline expiry completion=%v err=%v", notices, err)
	}
	active, err := GetDatasource(t.Context(), f.databaseURL, record.ID, proof, clock)
	if err != nil || active.Reassignment != nil || active.Settings.AgentID != target.Agent.ID {
		t.Fatalf("offline replacement=%+v err=%v", active, err)
	}
}
