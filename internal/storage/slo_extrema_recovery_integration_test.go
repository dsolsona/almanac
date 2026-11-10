//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationExtremaDiagnosticsAndAuditedRetry(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-retry", true)
	id := s.createSLO(t, "extrema-retry")
	var job pgtype.UUID
	if err := s.conn.QueryRow(t.Context(), `UPDATE public.almanac_slo_extrema_cursor
SET attempts=5,failing_since=$2::timestamptz-interval '8 days',lease_until=$2::timestamptz+interval '30 seconds',last_error='extrema_build_failed'
WHERE slo_id=$1 RETURNING id`, id, s.now).Scan(&job); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	page, err := ReadSloPreparation(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: id, Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.Id == job.Bytes {
			found = item.Stage == "extrema" && item.State == "failed" && item.Attempts == 5
		}
	}
	if !found {
		t.Fatal("exhausted extrema work missing from preparation diagnostics")
	}
	retry := RetrySloPreparationInput{Runtime: s.runtime, Proof: proof, SloID: id, JobID: job.String(), RequestID: "extrema-retry", Now: s.clock}
	if err := RetrySloPreparation(t.Context(), s.f.databaseURL, retry); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("retry stole an active lease: %v", err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET lease_until=NULL WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if err := RetrySloPreparation(t.Context(), s.f.databaseURL, retry); err != nil {
		t.Fatal(err)
	}
	if err := RetrySloPreparation(t.Context(), s.f.databaseURL, retry); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("retry replay replenished a running budget: %v", err)
	}
	var audits, attempts int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events
WHERE subject_id=$1 AND action='slo.extrema.retry' AND request_id=$2`, job, retry.RequestID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("retry did not atomically commit one audit: %d, %v", audits, err)
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT attempts FROM public.almanac_slo_extrema_cursor WHERE id=$1`, job).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("retry did not reset the addressed budget: %d, %v", attempts, err)
	}
}

func TestIntegrationExtremaAbandonedFifthLeaseBacksOff(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-crash", true)
	id := s.createSLO(t, "extrema-crash")
	q := dbgen.New(s.conn)
	claim := func() (dbgen.AlmanacSloExtremaCursor, error) {
		return q.ClaimSloExtremaCursor(t.Context(), dbgen.ClaimSloExtremaCursorParams{
			CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), ClosedThrough: timestamp(s.now.Truncate(time.Hour)), LeaseUntil: timestamp(s.now.Add(30 * time.Second))})
	}
	for range 5 {
		if _, err := claim(); err != nil {
			t.Fatal(err)
		}
		s.now = s.now.Add(31 * time.Second)
	}
	if _, err := claim(); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fifth abandoned lease did not back off: %v", err)
	}
	var next time.Time
	var failing pgtype.Timestamptz
	if err := s.conn.QueryRow(t.Context(), `SELECT next_attempt_at,failing_since FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&next, &failing); err != nil || next.Before(s.now.Add(5*time.Minute)) || !failing.Valid {
		t.Fatalf("crash exhaustion did not record a bounded failure: next=%s failing=%+v, %v", next, failing, err)
	}
}

func TestIntegrationConsistencyRecreatesMissingExtremaCursor(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-missing", true)
	id := s.createSLO(t, "extrema-missing")
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.runtime.checkDerivedConsistency(t.Context(), s.now); err != nil {
		t.Fatal(err)
	}
	var through time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&through); err != nil || !through.Equal(s.historyStart.Truncate(time.Hour)) {
		t.Fatalf("consistency check did not recreate bounded history work: %s, %v", through, err)
	}
}

func TestIntegrationExtremaCleanupKeepsBoundaryAndUnexpiredRows(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-cleanup", true)
	id := s.createSLO(t, "extrema-cleanup")
	cutoff := s.now.Add(-365 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_extrema
(slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
SELECT $1,1,1,$2,3600,point,point+interval '1 hour',0,$3,$4,'{}'
FROM generate_series($3::timestamptz-interval '2 hours',$3::timestamptz+interval '1 hour',interval '1 hour') point`, id, operationalCalculationVersion, cutoff.Truncate(time.Hour), s.now); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(7 * time.Minute)
	if err := s.runtime.cleanupDerivedSnapshots(t.Context(), s.now); err != nil {
		t.Fatal(err)
	}
	var expired, retained int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE range_end<=$2),count(*) FILTER(WHERE range_end>$2)
FROM public.almanac_slo_extrema WHERE slo_id=$1`, id, s.now.Add(-365*24*time.Hour)).Scan(&expired, &retained); err != nil || expired != 0 || retained != 2 {
		t.Fatalf("cleanup removed a retained boundary or kept expired rows: expired=%d retained=%d, %v", expired, retained, err)
	}
}

func TestIntegrationExtremaDetailReportsExpiredHistoryAfterCleanup(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-expired-read", true)
	id := s.createSLO(t, "extrema-expired-read")
	end := utcDayStart(s.historyStart).Add(48 * time.Hour)
	start := end.Add(-24 * time.Hour)
	s.now = end.Add(7 * time.Minute)
	if err := s.buildHistory(t, id); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND range_end<=$2`, id, end).Scan(&stored); err != nil || stored == 0 {
		t.Fatalf("fixture has no retained history to expire: rows=%d, %v", stored, err)
	}
	s.now = end.Add(366 * 24 * time.Hour)
	var verifier [32]byte
	verifier[0] = 99
	if _, err := IssueSession(t.Context(), s.f.databaseURL, IssueSessionInput{UserID: s.f.userID, ExpectedCredentialEpoch: s.f.epoch,
		ExpectedCredentialRevision: s.f.revision, Verifier: verifier, Now: s.now, RequestID: "extrema-expired-read"}); err != nil {
		t.Fatal(err)
	}
	input := OperationalDetailInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier},
		ID: id, At: &end, Start: &start, End: &end, BurnWindow: 21600, Now: s.clock}
	before, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
	if err != nil || before.HistoryPreparation != nil || len(before.BudgetSeries.Samples) == 0 {
		t.Fatalf("available retained rows should remain readable: %v", err)
	}
	for _, sample := range before.BudgetSeries.Samples {
		// The requested starting endpoint is a live edge, not a retained row.
		if sample.EvaluationAt.After(start) && (sample.Quality.MissingIntervals != 60 || sample.Quality.ExpiredIntervals != 0) {
			t.Fatal("advancing retention changed an untouched retained row")
		}
	}
	if len(before.ComplianceSeries.Days) != 1 || before.ComplianceSeries.Days[0].Quality.MissingIntervals != 60 || before.ComplianceSeries.Days[0].Quality.ExpiredIntervals != 0 {
		t.Fatal("advancing retention changed an untouched compliance day")
	}
	if err := s.runtime.cleanupDerivedSnapshots(t.Context(), s.now); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND range_end<=$2`, id, end).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("retention did not remove the requested history: rows=%d, %v", stored, err)
	}
	// A different burn selection forces a fresh read rather than the earlier pin.
	input.BurnWindow = 3600
	detail, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if detail.HistoryPreparation != nil {
		t.Errorf("expired history can never finish preparation: %+v", detail.HistoryPreparation)
	}
	for _, lane := range []struct {
		name    string
		samples []api.SloOperationalSample
		expired int64
	}{{"budget", detail.BudgetSeries.Samples, 60}, {"burn", detail.BurnSeries.Samples, 60}, {"attainment", detail.AttainmentSeries.Samples, 1}} {
		if len(lane.samples) == 0 {
			t.Errorf("%s has no expired samples", lane.name)
		}
		for _, sample := range lane.samples {
			if sample.State != "expired" || sample.Quality.State != "expired_history" || sample.Quality.ExpiredIntervals != lane.expired ||
				sample.Quality.MissingIntervals != 0 || sample.Quality.ObservedIntervals != 0 {
				t.Errorf("%s at %s lost expired interval counts: %+v", lane.name, sample.EvaluationAt, sample.Quality)
				break
			}
		}
	}
	if len(detail.ComplianceSeries.Days) != 1 {
		t.Fatalf("expected one completed compliance day, got %d", len(detail.ComplianceSeries.Days))
	}
	day := detail.ComplianceSeries.Days[0]
	if day.Status != "unknown" || day.Explanation != "expired_history" || day.Quality.State != "expired_history" ||
		day.Quality.ExpiredIntervals != 60 || day.Quality.MissingIntervals != 0 || day.Quality.ObservedIntervals != 0 {
		t.Errorf("expired compliance day became pending or missing: %+v", day)
	}
}
