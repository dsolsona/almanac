//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
)

// This is a retained-history correctness/restart regression, not the controlled
// HTTP/browser capacity workload. Setup and cold preparation are timed separately.
func verifyDerivedRetainedHistoryRegression(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "derived-retention")
	clock := func() time.Time { return f.base.Add(5 * time.Second) }
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	resume := ownershipVerifier(ownershipBytes(32, 39))
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, &AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "retention-test", Capabilities: capabilities}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, 1, clock); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var leaseID string
	if err := conn.QueryRow(t.Context(), `SELECT lease_id FROM public.almanac_agent_execution_grants WHERE agent_id=$1 AND generation=$2 ORDER BY configuration_revision LIMIT 1`, created.Agent.ID, grant.Generation).Scan(&leaseID); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: f.verifier}
	service, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "retention-service", DisplayName: "Retention service", IdempotencyKey: "retention-service", RequestID: "retention-service", Now: clock}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name    string
		cadence int
		days    int
	}{{"dense-fourteen-days", 15, 14}, {"minute-fifty-six-days", 60, 56}} {
		t.Run(fixture.name, func(t *testing.T) {
			setupAt := time.Now()
			settings := datasource.DefaultSettings(fixture.name, fixture.name, "https://metrics.example.test", created.Agent.ID)
			settings.PollingIntervalSeconds = fixture.cadence
			source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: fixture.name, RequestID: fixture.name, Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			measurement, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: proof, Name: fixture.name, DisplayName: fixture.name, DatasourceID: source.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: fixture.name, RequestID: fixture.name, Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			end := floorSliBoundary(clock(), time.Duration(fixture.cadence)*time.Second)
			start := end.Add(-time.Duration(fixture.days) * 24 * time.Hour)
			tx, err := conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			for month := start; !month.After(end); month = month.AddDate(0, 1, 0) {
				if err := prepareSliPartitions(t.Context(), tx, month); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$2 WHERE sli_id=$1 AND revision=1`, measurement.ID, start); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Keep real canonical triggers enabled. Hour-sized transactions avoid
			// an 80,640-update chain on one source-generation row during setup.
			for chunkStart := start; chunkStart.Before(end); chunkStart = chunkStart.Add(time.Hour) {
				chunkEnd := chunkStart.Add(time.Hour)
				if chunkEnd.After(end) {
					chunkEnd = end
				}
				batch, err := conn.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := batch.Exec(t.Context(), `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,$1,1,point+$7::bigint*interval '1 second','observed',decode(repeat('cd',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),$2,$3,1,$4,point,point+$7::bigint*interval '1 second'
FROM generate_series($5::timestamptz,$6::timestamptz-$7::bigint*interval '1 second',$7::bigint*interval '1 second') point`, measurement.ID, created.Agent.ID, grant.Generation, leaseID, chunkStart, chunkEnd, fixture.cadence); err != nil {
					rollbackTransaction(batch)
					t.Fatal(err)
				}
				if _, err := batch.Exec(t.Context(), `INSERT INTO public.almanac_sli_observation_values(interval_start,sli_id,measurement_revision,good_count,total_count)
SELECT interval_start,$1,1,CASE WHEN interval_start>=$2::timestamptz-interval '2 hours' THEN 98.125 ELSE 99.999 END,100.125
FROM public.almanac_sli_coverage WHERE sli_id=$1 AND measurement_revision=1 AND interval_start>=$3 AND interval_start<$4`, measurement.ID, end, chunkStart, chunkEnd); err != nil {
					rollbackTransaction(batch)
					t.Fatal(err)
				}
				if err := batch.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			_, objective, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: fixture.name, DisplayName: fixture.name, ServiceID: service.ID, SliID: measurement.ID, Target: "0.995", RollingWindowSeconds: 28 * 86400, EvaluationValidFrom: &start, IdempotencyKey: fixture.name, RequestID: fixture.name, Now: clock}, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("retained fixture cadence=%ds days=%d canonical_intervals=80640 setup=%s", fixture.cadence, fixture.days, time.Since(setupAt))
			logRetainedFactsPlan(t, conn, measurement.ID, start, end, fixture.cadence)
			runtime, err := OpenRuntime(t.Context(), f.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			coldAt := time.Now()
			for batch := range 32 {
				count, err := runtime.rebuildDerivedBatch(t.Context(), clock)
				if err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					break
				}
				if batch == 31 {
					t.Fatal("source preparation exceeded bounded fixture drain")
				}
			}
			pgID, _ := canonicalUUID(objective.ID)
			if err := runtime.refreshCurrentProjection(t.Context(), pgID, clock); err != nil {
				t.Fatal(err)
			}
			input := OperationalDetailInput{Runtime: runtime, Proof: proof, ID: objective.ID, Start: &start, End: &end, At: &end, BurnWindow: 3600, Now: clock}
			if _, err := ReadSloOperationalDetail(t.Context(), f.databaseURL, input); !errors.Is(err, ErrDerivedPending) {
				t.Fatalf("cold retained read did not queue history: %v", err)
			}
			if err := runtime.prepareNextHistory(t.Context(), clock); err != nil {
				t.Fatal(err)
			}
			prepared, err := ReadSloOperationalDetail(t.Context(), f.databaseURL, input)
			if err != nil || prepared.Generation == nil {
				t.Fatalf("retained history not published: %v", err)
			}
			t.Logf("retained fixture cadence=%ds cold_preparation=%s represented_budget=%d", fixture.cadence, time.Since(coldAt), len(prepared.BudgetSeries.Samples))
			// Close every pool and reopen against the persisted cache. A restart
			// must not discard the pinned generation or enqueue replacement work.
			runtime.Close()
			restarted, err := OpenRuntime(t.Context(), f.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			input.Runtime = restarted
			replayed, err := ReadSloOperationalDetail(t.Context(), f.databaseURL, input)
			if err != nil || !reflect.DeepEqual(replayed, prepared) {
				t.Fatalf("restart changed ready retained history: %v", err)
			}
		})
	}
}
