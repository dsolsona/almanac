//go:build integration

package storage

import (
	"testing"
	"time"
)

func TestIntegrationMaintenanceRetainedSchema(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	// Existing canonical rows and adjusted rows at the same bucket coexist;
	// accepting a requested authority must never overwrite legacy evidence.
	for _, revision := range []int64{0, 1} {
		_, err := f.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_extrema
		(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,
		resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
		VALUES ($1,1,1,$2,$3,3600,$4,$5,0,$4,$5,'{}')`, h.SloID, revision, operationalCalculationVersion, h.HourStart, h.HourStart.Add(time.Hour))
		if err != nil {
			t.Fatalf("retain authority %d: %v", revision, err)
		}
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1`, h.SloID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy/adjusted rows lost: count=%d err=%v", count, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema SET exclusion_revision=-1 WHERE slo_id=$1`, h.SloID); err == nil {
		t.Fatal("negative authority accepted")
	}
}
