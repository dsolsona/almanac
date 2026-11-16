//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceConsumerCurrent(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	row, err := q.GetSlo(t.Context(), h.SloID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	at := h.HourStart.Add(time.Hour)
	input := OperationalDetailInput{currentOnly: true, At: &at, Now: f.clock, BurnWindow: 3600,
		maintenance: &maintenanceConsumerSession{tx: tx, requestedRevision: 1, limits: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 10000, MaxFragments: 10000}}}
	got, err := buildSloOperationalDetail(t.Context(), q, row, source, input)
	if err != nil || got.Current.Performance.Good == nil || *got.Current.Performance.Good != "49500.000000000000" || got.Current.Performance.Total == nil || *got.Current.Performance.Total != "50000.000000000000" || got.Current.Quality.ExpectedIntervals != 50 || got.Burn1h.Performance.Total == nil || *got.Burn1h.Performance.Total != "50000.000000000000" {
		t.Fatalf("current and one-hour burn did not share adjusted counts: %+v err=%v", got, err)
	}
	for _, p := range []*api.SloCalculationProvenance{got.Current.CalculationProvenance, got.Burn1h.CalculationProvenance} {
		if p == nil || p.RequestedRevision != "1" || p.ComputedRevision != "1" || p.BuildId == nil || p.BuildId.String() != h.BuildID.String() {
			t.Fatalf("current public identity lost: %+v", p)
		}
	}
}
