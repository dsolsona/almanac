//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	page, err := ListSlis(t.Context(), f.databaseURL, proof, 50, "", "", "", clock)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != record.ID || page.Items[0].LatestObservation != nil || page.Items[0].ListProjection == nil ||
		page.Items[0].ListProjection.DatasourceDisplayName == nil || *page.Items[0].ListProjection.DatasourceDisplayName != source.Settings.DisplayName || len(page.Items[0].ListProjection.RecentMeasurements) != 0 || len(page.Items[0].ListProjection.LinkedSLOs) != 0 {
		t.Fatalf("new SLI list = %+v, %v", page, err)
	}
	filtered, err := ListSlis(t.Context(), f.databaseURL, proof, 50, "", "REQUESTS", source.ID, clock)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != record.ID {
		t.Fatalf("case-insensitive pre-pagination SLI filters = %+v, %v", filtered, err)
	}
	noMatch, err := ListSlis(t.Context(), f.databaseURL, proof, 50, "", "missing", source.ID, clock)
	if err != nil || len(noMatch.Items) != 0 || noMatch.NextCursor != "" {
		t.Fatalf("nonmatching SLI filter = %+v, %v", noMatch, err)
	}
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
	now = record.CreatedAt
	item := sli.ObservationItem{DeliveryID: uuid.NewString(), ExecutionID: uuid.NewString(), CreatedAt: now, LeaseID: grant.LeaseID,
		ConfigurationRevision: "1", SessionGeneration: "1", SLIID: record.ID, MeasurementRevision: "1", IntervalStart: record.EffectiveFrom,
		IntervalEnd: record.EffectiveFrom.Add(15 * time.Second), Outcome: sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: "query"}}
	if !record.EffectiveFrom.Before(record.CreatedAt) {
		t.Fatalf("initial validity %v did not precede creation %v", record.EffectiveFrom, record.CreatedAt)
	}
	beforeBound := item
	beforeBound.IntervalStart = record.EffectiveFrom.Add(-15 * time.Second)
	beforeBound.IntervalEnd = record.EffectiveFrom
	if result, err := IngestAgentObservation(t.Context(), f.databaseURL, issued.ID, issued.Verifier, beforeBound, clock); err != nil || result.Status != "rejected" {
		t.Fatalf("interval before initial validity: %+v, %v", result, err)
	}
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
	var cursor time.Time
	if err := conn.QueryRow(t.Context(), `SELECT last_attempted_interval_end FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, record.ID).Scan(&cursor); err != nil || !cursor.Equal(item.IntervalEnd) {
		t.Fatalf("persisted attempt cursor = %v, %v", cursor, err)
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
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_observation_values SET good_count=0, total_count=0 WHERE sli_id=$1 AND measurement_revision=1 AND interval_start=$2`, record.ID, item.IntervalStart); err != nil {
		t.Fatal("seed database-backed SLI zero-traffic projection failed")
	}
	projectionNow := func() time.Time { return item.IntervalEnd.Add(60 * time.Second) }
	zeroPage, err := ListSlis(t.Context(), f.databaseURL, proof, 50, "", "", "", projectionNow)
	if err != nil || len(zeroPage.Items) != 1 || zeroPage.Items[0].ListProjection == nil || len(zeroPage.Items[0].ListProjection.RecentMeasurements) != 1 ||
		zeroPage.Items[0].ListProjection.RecentMeasurements[0].State != "zero_traffic" || zeroPage.Items[0].ListProjection.RecentMeasurements[0].GoodCount == nil ||
		*zeroPage.Items[0].ListProjection.RecentMeasurements[0].GoodCount != "0" || zeroPage.Items[0].ListProjection.RecentMeasurements[0].TotalCount == nil ||
		*zeroPage.Items[0].ListProjection.RecentMeasurements[0].TotalCount != "0" {
		t.Fatalf("database-backed zero-traffic list projection = %+v, %v", zeroPage, err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_observation_values SET good_count=99, total_count=100 WHERE sli_id=$1 AND measurement_revision=1 AND interval_start=$2`, record.ID, item.IntervalStart); err != nil {
		t.Fatal("restore database-backed SLI sample after zero-traffic projection failed")
	}
	serviceInput := CreateObjectiveInput{Proof: proof, Name: "api", DisplayName: "API", IdempotencyKey: "service-create", RequestID: "service-create", Now: clock}
	service, _, err := CreateObjective(t.Context(), f.databaseURL, serviceInput, false)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	from := record.EffectiveFrom
	objectiveInput := CreateObjectiveInput{Proof: proof, Name: "availability", DisplayName: "Availability", ServiceID: service.ID, SliID: record.ID, Target: "0.999", RollingWindowSeconds: 120, EvaluationValidFrom: &from, IdempotencyKey: "slo-create", RequestID: "slo-create", Now: clock}
	_, objective, err := CreateObjective(t.Context(), f.databaseURL, objectiveInput, true)
	if err != nil {
		t.Fatalf("create SLO: %v", err)
	}
	_, replayObjective, err := CreateObjective(t.Context(), f.databaseURL, objectiveInput, true)
	if err != nil || replayObjective.ID != objective.ID || replayObjective.HistoricalRequestedBy == nil {
		t.Fatalf("SLO replay: %+v %v", replayObjective, err)
	}
	conflictingInput := objectiveInput
	conflictingInput.Target = "0.99"
	if _, _, err := CreateObjective(t.Context(), f.databaseURL, conflictingInput, true); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("changed SLO replay: %v", err)
	}
	type createServiceResult struct {
		service ServiceRecord
		err     error
	}
	createConcurrently := func(a, b CreateObjectiveInput) (createServiceResult, createServiceResult) {
		start := make(chan struct{})
		results := make(chan createServiceResult, 2)
		for _, input := range []CreateObjectiveInput{a, b} {
			go func() {
				<-start
				service, _, err := CreateObjective(t.Context(), f.databaseURL, input, false)
				results <- createServiceResult{service, err}
			}()
		}
		close(start)
		return <-results, <-results
	}
	parallel := CreateObjectiveInput{Proof: proof, Name: "parallel", DisplayName: "Parallel", IdempotencyKey: "parallel", RequestID: "parallel", Now: clock}
	left, right := createConcurrently(parallel, parallel)
	if left.err != nil || right.err != nil || left.service.ID != right.service.ID {
		t.Fatalf("concurrent replay: %+v %+v", left, right)
	}
	other := parallel
	other.IdempotencyKey = "parallel-other"
	other.Name = "new-concurrent"
	parallel.Name = other.Name
	parallel.IdempotencyKey = "parallel-new"
	left, right = createConcurrently(parallel, other)
	if !((left.err == nil && errors.Is(right.err, ErrObjectiveConflict)) || (right.err == nil && errors.Is(left.err, ErrObjectiveConflict))) {
		t.Fatalf("concurrent same-name creation: %+v %+v", left, right)
	}
	startUpdates := make(chan struct{})
	updates := make(chan error, 2)
	for _, name := range []string{"Service A", "Service B"} {
		go func() {
			<-startUpdates
			_, _, err := UpdateObjectiveMetadata(t.Context(), f.databaseURL, proof, service.ID, name, "service-update", service.Version, clock, false)
			updates <- err
		}()
	}
	close(startUpdates)
	updateA, updateB := <-updates, <-updates
	if !((updateA == nil && errors.Is(updateB, ErrObjectiveConflict)) || (updateB == nil && errors.Is(updateA, ErrObjectiveConflict))) {
		t.Fatalf("concurrent metadata update: %v %v", updateA, updateB)
	}
	evaluation, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, Now: clock})
	if err != nil {
		t.Fatalf("evaluate SLO: %v", err)
	}
	if evaluation.Performance.Attainment == nil || *evaluation.Performance.Attainment != "0.990000000000" || evaluation.Performance.State != "unknown" || !evaluation.Performance.Provisional || evaluation.Quality.ObservedIntervals != 1 || evaluation.Quality.MissingIntervals == 0 {
		t.Fatalf("partial SLO evaluation: %+v %+v", evaluation.Performance, evaluation.Quality)
	}
	if evaluation.Quality.Freshness != "fresh" {
		t.Fatalf("settled latest observation unexpectedly stale: %+v", evaluation.Quality)
	}
	// Establish the repeatable-read snapshot while another transaction owns the
	// user lock and has updated this session. The session lock then sees a row
	// changed after the snapshot and PostgreSQL aborts that transaction with 40001.
	lockTx, err = locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(t.Context())
	if _, err := lockTx.Exec(t.Context(), `SELECT id FROM public.almanac_users WHERE id=(SELECT user_id FROM public.almanac_sessions WHERE verifier=$1) FOR UPDATE`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(t.Context(), `UPDATE public.almanac_sessions SET last_seen_at=last_seen_at + interval '1 microsecond' WHERE verifier=$1`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	type evaluationResult struct {
		value api.SloEvaluation
		err   error
	}
	evaluations := make(chan evaluationResult, 1)
	go func() {
		value, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, Now: clock})
		evaluations <- evaluationResult{value, err}
	}()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_users%")
	if err := lockTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	retried := <-evaluations
	if retried.err != nil || !reflect.DeepEqual(retried.value.Performance, evaluation.Performance) || !reflect.DeepEqual(retried.value.Quality, evaluation.Quality) || !reflect.DeepEqual(retried.value.Timeline, evaluation.Timeline) {
		t.Fatalf("evaluation after concurrent session update: %+v, %v", retried.value, retried.err)
	}
	lockTx, err = locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(t.Context())
	if _, err := lockTx.Exec(t.Context(), `SELECT id FROM public.almanac_users WHERE id=(SELECT user_id FROM public.almanac_sessions WHERE verifier=$1) FOR UPDATE`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(t.Context(), `UPDATE public.almanac_sessions SET restricted=true WHERE verifier=$1`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	go func() {
		value, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, Now: clock})
		evaluations <- evaluationResult{value, err}
	}()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_users%")
	if err := lockTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if retried = <-evaluations; !errors.Is(retried.err, ErrAgentForbidden) {
		t.Fatalf("evaluation must recheck restricted session after retry: %v", retried.err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sessions SET restricted=false WHERE verifier=$1`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	setIntegrationUserRole(t, f, "viewer")
	if _, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "denied", DisplayName: "Denied", IdempotencyKey: "denied", RequestID: "denied", Now: clock}, false); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer create: %v", err)
	}
	if _, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, Now: clock}); err != nil {
		t.Fatalf("viewer evaluate: %v", err)
	}
	setIntegrationUserRole(t, f, "administrator")
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sessions SET restricted=true WHERE verifier=$1`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadObjective(t.Context(), f.databaseURL, proof, service.ID, clock, false); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("restricted service read: %v", err)
	}
	if _, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, Now: clock}); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("restricted SLO evaluation: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sessions SET restricted=false WHERE verifier=$1`, f.verifier[:]); err != nil {
		t.Fatal(err)
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
	zeroFrom := item.IntervalStart
	zeroInput := CreateObjectiveInput{Proof: proof, Name: "zero-window", DisplayName: "Zero window", ServiceID: service.ID, SliID: record.ID, Target: "0.99", RollingWindowSeconds: 120, EvaluationValidFrom: &zeroFrom, IdempotencyKey: "zero-window", RequestID: "zero-window", Now: clock}
	_, zeroSlo, err := CreateObjective(t.Context(), f.databaseURL, zeroInput, true)
	if err != nil {
		t.Fatalf("create zero SLO: %v", err)
	}
	zeroEvaluation, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: zeroSlo.ID, Now: clock})
	if err != nil || !zeroEvaluation.Performance.Provisional || zeroEvaluation.Performance.Reason == nil || *zeroEvaluation.Performance.Reason != "incomplete_measurements" || zeroEvaluation.Quality.MissingIntervals == 0 {
		t.Fatalf("zero traffic with missing intervals: %+v %v", zeroEvaluation, err)
	}
	asOf := item.IntervalEnd
	zeroComplete, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: zeroSlo.ID, At: &asOf, Now: clock})
	if err != nil || zeroComplete.Quality.State != "zero_traffic" || zeroComplete.Performance.Provisional || zeroComplete.Performance.Reason == nil || *zeroComplete.Performance.Reason != "zero_traffic" || zeroComplete.Performance.Attainment != nil {
		t.Fatalf("complete zero traffic: %+v %v", zeroComplete, err)
	}
	mixed, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, At: &asOf, Now: clock})
	if err != nil || mixed.Performance.State != "violated" || mixed.Quality.ZeroTrafficIntervals != 1 || mixed.Performance.Attainment == nil || *mixed.Performance.Attainment != "0.990000000000" {
		t.Fatalf("positive plus zero traffic: %+v %v", mixed, err)
	}
	future := now.Add(time.Second)
	if _, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, At: &future, Now: clock}); !errors.Is(err, ErrObjectiveEvaluationTime) {
		t.Fatalf("future evaluation: %v", err)
	}
	badBound := asOf.Add(-time.Second)
	if _, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, TimelineStart: &badBound, TimelineEnd: &asOf, Now: clock}); !errors.Is(err, ErrObjectiveTimelineRange) {
		t.Fatalf("unaligned timeline: %v", err)
	}
	if _, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, TimelineStart: &asOf, Now: clock}); !errors.Is(err, ErrObjectiveTimelineRange) {
		t.Fatalf("one timeline bound: %v", err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT last_attempted_interval_end FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, record.ID).Scan(&cursor); err != nil || !cursor.Equal(item.IntervalEnd) {
		t.Fatalf("advanced attempt cursor = %v, %v", cursor, err)
	}
	older := item
	older.DeliveryID, older.ExecutionID = uuid.NewString(), uuid.NewString()
	older.IntervalStart, older.IntervalEnd = record.EffectiveFrom, record.EffectiveFrom.Add(15*time.Second)
	older.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	if replay := ingest(older); replay.Status != "duplicate" {
		t.Fatalf("older duplicate: %+v", replay)
	}
	if err := conn.QueryRow(t.Context(), `SELECT last_attempted_interval_end FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, record.ID).Scan(&cursor); err != nil || !cursor.Equal(item.IntervalEnd) {
		t.Fatalf("older delivery moved cursor: %v, %v", cursor, err)
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
	now = secondSLI.CreatedAt.Add(61 * time.Second)
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
	now = otherSLI.CreatedAt
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
	if ready := empty.IntervalEnd.Add(time.Minute); now.Before(ready) {
		now = ready
		empty.CreatedAt = now
	}
	empty.Outcome = sli.ExecutionOutcome{State: sli.OutcomeCompleteEmpty}
	if result := ingest(empty); result.Status != "accepted" {
		t.Fatalf("complete empty coverage: %+v", result)
	}
	var emptyCount int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_coverage c LEFT JOIN public.almanac_sli_observation_values v USING (interval_start,sli_id,measurement_revision) WHERE c.sli_id=$1 AND c.interval_start=$2 AND c.outcome='complete_empty' AND v.sli_id IS NULL`, record.ID, empty.IntervalStart).Scan(&emptyCount); err != nil || emptyCount != 1 {
		t.Fatalf("complete empty coverage without fabricated values: %d, %v", emptyCount, err)
	}
	emptyFrom := empty.IntervalStart
	emptyInput := CreateObjectiveInput{Proof: proof, Name: "empty-only", DisplayName: "Empty only", ServiceID: service.ID, SliID: record.ID, Target: "0.99", RollingWindowSeconds: 120, EvaluationValidFrom: &emptyFrom, IdempotencyKey: "empty-only", RequestID: "empty-only", Now: clock}
	_, emptySlo, err := CreateObjective(t.Context(), f.databaseURL, emptyInput, true)
	if err != nil {
		t.Fatalf("create empty SLO: %v", err)
	}
	emptyEnd := empty.IntervalEnd
	emptyEvaluation, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: emptySlo.ID, At: &emptyEnd, Now: clock})
	if err != nil || emptyEvaluation.Quality.KnownEmptyIntervals != 1 || emptyEvaluation.Quality.FirstObservationAt != nil || emptyEvaluation.Quality.LastObservationAt != nil || emptyEvaluation.Quality.LatestReceiptAt == nil || emptyEvaluation.Quality.Freshness != "unknown" || emptyEvaluation.Performance.Attainment != nil || emptyEvaluation.Performance.Good != nil || emptyEvaluation.Performance.State != "unknown" {
		t.Fatalf("empty-only evaluation: %+v %v", emptyEvaluation, err)
	}
	positiveFrom := fresh.IntervalStart
	positiveInput := emptyInput
	positiveInput.Name = "positive-and-empty"
	positiveInput.IdempotencyKey = "positive-and-empty"
	positiveInput.EvaluationValidFrom = &positiveFrom
	_, positiveSlo, err := CreateObjective(t.Context(), f.databaseURL, positiveInput, true)
	if err != nil {
		t.Fatalf("create positive SLO: %v", err)
	}
	positiveEvaluation, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: positiveSlo.ID, At: &emptyEnd, Now: clock})
	if err != nil || positiveEvaluation.Quality.KnownEmptyIntervals != 1 || positiveEvaluation.Performance.State != "unknown" || !positiveEvaluation.Performance.Provisional || positiveEvaluation.Performance.Attainment == nil || *positiveEvaluation.Performance.Attainment != "1.000000000000" {
		t.Fatalf("positive and empty evaluation: %+v %v", positiveEvaluation, err)
	}
	now = maxTime(now, fresh.IntervalEnd.Add(15*time.Second+time.Minute+sli.DispatchPhase(record.ID, 15)+time.Microsecond))
	staleEvaluation, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: proof, ID: positiveSlo.ID, Now: clock})
	if err != nil || staleEvaluation.Quality.LastObservationAt == nil || !staleEvaluation.Quality.LastObservationAt.Equal(fresh.IntervalEnd) || staleEvaluation.Quality.LatestReceiptAt == nil || staleEvaluation.Quality.Freshness != "stale" {
		t.Fatalf("known-empty refreshed numerical observation: %+v %v", staleEvaluation.Quality, err)
	}
	longStart := record.EffectiveFrom
	longInput := CreateObjectiveInput{Proof: proof, Name: "retention-window", DisplayName: "Retention window", ServiceID: service.ID, SliID: record.ID, Target: "0.99", RollingWindowSeconds: 401 * 86400, EvaluationValidFrom: &longStart, IdempotencyKey: "retention-window", RequestID: "retention-window", Now: clock}
	_, longSlo, err := CreateObjective(t.Context(), f.databaseURL, longInput, true)
	if err != nil {
		t.Fatalf("create retention SLO: %v", err)
	}
	futureNow := now.Add(sli.ObservationRetention + time.Minute)
	futureVerifier := [32]byte{126}
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: futureVerifier, Now: futureNow, RequestID: "future-session"}); err != nil {
		t.Fatalf("future session: %v", err)
	}
	chartStart := longStart.Add(-15 * time.Second)
	chartEnd := ceilSliBoundary(futureNow.Add(-sli.ObservationRetention), 15*time.Second).Add(15 * time.Second)
	futureClock := func() time.Time { return futureNow }
	expiredEvaluation, err := EvaluateSlo(t.Context(), f.databaseURL, EvaluateSloInput{Proof: DatasourceProof{SessionVerifier: futureVerifier}, ID: longSlo.ID, TimelineStart: &chartStart, TimelineEnd: &chartEnd, Now: futureClock})
	if err != nil || expiredEvaluation.Quality.State != "expired_history" || expiredEvaluation.Quality.ExpiredIntervals == 0 || len(expiredEvaluation.Timeline.Items) < 2 || expiredEvaluation.Timeline.Items[0].State != "not_applicable" || expiredEvaluation.Timeline.Items[1].State != "expired" {
		t.Fatalf("expiry versus validity: %+v %v", expiredEvaluation, err)
	}
	// Recreate the pre-0016 schema in this disposable fixture, then verify that
	// upgrading existing history backfills the cursor without changing coverage.
	if _, err := conn.Exec(t.Context(), `ALTER TABLE public.almanac_sli_measurements DROP COLUMN last_attempted_interval_end`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `DROP TABLE public.almanac_slo_overview_snapshot_items, public.almanac_slo_overview_snapshots, public.almanac_objective_create_receipts, public.almanac_slo_definitions, public.almanac_slos, public.almanac_services CASCADE; DROP INDEX public.almanac_agent_ownership_proof_idx, public.almanac_sli_coverage_lease_idx, public.almanac_sli_execution_attempts_lease_idx, public.almanac_sli_delivery_receipts_lease_idx; DELETE FROM public.almanac_schema_migrations WHERE version IN (16, 17, 18, 19, 20)`); err != nil {
		t.Fatal(err)
	}
	if result, err := Upgrade(t.Context(), f.databaseURL); err != nil || result.Applied != 5 || result.DatabaseSchemaVersion != 20 {
		t.Fatalf("cursor migration: %+v, %v", result, err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT last_attempted_interval_end FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, record.ID).Scan(&cursor); err != nil || !cursor.Equal(empty.IntervalEnd) {
		t.Fatalf("backfilled cursor = %v, %v", cursor, err)
	}
	delayed, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Secrets: secrets, Name: "delayed-ack", DisplayName: "Delayed ACK", DatasourceID: source.ID,
		MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "delayed-ack", RequestID: "delayed-ack", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	expectedStart := sli.LatestSettledEnd(delayed.CreatedAt, 15, 60).Add(-15 * time.Second)
	if !delayed.EffectiveFrom.Equal(expectedStart) || delayed.ActivatedAt != nil {
		t.Fatalf("created validity/activation = %+v", delayed)
	}
	now = delayed.CreatedAt.Add(3 * time.Minute)
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, delayed.ConfigurationRevision, clock); err != nil {
		t.Fatalf("delayed acknowledgement: %v", err)
	}
	delayedAfter, err := GetSli(t.Context(), f.databaseURL, proof, delayed.ID, clock)
	if err != nil || !delayedAfter.EffectiveFrom.Equal(expectedStart) || delayedAfter.ActivatedAt == nil || !delayedAfter.ActivatedAt.Equal(now) {
		t.Fatalf("delayed activation shifted validity: %+v, %v", delayedAfter, err)
	}
	renewed, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, 1, ownershipNonce(92), clock)
	if err != nil {
		t.Fatalf("renewal after persisted observations: %v", err)
	}
	var renewedGrants []agentprotocol.MeasurementGrant
	if err := json.Unmarshal(renewed.AllowedMeasurements, &renewedGrants); err != nil {
		t.Fatal(err)
	}
	foundCursor := false
	for _, g := range renewedGrants {
		if g.SLIID == record.ID && g.LastAttemptedIntervalEnd != nil && g.LastAttemptedIntervalEnd.Equal(empty.IntervalEnd) {
			foundCursor = true
		}
	}
	if !foundCursor {
		t.Fatalf("renewed grant did not carry last persisted attempt: %+v", renewedGrants)
	}
	// Keep the original cursor migration and renewal assertions above before
	// inserting the later observation used for the snapshot isolation test.
	snapshotConn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(snapshotConn)
	snapshotTx, err := snapshotConn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshotTx.Rollback(t.Context())
	snapshotQueries := dbgen.New(snapshotTx)
	sliID, _ := canonicalUUID(record.ID)
	nextEnd := empty.IntervalEnd.Add(15 * time.Second)
	aggregateBefore, err := snapshotQueries.AggregateSloObservations(t.Context(), dbgen.AggregateSloObservationsParams{SliID: sliID, MeasurementRevision: 1, IntervalStart: timestamp(fresh.IntervalStart), IntervalEnd: timestamp(nextEnd)})
	if err != nil {
		t.Fatalf("snapshot aggregate: %v", err)
	}
	inserted := empty
	inserted.DeliveryID = uuid.NewString()
	inserted.ExecutionID = uuid.NewString()
	inserted.CreatedAt = now
	inserted.IntervalStart = empty.IntervalEnd
	inserted.IntervalEnd = nextEnd
	inserted.Outcome = sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "1", TotalCount: "1"}
	if result := ingest(inserted); result.Status != "accepted" {
		t.Fatalf("concurrent snapshot insert: %+v", result)
	}
	snapshotFacts, err := snapshotQueries.ListSloTimelineFacts(t.Context(), dbgen.ListSloTimelineFactsParams{SliID: sliID, MeasurementRevision: 1, IntervalStart: timestamp(fresh.IntervalStart), IntervalEnd: timestamp(nextEnd)})
	if err != nil || int64(len(snapshotFacts)) != aggregateBefore.ObservedIntervals+aggregateBefore.KnownEmptyIntervals {
		t.Fatalf("snapshot mixed generations: aggregate=%+v facts=%d error=%v", aggregateBefore, len(snapshotFacts), err)
	}
	if err := snapshotTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
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
