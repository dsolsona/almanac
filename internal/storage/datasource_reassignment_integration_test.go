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
)

func TestIntegrationReassignmentRetryPaginationDoesNotStarveLaterMoves(t *testing.T) {
	t.Parallel()
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
	if err := q.SetAgentHandoffConfiguration(t.Context(), dbgen.SetAgentHandoffConfigurationParams{AgentID: mustPGUUID(source.Agent.ID), Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteExpiredSourceHandoffConfiguration(t.Context(), mustPGUUID(source.Agent.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetAgentHandoffConfiguration(t.Context(), mustPGUUID(source.Agent.ID)); err != nil {
		t.Fatalf("pending moves lost marker: %v", err)
	}
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
	if _, err := conn.Exec(t.Context(), `DELETE FROM public.almanac_datasource_reassignments WHERE source_agent_id=$1`, source.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteExpiredSourceHandoffConfiguration(t.Context(), mustPGUUID(source.Agent.ID)); err != nil {
		t.Fatal(err)
	}
	if guarded, err := q.AgentHasPendingReassignments(t.Context(), mustPGUUID(source.Agent.ID)); err != nil || guarded {
		t.Fatalf("finished source still guarded: %v, %v", guarded, err)
	}
}

func TestIntegrationNewGenerationDoesNotInheritTransitionLease(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	credential, agent := createOwnershipAgent(t, f, "generation-transition")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: []byte(`[]`)}
	oldProof := ownershipVerifier(ownershipBytes(32, 101))
	old, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, credential.ID, credential.Verifier, oldProof, hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationAppliedProtocol(t.Context(), f.databaseURL, credential.ID, credential.Verifier, oldProof, old.Generation, old.ConfigurationRevision, agentprotocol.ProtocolV2, clock); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	q := dbgen.New(conn)
	revision, err := advanceAgentConfiguration(t.Context(), q, mustPGUUID(agent.Agent.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetAgentHandoffConfiguration(t.Context(), dbgen.SetAgentHandoffConfigurationParams{AgentID: mustPGUUID(agent.Agent.ID), Revision: revision}); err != nil {
		t.Fatal(err)
	}
	now = old.ExpiresAt.Add(time.Second)
	newProof := ownershipVerifier(ownershipBytes(32, 102))
	current, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, credential.ID, credential.Verifier, newProof, hello, clock)
	if err != nil || current.Generation <= old.Generation || current.ConfigurationRevision != revision {
		t.Fatalf("new generation grant=%+v desired=%d err=%v", current, revision, err)
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

func TestIntegrationLegacyReassignmentAdoptsImmediateTransfer(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"adopt", "cancel", "cancel-explicit"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
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
			if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_sli_measurements (sli_id,revision,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,activated_at,created_at) VALUES ($1,1,$2,$3,'event_counts','interval_count','good','total',$4,$5,$5)`, sliID, datasourceID, source.Agent.ID, f.base.Add(-time.Minute), base); err != nil {
				t.Fatal(err)
			}
			deadline := base.Add(time.Hour)
			if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_datasource_reassignments (datasource_id,source_agent_id,target_agent_id,requested_at,authority_expires_at,removal_revision,source_config_version,uses_v2_removal,retry_at,state,actor_id,request_id) VALUES ($1,$2,$3,$4,$5,1,1,false,$4,'waiting_for_previous_agent',$6,'legacy-handoff')`, datasourceID, source.Agent.ID, target.Agent.ID, base, deadline, f.userID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if mode != "adopt" {
				if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agents SET enabled=false WHERE id=$1`, target.Agent.ID); err != nil {
					t.Fatal(err)
				}
				setIntegrationUserRole(t, f, "editor")
				proof := DatasourceProof{SessionVerifier: f.verifier}
				before, err := GetDatasource(t.Context(), f.databaseURL, datasourceID, proof, func() time.Time { return base })
				if err != nil {
					t.Fatal(err)
				}
				canceled, err := UpdateDatasource(t.Context(), f.databaseURL, nil, UpdateDatasourceInput{Proof: proof, DatasourceID: datasourceID, ExpectedVersion: before.Version, Settings: before.Settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, CancelReassignment: mode == "cancel-explicit", RequestID: "cancel-legacy", Now: func() time.Time { return base }})
				if err != nil || canceled.Reassignment != nil || canceled.Settings.AgentID != source.Agent.ID || canceled.Version != before.Version+1 || canceled.ConfigurationRevision <= 1 || canceled.PreviousAgentID != target.Agent.ID || canceled.PreviousConfigurationRevision <= 1 {
					t.Fatalf("legacy cancellation: %+v err=%v", canceled, err)
				}
				var effective time.Time
				var revision int64
				if err := conn.QueryRow(t.Context(), `SELECT revision,effective_from FROM public.almanac_sli_measurements WHERE sli_id=$1`, sliID).Scan(&revision, &effective); err != nil || revision != 1 || !effective.Equal(f.base.Add(-time.Minute)) {
					t.Fatalf("cancellation rewrote measurement: %d %v err=%v", revision, effective, err)
				}
				return
			}
			if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, func() time.Time { return base }); err != nil || len(notices) != 2 {
				t.Fatalf("immediate legacy handoff=%v err=%v", notices, err)
			}
			notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, func() time.Time { return base.Add(10 * time.Second) })
			if err != nil || len(notices) != 0 {
				t.Fatalf("expired legacy handoff=%v err=%v", notices, err)
			}
			var agentID string
			if err := conn.QueryRow(t.Context(), `SELECT agent_id::text FROM public.almanac_datasources WHERE id=$1`, datasourceID).Scan(&agentID); err != nil || agentID != target.Agent.ID {
				t.Fatalf("assignment=%s err=%v", agentID, err)
			}
		})
	}
}

func TestIntegrationPendingTargetAndDeleteSerialize(t *testing.T) {
	t.Parallel()
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

func TestIntegrationOfflineV2WithUnappliedConfigurationTransfersImmediately(t *testing.T) {
	t.Parallel()
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
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, targetCredential.ID, targetCredential.Verifier, targetResume, &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["collect"]}]`)}, clock); err != nil {
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
	// Old releases could finish a move while its ownership boundary was two cadences ahead.
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var sliID string
	if err := conn.QueryRow(t.Context(), `SELECT sli_id::text FROM public.almanac_sli_measurements WHERE datasource_id=$1`, record.ID).Scan(&sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET agent_id=$2,effective_from=$3,activated_at=$4 WHERE sli_id=$1`, sliID, target.Agent.ID, f.base.Add(-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_ownership_epochs(sli_id,measurement_revision,agent_id,starts_at,ends_at,configuration_revision) VALUES($1,1,$2,$3,$4,1),($1,1,$5,$4,NULL,1)`, sliID, target.Agent.ID, f.base.Add(-time.Minute), f.base.Add(2*time.Minute), source.Agent.ID); err != nil {
		t.Fatal(err)
	}
	settings.AgentID = target.Agent.ID
	update := UpdateDatasourceInput{Proof: proof, DatasourceID: record.ID, ExpectedVersion: record.Version, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, RequestID: "offline-v2-move", Now: clock}
	if !now.Before(sourceGrant.ExpiresAt) {
		t.Fatal("source lease must remain valid")
	}
	pending, err := UpdateDatasource(t.Context(), f.databaseURL, nil, update)
	if err != nil || pending.Reassignment == nil {
		t.Fatalf("offline move=%+v err=%v", pending.Reassignment, err)
	}
	var owners int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_ownership_epochs WHERE sli_id=$1 AND starts_at<=$2 AND (ends_at IS NULL OR ends_at>$2)`, sliID, f.base.Add(time.Minute)).Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("legacy future handoff must retain exactly one interval owner: %d err=%v", owners, err)
	}
	if notices, err := ReconcileDatasourceReassignments(t.Context(), f.databaseURL, nil, clock); err != nil || len(notices) != 0 {
		t.Fatalf("progress reconciliation republished configuration=%v err=%v", notices, err)
	}
	active, err := GetDatasource(t.Context(), f.databaseURL, record.ID, proof, clock)
	if err != nil || active.Reassignment == nil || active.Settings.AgentID != target.Agent.ID {
		t.Fatalf("offline replacement=%+v err=%v", active, err)
	}
}
