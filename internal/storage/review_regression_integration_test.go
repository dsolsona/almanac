//go:build integration

package storage

import (
	"testing"
	"time"
)

func TestIntegrationMonthEndPartitionsAndLeaseIndexes(t *testing.T) {
	t.Parallel()
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	for _, year := range []int{2026, 2028} {
		tx, err := conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareSliPartitions(t.Context(), tx, time.Date(year, 1, 31, 23, 59, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"almanac_sli_observations"} {
			var exists bool
			name := table + "_" + time.Date(year, 2, 1, 0, 0, 0, 0, time.UTC).Format("200601")
			if err := conn.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", "public."+name).Scan(&exists); err != nil || !exists {
				t.Fatalf("missing %s: %v", name, err)
			}
		}
	}
	for _, index := range []string{"almanac_sli_observations_lease_idx", "almanac_sli_execution_attempts_lease_idx", "almanac_sli_delivery_receipts_lease_idx"} {
		var exists bool
		if err := conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname=$1 AND indexdef LIKE '%(lease_id)%')", index).Scan(&exists); err != nil || !exists {
			t.Fatalf("missing lease index %s: %v", index, err)
		}
	}
}

func TestIntegrationExistingObservationPartitionsAvoidPreparationLocks(t *testing.T) {
	t.Parallel()
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	now := time.Date(2028, 1, 15, 0, 0, 0, 0, time.UTC)
	setup, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Rollback(t.Context())
	if err := prepareSliPartitions(t.Context(), setup, now); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	locker := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(locker)
	held, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(t.Context())
	if _, err := held.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('almanac-sli-partition:almanac_sli_observations_202801', 0))`); err != nil {
		t.Fatal(err)
	}

	probe, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback(t.Context())
	if _, err := probe.Exec(t.Context(), "SET LOCAL lock_timeout='250ms'"); err != nil {
		t.Fatal(err)
	}
	if err := prepareSliPartitions(t.Context(), probe, now); err != nil {
		t.Fatalf("reusing existing partitions should not wait for DDL coordination: %v", err)
	}
}
