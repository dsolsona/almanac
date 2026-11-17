//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationMaintenanceRetainedCandidateAuthority(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	end := h.HourStart.Add(time.Hour)
	if _, err := f.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET built_through=$2,dirty_from=NULL,dirty_to=NULL WHERE slo_id=$1`, h.SloID, end); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{0, 1, 3} {
		if _, err := f.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_extrema(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
VALUES($1,1,1,$2,$3,3600,$4,$5,0,$4,$5,'{}')`, h.SloID, revision, operationalCalculationVersion, h.HourStart, end); err != nil {
			t.Fatal(err)
		}
	}
	q := dbgen.New(f.conn)
	for _, tc := range []struct{ requested, want int64 }{{0, 0}, {1, 1}, {2, 1}, {3, 3}} {
		rows, err := q.ReadSloExtremaParts(t.Context(), dbgen.ReadSloExtremaPartsParams{SloID: h.SloID, DefinitionRevision: 1, MeasurementRevision: 1, ExclusionRevision: tc.requested, CalculationVersion: operationalCalculationVersion, Starts: []pgtype.Timestamptz{timestamp(h.HourStart)}, Ends: []pgtype.Timestamptz{timestamp(end)}, Resolutions: []int32{3600}, Graph: []bool{true}, CoarseFrom: timestamp(h.HourStart)})
		if err != nil || len(rows) != 1 || !rows[0].Ready || rows[0].ExclusionRevision != tc.want {
			t.Fatalf("requested%d: candidate=%+v err=%v", tc.requested, rows, err)
		}
	}
}
