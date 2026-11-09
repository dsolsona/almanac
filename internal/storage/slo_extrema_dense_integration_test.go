//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationExtremaDenseHistoryKeepsCoverageWithinReadBudget(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "extrema-dense", true, 15*time.Second)
	id := s.createSLO(t, "extrema-dense")
	s.at = s.historyStart.Add(14 * 24 * time.Hour)
	s.now = s.at.Add(7 * time.Minute)
	for start := s.historyStart; start.Before(s.at); start = start.Add(time.Hour) {
		if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,
execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,c.sli_id,c.measurement_revision,point+interval '15 seconds','observed',c.result_digest,'{}',
gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),c.agent_id,c.session_generation,c.configuration_revision,c.lease_id,point,point+interval '15 seconds'
FROM (SELECT * FROM public.almanac_sli_coverage WHERE sli_id=$1 LIMIT 1) c
CROSS JOIN generate_series($2::timestamptz,$3::timestamptz-interval '15 seconds',interval '15 seconds') point
ON CONFLICT DO NOTHING`, s.sliID, start, start.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_coverage SET outcome='complete_empty'
WHERE sli_id=$1 AND mod(extract(epoch FROM interval_start)::bigint/15,2)=0`, s.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_sli_observation_values WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_observation_values
(interval_start,sli_id,measurement_revision,good_count,total_count)
SELECT interval_start,sli_id,measurement_revision,990,1000 FROM public.almanac_sli_coverage WHERE sli_id=$1 AND outcome='observed'`, s.sliID); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "dense extrema source")
	const maxTurns = 14*24 + 2
	for turn := range maxTurns {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
		if turn == maxTurns-1 {
			t.Fatal("dense extrema fixture did not finish bounded work")
		}
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
	base := extremaWindowInput(row, source, s.now, s.now.Add(-365*24*time.Hour))
	bound := operationalBounds{start: s.historyStart, end: s.at, effectiveAt: s.at}
	reader := &extremaReader{retainedSeriesReader: retainedSeriesReader{q: q, row: row, source: source, now: s.now}}
	if err := reader.prepare(t.Context(), base, bound); err != nil {
		t.Fatalf("dense history unavailable: %v", err)
	}
	got, _, _, err := reader.attainment(t.Context(), base, bound.start, bound.end, base.Cadence, "1")
	if err != nil {
		t.Fatal(err)
	}
	var full []api.SloOperationalSample
	for at := bound.start.Add(base.Cadence); !at.After(bound.end); at = at.Add(base.Cadence) {
		input := base
		input.At, input.Duration = at, base.Cadence
		outcome := "observed"
		if (at.Add(-base.Cadence).Unix()/15)%2 == 0 {
			outcome = "complete_empty"
		}
		input.Observations = []slo.Observation{{Start: at.Add(-base.Cadence), End: at, Received: at, Outcome: outcome, Good: "990", Total: "1000"}}
		if outcome == "complete_empty" {
			input.Observations[0].Good, input.Observations[0].Total = "", ""
		}
		value, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, operationalAttainmentSample(value, bound.start, bound.end, "1"))
	}
	want, err := reduceRetainedDisplay(full, bound.start, bound.end, base.Target, "attainment", 375)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("dense history changed exact samples or coverage: got=%d want=%d, %v", len(got), len(want), err)
	}
	bound.start = bound.end.Add(-90 * 24 * time.Hour)
	reader = &extremaReader{retainedSeriesReader: retainedSeriesReader{q: q, row: row, source: source, now: s.now}}
	if err := reader.prepare(t.Context(), base, bound); err != nil {
		t.Fatal(err)
	}
	all, _, _, err := reader.attainment(t.Context(), base, bound.start, bound.end, base.Cadence, "1")
	if err != nil {
		t.Fatal(err)
	}
	dayStart := utcDayStart(s.historyStart)
	dayEnd := dayStart.Add(24 * time.Hour)
	got = nil
	for _, sample := range all {
		if sample.EvaluationAt.After(dayStart) && !sample.EvaluationAt.After(dayEnd) {
			got = append(got, sample)
		}
	}
	through := 0
	for through < len(full) && !full[through].EvaluationAt.After(dayEnd) {
		through++
	}
	want, err = reduceRetainedLaneLimit(full[:through], dayStart, dayEnd, base.Target, "attainment", sloOperationalSeriesLimit)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("first partial day changed dense samples or coverage: got=%d want=%d, %v", len(got), len(want), err)
	}
}
