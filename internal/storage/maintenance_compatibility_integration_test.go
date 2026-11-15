//go:build integration

package storage

import (
	"context"
	"errors"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestIntegrationMaintenanceCompatibilitySchema(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	var present bool
	if err := f.conn.QueryRow(t.Context(), `SELECT to_regclass('public.almanac_maintenance_compatibility') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("durable contiguous compatibility authority is absent")
	}
}

func TestIntegrationMaintenanceCompatibilityCatchup(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"legacy-one", "legacy-two"} {
		if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input(key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes ENABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	var build dbgen.AlmanacMaintenanceBuild
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		build, err = prepareMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), f.now)
		return err
	})
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=1`, f.ids[0]); err == nil {
		t.Fatal("catch-up pin lost before analysis")
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("live-after-pin")); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p, err := advanceMaintenanceCompatibility(t.Context(), tx, build.ID, 1)
	if err != nil || p.Scanned != 1 || p.IndexedThrough != 1 {
		t.Fatalf("first:%+v %v", p, err)
	}
	rollbackTransaction(tx)
	for i := int64(1); i <= 2; i++ {
		compilerTx(t, f, func(tx pgx.Tx) error {
			var err error
			p, err = advanceMaintenanceCompatibility(t.Context(), tx, build.ID, 1)
			return err
		})
		if p.IndexedThrough != i || p.Scanned != 1 {
			t.Fatalf("page:%+v", p)
		}
	}
	if p.State != "complete" || p.IndexedThrough != build.TargetRevision {
		t.Fatalf("fixed target followed later event:%+v", p)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		build, err = prepareMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		p, err = advanceMaintenanceCompatibility(t.Context(), tx, build.ID, 1)
		return err
	})
	if p.State != "complete" || p.IndexedThrough != 3 {
		t.Fatalf("later target:%+v", p)
	}
}

func TestIntegrationMaintenanceCompatibilityMissingHistory(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"old-one", "old-two"} {
		if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input(key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.conn.Exec(t.Context(), `CREATE TEMP TABLE missing_event AS SELECT * FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=1`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=1`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes ENABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	var build dbgen.AlmanacMaintenanceBuild
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		build, err = prepareMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), f.now)
		return err
	})
	var p maintenanceCompatibilityProgress
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		p, err = advanceMaintenanceCompatibility(t.Context(), tx, build.ID, 1)
		return err
	})
	if p.State != "unavailable" || p.IndexedThrough != 0 {
		t.Fatalf("gap:%+v", p)
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("new-after-gap")); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	_, err = readMaintenanceCompatibility(t.Context(), tx, build.SloID, 0, 2, f.at, f.at.Add(time.Minute))
	rollbackTransaction(tx)
	if !errors.Is(err, ErrMaintenanceCompileUnavailable) {
		t.Fatalf("live cleared unavailable:%v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_slo_exclusion_changes SELECT * FROM missing_event`); err != nil {
		t.Fatal(err)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		p, err = advanceMaintenanceCompatibility(t.Context(), tx, build.ID, 2)
		return err
	})
	if p.State != "complete" || p.IndexedThrough != 2 {
		t.Fatalf("repaired contiguous:%+v", p)
	}
	tx, err = f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := advanceMaintenanceCompatibility(ctx, tx, build.ID, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel:%v", err)
	}
}
