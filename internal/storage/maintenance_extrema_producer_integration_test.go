//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func extremaProducerSession(t *testing.T, f maintenanceFixture, h maintenanceHourInput) (*maintenanceConsumerSession, dbgen.GetSloRow, dbgen.GetSloOperationalContextRow, slo.WindowInput) {
	t.Helper()
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rollbackTransaction(tx) })
	q := dbgen.New(tx)
	row, err := q.GetSlo(t.Context(), h.SloID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	base := slo.WindowInput{Now: f.now, Duration: time.Duration(row.RollingWindowSeconds) * time.Second, Cadence: cadence, Target: sli.NormalizeDecimal(row.Target), ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(), RetentionStart: f.now.Add(-sli.ObservationRetention), FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))}
	return &maintenanceConsumerSession{tx: tx, requestedRevision: row.ExclusionRevision, limits: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 10000, MaxFragments: 10000}}, row, source, base
}

func TestIntegrationMaintenanceExtremaProducerGrids(t *testing.T) {
	t.Parallel()
	for _, cadence := range []time.Duration{15 * time.Second, time.Minute, 70 * time.Second} {
		t.Run(cadence.String(), func(t *testing.T) {
			t.Parallel()
			f, h, mask := hourGridFixture(t, cadence)
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			session, row, source, base := extremaProducerSession(t, f, h)
			lanes, err := buildMaintenanceExtremaHour(t.Context(), session, row, base, h.HourStart)
			if err != nil {
				t.Fatal(err)
			}
			if cadence == 70*time.Second && (lanes.Burn1h != nil || lanes.Burn6h != nil) {
				t.Fatal("incompatible cadence manufactured burn lanes")
			}
			// Reduce independently evaluated raw windows using the existing exact
			// reducer; selection must match, not just the numeric final endpoint.
			for index, lane := range []*extremaLane{lanes.Window, lanes.Attainment, lanes.Burn1h, lanes.Burn6h} {
				if lane == nil {
					continue
				}
				input := base
				input.Duration = []time.Duration{base.Duration, cadence, time.Hour, 6 * time.Hour}[index]
				var values []slo.WindowResult
				for at := floorSliBoundary(h.HourStart, cadence).Add(cadence); !at.After(h.HourStart.Add(time.Hour)); at = at.Add(cadence) {
					input.At = at
					values = append(values, evaluationRawWindow(t, input, mask))
				}
				want, err := makeExtremaSummaryLane(values, h.HourStart, h.HourStart.Add(time.Hour), base.Target, []string{"budget", "attainment", "burn", "burn"}[index], "1")
				if err != nil {
					t.Fatal(err)
				}
				if len(lane.Marks) != len(want.Marks) {
					t.Fatalf("lane %d selected %d marks, want %d", index, len(lane.Marks), len(want.Marks))
				}
				for j, mark := range lane.Marks {
					if !mark.At.Equal(want.Marks[j].At) || !reflect.DeepEqual(mark.Labels, want.Marks[j].Labels) {
						t.Fatalf("lane %d selection differs at %d", index, j)
					}
				}
			}
			start := floorSliBoundary(h.HourStart, cadence)
			points, err := buildMaintenanceRetainedRepairPoints(t.Context(), session, row, source, start, h.HourStart.Add(time.Hour), f.now)
			if err != nil {
				t.Fatal(err)
			}
			wantPoints := int(floorSliBoundary(h.HourStart.Add(time.Hour), cadence).Sub(start) / cadence)
			if len(points) != wantPoints {
				t.Fatalf("points=%d want=%d", len(points), wantPoints)
			}
			for i, p := range points {
				input := base
				input.At = start.Add(time.Duration(i+1) * cadence)
				want := evaluationRawWindow(t, input, mask)
				if !reflect.DeepEqual(p.Current.Performance, operationalPerformance(want)) || !p.Current.EvaluationAt.Equal(input.At) {
					t.Fatalf("current point %d changed original cutoff/counts", i)
				}
				if p.Current.CalculationProvenance == nil || p.Current.CalculationProvenance.RequestedRevision != "1" {
					t.Fatal("current provenance missing")
				}
				input.Duration = cadence
				want = evaluationRawWindow(t, input, mask)
				if !reflect.DeepEqual(p.Attainment.Good, want.Good) || !reflect.DeepEqual(p.Attainment.Total, want.Total) || p.Attainment.CalculationProvenance == nil {
					t.Fatalf("attainment point %d not its actual duration", i)
				}
				for j, b := range []struct {
					seconds    int
					valueGood  *string
					provenance bool
				}{{3600, p.Burn1h.Performance.Good, p.Burn1h.CalculationProvenance != nil}, {21600, p.Burn6h.Performance.Good, p.Burn6h.CalculationProvenance != nil}} {
					if time.Duration(b.seconds)*time.Second%cadence != 0 {
						if b.provenance || b.valueGood != nil {
							t.Fatal("phantom burn")
						}
						continue
					}
					input.Duration = []time.Duration{time.Hour, 6 * time.Hour}[j]
					want = evaluationRawWindow(t, input, mask)
					if !reflect.DeepEqual(b.valueGood, want.Good) || !b.provenance {
						t.Fatalf("burn point %d duration %d", i, b.seconds)
					}
				}
			}
			encoded, err := marshalReceiptMetadata(lanes)
			if err != nil {
				t.Fatal(err)
			}
			rollbackTransaction(session.tx)
			if _, err = f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1`, f.sliID); err != nil {
				t.Fatal(err)
			}
			var saved extremaLanes
			if err = unmarshalReceiptMetadata(encoded, &saved); err != nil {
				t.Fatal(err)
			}
			for index, lane := range []*extremaLane{saved.Window, saved.Attainment, saved.Burn1h, saved.Burn6h} {
				if lane == nil {
					continue
				}
				for _, mark := range lane.Marks {
					input := base
					input.At = mark.At
					input.Duration = []time.Duration{base.Duration, cadence, time.Hour, 6 * time.Hour}[index]
					got, err := mark.result(t.Context(), input)
					want := evaluationRawWindow(t, input, mask)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("retained lane %d at %s: err=%v got=%+v want=%+v", index, mark.At, err, got, want)
					}
				}
			}
		})
	}
}

func TestIntegrationMaintenanceExtremaProducerAuthorityAndBounds(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	session, row, source, base := extremaProducerSession(t, f, h)
	wrong := row
	wrong.ExclusionRevision++
	if _, err := buildMaintenanceExtremaHour(t.Context(), session, wrong, base, h.HourStart); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("mismatched authority: %v", err)
	}
	if _, err := buildMaintenanceRetainedRepairPoints(t.Context(), session, wrong, source, h.HourStart, h.HourStart.Add(time.Minute), f.now); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("repair mismatched authority: %v", err)
	}
	if _, err := buildMaintenanceRetainedRepairPoints(t.Context(), session, row, source, h.HourStart, h.HourStart.Add(241*time.Minute), f.now); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("batch limit: %v", err)
	}
	got, err := buildMaintenanceExtremaHour(t.Context(), session, row, base, f.historyStart.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want, err := buildExtremaHour(t.Context(), dbgen.New(session.tx), row, base, f.historyStart.Add(-time.Hour))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-validity fallback differs: %v", err)
	}
}

func TestIntegrationMaintenanceExtremaProducerExcluded(t *testing.T) {
	t.Parallel()
	for _, unknown := range []bool{false, true} {
		name := "observed"
		if unknown {
			name = "unknown_events"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			m := f.input("whole-hour")
			m.Start = h.HourStart
			m.End = h.HourStart.Add(time.Hour)
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
				t.Fatal(err)
			}
			b := compilerCapture(t, f, nil)
			compilerFinish(t, f, b.ID, 10)
			h.BuildID = b.ID
			if unknown {
				if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET outcome='failed',good_count=NULL,total_count=NULL,error_code='network',ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, h.HourStart.Add(15*time.Minute)); err != nil {
					t.Fatal(err)
				}
				f.drain(t, "excluded unknown producer")
				if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h.HourStart).Scan(&h.SourceGeneration); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			session, row, source, base := extremaProducerSession(t, f, h)
			lanes, err := buildMaintenanceExtremaHour(t.Context(), session, row, base, h.HourStart)
			if err != nil {
				t.Fatal(err)
			}
			end := h.HourStart.Add(time.Hour)
			base.At = end
			var observations []slo.Observation
			for i := range 60 {
				start := h.HourStart.Add(time.Duration(i) * time.Minute)
				o := slo.Observation{Start: start, End: start.Add(time.Minute), Received: start.Add(time.Minute), Outcome: "observed", Good: "990", Total: "1000"}
				if unknown && i == 15 {
					o.Outcome = "failed"
					o.Good = ""
					o.Total = ""
				}
				observations = append(observations, o)
			}
			oracle := base
			oracle.Exclusions = []slo.ExclusionInterval{{Start: m.Start, End: m.End}}
			oracle.Observations = observations
			want, err := slo.EvaluateOperationalWindowContext(t.Context(), oracle)
			if err != nil {
				t.Fatal(err)
			}
			mark := lanes.Window.Marks[len(lanes.Window.Marks)-1]
			got, err := mark.result(t.Context(), base)
			if err != nil || !reflect.DeepEqual(got, want) || got.State != "excluded" || got.Burn != nil {
				t.Fatalf("excluded replay: got=%+v want=%+v err=%v", got, want, err)
			}
			points, err := buildMaintenanceRetainedRepairPoints(t.Context(), session, row, source, end.Add(-time.Minute), end, f.now)
			if err != nil || len(points) != 1 {
				t.Fatalf("repair: %v", err)
			}
			q := points[0].Current.Quality
			if q.State != "excluded" || q.ExcludedIntervals == nil || *q.ExcludedIntervals != 60 || points[0].Current.Performance.Burn != nil {
				t.Fatalf("excluded presentation %+v", points[0])
			}
			if unknown && (string(q.ExcludedGood) != "null" || string(q.ExcludedTotal) != "null") {
				t.Fatal("unknown excluded events invented counts")
			}
			if !unknown && (string(q.ExcludedGood) == "null" || string(q.ExcludedTotal) == "null") {
				t.Fatal("known excluded events lost counts")
			}
		})
	}
}

func TestIntegrationMaintenanceExtremaProducerTerminalGap(t *testing.T) {
	t.Parallel()
	f, h := terminalHourFixture(t)
	m := f.input("missing-history")
	m.Start = h.HourStart.Add(2 * time.Hour)
	m.End = m.Start.Add(10 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_builds SET phase='unavailable' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	session, row, source, base := extremaProducerSession(t, f, h)
	start := h.HourStart.Add(5 * time.Hour)
	lanes, err := buildMaintenanceExtremaHour(t.Context(), session, row, base, start)
	if err != nil {
		t.Fatal(err)
	}
	for _, mark := range lanes.Burn6h.Marks {
		input := base
		input.At = mark.At
		input.Duration = 6 * time.Hour
		got, err := mark.result(t.Context(), input)
		if err != nil || got.State != "unavailable" || got.Reason != "maintenance_history_unavailable" || got.Good != nil || mark.Maintenance == nil {
			t.Fatalf("terminal gap lost: %+v %v", got, err)
		}
	}
	end := start.Add(time.Hour)
	points, err := buildMaintenanceRetainedRepairPoints(t.Context(), session, row, source, end.Add(-time.Minute), end, f.now)
	if err != nil || len(points) != 1 {
		t.Fatalf("terminal repair: %v", err)
	}
	p := points[0]
	if p.Current.Performance.Good == nil || p.Current.CalculationProvenance == nil || p.Burn6h.Quality.State != "unavailable" || p.Burn6h.Performance.Good != nil || p.Burn6h.CalculationProvenance != nil || !p.Current.EvaluationAt.Equal(end) || !p.Burn6h.End.Equal(end) {
		t.Fatalf("mixed usable/terminal original cutoff lost: %+v", p)
	}
}

func TestIntegrationMaintenanceExtremaProducerCoarseCadence(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "coarse-producer", true, 2*time.Hour)
	id := s.createSLO(t, "coarse-objective")
	var verifier [32]byte
	verifier[0] = 98
	f := maintenanceFixture{selfHealFixture: s, ids: []string{id}, proof: DatasourceProof{SessionVerifier: verifier}}
	m := f.input("future-coarse-mask")
	m.Start, m.End = s.historyStart.Add(4*time.Hour), s.historyStart.Add(6*time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	f.drain(t, "coarse source")
	h := maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SloID: mustPGUUID(id)}}
	session, row, _, base := extremaProducerSession(t, f, h)
	// Alternate physical hours have zero/one actual 2h-cadence endpoint.
	for phase := range 2 {
		t.Run([]string{"no_endpoint", "one_endpoint"}[phase], func(t *testing.T) {
			start := s.historyStart.Add(time.Duration(phase) * time.Hour)
			got, err := buildMaintenanceExtremaHour(t.Context(), session, row, base, start)
			if err != nil {
				t.Fatal(err)
			}
			want, err := buildExtremaHour(t.Context(), dbgen.New(session.tx), row, base, start)
			if err != nil {
				t.Fatal(err)
			}
			if got.Burn1h != nil {
				t.Fatal("incompatible one-hour burn manufactured")
			}
			for i, lane := range []*extremaLane{got.Window, got.Attainment, got.Burn6h} {
				reference := []*extremaLane{want.Window, want.Attainment, want.Burn6h}[i]
				if lane == nil || len(lane.Marks) != phase || len(reference.Marks) != phase {
					t.Fatalf("phase %d lane %d geometry: %+v", phase, i, lane)
				}
				for j, mark := range lane.Marks {
					input := base
					input.At = mark.At
					if i == 2 {
						input.Duration = 6 * time.Hour
					}
					value, err := mark.result(t.Context(), input)
					original, refErr := reference.Marks[j].result(t.Context(), input)
					if err != nil || refErr != nil || !reflect.DeepEqual(value, original) {
						t.Fatalf("coarse endpoint changed: %v %v", err, refErr)
					}
				}
			}
		})
	}
}

func TestIntegrationMaintenanceExtremaProducerExpired(t *testing.T) {
	t.Parallel()
	for _, full := range []bool{false, true} {
		name := "partial_mask"
		if full {
			name = "full_mask"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			mask := []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}}
			if full {
				m := f.input("expired-full-mask")
				m.Start = h.HourStart
				m.End = h.HourStart.Add(time.Hour)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
					t.Fatal(err)
				}
				b := compilerCapture(t, f, nil)
				compilerFinish(t, f, b.ID, 10)
				mask = []slo.ExclusionInterval{{Start: m.Start, End: m.End}}
			}
			// Model raw retention, without manufacturing historical event totals.
			if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1`, f.sliID); err != nil {
				t.Fatal(err)
			}
			session, row, _, base := extremaProducerSession(t, f, h)
			base.RetentionStart = h.HourStart.Add(time.Hour)
			base.Now = base.RetentionStart.Add(sli.ObservationRetention)
			lanes, err := buildMaintenanceExtremaHour(t.Context(), session, row, base, h.HourStart)
			if err != nil {
				t.Fatal(err)
			}
			generation, err := dbgen.New(session.tx).GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
			if err != nil {
				t.Fatal(err)
			}
			authority := maintenanceRetainedRowAuthority{Scope: maintenancePreparationScope{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision}, SourceGeneration: generation, RequestedRevision: row.ExclusionRevision}
			for i, lane := range []*extremaLane{lanes.Window, lanes.Attainment, lanes.Burn1h, lanes.Burn6h} {
				for _, mark := range lane.Marks {
					input := base
					input.At = mark.At
					input.Duration = []time.Duration{base.Duration, base.Cadence, time.Hour, 6 * time.Hour}[i]
					oracle := input
					oracle.Exclusions = mask
					want, err := slo.EvaluateOperationalWindowContext(t.Context(), oracle)
					if err != nil {
						t.Fatal(err)
					}
					got, err := mark.result(t.Context(), input)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("expired lane %d at %s got=%+v want=%+v err=%v", i, mark.At, got, want, err)
					}
					if err := mark.validateMaintenanceRowAuthority(input, authority); err != nil {
						t.Fatalf("eligible expired mark lost row authority: %v", err)
					}
					if got.Excluded > 0 && (got.ExcludedGood != nil || got.ExcludedTotal != nil) {
						t.Fatal("expired excluded events invented counts")
					}
				}
			}
			final := lanes.Window.Marks[len(lanes.Window.Marks)-1]
			if !final.At.Equal(h.HourStart.Add(time.Hour)) {
				t.Fatal("original hour endpoint lost")
			}
			input := base
			input.At = final.At
			value, err := final.result(t.Context(), input)
			if err != nil || full && value.State != "excluded" || !full && (value.Excluded != 10 || value.Expired != 50) {
				t.Fatalf("expired denominator/full exclusion lost: %+v %v", value, err)
			}
		})
	}
}
