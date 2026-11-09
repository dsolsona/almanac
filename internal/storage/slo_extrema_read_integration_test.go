//go:build integration

package storage

import (
	"encoding/json/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationExtremaDetailMatchesRawOracleInOneSnapshot(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			t.Run("open hour", func(t *testing.T) { verifyExtremaDetailSnapshot(t, false, time.Duration(seconds)*time.Second) })
			t.Run("completed day", func(t *testing.T) { verifyExtremaDetailSnapshot(t, true, time.Duration(seconds)*time.Second) })
		})
	}
}

func TestIntegrationExtremaFirstOpenHourMatchesRawOracle(t *testing.T) {
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
		extrema, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !extrema {
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
	bound, err := resolveOperationalBounds(input, time.Duration(row.CadenceSeconds)*time.Second, row.RollingWindowSeconds)
	if err != nil {
		t.Fatal(err)
	}
	window := time.Duration(row.RollingWindowSeconds) * time.Second
	start := canonicalFactsStart(bound.effectiveAt, window, 6*time.Hour, bound.start, window,
		time.Duration(input.BurnWindow)*time.Second, row.EvaluationValidFrom.Time, row.SliValidFrom.Time,
		now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)
	rows, err := q.ListSloTimelineFacts(t.Context(), dbgen.ListSloTimelineFactsParams{SliID: row.SliID,
		MeasurementRevision: row.MeasurementRevision, IntervalStart: timestamp(start), IntervalEnd: timestamp(bound.effectiveAt)})
	if err != nil {
		t.Fatal(err)
	}
	facts := make([]slo.Observation, 0, len(rows))
	for _, fact := range rows {
		facts = append(facts, slo.Observation{Start: fact.IntervalStart.Time.UTC(), End: fact.IntervalEnd.Time.UTC(),
			Received: fact.RecordedAt.Time.UTC(), Outcome: fact.Outcome, Good: fact.Good, Total: fact.Total})
	}
	want, err := buildCanonicalOperationalDetail(t.Context(), facts, row, source, input)
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
	if got.HistoryPreparation != nil || !reflect.DeepEqual(got.Current, want.Current) ||
		!reflect.DeepEqual(got.Burn1h, want.Burn1h) || !reflect.DeepEqual(got.Burn6h, want.Burn6h) ||
		!reflect.DeepEqual(got.ComplianceSeries, want.ComplianceSeries) || !reflect.DeepEqual(got.ComplianceSummary, want.ComplianceSummary) {
		t.Fatalf("%s burn%d differs from raw current values or daily compliance", input.Period, input.BurnWindow)
	}
	base.Observations = facts
	for _, lane := range []struct {
		name     string
		duration time.Duration
		samples  []api.SloOperationalSample
	}{
		{"budget", window, got.BudgetSeries.Samples},
		{"burn", time.Duration(input.BurnWindow) * time.Second, got.BurnSeries.Samples},
		{"attainment", base.Cadence, got.AttainmentSeries.Samples},
	} {
		if lane.duration%base.Cadence != 0 {
			continue
		}
		if len(lane.samples) == 0 || len(lane.samples) > sloOperationalSeriesLimit {
			t.Fatalf("%s selected history is empty or unbounded: %d", lane.name, len(lane.samples))
		}
		base.Duration = lane.duration
		sweep, err := slo.NewWindowSweepContext(t.Context(), base)
		if err != nil {
			t.Fatal(err)
		}
		for _, sample := range lane.samples {
			value, err := sweep.AtContext(t.Context(), sample.EvaluationAt)
			if err != nil {
				t.Fatal(err)
			}
			expected := operationalSample(value, sample.BucketStart, sample.BucketEnd, sample.Revision)
			if lane.name == "burn" {
				burn := burnValue(value, int(lane.duration/time.Second))
				expected = retainedWindowSample(sample.EvaluationAt, burn.Quality, burn.Performance, sample.Revision)
				expected.BucketStart, expected.BucketEnd = sample.BucketStart, sample.BucketEnd
			}
			if lane.name == "attainment" {
				expected = operationalAttainmentSample(value, sample.EvaluationAt.Add(-base.Cadence), sample.EvaluationAt, sample.Revision)
				expected.BucketStart, expected.BucketEnd = sample.BucketStart, sample.BucketEnd
			}
			expected.RetainedExtrema, expected.DisplayCoverage = sample.RetainedExtrema, sample.DisplayCoverage
			if !reflect.DeepEqual(sample, expected) {
				actualBytes, _ := json.Marshal(sample)
				expectedBytes, _ := json.Marshal(expected)
				t.Fatalf("%s selected value differs from raw sweep at %s: extrema=%s raw=%s", lane.name, sample.EvaluationAt, actualBytes, expectedBytes)
			}
		}
	}
}
