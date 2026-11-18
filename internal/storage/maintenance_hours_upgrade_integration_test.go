//go:build integration

package storage

import "testing"

func TestIntegrationMaintenanceHourPopulatedUpgrade(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	dropMaintenanceCompatibilityFixture(t, f.conn)
	if _, err := f.conn.Exec(t.Context(), `DROP TABLE almanac_slo_maintenance_hours; DROP INDEX public.almanac_maintenance_revision_range_idx; DELETE FROM almanac_schema_migrations WHERE version>=63`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var result string
		err := f.conn.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'hours',(SELECT jsonb_agg(to_jsonb(h) ORDER BY hour_start) FROM almanac_sli_hourly_aggregates h),
 'raw',(SELECT jsonb_agg(to_jsonb(o) ORDER BY interval_start) FROM almanac_sli_observations o),
 'builds',(SELECT jsonb_agg(to_jsonb(b) ORDER BY id) FROM almanac_maintenance_builds b),
 'coverage',(SELECT jsonb_agg(to_jsonb(c) ORDER BY build_id,starts_at) FROM almanac_maintenance_coverage c),
 'generations',(SELECT jsonb_agg(to_jsonb(g) ORDER BY sli_id,measurement_revision) FROM almanac_sli_source_generations g))::text`).Scan(&result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	result, err := Upgrade(t.Context(), f.f.databaseURL)
	if err != nil || result.Applied != 7 || result.DatabaseSchemaVersion != 69 {
		t.Fatalf("upgrade %+v %v", result, err)
	}
	if snapshot() != before {
		t.Fatal("scoped-hour migration changed source facts or immutable mask")
	}
	if _, err := hourPrepare(t, f, in); err != nil {
		t.Fatal(err)
	}
}
