//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationCanonicalReceiptRawBound(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-raw-bound", true)
	q := dbgen.New(s.conn)
	params := dbgen.ReadCanonicalReceiptPopulationParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, HourStart: timestamp(s.historyStart)}
	got, err := q.ReadCanonicalReceiptPopulation(t.Context(), params)
	if err != nil || got.RawRows != 60 || got.Observed != 60 || sli.NormalizeDecimal(got.Good) != "59400" || !got.LatestReceipt.Time.Equal(s.historyStart.Add(time.Hour)) {
		t.Fatalf("one physical hour: %+v %v", got, err)
	}
	// Fixture-only malformed subsecond starts exercise the defensive overflow
	// sentinel. This is not an assertion that canonical ingestion accepts them.
	if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT point,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at FROM almanac_sli_observations CROSS JOIN generate_series($2::timestamptz+interval '0.5 second',$2::timestamptz+interval '3599.5 seconds',interval '1 second') point WHERE sli_id=$1 AND interval_start=$2`, s.sliID, s.historyStart); err != nil {
		t.Fatal(err)
	}
	got, err = q.ReadCanonicalReceiptPopulation(t.Context(), params)
	if err != nil || got.RawRows != 3601 {
		t.Fatalf("raw read ignored defensive cap: %+v %v", got, err)
	}
}
