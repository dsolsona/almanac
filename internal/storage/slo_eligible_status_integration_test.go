//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationNewSloUsesCompleteDueMeasurementsWithoutWaitingForFullWindow(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "eligible-status", true)
	valid := s.at.Add(-9 * time.Minute)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations
SET good_count=99,total_count=100 WHERE sli_id=$1 AND interval_start>=$2`, s.sliID, valid); err != nil {
		t.Fatal(err)
	}
	template := s.createSLOWithHistory(t, "eligible-template", time.Hour, valid)
	row, err := dbgen.New(s.conn).GetSlo(t.Context(), mustPGUUID(template))
	if err != nil {
		t.Fatal(err)
	}
	phase := sli.DispatchPhase(s.sliID, 60)
	s.now = s.at.Add(2*time.Minute + phase)
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	_, objective, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{
		Proof: proof, Name: "eligible-98", DisplayName: "Eligible 98%", ServiceID: row.ServiceID.String(), SliID: s.sliID,
		Target: "0.98", RollingWindowSeconds: 28 * 86400, EvaluationValidFrom: &valid,
		IdempotencyKey: "eligible-98", RequestID: "eligible-98", Now: s.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	s.drain(t, "nine eligible observations")
	status, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: objective.ID, Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	q, p := status.Current.Quality, status.Current.Performance
	if status.Urgency != "healthy" || q.State != "complete" || q.Freshness != "fresh" || q.ExpectedIntervals != 9 || q.ObservedIntervals != 9 || q.MissingIntervals != 0 ||
		p.RemainingFraction == nil || *p.RemainingFraction != "0.500000000000" || p.Burn == nil || *p.Burn != "0.500000000000" ||
		status.Burn1h.Performance.Provisional || !status.Current.Window.End.Equal(s.at) || !status.Generation.EvaluatedThrough.Equal(status.Current.EvaluationAt) {
		t.Fatalf("nine 99/100 observations against 98%%: urgency=%s current=%+v burn=%+v", status.Urgency, status.Current, status.Burn1h)
	}
	for _, at := range []*time.Time{nil, &status.Current.Window.End} {
		evaluation, err := EvaluateSlo(t.Context(), s.f.databaseURL, EvaluateSloInput{Proof: proof, ID: objective.ID, At: at, Now: s.clock})
		if err != nil || evaluation.Quality.ExpectedIntervals != 9 || evaluation.Quality.ObservedIntervals != 9 || evaluation.Performance.RemainingFraction == nil ||
			*evaluation.Performance.RemainingFraction != "0.500000000000" || !evaluation.Window.End.Equal(status.Current.Window.End) {
			t.Fatalf("current/canonical evaluation parity at %v: %+v err=%v", at, evaluation, err)
		}
	}
	if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(objective.ID), s.clock); err != nil {
		t.Fatal(err)
	}
	published, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: objective.ID, Now: s.clock})
	if err != nil || published.Urgency != "healthy" || published.Current.Quality.ExpectedIntervals != 9 || !published.Current.Window.End.Equal(status.Current.Window.End) {
		t.Fatalf("published current lost eligible coverage: %+v, %v", published, err)
	}
	_, waiting, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{
		Proof: proof, Name: "eligible-new", DisplayName: "Just created", ServiceID: row.ServiceID.String(), SliID: s.sliID,
		Target: "0.98", RollingWindowSeconds: 28 * 86400,
		IdempotencyKey: "eligible-new", RequestID: "eligible-new", Now: s.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	waitingStatus, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: waiting.ID, Now: s.clock})
	if err != nil || waitingStatus.LeadingReason.Code != "not_yet_eligible" || waitingStatus.Current.Quality.ExpectedIntervals != 0 {
		t.Fatalf("existing SLI history must not be silently included: %+v, %v", waitingStatus, err)
	}
	// Freshness expires inside this snapshot tick, without another receipt.
	s.now = s.now.Add(time.Microsecond)
	expired, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: objective.ID, Now: s.clock})
	if err != nil || expired.Urgency == "healthy" || expired.Current.Quality.Freshness != "stale" {
		t.Fatalf("cached Healthy survived its freshness deadline: %+v, %v", expired, err)
	}
	page, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, OperationalOverviewInput{Runtime: s.runtime, Proof: proof, Query: objective.Name, Limit: 20, Now: s.clock})
	if err != nil || page.GroupCounts.Healthy != 0 {
		t.Fatalf("overview retained expired Healthy status: %+v, %v", page.GroupCounts, err)
	}
	due, err := dbgen.New(s.conn).ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), MetricsAt: timestamp(s.now.Add(time.Hour))})
	if err != nil || !due.CurrentDue {
		t.Fatalf("freshness expiry did not schedule publication: %+v, %v", due, err)
	}
	objectives, err := dbgen.New(s.conn).ListDueDerivedObjectives(t.Context(), dbgen.ListDueDerivedObjectivesParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), BatchLimit: 100})
	found := false
	for _, id := range objectives {
		found = found || id.String() == objective.ID
	}
	if err != nil || !found {
		t.Fatalf("expired objective was not selected for refresh: %v, %v", objectives, err)
	}
	// No new observations arrive: the clock must still reveal the next gap.
	s.now = s.now.Add(2 * time.Minute)
	status, err = ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: objective.ID, Now: s.clock})
	if err != nil || status.Urgency == "healthy" || status.Current.Quality.State != "partial" || status.Current.Quality.MissingIntervals != 1 || status.Current.Quality.Freshness != "stale" {
		t.Fatalf("stopped collection was hidden: %+v, %v", status, err)
	}
}

func TestIntegrationPreparedDetailSeparatesLiveAndExplicitHistoricalEndpoints(t *testing.T) {
	for _, liveFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "live-first", false: "historical-first"}[liveFirst], func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "cache-endpoint", true)
			id := s.createSLO(t, "cache-endpoint")
			freshUntil := s.at.Add(2*time.Minute + sli.DispatchPhase(s.sliID, 60))
			s.now = freshUntil.Add(-time.Microsecond)
			s.drain(t, "cache endpoints")
			for range 10 {
				worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
				if err != nil {
					t.Fatal(err)
				}
				if !worked {
					break
				}
			}
			var verifier [32]byte
			verifier[0] = 98
			start, end := s.at.Add(-time.Hour), s.at
			historicalAt := floorSliBoundary(s.now, time.Minute)
			for _, live := range []bool{liveFirst, !liveFirst} {
				input := OperationalDetailInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: id, Start: &start, End: &end, BurnWindow: 3600, Now: s.clock}
				wantEnd := s.at
				if !live {
					input.At, wantEnd = &historicalAt, historicalAt
				}
				value, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
				if err != nil || value.HistoryPreparation != nil || !value.Current.Window.End.Equal(wantEnd) {
					t.Fatalf("live=%v cache endpoint=%s want=%s pending=%+v err=%v", live, value.Current.Window.End, wantEnd, value.HistoryPreparation, err)
				}
			}
			s.now = freshUntil.Add(time.Microsecond)
			expired, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, OperationalDetailInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: id, Start: &start, End: &end, BurnWindow: 3600, Now: s.clock})
			if err != nil || expired.Current.Quality.Freshness != "stale" {
				t.Fatalf("prepared live detail survived freshness expiry: %+v, %v", expired.Current.Quality, err)
			}
		})
	}
}
