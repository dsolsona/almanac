//go:build integration

package storage

import (
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestIntegrationMaintenanceCoverageHistorical(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("historical")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 1)
	next := f.input("new-authority")
	next.Start = input.End
	next.End = input.End.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, next); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	window := slo.WindowInput{At: input.Start.Add(30 * time.Minute), Duration: 10 * time.Minute, Cadence: time.Minute}
	got, err := readMaintenanceWindowCoverage(t.Context(), tx, mustPGUUID(f.ids[0]), b.ID, b.TargetRevision, []slo.WindowInput{window}, []slo.WindowTotals{{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].LatestEligibleEnd.Equal(input.Start) {
		t.Fatalf("global endpoint: %+v", got)
	}
	for _, p := range got[0].Points {
		want := uint64(0)
		if p.At.After(input.Start) {
			want = uint64(p.At.Sub(input.Start) / time.Minute)
		}
		if p.ExcludedBefore != want {
			t.Fatalf("rank %s got%d want%d", p.At, p.ExcludedBefore, want)
		}
	}
}
