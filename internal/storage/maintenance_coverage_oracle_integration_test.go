//go:build integration

package storage

import (
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5"
)

// The test enumerates literal observation starts and declaration membership.
// It does not use the compiler union/prefix implementation to calculate totals.
func coverageOracleTotals(input slo.WindowInput) slo.WindowTotals {
	totals := slo.WindowTotals{Good: new(big.Rat), Total: new(big.Rat)}
	first := input.At.Add(-input.Duration)
	for _, at := range []time.Time{input.ValidFrom, input.SliValidFrom, input.RetentionStart} {
		if at.After(first) {
			first = at
		}
	}
	var eg, et big.Rat
	for _, o := range input.Observations {
		if o.Start.Before(first) || o.End.After(input.At) {
			continue
		}
		excluded := false
		for _, m := range input.Exclusions {
			if !o.Start.Before(m.Start) && o.Start.Before(m.End) {
				excluded = true
			}
		}
		if o.Outcome != "observed" && o.Outcome != "complete_empty" {
			continue
		}
		if excluded {
			if o.Outcome == "complete_empty" {
				totals.Excluded.KnownEmpty++
				continue
			}
			g, _ := new(big.Rat).SetString(o.Good)
			n, _ := new(big.Rat).SetString(o.Total)
			eg.Add(&eg, g)
			et.Add(&et, n)
			totals.Excluded.Observed++
			continue
		}
		if o.Outcome == "observed" {
			g, _ := new(big.Rat).SetString(o.Good)
			n, _ := new(big.Rat).SetString(o.Total)
			totals.Good.Add(totals.Good, g)
			totals.Total.Add(totals.Total, n)
			totals.Observed++
			if n.Sign() == 0 {
				totals.ZeroTraffic++
			}
			if totals.FirstObservation == nil {
				totals.FirstObservation = new(o.Start)
			}
			totals.LastObservation = new(o.End)
		} else {
			totals.KnownEmpty++
		}
		if totals.LatestReceipt == nil || o.Received.After(*totals.LatestReceipt) {
			totals.LatestReceipt = new(o.Received)
		}
	}
	if totals.Excluded.Observed > 0 {
		totals.Excluded.Good = new(big.Rat).Set(&eg)
		totals.Excluded.Total = new(big.Rat).Set(&et)
	}
	return totals
}

func TestIntegrationMaintenanceCoverageRawOracle(t *testing.T) {
	for _, cadence := range []time.Duration{time.Minute, 70 * time.Second} {
		t.Run(cadence.String(), func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixtureCadence(t, "coverage", true, cadence)
			id := s.createSLO(t, "coverage")
			var verifier [32]byte
			verifier[0] = 98
			f := maintenanceFixture{s, []string{id}, DatasourceProof{SessionVerifier: verifier}}
			start := ceilSliBoundary(f.at.Add(time.Hour), cadence)
			masks := []slo.ExclusionInterval{}
			for i, bounds := range [][2]int{{1, 4}, {3, 6}, {10, 14}} {
				m := f.input(fmt.Sprintf("mask-%d", i))
				m.Start = start.Add(time.Duration(bounds[0]) * cadence)
				m.End = start.Add(time.Duration(bounds[1]) * cadence)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
					t.Fatal(err)
				}
				masks = append(masks, slo.ExclusionInterval{Start: m.Start, End: m.End})
			}
			build := compilerCapture(t, f, nil)
			compilerFinish(t, f, build.ID, 2)
			// A later authority would exclude the otherwise observed first interval.
			m := f.input("later")
			m.Start = start
			m.End = start.Add(cadence)
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
				t.Fatal(err)
			}
			base := slo.WindowInput{At: start.Add(16 * cadence), Now: start.Add(17 * cadence), Duration: 16 * cadence, Cadence: cadence, Target: "0.99", ValidFrom: start, SliValidFrom: start, RetentionStart: start, Exclusions: masks}
			for i := range 16 {
				if i == 7 {
					continue
				}
				o := slo.Observation{Start: start.Add(time.Duration(i) * cadence), End: start.Add(time.Duration(i+1) * cadence), Received: start.Add(time.Duration(i+2) * cadence), Outcome: "observed", Good: "9.125", Total: "10.25"}
				if i == 3 || i == 8 {
					o.Outcome = "complete_empty"
					o.Good = ""
					o.Total = ""
				}
				if i == 9 {
					o.Good = "0"
					o.Total = "0"
				}
				if i == 15 {
					o.Outcome = "failed"
					o.Good = ""
					o.Total = ""
					o.Received = base.Now.Add(time.Hour)
				}
				base.Observations = append(base.Observations, o)
			}
			inputs := []slo.WindowInput{base, base, base, base, base}
			inputs[1].ValidFrom = start.Add(2*cadence + time.Microsecond)
			inputs[1].RetentionStart = start.Add(5*cadence + time.Microsecond)
			inputs[2].At = start.Add(13 * cadence)
			inputs[2].Duration = 2 * cadence
			inputs[3].RetentionStart = base.At.Add(cadence)
			inputs[4].ValidFrom = base.At.Add(cadence)
			totals := make([]slo.WindowTotals, len(inputs))
			raw := make([]slo.WindowInput, len(inputs))
			copy(raw, inputs)
			for i := range inputs {
				totals[i] = coverageOracleTotals(inputs[i])
				inputs[i].Observations = nil
				inputs[i].Exclusions = nil
			}
			tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			certs, err := readMaintenanceWindowCoverage(t.Context(), tx, mustPGUUID(id), build.ID, build.TargetRevision, inputs, totals)
			if err != nil {
				t.Fatal(err)
			}
			for i := range inputs {
				got, err := slo.EvaluateOperationalCoveredTotals(t.Context(), inputs[i], totals[i], certs[i])
				if err != nil {
					t.Fatalf("window %d: %v", i, err)
				}
				want, err := slo.EvaluateOperationalWindowContext(t.Context(), raw[i])
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("window %d differs\ngot %+v\nwant %+v", i, got, want)
				}
			}
			if !certs[2].LatestEligibleEnd.Equal(start.Add(10 * cadence)) {
				t.Fatal("full-window endpoint clamped")
			}
		})
	}
}
