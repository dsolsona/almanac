//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
)

func TestIntegrationRetainedDailyMetAndShortenedMatchRawOracle(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "daily-retained-oracle", true)
	end := utcDayStart(s.at).Add(24 * time.Hour)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO public.almanac_sli_observations
(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,c.sli_id,c.measurement_revision,'observed',990,1000,c.result_digest,'{}',
gen_random_uuid(),gen_random_uuid(),c.agent_id,c.session_generation,c.configuration_revision,c.lease_id,point+interval '1 minute',point+interval '1 minute'
FROM (SELECT * FROM public.almanac_sli_observations WHERE sli_id=$1 LIMIT 1)c
CROSS JOIN generate_series($2::timestamptz-interval '1 hour',$2::timestamptz-interval '1 minute',interval '1 minute')point`, s.sliID, end); err != nil {
		t.Fatal(err)
	}
	s.now = end.Add(90 * time.Second)
	s.at = s.now
	met := s.createSLO(t, "daily-met")
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	row, err := dbgen.New(s.conn).GetSlo(t.Context(), mustPGUUID(met))
	if err != nil {
		t.Fatal(err)
	}
	shortFrom := end.Add(-30 * time.Minute)
	_, shortSLO, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "daily-shortened", DisplayName: "Daily shortened", ServiceID: row.ServiceID.String(), SliID: s.sliID, Target: "0.99", RollingWindowSeconds: 3600, EvaluationValidFrom: &shortFrom, IdempotencyKey: "daily-shortened", RequestID: "daily-shortened", Now: s.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOperationalOverviewForTest(t.Context(), s.f.databaseURL, OperationalOverviewInput{Proof: proof, Now: s.clock}); err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	start := end.Add(-24 * time.Hour)
	for _, fixture := range []struct{ id, status, reason string }{{met, "met", ""}, {shortSLO.ID, "unknown", "shortened_window"}} {
		input := OperationalDetailInput{Proof: proof, ID: fixture.id, At: &end, Start: &start, End: &end, BurnWindow: 3600, Now: s.clock}
		want, err := readOperationalDetailForTest(t.Context(), s.f.databaseURL, input)
		if err != nil {
			t.Fatal(err)
		}
		input.Runtime = s.runtime
		got, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
		if err != nil || got.HistoryPreparation != nil || !reflect.DeepEqual(got.ComplianceSeries, want.ComplianceSeries) || !reflect.DeepEqual(got.ComplianceSummary, want.ComplianceSummary) {
			t.Fatalf("retained daily %s differs from raw fixture: got=%+v want=%+v err=%v", fixture.status, got.ComplianceSeries, want.ComplianceSeries, err)
		}
		if len(got.ComplianceSeries.Days) != 1 || string(got.ComplianceSeries.Days[0].Status) != fixture.status || fixture.reason != "" && got.ComplianceSeries.Days[0].Explanation != fixture.reason {
			t.Fatalf("unexpected daily fixture: %+v", got.ComplianceSeries)
		}
	}
}

func TestIntegrationOperationalOverviewGroupsAndIndependentBurnWindows(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "operational-agent")
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	resume := ownershipVerifier(ownershipBytes(32, 76))
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume,
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, 1, clock); err != nil {
		t.Fatal(err)
	}
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: DatasourceProof{SessionVerifier: f.verifier},
		Settings: datasource.DefaultSettings("operational-metrics", "Operational Metrics", "https://metrics.example.test", created.Agent.ID),
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "operational-source", RequestID: "operational-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	createSLI := func(name string) SliRecord {
		t.Helper()
		value, createErr := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: DatasourceProof{SessionVerifier: f.verifier}, Name: name, DisplayName: name,
			DatasourceID: source.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount,
			GoodQuery: "good", TotalQuery: "total", IdempotencyKey: name, RequestID: name, Now: clock})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return value
	}
	badSLI := createSLI("operational-bad")
	goodSLI := createSLI("operational-good")
	start := f.base.Add(6 * time.Hour)
	at := start.Add(6 * time.Hour)
	historyStart := start.Add(-time.Hour)
	missingAt := start.Add(-30 * time.Minute)
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	for _, measurement := range []SliRecord{badSLI, goodSLI} {
		if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$1 WHERE sli_id=$2 AND revision=1`, historyStart, measurement.ID); err != nil {
			t.Fatal(err)
		}
	}
	var leaseID string
	if err := conn.QueryRow(t.Context(), `SELECT lease_id FROM public.almanac_agent_execution_grants WHERE agent_id=$1 AND generation=$2 ORDER BY configuration_revision LIMIT 1`, created.Agent.ID, grant.Generation).Scan(&leaseID); err != nil {
		t.Fatal(err)
	}
	seed := func(measurement SliRecord, bad bool) {
		t.Helper()
		_, seedErr := execObservationFixture(t, conn, measurement.ID, `
INSERT INTO public.almanac_sli_observations
(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
 execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,$1,1,'observed',
 CASE WHEN NOT $8 THEN 1000 WHEN point=$6::timestamptz-interval '1 minute' THEN 990
 WHEN point>=$6::timestamptz-interval '6 hours' AND point<$6::timestamptz-interval '6 hours'+interval '5 minutes' THEN 1000 ELSE 0 END,
 CASE WHEN NOT $8 THEN 1000 WHEN point=$6::timestamptz-interval '1 minute' THEN 1000
 WHEN point>=$6::timestamptz-interval '6 hours' AND point<$6::timestamptz-interval '6 hours'+interval '5 minutes' THEN 1000 ELSE 0 END,
 decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),$2,$3,1,$4,point,point+interval '1 minute'
FROM generate_series($5::timestamptz,$6::timestamptz-interval '1 minute',interval '1 minute') point
WHERE NOT (NOT $8 AND point=$7::timestamptz)`, measurement.ID, created.Agent.ID, grant.Generation, leaseID, historyStart, at, missingAt, bad)
		if seedErr != nil {
			t.Fatal(seedErr)
		}
	}
	seed(badSLI, true)
	seed(goodSLI, false)
	now = at
	var freshVerifier [32]byte
	freshVerifier[0] = 99
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch,
		ExpectedCredentialRevision: f.revision, Verifier: freshVerifier, Now: at.Add(-time.Minute), RequestID: "operational-fresh-session"}); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: freshVerifier}
	service, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "operational-service", DisplayName: "Operational Service", RequestID: "operational-service", IdempotencyKey: "operational-service", Now: clock}, false)
	if err != nil {
		t.Fatal(err)
	}
	createSLO := func(name, sliID string, validFrom time.Time) SloRecord {
		t.Helper()
		_, record, createErr := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name, DisplayName: name,
			ServiceID: service.ID, SliID: sliID, Target: "0.99", RollingWindowSeconds: int64(6 * time.Hour / time.Second), EvaluationValidFrom: &validFrom,
			IdempotencyKey: name, RequestID: name, Now: clock}, true)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return record
	}
	attention := createSLO("operational-attention", badSLI.ID, start)
	healthy := createSLO("operational-healthy", goodSLI.ID, historyStart)
	watch := createSLO("operational-watch", goodSLI.ID, at.Add(-30*time.Minute))
	readAt := at
	readNow := func() time.Time { return readAt }
	t.Run("derived aggregate, durable preparation and recovery", func(t *testing.T) {
		verifyDerivedOperationalRecovery(t, f.databaseURL, proof, attention.ID, readNow)
	})
	t.Run("blocked current work remains fair and exposes failures", func(t *testing.T) {
		verifyBlockedCurrentFairnessAndFailureStates(t, f.databaseURL, proof, attention.ID, healthy.ID, readNow)
	})
	t.Run("cancelled filter releases its reserved snapshot", func(t *testing.T) {
		lockTx, err := conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer lockTx.Rollback(t.Context())
		if _, err := lockTx.Exec(t.Context(), "LOCK TABLE public.almanac_slos IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		_, err = ReadSloOperationalOverview(ctx, f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 50, Now: readNow})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancelled filter returned %v", err)
		}

	})
	var page api.SloOperationalOverviewPage
	for refresh := range 5 {
		page, err = readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 50, Now: readNow})
		if err != nil {
			t.Fatalf("single-page refresh %d: %v", refresh+1, err)
		}

	}
	if page.GroupCounts.NeedsAttention != 1 || page.GroupCounts.Watch != 1 || page.GroupCounts.Healthy != 1 || len(page.Items) != 3 || page.NextCursor != "" {
		t.Fatalf("overview groups/items = %+v/%d cursor=%q", page.GroupCounts, len(page.Items), page.NextCursor)
	}
	first, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 1, Now: readNow})
	if err != nil || first.NextCursor == "" || first.PreviousCursor != "" {
		t.Fatalf("first paginated overview = %+v, %v", first, err)
	}
	if _, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 1, Cursor: first.NextCursor, Query: "different", Now: readNow}); !errors.Is(err, ErrOperationalSnapshot) {
		t.Fatalf("snapshot filter binding error = %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slos SET display_name='Healthy metadata changed',version=version+1,updated_at=$2 WHERE id=$1`, healthy.ID, at); err != nil {
		t.Fatal(err)
	}
	readAt = readAt.Add(time.Second)
	if _, err := execObservationFixture(t, conn, goodSLI.ID, `
INSERT INTO public.almanac_sli_observations
    (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
VALUES ($1::timestamptz,$2,1,'observed',1000,1000,decode(repeat('cd',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),$3,$4,1,$5,$1::timestamptz,$6::timestamptz)`, missingAt, goodSLI.ID, created.Agent.ID, grant.Generation, leaseID, at); err != nil {
		t.Fatal(err)
	}
	secondInput := OperationalOverviewInput{Proof: proof, Limit: 1, Cursor: first.NextCursor, Now: readNow}
	second, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, secondInput)
	if err != nil || second.NextCursor == "" || second.PreviousCursor == "" {
		t.Fatalf("second paginated overview = %+v, %v", second, err)
	}
	replayedSecond, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, secondInput)
	if err != nil || len(replayedSecond.Items) != 1 || replayedSecond.Items[0].Slo.Id != second.Items[0].Slo.Id || replayedSecond.NextCursor != second.NextCursor {
		t.Fatalf("replayed second page = %+v, %v", replayedSecond, err)
	}
	previousPage, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 1, Cursor: second.PreviousCursor, Now: readNow})
	if err != nil || len(previousPage.Items) != 1 || previousPage.Items[0].Slo.Id != first.Items[0].Slo.Id {
		t.Fatalf("previous page = %+v, %v", previousPage, err)
	}
	thirdInput := OperationalOverviewInput{Proof: proof, Limit: 1, Cursor: second.NextCursor, Now: readNow}
	third, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, thirdInput)
	if err != nil || len(third.Items) != 1 || third.Items[0].Slo.Name != healthy.Name || third.Items[0].Slo.DisplayName != "Healthy metadata changed" || third.NextCursor != "" {
		t.Fatalf("third page did not read live metadata: %+v, %v", third, err)
	}
	replayedThird, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, thirdInput)
	if err != nil || len(replayedThird.Items) != 1 || replayedThird.Items[0].Slo.DisplayName != "Healthy metadata changed" {
		t.Fatalf("replayed final page = %+v, %v", replayedThird, err)
	}
	refreshed, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 50, Query: healthy.Name, Now: readNow})
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Slo.DisplayName != "Healthy metadata changed" {
		t.Fatalf("refreshed overview after late write = %+v, %v", refreshed, err)
	}
	for _, sample := range refreshed.Items[0].BudgetSparkline {
		if sample.State == "partial" && !sample.EvaluationAt.Before(missingAt) && sample.EvaluationAt.Before(missingAt.Add(6*time.Hour+time.Minute)) {
			t.Fatal("new snapshot kept the cleared historical gap marker")
		}
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_users SET role='editor' WHERE id=$1`, f.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 1, Cursor: first.NextCursor, Now: readNow}); err != nil {
		t.Fatalf("authorized current role could not use the keyset cursor: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_users SET role='administrator' WHERE id=$1`, f.userID); err != nil {
		t.Fatal(err)
	}
	detail, err := readOperationalDetailForTest(t.Context(), f.databaseURL, OperationalDetailInput{Proof: proof, ID: attention.ID, At: &at, Start: &start, End: &at, BurnWindow: 3600, Now: readNow})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Burn1h.Performance.Burn == nil || detail.Burn6h.Performance.Burn == nil || !sameDecimal(*detail.Burn1h.Performance.Burn, "1") || !sameDecimal(*detail.Burn6h.Performance.Burn, "0.166666666667") {
		t.Fatalf("independent burn windows = 1h:%v 6h:%v", detail.Burn1h.Performance.Burn, detail.Burn6h.Performance.Burn)
	}
	if detail.Burn1h.Performance.Bad == nil || detail.Burn6h.Performance.Bad == nil || *detail.Burn1h.Performance.Bad != "10.000000000000" || *detail.Burn6h.Performance.Bad != "10.000000000000" {
		t.Fatalf("bad counts = 1h:%v 6h:%v", detail.Burn1h.Performance.Bad, detail.Burn6h.Performance.Bad)
	}
	if detail.Current.Performance.Total == nil || *detail.Current.Performance.Total != "6000.000000000000" || detail.Current.Performance.Good == nil || *detail.Current.Performance.Good != "5990.000000000000" {
		t.Fatalf("six-hour current totals = good:%v total:%v", detail.Current.Performance.Good, detail.Current.Performance.Total)
	}
	if _, err := execObservationFixture(t, conn, badSLI.ID, `UPDATE public.almanac_sli_observations SET ingestion_generation=0,good_count=CASE WHEN interval_start >= $3::timestamptz - interval '1 hour' THEN 1000 ELSE 0 END,total_count=1000 WHERE sli_id=$1 AND interval_start >= $2 AND interval_start < $3`, badSLI.ID, start, at); err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, conn, goodSLI.ID, `UPDATE public.almanac_sli_observations SET ingestion_generation=0,good_count=990,total_count=1000 WHERE sli_id=$1 AND interval_start >= $2 AND interval_start < $3`, goodSLI.ID, start, at); err != nil {
		t.Fatal(err)
	}
	var finalHourGood, finalHourTotal string
	if err := conn.QueryRow(t.Context(), `SELECT sum(good_count)::text,sum(total_count)::text FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start >= $2::timestamptz - interval '1 hour' AND interval_start < $2`, badSLI.ID, at).Scan(&finalHourGood, &finalHourTotal); err != nil || !sameDecimal(finalHourGood, "60000") || !sameDecimal(finalHourTotal, "60000") {
		t.Fatalf("standalone final-hour success = %s/%s, %v", finalHourGood, finalHourTotal, err)
	}
	dailyRange := OperationalDetailInput{Proof: proof, ID: attention.ID, At: &at, Start: &start, End: &at, BurnWindow: 3600, Now: readNow}
	violatedDaily, err := readOperationalDetailForTest(t.Context(), f.databaseURL, dailyRange)
	if err != nil {
		t.Fatalf("daily violated detail: %v", err)
	}
	if len(violatedDaily.ComplianceSeries.Days) != 1 || violatedDaily.ComplianceSeries.Days[0].Status != api.SloDailyComplianceDayStatusViolated ||
		!violatedDaily.ComplianceSeries.Days[0].RequestedAt.Equal(at) || !violatedDaily.ComplianceSeries.Days[0].DayEnd.Equal(at) ||
		!violatedDaily.ComplianceSeries.Days[0].EffectiveAt.Equal(at) || violatedDaily.ComplianceSummary.EvaluableDays != 1 || violatedDaily.ComplianceSummary.ViolatedDays != 1 ||
		violatedDaily.ComplianceSeries.Days[0].Performance.Attainment == nil || !sameDecimal(*violatedDaily.ComplianceSeries.Days[0].Performance.Attainment, "0.166666666667") {
		t.Fatalf("completed rolling violation = %+v / %+v", violatedDaily.ComplianceSeries, violatedDaily.ComplianceSummary)
	}
	dailyRange.ID = healthy.ID
	metDaily, err := readOperationalDetailForTest(t.Context(), f.databaseURL, dailyRange)
	if err != nil || len(metDaily.ComplianceSeries.Days) != 1 || metDaily.ComplianceSeries.Days[0].Status != api.SloDailyComplianceDayStatusMet || metDaily.ComplianceSummary.MetDays != 1 ||
		metDaily.ComplianceSeries.Days[0].Performance.Attainment == nil || !sameDecimal(*metDaily.ComplianceSeries.Days[0].Performance.Attainment, "0.99") {
		t.Fatalf("completed rolling exact target result = %+v / %+v, %v", metDaily.ComplianceSeries, metDaily.ComplianceSummary, err)
	}
	dailyRange.ID = watch.ID
	shortenedDaily, err := readOperationalDetailForTest(t.Context(), f.databaseURL, dailyRange)
	if err != nil || len(shortenedDaily.ComplianceSeries.Days) != 1 || shortenedDaily.ComplianceSeries.Days[0].Status != api.SloDailyComplianceDayStatusUnknown ||
		!shortenedDaily.ComplianceSeries.Days[0].Performance.Provisional || shortenedDaily.ComplianceSeries.Days[0].Explanation != "shortened_window" || shortenedDaily.ComplianceSummary.UnknownDays != 1 {
		t.Fatalf("shortened daily rolling result = %+v / %+v, %v", shortenedDaily.ComplianceSeries, shortenedDaily.ComplianceSummary, err)
	}
	if _, err := execObservationFixture(t, conn, badSLI.ID, `UPDATE public.almanac_sli_observations SET ingestion_generation=0,
good_count=CASE WHEN interval_start=$2::timestamptz-interval '1 minute' THEN 990 WHEN interval_start >= $2::timestamptz-interval '6 hours' AND interval_start < $2::timestamptz-interval '6 hours'+interval '5 minutes' THEN 1000 ELSE 0 END,
total_count=CASE WHEN interval_start=$2::timestamptz-interval '1 minute' THEN 1000 WHEN interval_start >= $2::timestamptz-interval '6 hours' AND interval_start < $2::timestamptz-interval '6 hours'+interval '5 minutes' THEN 1000 ELSE 0 END
WHERE sli_id=$1 AND interval_start >= $3 AND interval_start < $2`, badSLI.ID, at, start); err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, conn, goodSLI.ID, `UPDATE public.almanac_sli_observations SET ingestion_generation=0,good_count=1000,total_count=1000 WHERE sli_id=$1 AND interval_start >= $2 AND interval_start < $3`, goodSLI.ID, start, at); err != nil {
		t.Fatal(err)
	}
	activationOnly := createSLO("operational-activation-only", goodSLI.ID, at)
	activationDetail, err := readOperationalDetailForTest(t.Context(), f.databaseURL, OperationalDetailInput{Proof: proof, ID: activationOnly.ID, Now: readNow})
	if err != nil {
		t.Fatalf("new objective detail with pre-validity SLI facts: %v", err)
	}
	for _, sample := range activationDetail.AttainmentSeries.Samples {
		if sample.State != "not_applicable" || sample.Quality.ObservedIntervals != 0 || sample.Good != nil {
			t.Fatalf("pre-validity observation entered attainment sample: %+v", sample)
		}
	}
	if len(activationDetail.ComplianceSeries.Days) != 7 || activationDetail.ComplianceSummary.NotApplicableDays != 7 || activationDetail.ComplianceSeries.Days[len(activationDetail.ComplianceSeries.Days)-1].Status != api.SloDailyComplianceDayStatusNotApplicable {
		t.Fatalf("pre-validity completed day classification = %+v / %+v", activationDetail.ComplianceSeries, activationDetail.ComplianceSummary)
	}
	longHistory := at.Add(-40 * 24 * time.Hour)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$1 WHERE sli_id=$2 AND revision=1`, longHistory, goodSLI.ID); err != nil {
		t.Fatal(err)
	}
	irrelevantOldFact := at.Add(-30 * 24 * time.Hour)
	partitionTx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSliPartitions(t.Context(), partitionTx, irrelevantOldFact); err != nil {
		t.Fatalf("prepare old-fact partitions: %v", err)
	}
	if err := partitionTx.Commit(t.Context()); err != nil {
		t.Fatalf("commit old-fact partitions: %v", err)
	}
	if _, err := execObservationFixture(t, conn, goodSLI.ID, `
INSERT INTO public.almanac_sli_observations
    (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
VALUES ($1::timestamptz,$2,1,'observed',1000,1000,decode(repeat('ef',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),$3,$4,1,$5,$1::timestamptz,$1::timestamptz + interval '1 minute')`, irrelevantOldFact, goodSLI.ID, created.Agent.ID, grant.Generation, leaseID); err != nil {
		t.Fatal(err)
	}
	longObjective := createSLO("operational-long-horizon", goodSLI.ID, longHistory)
	if longObjective.ID == "" {
		t.Fatal("long-horizon objective was not created")
	}
	readAt = at.Add(2*time.Minute + time.Microsecond)
	if _, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 50, Now: readNow}); err != nil {
		t.Fatalf("overview with out-of-horizon old fact: %v", err)
	}
	forbidden, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 1, Now: readNow})
	if err != nil || forbidden.NextCursor == "" {
		t.Fatalf("revocation fixture snapshot = %+v, %v", forbidden, err)
	}
	revokedAt := readAt.Add(time.Second)
	if revoked, err := RevokeSession(t.Context(), f.databaseURL, freshVerifier, revokedAt, "revoke-operational-session"); err != nil || !revoked {
		t.Fatalf("revoke operational session = %t, %v", revoked, err)
	}
	if _, err := readOperationalOverviewForTest(t.Context(), f.databaseURL, OperationalOverviewInput{Proof: proof, Limit: 1, Cursor: forbidden.NextCursor, Now: func() time.Time { return revokedAt.Add(time.Second) }}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session snapshot error = %v", err)
	}
	groups := map[string]string{}
	for _, item := range page.Items {
		groups[item.Slo.Name] = string(item.Urgency)
	}
	if groups[attention.Name] != "needs_attention" || groups[watch.Name] != "watch" || groups[healthy.Name] != "healthy" {
		t.Fatalf("overview classifications = %v", groups)
	}
}

func sameDecimal(actual, expected string) bool {
	a, okA := new(big.Rat).SetString(actual)
	b, okB := new(big.Rat).SetString(expected)
	return okA && okB && a.Cmp(b) == 0
}
