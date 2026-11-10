//go:build integration

package storage

import (
	"encoding/json/v2"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationCurrentUsesExtremaSparklineWithoutLegacyWork(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-current", true)
	id := s.createSLO(t, "extrema-current")
	s.drain(t, "current extrema source")
	for range 10 {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	var legacy int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM pg_class
WHERE relnamespace='public'::regnamespace AND relname=ANY(ARRAY[
'almanac_slo_series_points','almanac_slo_series_tiles','almanac_slo_series_tile_work',
'almanac_slo_series_repair','almanac_slo_display_blocks','almanac_slo_history_jobs','almanac_slo_history_admission'])`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 {
		t.Errorf("legacy history pipeline tables remain: %d", legacy)
	}
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	current, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: id, Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	if current.Current.Performance.Good == nil || *current.Current.Performance.Good != "59400.000000000000" ||
		current.Current.Performance.Total == nil || *current.Current.Performance.Total != "60000.000000000000" {
		t.Fatalf("current arithmetic changed: %+v", current.Current.Performance)
	}
	var payload []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var item api.SloOperationalOverviewItem
	if err := json.Unmarshal(payload, &item); err != nil {
		t.Fatal(err)
	}
	if item.BudgetHistoryState == nil || *item.BudgetHistoryState != "ready" || len(item.BudgetSparkline) == 0 || len(item.BudgetSparkline) > 120 {
		t.Fatalf("ready extrema do not supply the list sparkline: state=%v samples=%d", item.BudgetHistoryState, len(item.BudgetSparkline))
	}
	verifyFixtureSparkline(t, s, item)
	// A retained hour before validity still participates in due-work detection.
	s.now = s.now.Add(time.Second)
	q := dbgen.New(s.conn)
	row, err := q.GetSlo(t.Context(), mustPGUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	base := extremaWindowInput(row, source, s.now, s.now.Add(-365*24*time.Hour))
	prefix, err := buildExtremaHour(t.Context(), q, row, base, s.historyStart.Truncate(time.Hour).Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	payloadPrefix, err := json.Marshal(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_extrema
(slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
SELECT slo_id,definition_revision,measurement_revision,calculation_version,3600,$2::timestamptz-interval '1 hour',$2,source_generation,retention_start,$3,$4
FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600 LIMIT 1`, id, s.historyStart.Truncate(time.Hour), s.now, payloadPrefix); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	due, err := q.ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), MetricsAt: timestamp(s.now.Add(time.Minute))})
	if err != nil || due.CurrentDue {
		t.Fatalf("pre-validity publication left current work continuously due: %+v %v", due, err)
	}
	objectives, err := q.ListDueDerivedObjectives(t.Context(), dbgen.ListDueDerivedObjectivesParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), BatchLimit: 100})
	if err != nil || len(objectives) != 0 {
		t.Fatalf("pre-validity publication repeatedly selected objective: %d %v", len(objectives), err)
	}
	before := append([]api.SloOperationalSample(nil), item.BudgetSparkline...)
	s.now = s.now.Add(time.Minute)
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		t.Fatal(err)
	}
	lastStart := before[len(before)-1].BucketStart
	var oldClosed, newClosed []api.SloOperationalSample
	for _, sample := range before {
		if sample.BucketStart.Before(lastStart) {
			oldClosed = append(oldClosed, sample)
		}
	}
	for _, sample := range item.BudgetSparkline {
		if sample.BucketStart.Before(lastStart) {
			newClosed = append(newClosed, sample)
		}
	}
	if !reflect.DeepEqual(oldClosed, newClosed) {
		t.Fatal("cadence refresh changed closed buckets")
	}
	if !item.BudgetSparkline[len(item.BudgetSparkline)-1].EvaluationAt.Equal(floorSliBoundary(s.now, s.cadence)) {
		t.Fatal("cadence refresh failed to replace the last bucket at the cadence boundary")
	}
	verifyFixtureSparkline(t, s, item)
}

func TestIntegrationLateExtremaRebuildRefreshesCoveredSparkline(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-late-sparkline", true)
	valid := s.at.Add(-4 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$1 WHERE sli_id=$2`, valid, s.sliID); err != nil {
		t.Fatal(err)
	}
	id := s.createSLOWithHistory(t, "extrema-late-sparkline", time.Hour, valid)
	s.drain(t, "sparkline source")
	drain := func() {
		t.Helper()
		for range 4*24 + 2 {
			worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
		t.Fatal("extrema did not drain")
	}
	refresh := func() api.SloOperationalOverviewItem {
		t.Helper()
		if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
			t.Fatal(err)
		}
		var payload []byte
		if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var item api.SloOperationalOverviewItem
		if err := json.Unmarshal(payload, &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	drain()
	before := refresh()
	verifyFixtureSparkline(t, s, before)
	late := s.at.Add(-3*24*time.Hour + 10*time.Minute)
	s.now = s.now.Add(time.Second)
	if _, err := execObservationFixture(t, s.conn, s.sliID, extremaCoverageInsert, s.sliID, late, s.now); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "late sparkline source")
	pending := refresh()
	if pending.BudgetHistoryState == nil || *pending.BudgetHistoryState != "preparing" || !reflect.DeepEqual(before.BudgetSparkline, pending.BudgetSparkline) {
		t.Fatal("late data changed closed sparkline buckets before their extrema rebuild")
	}
	drain()
	after := refresh()
	verifyFixtureSparkline(t, s, after)
	if after.BudgetHistoryState == nil || *after.BudgetHistoryState != "ready" || reflect.DeepEqual(before.BudgetSparkline, after.BudgetSparkline) {
		t.Fatal("rebuilt covered hour did not refresh the sparkline at the same cadence boundary")
	}
	// Requested-start metadata follows the later request; the evaluated window is unchanged.
	before.Current.Window.RequestedStart = after.Current.Window.RequestedStart
	if !reflect.DeepEqual(before.Current, after.Current) {
		t.Fatal("three-day-old arrival changed the one-hour current calculation")
	}
	width := retainedDisplayWidth(s.at.Add(-7*24*time.Hour), s.at, 30)
	affectedStart := late.Truncate(width)
	affectedEnd := late.Add(time.Hour).Truncate(width).Add(width)
	unaffected := func(samples []api.SloOperationalSample) []api.SloOperationalSample {
		var result []api.SloOperationalSample
		for _, sample := range samples {
			if sample.BucketEnd.Before(affectedStart) || !sample.BucketStart.Before(affectedEnd) {
				result = append(result, sample)
			}
		}
		return result
	}
	if !reflect.DeepEqual(unaffected(before.BudgetSparkline), unaffected(after.BudgetSparkline)) {
		t.Fatal("late rebuild changed unrelated sparkline buckets")
	}
}

func verifyFixtureSparkline(t *testing.T, s *selfHealFixture, item api.SloOperationalOverviewItem) {
	t.Helper()
	closedNumeric := 0
	for _, sample := range item.BudgetSparkline {
		// This fixture has 990/1000 once per minute in [historyStart, at).
		from := maxTime(sample.EvaluationAt.Add(-time.Hour), s.historyStart)
		through := sample.EvaluationAt
		if through.After(s.at) {
			through = s.at
		}
		observed := max(int64(0), int64(through.Sub(from)/time.Minute))
		if observed == 0 {
			continue
		}
		if sample.Quality.ObservedIntervals != observed || sample.Good == nil || sample.Total == nil ||
			*sample.Good != fmt.Sprintf("%d.000000000000", observed*990) || *sample.Total != fmt.Sprintf("%d.000000000000", observed*1000) {
			t.Fatalf("sparkline differs from independent fixture totals at %s: observed=%d sample=%+v", sample.EvaluationAt, observed, sample)
		}
		if !sample.EvaluationAt.After(s.now.Truncate(time.Hour)) {
			closedNumeric++
		}
	}
	if closedNumeric == 0 {
		t.Fatal("sparkline oracle exercised no closed-hour numeric value")
	}
}
