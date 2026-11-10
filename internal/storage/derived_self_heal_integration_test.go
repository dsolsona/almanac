//go:build integration

package storage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// selfHealFixture is one SLI with three hours of one-minute interval records (values optional) and a
// clock the test moves, for the ADR-0034 recovery tests.
type selfHealFixture struct {
	f            agentFixture
	conn         *pgx.Conn
	runtime      *Runtime
	sliID        string
	historyStart time.Time
	at           time.Time
	now          time.Time
	cadence      time.Duration
}

func (s *selfHealFixture) clock() time.Time { return s.now }

func newSelfHealFixture(t *testing.T, name string, withValues bool) *selfHealFixture {
	return newSelfHealFixtureCadence(t, name, withValues, time.Minute)
}

func newSelfHealFixtureCadence(t *testing.T, name string, withValues bool, cadence time.Duration) *selfHealFixture {
	t.Helper()
	f := newAgentFixture(t)
	s := &selfHealFixture{f: f, now: f.base.Add(5 * time.Second), cadence: cadence}
	issued, created := createOwnershipAgent(t, f, name+"-agent")
	capabilities, err := json.Marshal([]agentprotocol.Capability{{Provider: "prometheus", ConfigurationFormats: []string{"prometheus-http-v1"}, Operations: []string{"collect", "connection_test", "query_preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	resume := ownershipVerifier(ownershipBytes(32, 81))
	grant, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume,
		&AgentRuntimeHello{Protocol: agentprotocol.ProtocolV1, SoftwareVersion: "test", Capabilities: capabilities}, s.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier, resume, grant.Generation, 1, s.clock); err != nil {
		t.Fatal(err)
	}
	settings := datasource.DefaultSettings(name+"-metrics", "Self-heal Metrics", "https://metrics.example.test", created.Agent.ID)
	settings.PollingIntervalSeconds = int(cadence / time.Second)
	source, err := CreateDatasource(t.Context(), f.databaseURL, nil, CreateDatasourceInput{Proof: DatasourceProof{SessionVerifier: f.verifier},
		Settings: settings,
		Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: name + "-source", RequestID: name + "-source", Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := CreateSli(t.Context(), f.databaseURL, CreateSliInput{Proof: DatasourceProof{SessionVerifier: f.verifier}, Name: name, DisplayName: name,
		DatasourceID: source.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount,
		GoodQuery: "good", TotalQuery: "total", IdempotencyKey: name + "-sli", RequestID: name + "-sli", Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	s.sliID = measurement.ID
	s.historyStart = ceilSliBoundary(f.base.Add(time.Hour), cadence)
	s.at = s.historyStart.Add(3 * time.Hour)
	s.conn = integrationConnect(t, f.databaseURL)
	t.Cleanup(func() { closeIntegrationConn(s.conn) })
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$1 WHERE sli_id=$2 AND revision=1`, s.historyStart, s.sliID); err != nil {
		t.Fatal(err)
	}
	var leaseID string
	if err := s.conn.QueryRow(t.Context(), `SELECT lease_id FROM public.almanac_agent_execution_grants WHERE agent_id=$1 AND generation=$2 ORDER BY configuration_revision LIMIT 1`, created.Agent.ID, grant.Generation).Scan(&leaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `
INSERT INTO public.almanac_sli_coverage
    (interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point, $1, 1, point + make_interval(secs=>$7), 'observed', decode(repeat('ab',32),'hex'), '{}', gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), $2, $3, 1, $4, point, point + make_interval(secs=>$7)
FROM generate_series($5::timestamptz, $6::timestamptz - make_interval(secs=>$7), make_interval(secs=>$7)) point`,
		s.sliID, created.Agent.ID, grant.Generation, leaseID, s.historyStart, s.at, cadence.Seconds()); err != nil {
		t.Fatal(err)
	}
	if withValues {
		s.insertValues(t)
	}
	s.now = s.at
	s.runtime, err = OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.runtime.Close)
	return s
}

// insertValues commits 990 good of 1,000 for every interval, so every full window attains exactly 99%.
func (s *selfHealFixture) insertValues(t *testing.T) {
	t.Helper()
	if _, err := s.conn.Exec(t.Context(), `
INSERT INTO public.almanac_sli_observation_values(interval_start,sli_id,measurement_revision,good_count,total_count)
SELECT point, $1, 1, 990, 1000 FROM generate_series($2::timestamptz, $3::timestamptz - make_interval(secs=>$4), make_interval(secs=>$4)) point`,
		s.sliID, s.historyStart, s.at, s.cadence.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// drain runs the aggregator until nothing it may claim now remains.
func (s *selfHealFixture) drain(t *testing.T, stage string) {
	t.Helper()
	for range 200 {
		count, err := s.runtime.rebuildDerivedBatch(t.Context(), s.clock)
		if err != nil && !errors.Is(err, ErrDerivedPending) {
			t.Fatalf("%s: rebuild derived aggregates: %v", stage, err)
		}
		if count == 0 {
			return
		}
	}
	t.Fatalf("%s: derived aggregates did not drain", stage)
}

// createSLO creates a one-hour-window 99% SLO evaluated from the start of the history.
func (s *selfHealFixture) createSLO(t *testing.T, name string) string {
	t.Helper()
	window := ((time.Hour + s.cadence - 1) / s.cadence) * s.cadence
	return s.createSLOWithHistory(t, name, window, s.historyStart)
}

func (s *selfHealFixture) createSLOWithHistory(t *testing.T, name string, window time.Duration, validFrom time.Time) string {
	t.Helper()
	var verifier [32]byte
	verifier[0] = 98
	if _, err := IssueSession(t.Context(), s.f.databaseURL, IssueSessionInput{UserID: s.f.userID, ExpectedCredentialEpoch: s.f.epoch,
		ExpectedCredentialRevision: s.f.revision, Verifier: verifier, Now: s.at.Add(-time.Minute), RequestID: name + "-session"}); err != nil {
		t.Fatal(err)
	}
	proof := DatasourceProof{SessionVerifier: verifier}
	service, _, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name + "-service", DisplayName: "Self-heal Service",
		RequestID: name + "-service", IdempotencyKey: name + "-service", Now: s.clock}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, objective, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name + "-slo", DisplayName: "Self-heal SLO",
		ServiceID: service.ID, SliID: s.sliID, Target: "0.99", RollingWindowSeconds: int64(window / time.Second), EvaluationValidFrom: &validFrom,
		IdempotencyKey: name + "-slo", RequestID: name + "-slo", Now: s.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	return objective.ID
}

// buildHistory runs aggregation, current refresh and extrema until the closed hours are complete.
func (s *selfHealFixture) buildHistory(t *testing.T, sloID string) error {
	t.Helper()
	var lastErr error
	for range 400 {
		s.drain(t, "during repair")
		if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil && !errors.Is(err, ErrDerivedPending) {
			lastErr = err
		}
		if _, err := s.runtime.buildSloExtrema(t.Context(), s.clock); err != nil && !errors.Is(err, ErrDerivedPending) {
			lastErr = err
		}
		remaining := true
		if err := s.conn.QueryRow(t.Context(), `SELECT built_through<$2 OR coalesce(dirty_from<LEAST(dirty_to,$2),false) FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, sloID, s.now.Truncate(time.Hour)).Scan(&remaining); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if !remaining {
			return nil
		}
		s.now = s.now.Add(time.Minute)
	}
	if lastErr == nil {
		lastErr = errors.New("retained history did not complete")
	}
	return lastErr
}

// requireExactHistory checks each selected closed-hour mark against the fixture's exact 99% ratio.
func (s *selfHealFixture) requireExactHistory(t *testing.T, sloID string) {
	t.Helper()
	var points, wrong int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (
WHERE (mark->>'observed')::bigint IS DISTINCT FROM 60
OR (mark->>'good')::numeric IS DISTINCT FROM 59400
OR (mark->>'total')::numeric IS DISTINCT FROM 60000)
FROM public.almanac_slo_extrema e CROSS JOIN LATERAL jsonb_array_elements(e.lanes#>'{window,marks}') mark
WHERE slo_id=$1 AND resolution_seconds=3600
AND (mark->>'at')::timestamptz>$2::timestamptz+interval '1 hour' AND (mark->>'at')::timestamptz<=$3`, sloID, s.historyStart, s.at).Scan(&points, &wrong); err != nil {
		t.Fatal(err)
	}
	if points < 2 || wrong != 0 {
		t.Fatalf("extrema marks after the first full window: %d, with attainment other than 99%%: %d", points, wrong)
	}
}

// ADR-0034: the performance seed of 2026-11-06 committed interval records before their values; the
// aggregator ran in between and stored zero totals for observed intervals, and every calculation that
// read them failed until its work stopped for good. This reproduces that order: records, aggregation,
// values, then an SLO with a historical start whose long history must still be built correctly.
func TestIntegrationDerivedDataHealsWhenValuesArriveAfterAggregation(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "self-heal-late", false)
	s.drain(t, "before values")
	s.insertValues(t)
	s.drain(t, "after values")
	sloID := s.createSLO(t, "self-heal-late")
	if err := s.buildHistory(t, sloID); err != nil {
		t.Fatalf("retained history never completed; last error: %v", err)
	}
	s.requireExactHistory(t, sloID)
}

// ADR-0034: aggregates stored before the source fix (or corrupted any other way) are detected by the
// calculations that read them, rebuilt from the raw observations, and the history is still exact.
func TestIntegrationDerivedDataRebuildsInconsistentAggregates(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "self-heal-corrupt", true)
	s.drain(t, "initial aggregation")
	// The state the 2026-11-06 seed left behind: observed intervals with zero totals.
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_hourly_aggregates SET good=0,total=0,zero_traffic_intervals=0 WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	sloID := s.createSLO(t, "self-heal-corrupt")
	if err := s.buildHistory(t, sloID); err != nil {
		t.Fatalf("retained history never completed; last error: %v", err)
	}
	s.requireExactHistory(t, sloID)
}

// ADR-0034: failing work keeps being claimed after five failures on the retry schedule, stops after
// seven days of continuous failure, and starts again by itself once its SLI receives observations.
func TestIntegrationDerivedRetriesContinueStopAndRestart(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "self-heal-retry", true)
	s.drain(t, "initial aggregation")
	sloID := s.createSLO(t, "self-heal-retry")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil && !errors.Is(err, ErrDerivedPending) {
		t.Fatal(err)
	}
	claim := func() bool {
		t.Helper()
		_, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil && !errors.Is(err, ErrDerivedPending) {
			t.Fatalf("repair: %v", err)
		}
		var leased bool
		if err := s.conn.QueryRow(t.Context(), `SELECT attempts=0 AND failing_since IS NULL FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, sloID).Scan(&leased); err != nil {
			t.Fatal(err)
		}
		return leased
	}
	// Each step rewinds the extrema cursor to the start of the history, so there is work left to claim.
	setState := func(attempts int, failingSince *time.Time, next time.Time) {
		t.Helper()
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET attempts=$2,failing_since=$3,next_attempt_at=$4,lease_until=NULL,built_through=$5 WHERE slo_id=$1`,
			sloID, attempts, failingSince, next, s.historyStart.Truncate(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	// Failing for an hour after five failures: still claimed once its delay has passed, and a success
	// clears the failing state.
	hourAgo := s.now.Add(-time.Hour)
	setState(5, &hourAgo, s.now.Add(-time.Second))
	if !claim() {
		t.Fatal("work failing for an hour was not claimed again after its delay")
	}

	// Failing for eight days, with no observations since the failures began: stopped, not claimed,
	// and still stopped after the consistency check.
	eightDaysAgo := s.now.Add(-8 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_source_generations SET updated_at=$2 WHERE sli_id=$1`, s.sliID, eightDaysAgo.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	setState(5, &eightDaysAgo, s.now.Add(-time.Second))
	if claim() {
		t.Fatal("work failing for eight days was claimed")
	}
	if states, _, err := s.runtime.checkDerivedConsistency(t.Context(), s.now); err != nil || states.Stopped != 1 {
		t.Fatalf("consistency check did not report the stopped SLO: %+v, %v", states, err)
	}

	// New observations for its SLI restart it.
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_source_generations SET updated_at=$2 WHERE sli_id=$1`, s.sliID, s.now); err != nil {
		t.Fatal(err)
	}
	if _, restarted, err := s.runtime.checkDerivedConsistency(t.Context(), s.now); err != nil || restarted != 1 {
		t.Fatalf("consistency check did not restart the stopped work: restarted=%d, %v", restarted, err)
	}
	if !claim() {
		t.Fatal("restarted work was not claimed")
	}

	// The schedule after a failure: 2, 4, 8 and 16 seconds, then 5 minutes, then hourly after a day.
	for _, step := range []struct {
		attempts     int
		failingSince time.Duration
		want         time.Duration
	}{{1, 0, 2 * time.Second}, {4, 0, 16 * time.Second}, {5, time.Hour, 5 * time.Minute}, {5, 2 * 24 * time.Hour, time.Hour}} {
		since := s.now.Add(-step.failingSince)
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET attempts=$2,failing_since=$3,lease_epoch=lease_epoch+1 WHERE slo_id=$1`, sloID, step.attempts, &since); err != nil {
			t.Fatal(err)
		}
		var row struct {
			id             pgtype.UUID
			epoch, version int64
		}
		if err := s.conn.QueryRow(t.Context(), `SELECT id,lease_epoch,version FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, sloID).Scan(&row.id, &row.epoch, &row.version); err != nil {
			t.Fatal(err)
		}
		if err := dbgen.New(s.conn).FailSloExtremaCursor(t.Context(), dbgen.FailSloExtremaCursorParams{ID: row.id,
			Version: row.version, LeaseEpoch: row.epoch, Now: timestamp(s.now), Reason: "test_failure"}); err != nil {
			t.Fatal(err)
		}
		var next time.Time
		if err := s.conn.QueryRow(t.Context(), `SELECT next_attempt_at FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, sloID).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if got := next.Sub(s.now); got != step.want {
			t.Fatalf("after failure %d (failing for %s) the next attempt is in %s, want %s", step.attempts, step.failingSince, got, step.want)
		}
	}
}

// Review of PR #65: a temporary wait must not erase a real failure, work stops only after five failures
// and seven days of them, retention-expired hours are never rebuilt, and a failing source hour shows
// its SLOs as failing rather than merely catching up.
func TestIntegrationDerivedFailureStateSurvivesWaitsAndShowsBlockedSources(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "self-heal-state", true)
	s.drain(t, "initial aggregation")
	sloID := s.createSLO(t, "self-heal-state")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil && !errors.Is(err, ErrDerivedPending) {
		t.Fatal(err)
	}
	q := dbgen.New(s.conn)
	var id pgtype.UUID
	if err := s.conn.QueryRow(t.Context(), `SELECT id FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, sloID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	repair := func() (attempts int, epoch, version int64) {
		t.Helper()
		if err := s.conn.QueryRow(t.Context(), `SELECT attempts,lease_epoch,version FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, sloID).Scan(&attempts, &epoch, &version); err != nil {
			t.Fatal(err)
		}
		return attempts, epoch, version
	}

	// Failing at the cap: a temporary wait keeps all five failures.
	hourAgo := s.now.Add(-time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET attempts=5,failing_since=$2,built_through=$3 WHERE slo_id=$1`, sloID, hourAgo, s.historyStart.Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, epoch, version := repair()
	if err := q.DeferSloExtremaCursor(t.Context(), dbgen.DeferSloExtremaCursorParams{ID: id, Version: version, LeaseEpoch: epoch, NextAttemptAt: timestamp(s.now)}); err != nil {
		t.Fatal(err)
	}
	if attempts, _, _ := repair(); attempts != 5 {
		t.Fatalf("a temporary wait reduced a failing item's attempts to %d", attempts)
	}

	// Four failures over eight days of waits: not stopped, still claimed.
	eightDaysAgo := s.now.Add(-8 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET attempts=4,failing_since=$2,next_attempt_at=$3,lease_until=NULL WHERE slo_id=$1`, sloID, eightDaysAgo, s.now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runtime.buildSloExtrema(t.Context(), s.clock); err != nil && !errors.Is(err, ErrDerivedPending) {
		t.Fatal(err)
	}
	if attempts, _, _ := repair(); attempts != 0 {
		t.Fatalf("work with four failures was not claimed after eight days: attempts=%d", attempts)
	}

	// An hour retention expired is never claimed for rebuilding.
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at,attempts,last_error)
VALUES ($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$3,5,'retention_expired')`, s.sliID, s.historyStart, s.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count, err := s.runtime.rebuildDerivedBatch(t.Context(), s.clock); err != nil || count != 0 {
		t.Fatalf("a retention-expired hour was claimed: count=%d, %v", count, err)
	}

	// A failing source hour shows the SLO that reads it as failing.
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET last_error='aggregate_rebuild_failed',failing_since=$2 WHERE sli_id=$1`, s.sliID, hourAgo); err != nil {
		t.Fatal(err)
	}
	if states, _, err := s.runtime.checkDerivedConsistency(t.Context(), s.now); err != nil || states.Failing != 1 {
		t.Fatalf("a failing source hour did not show its SLO as failing: %+v, %v", states, err)
	}
}
