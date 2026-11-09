//go:build integration

package storage

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed testdata/mark_observation_fixture.sql
var observationFixtureMarkSQL string

// Bulk fixtures reproduce acceptance marking without one API call per raw interval.
func markObservationFixture(ctx context.Context, db dbgen.DBTX, sliID string) error {
	_, err := db.Exec(ctx, observationFixtureMarkSQL, sliID)
	return err
}

func execObservationFixture(t *testing.T, conn *pgx.Conn, sliID, statement string, arguments ...any) (pgconn.CommandTag, error) {
	t.Helper()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer rollbackTransaction(tx)
	result, err := tx.Exec(t.Context(), statement, arguments...)
	if err != nil {
		return result, err
	}
	if result.RowsAffected() > 0 {
		if err := markObservationFixture(t.Context(), tx, sliID); err != nil {
			return result, err
		}
	}
	return result, tx.Commit(t.Context())
}

func TestIntegrationObservationFixtureMarksEveryRawInterval(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "fixture-marking", true, 70*time.Second)
	id := s.createSLO(t, "fixture-marking")
	var generation, distinct, version int64
	if err := s.conn.QueryRow(t.Context(), `SELECT g.generation,
(SELECT count(DISTINCT ingestion_generation) FROM public.almanac_sli_observations WHERE sli_id=$1),w.version
FROM public.almanac_sli_source_generations g JOIN public.almanac_slo_extrema_cursor w
ON w.measurement_revision=g.measurement_revision WHERE g.sli_id=$1 AND w.slo_id=$2`, s.sliID, id).Scan(&generation, &distinct, &version); err != nil {
		t.Fatal(err)
	}
	if want := int64(s.at.Sub(s.historyStart) / s.cadence); generation != want || distinct != want {
		t.Fatalf("bulk seed lost raw intervals or individual watermarks: generation=%d distinct=%d want=%d", generation, distinct, want)
	}
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations
SET good_count=991,ingestion_generation=0 WHERE sli_id=$1 AND interval_start IN
(SELECT interval_start FROM public.almanac_sli_observations WHERE sli_id=$1 ORDER BY interval_start LIMIT 2)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	var nextGeneration, nextVersion, hourGeneration int64
	if err := s.conn.QueryRow(t.Context(), `SELECT g.generation,w.version,d.generation
FROM public.almanac_sli_source_generations g JOIN public.almanac_slo_extrema_cursor w ON w.measurement_revision=g.measurement_revision
JOIN public.almanac_derived_dirty_ranges d ON d.sli_id=g.sli_id AND d.measurement_revision=g.measurement_revision
WHERE g.sli_id=$1 AND w.slo_id=$2 AND d.range_start=$3`, s.sliID, id, s.historyStart.Truncate(time.Hour)).Scan(&nextGeneration, &nextVersion, &hourGeneration); err != nil {
		t.Fatal(err)
	}
	if nextGeneration != generation+2 || nextVersion != version+2 || hourGeneration != nextGeneration {
		t.Fatalf("bulk mutation lost per-observation marking: source=%d cursor=%d hour=%d", nextGeneration, nextVersion, hourGeneration)
	}
	result, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO public.almanac_sli_observations
SELECT * FROM public.almanac_sli_observations WHERE sli_id=$1 LIMIT 1 ON CONFLICT DO NOTHING`, s.sliID)
	if err != nil || result.RowsAffected() != 0 {
		t.Fatalf("fixture duplicate was not a no-op: %v", err)
	}
	var unchanged bool
	if err := s.conn.QueryRow(t.Context(), `SELECT g.generation=$3 AND w.version=$4
FROM public.almanac_sli_source_generations g JOIN public.almanac_slo_extrema_cursor w ON w.measurement_revision=g.measurement_revision
WHERE g.sli_id=$1 AND w.slo_id=$2`, s.sliID, id, nextGeneration, nextVersion).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("fixture duplicate marked source or cursor work: %v", err)
	}
}
