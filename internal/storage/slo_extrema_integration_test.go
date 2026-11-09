//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

const extremaCoverageInsert = `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,
execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT $2,c.sli_id,c.measurement_revision,$2::timestamptz+interval '1 minute','complete_empty',c.result_digest,'{}',
gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),c.agent_id,c.session_generation,c.configuration_revision,c.lease_id,$2,$3
FROM public.almanac_sli_coverage c WHERE c.sli_id=$1 LIMIT 1 ON CONFLICT DO NOTHING`

func TestIntegrationExtremaLeaseFencesReclaimsAndArrivals(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-lease", true)
	id := s.createSLO(t, "extrema-lease")
	q := dbgen.New(s.conn)
	claim := func() (dbgen.AlmanacSloExtremaCursor, error) {
		return q.ClaimSloExtremaCursor(t.Context(), dbgen.ClaimSloExtremaCursorParams{
			CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), ClosedThrough: timestamp(s.now.Truncate(time.Hour)), LeaseUntil: timestamp(s.now.Add(30 * time.Second))})
	}
	first, err := claim()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim(); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("active lease claimed twice: %v", err)
	}
	s.now = s.now.Add(31 * time.Second)
	second, err := claim()
	if err != nil || second.LeaseEpoch <= first.LeaseEpoch {
		t.Fatalf("crashed lease did not recover: %+v, %v", second, err)
	}
	count, err := q.PublishSloExtrema(t.Context(), dbgen.PublishSloExtremaParams{ID: first.ID, Version: first.Version, LeaseEpoch: first.LeaseEpoch,
		ResolutionSeconds: 3600, RangeStart: timestamp(s.historyStart), RangeEnd: timestamp(s.historyStart.Add(time.Hour)), SourceGeneration: 1,
		RetentionStart: timestamp(s.now.Add(-365 * 24 * time.Hour)), PublishedAt: timestamp(s.now), Lanes: []byte(`{}`)})
	if err != nil || count != 0 {
		t.Fatalf("superseded worker published: %d, %v", count, err)
	}
	late := s.at.Add(10 * time.Minute)
	if _, err := s.conn.Exec(t.Context(), extremaCoverageInsert, s.sliID, late, s.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	count, err = q.AdvanceSloExtremaCursor(t.Context(), dbgen.AdvanceSloExtremaCursorParams{ID: second.ID, Version: second.Version, LeaseEpoch: second.LeaseEpoch,
		BuiltThrough: timestamp(s.at), NextAttemptAt: timestamp(s.now)})
	if err != nil || count != 0 {
		t.Fatalf("arrival between claim and acknowledgment was lost: %d, %v", count, err)
	}
	var from time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT dirty_from FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&from); err != nil || !from.Equal(late.Truncate(time.Hour)) {
		t.Fatalf("new invalidation disappeared: %s, %v", from, err)
	}
}

func TestIntegrationExtremaDailyCompositionMatchesFullCanonicalSweep(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			t.Parallel()
			cadence := time.Duration(seconds) * time.Second
			s := newSelfHealFixtureCadence(t, "extrema-day", true, cadence)
			id := s.createSLO(t, "extrema-day")
			if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_coverage SET outcome='complete_empty' WHERE sli_id=$1 AND interval_start=$2`, s.sliID, s.historyStart); err != nil {
				t.Fatal(err)
			}
			if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_sli_observation_values WHERE sli_id=$1 AND interval_start=$2`, s.sliID, s.historyStart); err != nil {
				t.Fatal(err)
			}
			s.now = utcDayStart(s.at).Add(24 * time.Hour)
			s.drain(t, "daily extrema source")
			for range 10 {
				worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
				if err != nil {
					t.Fatal(err)
				}
				if !worked {
					break
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
			var payload []byte
			var retention time.Time
			if err := s.conn.QueryRow(t.Context(), `SELECT lanes,retention_start FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=86400`, id).Scan(&payload, &retention); err != nil {
				t.Fatal(err)
			}
			var lanes extremaLanes
			if err := json.Unmarshal(payload, &lanes); err != nil {
				t.Fatal(err)
			}
			start, end := s.now.Add(-24*time.Hour), s.now
			base := extremaWindowInput(row, source, s.now, retention)
			facts, err := q.ListSloTimelineFacts(t.Context(), dbgen.ListSloTimelineFactsParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision,
				IntervalStart: timestamp(s.historyStart), IntervalEnd: timestamp(s.at)})
			if err != nil {
				t.Fatal(err)
			}
			for _, fact := range facts {
				base.Observations = append(base.Observations, slo.Observation{Start: fact.IntervalStart.Time, End: fact.IntervalEnd.Time, Received: fact.RecordedAt.Time,
					Outcome: fact.Outcome, Good: fact.Good, Total: fact.Total})
			}
			var full [4][]api.SloOperationalSample
			for index, duration := range [4]time.Duration{base.Duration, cadence, time.Hour, 6 * time.Hour} {
				if duration%cadence != 0 {
					continue
				}
				input := base
				input.Duration = duration
				sweep, err := slo.NewWindowSweepContext(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				for at := floorSliBoundary(start, cadence).Add(cadence); !at.After(end); at = at.Add(cadence) {
					value, err := sweep.AtContext(t.Context(), at)
					if err != nil {
						t.Fatal(err)
					}
					sample := operationalSample(value, start, end, "1")
					if index == 1 {
						sample = operationalAttainmentSample(value, start, end, "1")
					} else if index >= 2 {
						burn := burnValue(value, int(duration/time.Second))
						sample = retainedWindowSample(at, burn.Quality, burn.Performance, "1")
						sample.BucketStart, sample.BucketEnd = start, end
					}
					full[index] = append(full[index], sample)
				}
			}
			want, err := compactRetainedTile(retainedSloTile{Budget: full[0], Attainment: full[1], Burn1h: full[2], Burn6h: full[3]}, start, end, base.Target)
			if err != nil {
				t.Fatal(err)
			}
			if time.Hour%cadence != 0 {
				want.Burn1h = nil
			}
			if 6*time.Hour%cadence != 0 {
				want.Burn6h = nil
			}
			base.Observations = nil
			got, err := lanes.tile(t.Context(), base, start, end, "1")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("daily composition changed canonical samples at %ds cadence: %v", seconds, err)
			}
			if lanes.Compliance == nil || !lanes.Compliance.DayEnd.Equal(end) || !lanes.Compliance.Mark.At.Equal(floorSliBoundary(end, cadence)) {
				t.Fatalf("compliance used midnight instead of its actual cadence endpoint: %+v", lanes.Compliance)
			}
			found := false
			for _, mark := range lanes.Attainment.Marks {
				if mark.At.Equal(s.historyStart.Add(cadence)) {
					found = mark.KnownEmpty == 1 && mark.LatestReceipt != nil && mark.LatestReceipt.Equal(mark.At)
				}
			}
			if !found {
				t.Fatal("daily mark lost known-empty receipt provenance")
			}
		})
	}
}

func TestIntegrationExtremaWorkerClosesHoursWithExactInputs(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) { verifyExtremaWorkerCadence(t, time.Duration(seconds)*time.Second) })
	}
}

func verifyExtremaWorkerCadence(t *testing.T, cadence time.Duration) {
	t.Helper()
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "extrema-worker", true, cadence)
	id := s.createSLO(t, "extrema-worker")
	s.drain(t, "extrema source")
	for range 10 {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
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
	hours, err := q.ReadSloExtremaHours(t.Context(), dbgen.ReadSloExtremaHoursParams{SloID: row.ID, DefinitionRevision: row.DefinitionRevision,
		MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion,
		RangeStart: timestamp(s.historyStart.Truncate(time.Hour)), RangeEnd: timestamp(s.at.Add(time.Hour))})
	if err != nil || len(hours) != 3 {
		t.Fatalf("closed history missing or open hour persisted: %d, %v", len(hours), err)
	}
	for _, hour := range hours {
		points, err := buildRetainedRepairPoints(t.Context(), q, row, source, floorSliBoundary(hour.RangeStart.Time, cadence), hour.RangeEnd.Time, s.now)
		if err != nil {
			t.Fatal(err)
		}
		want, err := compactRetainedPoints(points, hour.RangeStart.Time, hour.RangeEnd.Time, row.Target, "1")
		if err != nil {
			t.Fatal(err)
		}
		if time.Hour%cadence != 0 {
			want.Burn1h = nil
		}
		if 6*time.Hour%cadence != 0 {
			want.Burn6h = nil
		}
		var lanes extremaLanes
		if err := json.Unmarshal(hour.Lanes, &lanes); err != nil {
			t.Fatal(err)
		}
		got, err := lanes.tile(t.Context(), extremaWindowInput(row, source, s.now, hour.RetentionStart.Time), hour.RangeStart.Time, hour.RangeEnd.Time, "1")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("hourly marks changed selected exact samples: %v\ngot=%+v\nwant=%+v", err, got, want)
		}
	}
	var through time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&through); err != nil || !through.Equal(s.at.Truncate(time.Hour)) {
		t.Fatalf("cursor did not stop at last closed hour: %s, %v", through, err)
	}
}

func TestIntegrationExtremaInvalidationIsAtomicAndBounded(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-dirty", true)
	id := s.createSLO(t, "extrema-dirty")
	var built time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&built); err != nil {
		t.Fatal(err)
	}
	if !built.Equal(s.historyStart.Truncate(time.Hour)) {
		t.Fatalf("new definition omitted eligible history: %s", built)
	}
	late := s.at.Add(10 * time.Minute)
	s.now = late.Add(3 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET built_through=$2,attempts=5,
failing_since=$2::timestamptz-interval '8 days' WHERE slo_id=$1`, id, s.now.Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	insert := extremaCoverageInsert
	tx, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), insert, s.sliID, late, s.now); err != nil {
		t.Fatal(err)
	}
	var from, to time.Time
	var version int64
	var attempts int
	if err := tx.QueryRow(t.Context(), `SELECT dirty_from,dirty_to,version,attempts FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&from, &to, &version, &attempts); err != nil {
		t.Fatal(err)
	}
	if !from.Equal(late.Truncate(time.Hour)) || !to.Equal(late.Truncate(time.Hour).Add(7*time.Hour)) || attempts != 0 {
		t.Fatalf("late observation globally fenced history or failed to restart work: %s..%s attempts=%d", from, to, attempts)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), insert, s.sliID, late, s.now); err != nil {
		t.Fatal(err)
	}
	var duplicateVersion int64
	if err := s.conn.QueryRow(t.Context(), `SELECT version FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&duplicateVersion); err != nil || duplicateVersion != version {
		t.Fatalf("duplicate coverage changed extrema work: %d != %d, %v", duplicateVersion, version, err)
	}
}
