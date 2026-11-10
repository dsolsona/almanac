//go:build integration

package storage

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationLateExtremaRebuildUsesCurrentCutoffAndPreservesNeighbour(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-retention", true)
	valid := s.historyStart.Add(-28 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$2 WHERE sli_id=$1`, s.sliID, valid); err != nil {
		t.Fatal(err)
	}
	late := s.historyStart.Add(10 * time.Minute)
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_sli_observation_values WHERE sli_id=$1 AND interval_start=$2`, s.sliID, late); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_sli_coverage WHERE sli_id=$1 AND interval_start=$2`, s.sliID, late); err != nil {
		t.Fatal(err)
	}
	id := s.createSLOWithHistory(t, "extrema-retention", 28*24*time.Hour, valid)
	s.now = s.historyStart.Add(29 * 24 * time.Hour)
	// Only this month of rows is needed to exercise the historical repair.
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET built_through=$2 WHERE slo_id=$1`, id, utcDayStart(s.historyStart)); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "retention extrema source")
	drain := func() {
		t.Helper()
		for range 29*24 + 2 {
			worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
		t.Fatal("bounded historical repair did not drain")
	}
	drain()
	neighbour := s.historyStart.Add(-time.Hour)
	var before, after []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT lanes FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, neighbour).Scan(&before); err != nil {
		t.Fatal(err)
	}
	s.now = s.historyStart.Add(364 * 24 * time.Hour)
	// Isolate late repair from the unrelated forward catch-up after the clock jump.
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET built_through=$2 WHERE slo_id=$1`, id, s.now.Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), extremaCoverageInsert, s.sliID, late, s.now); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "late retention source")
	drain()
	if err := s.conn.QueryRow(t.Context(), `SELECT lanes FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, neighbour).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatalf("repair changed an untouched hour: %v", err)
	}
	var payload []byte
	var retention time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT lanes,retention_start FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, s.historyStart).Scan(&payload, &retention); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(s.conn)
	row, err := q.GetSlo(t.Context(), mustPGUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	var lanes extremaLanes
	if err := json.Unmarshal(payload, &lanes); err != nil {
		t.Fatal(err)
	}
	base := extremaWindowInput(row, source, s.now, retention)
	value, err := lanes.Window.Marks[len(lanes.Window.Marks)-1].result(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !retention.Equal(s.now.Add(-365*24*time.Hour)) || value.Expired != 38820 || value.Observed != 59 || value.KnownEmpty != 1 ||
		value.Good == nil || *value.Good != "58410.000000000000" || value.Total == nil || *value.Total != "59000.000000000000" {
		t.Fatalf("late rebuild lost exact retained totals or expired counts: %+v", value)
	}
	var outside int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_extrema
WHERE slo_id=$1 AND resolution_seconds=3600 AND published_at=$2 AND (range_start<$3 OR range_start>=$4)`, id, s.now, s.historyStart, s.historyStart.Add(28*24*time.Hour+7*time.Hour)).Scan(&outside); err != nil || outside != 0 {
		t.Fatalf("late repair republished an unaffected hour: %d, %v", outside, err)
	}
}
