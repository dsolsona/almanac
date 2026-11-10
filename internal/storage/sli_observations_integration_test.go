//go:build integration

package storage

import "testing"

func TestIntegrationCanonicalObservationsUseOneTableAndExplicitMarking(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var merged, oldRemoved bool
	if err := conn.QueryRow(t.Context(), `SELECT to_regclass('public.almanac_sli_observations') IS NOT NULL,
to_regclass('public.almanac_sli_coverage') IS NULL AND to_regclass('public.almanac_sli_observation_values') IS NULL`).Scan(&merged, &oldRemoved); err != nil {
		t.Fatal(err)
	}
	if !merged || !oldRemoved {
		t.Fatal("canonical observations still use the two-table layout")
	}
	var removedColumns, triggers int
	var key string
	if err := conn.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='almanac_sli_observations' AND column_name IN ('interval_end','observation_id')),
(SELECT count(*) FROM pg_trigger WHERE tgrelid='public.almanac_sli_observations'::regclass AND NOT tgisinternal),
(SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='public.almanac_sli_observations'::regclass AND contype='p')`).Scan(&removedColumns, &triggers, &key); err != nil {
		t.Fatal(err)
	}
	if removedColumns != 0 || triggers != 0 || key != "PRIMARY KEY (sli_id, measurement_revision, interval_start)" {
		t.Fatalf("observation layout retains redundant fields, hidden marking or a second access key: columns=%d triggers=%d key=%s", removedColumns, triggers, key)
	}
}
