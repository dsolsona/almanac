//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationAgentRuntimeExplanationReflectsAssignment(t *testing.T) {
	f := newAgentFixture(t)
	credential, created := createOwnershipAgent(t, f, "runtime-assignment")
	clock := func() time.Time { return f.base.Add(5 * time.Second) }
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["collect","connection_test","query_preview"]}]`)}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, credential.ID, credential.Verifier, ownershipVerifier(ownershipBytes(32, 79)), hello, clock); err != nil {
		t.Fatal(err)
	}
	input := AgentReadInput{SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: clock}
	before, err := GetAgent(t.Context(), f.databaseURL, input)
	if err != nil || before.Runtime == nil || before.Runtime.CollectionExplanation != "no_assignment" {
		t.Fatalf("unassigned runtime=%+v err=%v", before.Runtime, err)
	}
	settings := datasource.DefaultSettings("runtime-metrics", "Runtime metrics", "https://metrics.example.test", created.Agent.ID)
	if _, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: DatasourceProof{SessionVerifier: f.verifier}, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "runtime-source", RequestID: "runtime-source", Now: clock}); err != nil {
		t.Fatal(err)
	}
	after, err := GetAgent(t.Context(), f.databaseURL, input)
	if err != nil || after.Runtime == nil || after.Runtime.CollectionExplanation != "none" {
		t.Fatalf("assigned runtime=%+v err=%v", after.Runtime, err)
	}
	page, err := ListAgents(t.Context(), f.databaseURL, ListAgentsInput{SessionVerifier: f.verifier, Limit: 10, Now: clock})
	if err != nil || len(page.Items) != 1 || page.Items[0].Runtime == nil || page.Items[0].Runtime.CollectionExplanation != "none" {
		t.Fatalf("assigned list=%+v err=%v", page, err)
	}
}

func TestIntegrationAgentOwnershipAndEmptyExecutionGrantCommitTogether(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "runtime-grant")
	proof := ownershipBytes(32, 71)
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "1.0.0", Capabilities: []byte(`[]`)}
	first, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), hello, clock)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || first.ConfigurationRevision != 1 || first.LeaseID == "" || string(first.AllowedMeasurements) != "[]" {
		t.Fatalf("initial execution state = %+v", first)
	}
	if err := RecordAgentConfigurationFailed(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), first.Generation, 1, "unsafe_detail", clock); !errors.Is(err, ErrAgentOwnershipInput) {
		t.Fatalf("unsafe configuration failure code error = %v", err)
	}
	var snapshot agentprotocol.ConfigurationSnapshot
	if err := json.Unmarshal(first.ConfigurationSnapshot, &snapshot); err != nil || snapshot.Assignments == nil || len(snapshot.Assignments) != 0 {
		t.Fatalf("initial snapshot assignments = %s, %v", first.ConfigurationSnapshot, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var ownerCount, grantCount, configCount int
	if err := conn.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1 AND generation=1),
		(SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1 AND generation=1),
		(SELECT count(*) FROM public.almanac_agent_configurations WHERE agent_id=$1 AND revision=1)`, created.Agent.ID).Scan(&ownerCount, &grantCount, &configCount); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if ownerCount != 1 || grantCount != 1 || configCount != 1 {
		t.Fatalf("durable owner/grant/config counts = %d/%d/%d", ownerCount, grantCount, configCount)
	}
	if err := RecordAgentConfigurationFailed(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), first.Generation+1, 1, "apply_failed", clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("stale-generation configuration failure error = %v", err)
	}
	if err := RecordAgentConfigurationFailed(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), first.Generation, 1, "apply_failed", clock); err != nil {
		t.Fatalf("record valid configuration failure: %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var failureCode pgtype.Text
	var failureRevision pgtype.Int8
	var failureHealth, failureProgress string
	if err := conn.QueryRow(t.Context(), `SELECT configuration_failure_code,configuration_failure_revision,scheduler_health,collection_progress FROM public.almanac_agent_runtime_status WHERE agent_id=$1`, created.Agent.ID).Scan(&failureCode, &failureRevision, &failureHealth, &failureProgress); err != nil {
		t.Fatal("read configuration failure status failed")
	}
	closeIntegrationConn(conn)
	if !failureCode.Valid || failureCode.String != "apply_failed" || !failureRevision.Valid || failureRevision.Int64 != 1 || failureHealth != "degraded" || failureProgress != "blocked" {
		t.Fatalf("configuration failure status = %s/%d/%s/%s", failureCode.String, failureRevision.Int64, failureHealth, failureProgress)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation+1, 1, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("stale-generation applied configuration error = %v", err)
	}
	nonce := ownershipNonce(72)
	now = now.Add(10 * time.Second)
	heartbeat := &AgentRuntimeHeartbeat{ReceivedAt: now, LeaseID: first.LeaseID, AppliedConfigurationRevision: 0,
		SchedulerHealth: "starting", CollectionProgress: "not_started"}
	renewed, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 0, nonce, heartbeat, clock)
	if err != nil || renewed.RenewalSequence != 1 || renewed.LeaseID == first.LeaseID || renewed.ConfigurationRevision != first.ConfigurationRevision {
		t.Fatalf("renewed execution state = %+v, %v", renewed, err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var appliedRevision int64
	if err := conn.QueryRow(t.Context(), "SELECT applied_configuration_revision FROM public.almanac_agent_runtime_status WHERE agent_id=$1", created.Agent.ID).Scan(&appliedRevision); err != nil {
		t.Fatal("read unapplied heartbeat revision failed")
	}
	closeIntegrationConn(conn)
	if appliedRevision != 0 {
		t.Fatalf("unapplied heartbeat revision = %d, want 0", appliedRevision)
	}
	conn = integrationConnect(t, f.databaseURL)
	if err := conn.QueryRow(t.Context(), "SELECT configuration_failure_code,configuration_failure_revision FROM public.almanac_agent_runtime_status WHERE agent_id=$1", created.Agent.ID).Scan(&failureCode, &failureRevision); err != nil {
		t.Fatal("read failure after unapplied heartbeat failed")
	}
	closeIntegrationConn(conn)
	if !failureCode.Valid || failureCode.String != "apply_failed" || !failureRevision.Valid || failureRevision.Int64 != 1 {
		t.Fatalf("unapplied heartbeat cleared failure state %+v/%+v", failureCode, failureRevision)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 1, clock); err != nil {
		t.Fatalf("record matching applied configuration: %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	if err := conn.QueryRow(t.Context(), "SELECT configuration_failure_code,configuration_failure_revision FROM public.almanac_agent_runtime_status WHERE agent_id=$1", created.Agent.ID).Scan(&failureCode, &failureRevision); err != nil {
		t.Fatal("read failure after successful application failed")
	}
	closeIntegrationConn(conn)
	if failureCode.Valid || failureRevision.Valid {
		t.Fatalf("successful application retained failure state %q/%d", failureCode.String, failureRevision.Int64)
	}
	now = renewed.IssuedAt.Add(time.Minute)
	replay, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 0, nonce, heartbeat, clock)
	if err != nil || replay.RenewalSequence != renewed.RenewalSequence || replay.LeaseID != renewed.LeaseID || !replay.IssuedAt.Equal(renewed.IssuedAt) || !replay.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("renewal replay = %+v, %v", replay, err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var historical, current int
	if err := conn.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE lease_id=$2)
FROM public.almanac_agent_execution_grants WHERE agent_id=$1`, created.Agent.ID, first.LeaseID).Scan(&grantCount, &historical); err != nil {
		t.Fatal("read immutable grants after replay failed")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1 AND lease_id=$2`, created.Agent.ID, renewed.LeaseID).Scan(&current); err != nil {
		t.Fatal("read renewed grant failed")
	}
	closeIntegrationConn(conn)
	if grantCount != 2 || historical != 1 || current != 1 {
		t.Fatalf("initial/renewed immutable grant rows = %d/%d/%d", historical, current, grantCount)
	}

	tooEarly := &AgentRuntimeHeartbeat{ReceivedAt: renewed.IssuedAt.Add(10*time.Second - time.Microsecond), LeaseID: renewed.LeaseID, AppliedConfigurationRevision: 1, SchedulerHealth: "ready", CollectionProgress: "idle"}
	if _, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 1, ownershipNonce(73), tooEarly, func() time.Time { return tooEarly.ReceivedAt }); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("renewal before ten-second boundary error = %v", err)
	}
	next := &AgentRuntimeHeartbeat{ReceivedAt: renewed.IssuedAt.Add(10 * time.Second), LeaseID: renewed.LeaseID, AppliedConfigurationRevision: 1, SchedulerHealth: "ready", CollectionProgress: "idle"}
	now = next.ReceivedAt
	renewedAgain, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 1, ownershipNonce(73), next, clock)
	if err != nil || renewedAgain.RenewalSequence != 2 || renewedAgain.LeaseID == renewed.LeaseID {
		t.Fatalf("renewal at ten-second boundary = %+v, %v", renewedAgain, err)
	}
	if _, err := ResumeAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), first.Generation, hello, clock); err != nil {
		t.Fatalf("resume same generation after heartbeat: %v", err)
	}
	var status struct {
		Heartbeat time.Time
		Applied   int64
		Health    string
		Progress  string
	}
	conn = integrationConnect(t, f.databaseURL)
	var currentLeaseID string
	if err := conn.QueryRow(t.Context(), `SELECT last_heartbeat_at,applied_configuration_revision,scheduler_health,collection_progress FROM public.almanac_agent_runtime_status WHERE agent_id=$1`, created.Agent.ID).Scan(&status.Heartbeat, &status.Applied, &status.Health, &status.Progress); err != nil {
		t.Fatal("read persisted heartbeat status failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT current_lease_id FROM public.almanac_agent_ownership WHERE agent_id=$1", created.Agent.ID).Scan(&currentLeaseID); err != nil {
		t.Fatal("read current execution grant pointer failed")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1`, created.Agent.ID).Scan(&grantCount); err != nil {
		t.Fatal("read retained grant count failed")
	}
	closeIntegrationConn(conn)
	if !status.Heartbeat.Equal(next.ReceivedAt) || status.Applied != 1 || status.Health != "ready" || status.Progress != "idle" || grantCount != 3 || currentLeaseID != renewedAgain.LeaseID {
		t.Fatalf("persisted applied/heartbeat status=%+v retained grants=%d current-lease-match=%t", status, grantCount, currentLeaseID == renewedAgain.LeaseID)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation+1, 1, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("stale-generation status write error = %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET revoked_at=$2 WHERE id=$1", issued.ID, now); err != nil {
		t.Fatal("revoke runtime test credential failed")
	}
	closeIntegrationConn(conn)
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 1, clock); !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatalf("revoked-credential status write error = %v", err)
	}
	if _, err := RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 2, ownershipNonce(74), next, clock); !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatalf("revoked-credential heartbeat error = %v", err)
	}
}

func TestIntegrationAgentRuntimeGrantFailureRollsBackOwnership(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "runtime-grant-failure")
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_agent_execution_grant_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected execution grant failure'; END $$`); err != nil {
		t.Fatal("create grant failure trigger function failed")
	}
	if _, err := conn.Exec(t.Context(), `CREATE TRIGGER fail_agent_execution_grant_insert BEFORE INSERT ON public.almanac_agent_execution_grants FOR EACH ROW EXECUTE FUNCTION public.fail_agent_execution_grant_insert()`); err != nil {
		t.Fatal("create grant failure trigger failed")
	}
	closeIntegrationConn(conn)
	hello := &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "1.0.0", Capabilities: []byte(`[]`)}
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(ownershipBytes(32, 81)), hello, func() time.Time { return f.base.Add(4 * time.Second) }); err == nil {
		t.Fatal("ownership acquisition with forced grant failure unexpectedly succeeded")
	}
	conn = integrationConnect(t, f.databaseURL)
	var ownerships, grants, configurations, statuses int
	if err := conn.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1),
 (SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1),
 (SELECT count(*) FROM public.almanac_agent_configurations WHERE agent_id=$1),
 (SELECT count(*) FROM public.almanac_agent_runtime_status WHERE agent_id=$1)`, created.Agent.ID).Scan(&ownerships, &grants, &configurations, &statuses); err != nil {
		t.Fatal("read rolled-back runtime state failed")
	}
	closeIntegrationConn(conn)
	if ownerships != 0 || grants != 0 || configurations != 0 || statuses != 0 {
		t.Fatalf("failed runtime acquisition left rows ownership/grant/config/status=%d/%d/%d/%d", ownerships, grants, configurations, statuses)
	}
}

func TestIntegrationStaleHeartbeatDoesNotRegressAppliedRevision(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "stale-heartbeat")
	now := f.base.Add(5 * time.Second)
	if _, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(ownershipBytes(32, 54)), &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "test", Capabilities: []byte(`[]`)}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_runtime_status SET desired_configuration_revision=2, applied_configuration_revision=2, scheduler_health='ready', collection_progress='running' WHERE agent_id=$1`, created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	id, ok := canonicalUUID(created.Agent.ID)
	if !ok {
		t.Fatal("invalid agent ID")
	}
	rows, err := dbgen.New(conn).UpdateAgentRuntimeHeartbeat(t.Context(), dbgen.UpdateAgentRuntimeHeartbeatParams{AgentID: id, Generation: 1, ReceivedAt: timestamp(now), AppliedConfigurationRevision: 1, SchedulerHealth: "degraded", CollectionProgress: "blocked"})
	if err != nil || rows != 1 {
		t.Fatalf("stale heartbeat rows=%d err=%v", rows, err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_runtime_status SET desired_configuration_revision=3 WHERE agent_id=$1`, created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = dbgen.New(conn).UpdateAgentRuntimeHeartbeat(t.Context(), dbgen.UpdateAgentRuntimeHeartbeatParams{AgentID: id, Generation: 1, ReceivedAt: timestamp(now), AppliedConfigurationRevision: 3, SchedulerHealth: "ready", CollectionProgress: "running"})
	if err != nil || rows != 1 {
		t.Fatalf("unacknowledged heartbeat rows=%d err=%v", rows, err)
	}
	var revision int64
	var health, progress string
	if err := conn.QueryRow(t.Context(), `SELECT applied_configuration_revision,scheduler_health,collection_progress FROM public.almanac_agent_runtime_status WHERE agent_id=$1`, created.Agent.ID).Scan(&revision, &health, &progress); err != nil || revision != 2 || health != "ready" || progress != "running" {
		t.Fatalf("stale heartbeat regressed status revision=%d health=%s progress=%s err=%v", revision, health, progress, err)
	}
}
