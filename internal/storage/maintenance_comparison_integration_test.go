//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceComparisonExactUnion(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("outer")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	// Two disjoint pieces force persisted multi-turn comparison.
	extra := f.input("second")
	extra.Start = input.End.Add(time.Hour)
	extra.End = extra.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, extra); err != nil {
		t.Fatal(err)
	}
	a := compilerCapture(t, f, nil)
	compilerFinish(t, f, a.ID, 1)
	overlap := f.input("overlap")
	overlap.Start = input.Start.Add(time.Minute)
	overlap.End = input.End.Add(-time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, overlap); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 1)
	var c dbgen.AlmanacMaintenanceComparison
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = prepareMaintenanceComparison(t.Context(), tx, b.ID, a.ID, f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		for _, limit := range []int{0, 1001} {
			if _, err := advanceMaintenanceComparison(t.Context(), tx, c.ID, limit, f.now); !errors.Is(err, ErrMaintenanceInvalid) {
				t.Fatalf("limit%d:%v", limit, err)
			}
		}
		return nil
	})
	// A rolled-back first page must not persist a cursor or equality.
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first, err := advanceMaintenanceComparison(t.Context(), tx, c.ID, 1, f.now)
	if err != nil || first.State != "pending" || first.Compared != 1 {
		t.Fatalf("first page %+v %v", first, err)
	}
	rollbackTransaction(tx)
	for n := 1; n <= 3; n++ {
		compilerTx(t, f, func(tx pgx.Tx) error {
			var err error
			c, err = advanceMaintenanceComparison(t.Context(), tx, c.ID, 1, f.now)
			return err
		})
		if n < 3 && c.State != "pending" {
			t.Fatalf("premature equality:%+v", c)
		}
	}
	if c.State != "equal" || c.Compared != 2 {
		t.Fatalf("terminal:%+v", c)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		if err := cancelMaintenanceComparison(t.Context(), tx, c.ID, f.now); !errors.Is(err, ErrMaintenanceConflict) {
			t.Fatalf("cancel equal:%v", err)
		}
		return nil
	})
	proofTx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	p, err := readMaintenanceCompatibility(t.Context(), proofTx, a.SloID, a.TargetRevision, b.TargetRevision, input.Start, input.End)
	rollbackTransaction(proofTx)
	if err != nil || p.Kind != "equal_union" || p.ComparisonID != c.ID.String() {
		t.Fatalf("overlapping unchanged proof %+v %v", p, err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_maintenance_coverage WHERE build_id=$1`, a.ID); err == nil {
		t.Fatal("comparison input deleted")
	}
	different := f.input("different")
	different.Start = extra.End.Add(time.Hour)
	different.End = different.Start.Add(time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, different); err != nil {
		t.Fatal(err)
	}
	d := compilerCapture(t, f, nil)
	compilerFinish(t, f, d.ID, 1)
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = prepareMaintenanceComparison(t.Context(), tx, a.ID, d.ID, f.now)
		return err
	})
	for range 4 {
		compilerTx(t, f, func(tx pgx.Tx) error {
			var err error
			c, err = advanceMaintenanceComparison(t.Context(), tx, c.ID, 1, f.now)
			return err
		})
		if c.State != "pending" {
			break
		}
	}
	if c.State != "different" {
		t.Fatalf("different union:%+v", c)
	}
	// Cancellation is durable for a distinct direct pair; it never changes a result.
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = prepareMaintenanceComparison(t.Context(), tx, b.ID, d.ID, f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error { return cancelMaintenanceComparison(t.Context(), tx, c.ID, f.now) })
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = advanceMaintenanceComparison(t.Context(), tx, c.ID, 1, f.now)
		return err
	})
	if c.State != "cancelled" || c.Compared != 0 {
		t.Fatalf("cancel resumed:%+v", c)
	}
}
