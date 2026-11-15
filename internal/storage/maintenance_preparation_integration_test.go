//go:build integration

package storage

import "testing"

func TestIntegrationMaintenancePreparationSchema(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	var present bool
	if err := f.conn.QueryRow(t.Context(), `SELECT to_regclass('public.almanac_maintenance_preparation') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("durable maintenance preparation queue is missing")
	}
}
