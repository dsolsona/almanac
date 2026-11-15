//go:build integration

package storage

import (
	"errors"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceCompatibilitySnapshot(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("initial")
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	p, err := readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), 0, 1, input.End, input.End.Add(time.Hour))
	if err != nil || p.Kind != "indexed_absence" {
		t.Fatalf("initial:%+v %v", p, err)
	}
	if _, err := EditMaintenance(t.Context(), f.f.databaseURL, compatibilityEdit(f, r, "concurrent", r.Start, r.End.Add(time.Hour), r.SloIDs)); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), 1, 2, input.End, input.End.Add(time.Hour)); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("cross-snapshot authority:%v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	p, err = readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), 1, 2, input.End, input.End.Add(time.Hour))
	if err != nil || p.Kind != "changed" {
		t.Fatalf("new snapshot:%+v %v", p, err)
	}
}

func TestIntegrationMaintenanceComparisonStaleDelete(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	a := f.input("first")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, a); err != nil {
		t.Fatal(err)
	}
	b := f.input("second")
	b.Start = a.End.Add(time.Hour)
	b.End = b.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, b); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, f, nil)
	compilerStep(t, f, build.ID, 2) // Seal both raw declarations.
	compilerStep(t, f, build.ID, 1) // First union frontier.
	compilerStep(t, f, build.ID, 1) // Emit first row, keep second frontier.
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	var stage string
	if err := tx.QueryRow(t.Context(), `SELECT phase FROM almanac_maintenance_builds WHERE id=$1`, build.ID).Scan(&stage); err != nil || stage != "union" {
		t.Fatalf("stage %s:%v", stage, err)
	}
	publisher := f
	copySource := *f.selfHealFixture
	publisher.selfHealFixture = &copySource
	publisher.conn = integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(publisher.conn)
	compilerFinish(t, publisher, build.ID, 1)
	_, err = tx.Exec(t.Context(), `DELETE FROM almanac_maintenance_coverage WHERE build_id=$1`, build.ID)
	if compilerPGCode(err) != "40001" {
		t.Fatalf("stale RR deletion bypassed completed pin:%v", err)
	}
}

func TestIntegrationMaintenanceCompatibilitySameRevisionBeforeCatchup(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	// Model a genuine pre-analysis declaration, while retaining its immutable R1 union.
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	input := f.input("legacy-same-revision")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes ENABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	retained := compilerCapture(t, f, nil)
	compilerFinish(t, f, retained.ID, 1)
	checkSame := func(t *testing.T) {
		t.Helper()
		tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackTransaction(tx)
		recorder := &compatibilityQueryTx{Tx: tx}
		p, err := readMaintenanceCompatibility(t.Context(), recorder, retained.SloID, 1, 1, input.Start, input.End)
		if err != nil || p.Kind != "same_revision" || p.ComputedRevision != 1 || p.RequestedRevision != 1 || p.IndexedThrough != 0 {
			t.Fatalf("retained identical R1 blocked by catch-up: %+v %v", p, err)
		}
		if !strings.Contains(recorder.sql, "ReadMaintenanceCompatibilityAuthority") {
			t.Fatal("same revision queried changed inputs")
		}
	}
	t.Run("unprepared", checkSame)
	// A later missing event must not invalidate the already retained same-mask pair.
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("later-gap")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=2`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	var pin dbgen.AlmanacMaintenanceBuild
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		pin, err = prepareMaintenanceCompatibility(t.Context(), tx, retained.SloID, f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, err := advanceMaintenanceCompatibility(t.Context(), tx, pin.ID, 3)
		if err == nil && (p.State != "unavailable" || p.IndexedThrough != 0) {
			t.Fatalf("gap fixture: %+v", p)
		}
		return err
	})
	t.Run("unavailable_later_gap", checkSame)
	t.Run("different_revision_still_unavailable", func(t *testing.T) {
		tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackTransaction(tx)
		if _, err := readMaintenanceCompatibility(t.Context(), tx, retained.SloID, 0, 1, input.Start, input.End); !errors.Is(err, ErrMaintenanceCompileUnavailable) {
			t.Fatalf("changed revision bypassed preparation: %v", err)
		}
		if _, err := readMaintenanceCompatibility(t.Context(), tx, retained.SloID, 3, 3, input.Start, input.End); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
			t.Fatalf("future equal revisions bypassed authority: %v", err)
		}
	})
	t.Run("read_committed_rejected", func(t *testing.T) {
		tx, err := f.conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackTransaction(tx)
		if _, err := readMaintenanceCompatibility(t.Context(), tx, retained.SloID, 1, 1, input.Start, input.End); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
			t.Fatalf("equal revisions bypassed RR: %v", err)
		}
	})
}
