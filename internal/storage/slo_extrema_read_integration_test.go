//go:build integration

package storage

import (
	"encoding/json/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationExtremaDetailMatchesRetainedInOneSnapshot(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			t.Run("open hour", func(t *testing.T) { verifyExtremaDetailSnapshot(t, false, time.Duration(seconds)*time.Second) })
			t.Run("completed day", func(t *testing.T) { verifyExtremaDetailSnapshot(t, true, time.Duration(seconds)*time.Second) })
		})
	}
}

func TestIntegrationExtremaFirstOpenHourMatchesRetained(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-first-hour", true)
	s.historyStart = s.historyStart.Add(7 * time.Minute)
	s.at = s.historyStart.Add(7 * time.Minute)
	s.now = s.at
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$2 WHERE sli_id=$1`, s.sliID, s.historyStart); err != nil {
		t.Fatal(err)
	}
	id := s.createSLO(t, "extrema-first-hour")
	s.now = s.now.Add(7 * time.Minute)
	s.drain(t, "first open hour source")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	verifyExtremaDetailReadersInSnapshot(t, s, id)
}

func verifyExtremaDetailSnapshot(t *testing.T, completedDay bool, cadence time.Duration) {
	t.Helper()
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "extrema-detail", true, cadence)
	id := s.createSLO(t, "extrema-detail")
	s.now = s.at.Add(7 * time.Minute)
	if completedDay {
		s.now = utcDayStart(s.at).Add(24*time.Hour + 7*time.Minute)
	}
	s.drain(t, "extrema detail source")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	for turn := range 400 {
		repaired, err := s.runtime.repairRetainedSeries(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		tiled, err := s.runtime.prepareRetainedTile(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		extrema, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !repaired && !tiled && !extrema {
			break
		}
		if turn == 399 {
			t.Fatal("bounded detail fixture did not finish")
		}
	}
	verifyExtremaDetailReadersInSnapshot(t, s, id)
}

func verifyExtremaDetailReadersInSnapshot(t *testing.T, s *selfHealFixture, id string) {
	t.Helper()
	tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	row, err := q.GetSlo(t.Context(), mustPGUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"7d", "90d", "1y", "window"} {
		for _, burn := range []int{3600, 21600} {
			verifyExtremaDetailReaders(t, q, row, source, OperationalDetailInput{Runtime: s.runtime, ID: id, Period: period, BurnWindow: burn, Now: s.clock})
		}
	}
}

func verifyExtremaDetailReaders(t *testing.T, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, input OperationalDetailInput) {
	t.Helper()
	now := input.Now()
	input.retainedReader = &retainedSeriesReader{q: q, row: row, source: source, now: now}
	want, err := buildSloOperationalDetail(t.Context(), q, row, source, input)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := resolveOperationalBounds(input, time.Duration(row.CadenceSeconds)*time.Second, row.RollingWindowSeconds)
	if err != nil {
		t.Fatal(err)
	}
	reader := &extremaReader{retainedSeriesReader: retainedSeriesReader{q: q, row: row, source: source, now: now}}
	base := extremaWindowInput(row, source, now, now.Add(-365*24*time.Hour))
	if err := reader.prepare(t.Context(), base, bound); err != nil {
		t.Fatal(err)
	}
	input.retainedReader = &reader.retainedSeriesReader
	got, err := buildSloOperationalDetail(t.Context(), q, row, source, input)
	if err != nil {
		t.Fatal(err)
	}
	got.EvaluationId = want.EvaluationId
	if !reflect.DeepEqual(got, want) {
		left, right := reflect.ValueOf(got), reflect.ValueOf(want)
		for i := range left.NumField() {
			if !reflect.DeepEqual(left.Field(i).Interface(), right.Field(i).Interface()) {
				t.Logf("different detail field: %s", left.Type().Field(i).Name)
			}
		}
		for _, lane := range []struct {
			name      string
			got, want []api.SloOperationalSample
		}{
			{"budget", got.BudgetSeries.Samples, want.BudgetSeries.Samples}, {"burn", got.BurnSeries.Samples, want.BurnSeries.Samples}, {"attainment", got.AttainmentSeries.Samples, want.AttainmentSeries.Samples}} {
			if reflect.DeepEqual(lane.got, lane.want) {
				continue
			}
			t.Logf("%s sample counts: extrema=%d retained=%d", lane.name, len(lane.got), len(lane.want))
			for index := range min(len(lane.got), len(lane.want)) {
				if !reflect.DeepEqual(lane.got[index], lane.want[index]) {
					g, _ := json.Marshal(lane.got[index])
					w, _ := json.Marshal(lane.want[index])
					t.Logf("%s first difference at %d: extrema=%s retained=%s", lane.name, index, g, w)
					break
				}
			}
		}
		t.Fatalf("%s burn%d changed assembled detail samples, coverage or compliance", input.Period, input.BurnWindow)
	}
}
