package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func maintenanceDayFixture(t *testing.T, cadence time.Duration, masked bool) (dbgen.GetSloRow, slo.WindowInput, time.Time, []maintenanceExtremaDayPart) {
	t.Helper()
	seed, _, _ := retainedMaintenanceFixture(t)
	end := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	start := end.Add(-24 * time.Hour)
	duration := ((time.Hour + cadence - 1) / cadence) * cadence
	base := slo.WindowInput{Now: end, Duration: duration, Cadence: cadence, Target: "0.999", ValidFrom: start.Add(-24 * time.Hour), SliValidFrom: start.Add(-24 * time.Hour), RetentionStart: start.Add(-24 * time.Hour), FreshnessGrace: cadence}
	row := dbgen.GetSloRow{ID: seed.Maintenance.Scope.SloID, SliID: seed.Maintenance.Scope.SliID, DefinitionRevision: 1, MeasurementRevision: 1, ExclusionRevision: 2, CadenceSeconds: int32(cadence / time.Second), RollingWindowSeconds: int64(duration / time.Second), Target: base.Target}
	var mask []slo.ExclusionInterval
	if masked {
		mask = []slo.ExclusionInterval{{Start: start.Add(3 * time.Hour), End: start.Add(4 * time.Hour)}, {Start: start.Add(9*time.Hour + 20*time.Minute), End: start.Add(9*time.Hour + 30*time.Minute)}}
	}
	parts := make([]maintenanceExtremaDayPart, 24)
	for hour := range 24 {
		at := start.Add(time.Duration(hour) * time.Hour)
		part := &parts[hour]
		part.Start = at
		part.RetentionStart = base.RetentionStart.Add(time.Duration(hour) * time.Minute)
		part.Row = maintenanceRetainedRowAuthority{Scope: seed.Maintenance.Scope, RequestedRevision: 2, SourceGeneration: int64(10 + hour)}
		var lanes [4]*extremaLane
		for lane, duration := range []time.Duration{base.Duration, cadence, time.Hour, 6 * time.Hour} {
			if duration%cadence != 0 {
				continue
			}
			input := base
			input.Duration = duration
			input.RetentionStart = part.RetentionStart
			var values []slo.WindowResult
			var originals []extremaMark
			first, last := floorSliBoundary(at, cadence).Add(cadence), floorSliBoundary(at.Add(time.Hour), cadence)
			endpoints := []time.Time{first, last}
			if first.Equal(last) {
				endpoints = endpoints[:1]
			}
			for _, endpoint := range endpoints {
				if endpoint.Before(first) || endpoint.After(last) {
					continue
				}
				input.At = endpoint
				raw := input
				raw.Exclusions = mask
				for point := input.At.Add(-duration); point.Before(input.At); point = point.Add(cadence) {
					good := "990"
					if hour == 11 {
						good = "950"
					}
					raw.Observations = append(raw.Observations, slo.Observation{Start: point, End: point.Add(cadence), Received: point.Add(cadence), Outcome: "observed", Good: good, Total: "1000"})
				}
				value, err := slo.EvaluateOperationalWindowContext(t.Context(), raw)
				if err != nil {
					t.Fatal(err)
				}
				selected, err := makeExtremaSummaryLane([]slo.WindowResult{value}, at, at.Add(time.Hour), base.Target, []string{"budget", "attainment", "burn", "burn"}[lane], "1")
				if err != nil {
					t.Fatal(err)
				}
				mark := selected.Marks[0]
				m := &retainedMaintenanceInputs{Version: 1, Scope: part.Row.Scope, SourceGeneration: part.Row.SourceGeneration - 1, ComputedRevision: 0, RequestedRevision: 2, Proof: &maintenanceCompatibilityProof{SloID: row.ID.String(), ComputedRevision: 0, RequestedRevision: 2, IndexedThrough: 2, Start: input.At.Add(-duration), End: input.At, Kind: "indexed_absence"}}
				if masked {
					m.ComputedRevision = 1
					m.BuildID = seed.Maintenance.BuildID
					m.Proof.ComputedRevision = 1
					m.ExcludedGood, m.ExcludedTotal = value.ExcludedGood, value.ExcludedTotal
					m.ExcludedObserved = value.Excluded
					totals := slo.WindowTotals{Observed: value.Observed, FirstObservation: value.FirstObservation, LastObservation: value.LastObservation}
					latest := input.At
					for _, span := range mask {
						if span.Start.Before(input.At) && !span.End.Before(input.At) {
							latest = span.Start
						}
					}
					anchors, err := slo.RequiredCoverageAnchors(input, totals, latest)
					if err != nil {
						t.Fatal(err)
					}
					m.Coverage = &slo.WindowCoverage{LatestEligibleEnd: latest}
					for _, anchor := range anchors {
						var rank uint64
						for _, span := range mask {
							stop := anchor
							if span.End.Before(stop) {
								stop = span.End
							}
							if stop.After(span.Start) {
								rank += uint64(stop.Sub(span.Start) / cadence)
							}
						}
						m.Coverage.Points = append(m.Coverage.Points, slo.ExclusionCoveragePoint{At: anchor, ExcludedBefore: rank})
					}
				}
				mark.Maintenance = m
				got, err := mark.result(t.Context(), input)
				if err != nil || !reflect.DeepEqual(got, value) {
					t.Fatalf("fixture exact replay: %v", err)
				}
				values = append(values, value)
				originals = append(originals, mark)
			}
			built, err := makeExtremaSummaryLane(values, at, at.Add(time.Hour), base.Target, []string{"budget", "attainment", "burn", "burn"}[lane], "1")
			if err != nil {
				t.Fatal(err)
			}
			for j, selected := range built.Marks {
				for _, original := range originals {
					if original.At.Equal(selected.At) {
						original.Labels = selected.Labels
						built.Marks[j] = original
						break
					}
				}
			}
			lanes[lane] = &built
		}
		part.Lanes = extremaLanes{Window: lanes[0], Attainment: lanes[1], Burn1h: lanes[2], Burn6h: lanes[3]}
	}
	return row, base, end, parts
}

func TestMaintenanceExtremaDayCompose(t *testing.T) {
	t.Parallel()
	for _, cadence := range []time.Duration{15 * time.Second, time.Minute, 70 * time.Second, 2 * time.Hour} {
		t.Run(cadence.String(), func(t *testing.T) {
			t.Parallel()
			row, base, end, parts := maintenanceDayFixture(t, cadence, cadence == time.Minute)
			before, _ := json.Marshal(parts)
			got, err := composeMaintenanceExtremaDay(t.Context(), row, base, end, parts)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(parts)
			if string(before) != string(after) {
				t.Fatal("composer mutated hourly evidence")
			}
			if got.Compliance == nil || !got.Compliance.Mark.At.Equal(floorSliBoundary(end, cadence)) || !got.Compliance.DayEnd.Equal(end) {
				t.Fatal("compliance did not use actual final cadence endpoint")
			}
			for lane, result := range got.list() {
				if lane >= 2 && []time.Duration{time.Hour, 6 * time.Hour}[lane-2]%cadence != 0 {
					if result != nil {
						t.Fatal("phantom burn lane")
					}
					continue
				}
				if result == nil || len(result.Marks) < 2 {
					t.Fatal("day selection lost endpoints")
				}
				for _, selected := range result.Marks {
					found := false
					for _, part := range parts {
						for _, original := range part.Lanes.list()[lane].Marks {
							if selected.At.Equal(original.At) {
								found = true
								original.RetentionStart = part.RetentionStart
								original.Labels = selected.Labels
								if !reflect.DeepEqual(selected, original) {
									t.Fatal("selected mark lost original retained identity/population")
								}
							}
						}
					}
					if !found {
						t.Fatal("manufactured selected endpoint")
					}
				}
			}
		})
	}
}

func TestMaintenanceExtremaDayRejectsIncompleteAuthority(t *testing.T) {
	t.Parallel()
	row, base, end, original := maintenanceDayFixture(t, time.Minute, true)
	for _, kind := range []string{"missing", "duplicate", "gap", "wrong_scope", "wrong_revision", "newer_source", "missing_lane", "empty_eligible", "foreign_compliance", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var parts []maintenanceExtremaDayPart
			if err := json.Unmarshal(data, &parts); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch kind {
			case "missing":
				parts = parts[:23]
			case "duplicate":
				parts[2].Start = parts[1].Start
			case "gap":
				parts[2].Start = parts[2].Start.Add(time.Minute)
			case "wrong_scope":
				parts[2].Row.Scope.SloID = row.SliID
			case "wrong_revision":
				parts[2].Row.RequestedRevision++
			case "newer_source":
				parts[2].Row.SourceGeneration = 0
			case "missing_lane":
				parts[2].Lanes.Attainment = nil
			case "empty_eligible":
				parts[2].Lanes.Window.Marks = nil
			case "foreign_compliance":
				m := parts[2].Lanes.Window.Marks[1]
				m.Maintenance = new(*m.Maintenance)
				m.Maintenance.Scope.SloID = row.SliID
				parts[2].Lanes.Compliance = &extremaCompliance{DayEnd: parts[2].Start.Add(time.Hour), Mark: m}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := composeMaintenanceExtremaDay(ctx, row, base, end, parts)
			if err == nil {
				t.Fatal("incomplete/unauthenticated evidence accepted")
			}
			if (kind == "missing" || kind == "missing_lane" || kind == "empty_eligible") && !errors.Is(err, ErrDerivedPending) {
				t.Fatalf("missing part should remain pending: %v", err)
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestMaintenanceExtremaDayRetainedEdges(t *testing.T) {
	t.Parallel()
	row, base, end, parts := maintenanceDayFixture(t, time.Minute, true)
	// This endpoint reuses an older unmasked hour; the day must preserve the
	// original key and proof, not relabel it to its row's newer generation.
	last := &parts[23].Lanes.Window.Marks[1]
	oldBuild := mustPGUUID("cfd5bc95-7f07-497b-8abc-07c458112f31")
	last.Maintenance.ReusedHours = []maintenanceReusedHour{{Key: maintenanceHourKey{SloID: row.ID, SliID: row.SliID, BuildID: oldBuild, DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: 7, HourStart: end.Add(-time.Hour)}, ComputedRevision: 0, Proof: maintenanceCompatibilityProof{SloID: row.ID.String(), ComputedRevision: 0, RequestedRevision: 1, IndexedThrough: 1, Start: end.Add(-time.Hour), End: end, Kind: "indexed_absence"}}}
	last.RetentionStart = parts[23].RetentionStart.Add(time.Second)
	parts[23].Lanes.Compliance = &extremaCompliance{DayEnd: end, Mark: *last}
	// A terminal historical interval is retained as a gap, never recomputed as
	// missing observations or dropped while reducing otherwise healthy hours.
	lane := parts[20].Lanes.Burn6h
	for index := range lane.Marks {
		mark := &lane.Marks[index]
		input := base
		input.Duration = 6 * time.Hour
		input.At = mark.At
		value, err := unavailableMaintenanceWindow(input, "maintenance_history_unavailable")
		if err != nil {
			t.Fatal(err)
		}
		sample := operationalSample(value, parts[20].Start, parts[20].Start.Add(time.Hour), "1")
		mark.State, mark.Quality, mark.Freshness = sample.State, sample.Quality.State, sample.Quality.Freshness
		mark.Good, mark.Total, mark.Observed = "0", "0", 0
		mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
		mark.Maintenance = &retainedMaintenanceInputs{Version: 1, Scope: parts[20].Row.Scope, SourceGeneration: parts[20].Row.SourceGeneration, RequestedRevision: 2, UnavailableReason: "maintenance_history_unavailable"}
	}
	got, err := composeMaintenanceExtremaDay(t.Context(), row, base, end, parts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Compliance.Mark, *last) {
		t.Fatal("compliance changed original source/proof/hour key/retention")
	}
	found := false
	for _, mark := range got.Burn6h.Marks {
		if mark.At.After(parts[20].Start) && !mark.At.After(parts[20].Start.Add(time.Hour)) {
			found = true
			input := base
			input.Duration = 6 * time.Hour
			value, err := mark.result(t.Context(), input)
			if err != nil || value.State != "unavailable" || value.Good != nil || value.Burn != nil || value.Reason != "maintenance_history_unavailable" {
				t.Fatalf("terminal mark lost: %+v %v", value, err)
			}
		}
	}
	if !found {
		t.Fatal("terminal gap disappeared from day selection")
	}
}

func TestMaintenanceExtremaDayBeforeValidity(t *testing.T) {
	t.Parallel()
	row, base, end, parts := maintenanceDayFixture(t, 2*time.Hour, false)
	base.ValidFrom = end.Add(base.Cadence)
	reader := retainedSeriesReader{row: row}
	for index := range parts {
		part := &parts[index]
		tile, err := reader.beforeValidity(t.Context(), base, retainedReadPart{start: part.Start, end: part.Start.Add(time.Hour), resolution: 3600})
		if err != nil {
			t.Fatal(err)
		}
		part.Lanes, err = makeExtremaLanes([4][]api.SloOperationalSample{tile.Budget, tile.Attainment, tile.Burn1h, tile.Burn6h}, part.Start, part.Start.Add(time.Hour), base.Target, base.Cadence)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := composeMaintenanceExtremaDay(t.Context(), row, base, end, parts)
	if err != nil {
		t.Fatal(err)
	}
	for _, mark := range got.Window.Marks {
		value, err := mark.result(t.Context(), base)
		input := base
		input.At = mark.At
		want, oracleErr := slo.EvaluateOperationalWindowContext(t.Context(), input)
		if err != nil || oracleErr != nil || !reflect.DeepEqual(value, want) || value.State != "not_applicable" || value.Expected != 0 || value.Excluded != 0 || mark.Maintenance != nil {
			t.Fatalf("pre-validity became excluded/pending: %+v %v", value, err)
		}
	}
}

func TestMaintenanceExtremaDayOwnsSelectedEvidence(t *testing.T) {
	t.Parallel()
	mutate := func(mark *extremaMark) {
		mark.Maintenance.SourceGeneration++
		mark.Maintenance.Proof.IndexedThrough++
		mark.Maintenance.Coverage.Points[0].ExcludedBefore++
		*mark.LatestReceipt = mark.LatestReceipt.Add(time.Second)
		*mark.FirstObservation = mark.FirstObservation.Add(time.Second)
		*mark.LastObservation = mark.LastObservation.Add(time.Second)
		mark.Labels[0] = "ownership-test"
	}
	for _, direction := range []string{"output_to_input", "input_to_output"} {
		t.Run(direction, func(t *testing.T) {
			t.Parallel()
			row, base, end, parts := maintenanceDayFixture(t, time.Minute, true)
			day, err := composeMaintenanceExtremaDay(t.Context(), row, base, end, parts)
			if err != nil {
				t.Fatal(err)
			}
			if direction == "output_to_input" {
				before, err := json.Marshal(parts)
				if err != nil {
					t.Fatal(err)
				}
				mutate(&day.Window.Marks[0])
				mutate(&day.Compliance.Mark)
				after, err := json.Marshal(parts)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatal("returned day/compliance mutated caller-owned hourly evidence")
				}
			} else {
				before, err := json.Marshal(day)
				if err != nil {
					t.Fatal(err)
				}
				mutate(&parts[0].Lanes.Window.Marks[0])
				mutate(&parts[23].Lanes.Window.Marks[1])
				after, err := json.Marshal(day)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatal("hourly evidence mutation changed returned day/compliance")
				}
			}
		})
	}
}
