//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// The grant is seeded after management creates the SLI so this test isolates
// durable ingestion semantics; configuration activation has separate tests.
func TestIntegrationSLIIngestionAttemptsReplayAndCanonicalConflict(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "sli-ingest")
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	resume := ownershipVerifier(ownershipBytes(32, 63))
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume,
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, 1, clock); err != nil {
		t.Fatal(err)
	}
	keyID := "c79c723a-81a2-4e0f-ae09-f2a071b2ef45"
	keyPath := filepath.Join(t.TempDir(), "keyset")
	if err := sourcesecret.Provision(keyPath, keyID); err != nil {
		t.Fatal(err)
	}
	secrets, err := sourcesecret.Load(keyPath, keyID)
	if err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	source, err := CreateDatasource(t.Context(), f.databaseURL, secrets, CreateDatasourceInput{
		Proof: proof, Settings: datasource.Settings{Name: "metrics", DisplayName: "Metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 15, SettlingDelaySeconds: 60, AgentID: created.Agent.ID},
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "source", RequestID: "source", Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := CreateSliInput{Proof: proof, Secrets: secrets, Name: "requests", DisplayName: "Requests", DatasourceID: source.ID,
		MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "sli", RequestID: "sli", Now: clock}
	locker := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(locker)
	lockTx, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(t.Context())
	partitionKey := "almanac-sli-partition:almanac_sli_coverage_" + now.Format("200601")
	if _, err := lockTx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, partitionKey); err != nil {
		t.Fatal(err)
	}
	type createResult struct {
		record SliRecord
		err    error
	}
	createdResult := make(chan createResult, 1)
	go func() {
		value, err := CreateSli(t.Context(), f.databaseURL, input)
		createdResult <- createResult{value, err}
	}()
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%pg_advisory_xact_lock%")
	_, lockErr := observer.Exec(t.Context(), `SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE NOWAIT`, created.Agent.ID)
	if pgErr, ok := errors.AsType[*pgconn.PgError](lockErr); !ok || pgErr.Code != "55P03" {
		t.Fatalf("creation must hold agent lock before partition lock: %v", lockErr)
	}
	renewedResult := make(chan error, 1)
	go func() {
		_, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, 0, ownershipNonce(91), func() time.Time { return now.Add(10 * time.Second) })
		renewedResult <- err
	}()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
	if err := lockTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := <-createdResult
	if result.err != nil {
		t.Fatalf("SLI creation after partition lock: %v", result.err)
	}
	if err := <-renewedResult; err != nil {
		t.Fatalf("concurrent agent renewal: %v", err)
	}
	record := result.record
	unchanged, err := UpdateSliMetadata(t.Context(), f.databaseURL, UpdateSliMetadataInput{Proof: proof, SliID: record.ID, ExpectedVersion: record.Version, DisplayName: record.DisplayName, RequestID: "noop", Now: clock})
	if err != nil || unchanged.Version != record.Version {
		t.Fatalf("metadata no-op version=%d, err=%v", unchanged.Version, err)
	}
	setIntegrationUserRole(t, f, "viewer")
	if _, err := GetSli(t.Context(), f.databaseURL, proof, record.ID, clock); err != nil {
		t.Fatalf("viewer SLI read: %v", err)
	}
	if _, err := UpdateSliMetadata(t.Context(), f.databaseURL, UpdateSliMetadataInput{Proof: proof, SliID: record.ID, ExpectedVersion: record.Version, DisplayName: "Denied", RequestID: "viewer-update", Now: clock}); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer SLI mutation: %v", err)
	}
	setIntegrationUserRole(t, f, "administrator")
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	allowed, err := json.Marshal([]agentprotocol.MeasurementGrant{{SLIID: record.ID, MeasurementRevision: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_execution_grants SET allowed_measurements=$1 WHERE lease_id=$2`, allowed, grant.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET activated_at=$1 WHERE sli_id=$2`, f.base.Add(4*time.Second), record.ID); err != nil {
		t.Fatal(err)
	}
	now = record.EffectiveFrom.Add(15*time.Second + time.Minute)
	item := sli.ObservationItem{DeliveryID: uuid.NewString(), ExecutionID: uuid.NewString(), CreatedAt: now, LeaseID: grant.LeaseID,
		ConfigurationRevision: "1", SessionGeneration: "1", SLIID: record.ID, MeasurementRevision: "1", IntervalStart: record.EffectiveFrom,
		IntervalEnd: record.EffectiveFrom.Add(15 * time.Second), Outcome: sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: "query"}}
	wrongSLI := item
	wrongSLI.SLIID = uuid.NewString()
	if _, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, wrongSLI, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("wrong SLI authorization: %v", err)
	}
	fenced := item
	fenced.SessionGeneration = "2"
	if _, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, fenced, clock); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("fenced generation authorization: %v", err)
	}
	// Remove an empty values partition to verify ingestion fails closed and rolls
	// back coverage and receipt; restore it through the admission preparation path.
	partition := "almanac_sli_observation_values_" + item.IntervalStart.Format("200601")
	if _, err := conn.Exec(t.Context(), "DROP TABLE public."+partition); err != nil {
		t.Fatal(err)
	}
	missingPartition := item
	missingPartition.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	if _, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, missingPartition, clock); err == nil {
		t.Fatal("missing partition accepted")
	}
	var committed int
	if err := conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM public.almanac_sli_coverage WHERE sli_id=$1) + (SELECT count(*) FROM public.almanac_sli_delivery_receipts WHERE sli_id=$1)`, record.ID).Scan(&committed); err != nil || committed != 0 {
		t.Fatalf("missing partition left partial writes: %d, %v", committed, err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSliPartitions(t.Context(), tx, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	ingest := func(value sli.ObservationItem) SliIngestResult {
		t.Helper()
		result, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, value, clock)
		if err != nil {
			t.Fatalf("ingestion failed: %v", err)
		}
		return result
	}
	if result := ingest(item); result.Status != "accepted" {
		t.Fatalf("failed attempt: %+v", result)
	}
	repeatedExecution := item
	repeatedExecution.DeliveryID = uuid.NewString()
	if result := ingest(repeatedExecution); result.Status != "conflicting" || result.ErrorCode != "execution_id_reused" {
		t.Fatalf("reused execution: %+v", result)
	}
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	accepted := ingest(item)
	if accepted.Status != "accepted" {
		t.Fatalf("observation after failed attempt: %+v", accepted)
	}
	if replay := ingest(item); replay.Status != "accepted" || replay.ObservationID != accepted.ObservationID {
		t.Fatalf("replay: %+v", replay)
	}
	changed := item
	changed.Outcome.GoodCount = "98"
	if conflict := ingest(changed); conflict.Status != "conflicting" || conflict.ErrorCode != "delivery_id_reused" {
		t.Fatalf("changed delivery: %+v", conflict)
	}
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.Outcome.GoodCount, item.Outcome.TotalCount = "99.0", "100.000"
	if duplicate := ingest(item); duplicate.Status != "duplicate" {
		t.Fatalf("normalized duplicate: %+v", duplicate)
	}
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.Outcome.GoodCount = "98"
	if conflict := ingest(item); conflict.Status != "conflicting" {
		t.Fatalf("canonical conflict: %+v", conflict)
	}
	var good string
	if err := conn.QueryRow(t.Context(), `SELECT good_count::text FROM public.almanac_sli_observation_values WHERE sli_id=$1`, record.ID).Scan(&good); err != nil || good != "99.000000000000" {
		t.Fatalf("canonical value = %q, %v", good, err)
	}
	now = now.Add(15 * time.Second)
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.CreatedAt = now
	item.IntervalStart = item.IntervalEnd
	item.IntervalEnd = item.IntervalStart.Add(15 * time.Second)
	item.Outcome = sli.ExecutionOutcome{State: sli.OutcomeIncomplete, MissingComponents: []string{"good"}}
	if result := ingest(item); result.Status != "accepted" {
		t.Fatalf("incomplete attempt: %+v", result)
	}
	item.DeliveryID, item.ExecutionID = uuid.NewString(), uuid.NewString()
	item.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "0", TotalCount: "0"}
	if result := ingest(item); result.Status != "accepted" {
		t.Fatalf("zero traffic after incomplete: %+v", result)
	}
	now = now.Add(45 * time.Second)
	first, err := ListSliObservations(t.Context(), f.databaseURL, proof, record.ID, 2, "", clock)
	if err != nil || len(first.Items) != 0 || len(first.MissingIntervals) != 1 || first.NextCursor == "" {
		t.Fatalf("first missing page: %+v, %v", first, err)
	}
	if _, err := ListSliObservations(t.Context(), f.databaseURL, proof, uuid.NewString(), 2, first.NextCursor, clock); !errors.Is(err, ErrSliInvalid) {
		t.Fatalf("cross-SLI history cursor: %v", err)
	}
	second, err := ListSliObservations(t.Context(), f.databaseURL, proof, record.ID, 2, first.NextCursor, clock)
	if err != nil || len(second.Items) != 1 || len(second.MissingIntervals) != 1 || second.NextCursor == "" || !second.InspectedEnd.Equal(*first.InspectedStart) {
		t.Fatalf("second missing page: %+v, %v", second, err)
	}
	third, err := ListSliObservations(t.Context(), f.databaseURL, proof, record.ID, 2, second.NextCursor, clock)
	if err != nil || len(third.Items) != 1 || len(third.MissingIntervals) != 0 || third.NextCursor != "" || !third.InspectedEnd.Equal(*second.InspectedStart) {
		t.Fatalf("third history page: %+v, %v", third, err)
	}
	concurrent := item
	concurrent.DeliveryID, concurrent.ExecutionID = uuid.NewString(), uuid.NewString()
	concurrent.IntervalStart = item.IntervalEnd
	concurrent.IntervalEnd = concurrent.IntervalStart.Add(15 * time.Second)
	concurrent.CreatedAt = now
	concurrent.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	type deliveryResult struct {
		value SliIngestResult
		err   error
	}
	deliverConcurrently := func(left, right sli.ObservationItem) (deliveryResult, deliveryResult) {
		start := make(chan struct{})
		results := make(chan deliveryResult, 2)
		for _, value := range []sli.ObservationItem{left, right} {
			go func() {
				<-start
				got, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, value, clock)
				results <- deliveryResult{got, err}
			}()
		}
		close(start)
		return <-results, <-results
	}
	a, b := deliverConcurrently(concurrent, concurrent)
	if a.err != nil || b.err != nil || a.value.Status != "accepted" || b.value.Status != "accepted" || a.value.ObservationID != b.value.ObservationID {
		t.Fatalf("concurrent identical deliveries: %+v %+v", a, b)
	}
	concurrent.DeliveryID, concurrent.ExecutionID = uuid.NewString(), uuid.NewString()
	concurrent.IntervalStart = concurrent.IntervalEnd
	concurrent.IntervalEnd = concurrent.IntervalStart.Add(15 * time.Second)
	changedConcurrent := concurrent
	changedConcurrent.Outcome.GoodCount = "98"
	a, b = deliverConcurrently(concurrent, changedConcurrent)
	if a.err != nil || b.err != nil || !((a.value.Status == "accepted" && b.value.Status == "conflicting") || (a.value.Status == "conflicting" && b.value.Status == "accepted")) {
		t.Fatalf("concurrent changed deliveries: %+v %+v", a, b)
	}
	secondSLI, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Secrets: secrets, Name: "never-collected", DisplayName: "Never collected", DatasourceID: source.ID,
		MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "second-sli", RequestID: "second-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET activated_at=$1 WHERE sli_id=$2`, now, secondSLI.ID); err != nil {
		t.Fatal(err)
	}
	now = secondSLI.EffectiveFrom.Add(2 * time.Minute)
	fresh := item
	fresh.DeliveryID, fresh.ExecutionID, fresh.CreatedAt = uuid.NewString(), uuid.NewString(), now
	fresh.IntervalEnd = floorSliBoundary(now.Add(-time.Minute), 15*time.Second)
	fresh.IntervalStart = fresh.IntervalEnd.Add(-15 * time.Second)
	fresh.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "1", TotalCount: "1"}
	if result := ingest(fresh); result.Status != "accepted" {
		t.Fatalf("fresh SLI observation: %+v", result)
	}
	healthy, err := GetSli(t.Context(), f.databaseURL, proof, record.ID, clock)
	if err != nil || healthy.Freshness != "fresh" {
		t.Fatalf("collecting SLI freshness: %+v, %v", healthy, err)
	}
	missing, err := GetSli(t.Context(), f.databaseURL, proof, secondSLI.ID, clock)
	if err != nil || missing.Freshness != "stale" || missing.LastAttemptAt != nil || missing.LastObservationAt != nil {
		t.Fatalf("uncollected SLI freshness: %+v, %v", missing, err)
	}
	otherCredential, otherAgent := createOwnershipAgent(t, f, "other-sli-agent")
	otherResume := ownershipVerifier(ownershipBytes(32, 64))
	otherGrant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, otherCredential.ID, otherCredential.Verifier, otherResume,
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, func() time.Time { return f.base.Add(4 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, otherCredential.ID, otherCredential.Verifier, otherResume, otherGrant.Generation, 1, func() time.Time { return f.base.Add(4 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	otherSource, err := CreateDatasource(t.Context(), f.databaseURL, secrets, CreateDatasourceInput{Proof: proof,
		Settings: datasource.Settings{Name: "other-metrics", DisplayName: "Other metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 15, SettlingDelaySeconds: 60, AgentID: otherAgent.Agent.ID},
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "other-source", RequestID: "other-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	otherSLI, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Secrets: secrets, Name: "other-requests", DisplayName: "Other requests", DatasourceID: otherSource.ID,
		MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "other-sli", RequestID: "other-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	otherAllowed, err := json.Marshal([]agentprotocol.MeasurementGrant{{SLIID: otherSLI.ID, MeasurementRevision: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_execution_grants SET allowed_measurements=$1 WHERE lease_id=$2`, otherAllowed, otherGrant.LeaseID); err != nil {
		t.Fatal(err)
	}
	now = otherSLI.EffectiveFrom.Add(15*time.Second + time.Minute)
	otherItem := sli.ObservationItem{DeliveryID: accepted.DeliveryID, ExecutionID: uuid.NewString(), CreatedAt: now, LeaseID: otherGrant.LeaseID,
		ConfigurationRevision: "1", SessionGeneration: "1", SLIID: otherSLI.ID, MeasurementRevision: "1", IntervalStart: otherSLI.EffectiveFrom,
		IntervalEnd: otherSLI.EffectiveFrom.Add(15 * time.Second), Outcome: sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "2", TotalCount: "2"}}
	otherResult, err := IngestAgentObservation(t.Context(), f.databaseURL, otherCredential.ID, otherCredential.Verifier, otherItem, clock)
	if err != nil || otherResult.Status != "accepted" {
		t.Fatalf("second producer same delivery ID: %+v, %v", otherResult, err)
	}
	empty := fresh
	empty.DeliveryID, empty.ExecutionID, empty.CreatedAt = uuid.NewString(), uuid.NewString(), now
	empty.IntervalStart = fresh.IntervalEnd
	empty.IntervalEnd = empty.IntervalStart.Add(15 * time.Second)
	empty.Outcome = sli.ExecutionOutcome{State: sli.OutcomeCompleteEmpty}
	if result := ingest(empty); result.Status != "accepted" {
		t.Fatalf("complete empty coverage: %+v", result)
	}
	var emptyCount int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_coverage c LEFT JOIN public.almanac_sli_observation_values v USING (interval_start,sli_id,measurement_revision) WHERE c.sli_id=$1 AND c.interval_start=$2 AND c.outcome='complete_empty' AND v.sli_id IS NULL`, record.ID, empty.IntervalStart).Scan(&emptyCount); err != nil || emptyCount != 1 {
		t.Fatalf("complete empty coverage without fabricated values: %d, %v", emptyCount, err)
	}
	monthTx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSliPartitions(t.Context(), monthTx, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := monthTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var partitions int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relname IN ('almanac_sli_coverage_202610','almanac_sli_coverage_202611','almanac_sli_coverage_202612','almanac_sli_observation_values_202610','almanac_sli_observation_values_202611','almanac_sli_observation_values_202612')`).Scan(&partitions); err != nil || partitions != 6 {
		t.Fatalf("month boundary preparation count=%d err=%v", partitions, err)
	}
	// Keep durable grant evidence beyond the delivery window to isolate replay
	// expiry from the independent 400-day observation cutoff.
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_execution_grants SET retain_until=$1 WHERE lease_id=$2`, now.Add(10*24*time.Hour), grant.LeaseID); err != nil {
		t.Fatal(err)
	}
	expiredClock := func() time.Time { return item.CreatedAt.Add(8 * 24 * time.Hour) }
	expired, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, item, expiredClock)
	if err != nil || expired.Status != "rejected" || expired.ErrorCode != "replay_expired" {
		t.Fatalf("expired delivery: %+v, %v", expired, err)
	}
	revokeTx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer revokeTx.Rollback(t.Context())
	if _, err := revokeTx.Exec(t.Context(), `SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE`, created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	replayResult := make(chan error, 1)
	go func() {
		_, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, item, clock)
		replayResult <- err
	}()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
	if _, err := revokeTx.Exec(t.Context(), `UPDATE public.almanac_agent_credentials SET revoked_at=$1 WHERE id=$2`, now, issued.ID); err != nil {
		t.Fatal(err)
	}
	if err := revokeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-replayResult; !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatalf("revoked credential concurrent replay authorization: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_agent_ownership SET current_lease_id=NULL WHERE agent_id=$1`, created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	pruneAt := now.Add(11 * 24 * time.Hour)
	var eligible bool
	if err := conn.QueryRow(t.Context(), `SELECT retain_until < $2 FROM public.almanac_agent_execution_grants WHERE lease_id=$1`, grant.LeaseID, pruneAt).Scan(&eligible); err != nil || !eligible {
		t.Fatalf("grant not eligible for pruning: %t, %v", eligible, err)
	}
	if err := dbgen.New(conn).DeleteExpiredAgentExecutionGrants(t.Context(), dbgen.DeleteExpiredAgentExecutionGrantsParams{AgentID: mustPGUUID(created.Agent.ID), RetainUntil: timestamp(pruneAt)}); err != nil {
		t.Fatalf("referenced grant pruning failed: %v", err)
	}
	var retained int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agent_execution_grants WHERE lease_id=$1`, grant.LeaseID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("referenced grant retained=%d err=%v", retained, err)
	}
}
