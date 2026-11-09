//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func TestIntegrationOpenHourDetailKeepsExactValuesWithoutExtrema(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "open-hour", true)
	id := s.createSLO(t, "open-hour")
	s.now = s.at.Add(7 * time.Minute)
	s.drain(t, "open-hour source")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	for range 100 {
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
	value, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, OperationalDetailInput{
		Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: id, Period: "7d", Now: s.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	var last *api.SloOperationalSample
	for i := range value.BudgetSeries.Samples {
		if value.BudgetSeries.Samples[i].EvaluationAt.Equal(s.now) {
			last = &value.BudgetSeries.Samples[i]
		}
	}
	// Seven missing minutes leave 53 observed intervals, each with 990 good of 1,000.
	if last == nil || last.Good == nil || *last.Good != "52470.000000000000" ||
		last.Total == nil || *last.Total != "53000.000000000000" ||
		last.Quality.State != "partial" || last.Quality.ObservedIntervals != 53 || last.Quality.MissingIntervals != 7 {
		t.Fatalf("open-hour graph lost exact points or hid missing observations: %+v", last)
	}
	var tiles int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_extrema
WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, s.now.Truncate(time.Hour)).Scan(&tiles); err != nil || tiles != 0 {
		t.Fatalf("open-hour read required a prematurely published tile: %d, %v", tiles, err)
	}
}
