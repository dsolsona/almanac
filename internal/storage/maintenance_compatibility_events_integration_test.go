//go:build integration

package storage

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func compatibilityEdit(f maintenanceFixture, r MaintenanceRecord, key string, start, end time.Time, ids []string) EditMaintenanceInput {
	return EditMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: r.Revision, Title: r.Title, Reason: key, Start: start, End: end, SloIDs: ids, IdempotencyKey: key, RequestID: key, Now: f.clock}
}

func TestIntegrationMaintenanceCompatibilityEvents(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("original")
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	r, err = EditMaintenance(t.Context(), f.f.databaseURL, compatibilityEdit(f, r, "metadata", r.Start, r.End, r.SloIDs))
	if err != nil {
		t.Fatal(err)
	}
	r, err = EditMaintenance(t.Context(), f.f.databaseURL, compatibilityEdit(f, r, "move", r.Start.Add(30*time.Minute), r.End.Add(30*time.Minute), r.SloIDs))
	if err != nil {
		t.Fatal(err)
	}
	r, err = EditMaintenance(t.Context(), f.f.databaseURL, compatibilityEdit(f, r, "scope", r.Start, r.End, []string{f.ids[1]}))
	if err != nil {
		t.Fatal(err)
	}
	type interval struct {
		revision   int64
		start, end time.Time
	}
	for i, want := range [][]interval{
		{{1, input.Start, input.End}, {3, input.Start, input.Start.Add(30 * time.Minute)}, {3, input.End, input.End.Add(30 * time.Minute)}, {4, input.Start.Add(30 * time.Minute), input.End.Add(30 * time.Minute)}},
		{{1, r.Start, r.End}},
	} {
		rows, err := f.conn.Query(t.Context(), `SELECT revision,lower(input_range),upper(input_range) FROM almanac_maintenance_changed_inputs WHERE slo_id=$1 ORDER BY revision,part`, f.ids[i])
		if err != nil {
			t.Fatal(err)
		}
		var got []interval
		for rows.Next() {
			var x interval
			if err := rows.Scan(&x.revision, &x.start, &x.end); err != nil {
				t.Fatal(err)
			}
			x.start = x.start.UTC()
			x.end = x.end.UTC()
			got = append(got, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("scope%d got%+v want%+v", i, got, want)
		}
	}
	var empty int
	var through int64
	if err := f.conn.QueryRow(t.Context(), `SELECT range_count FROM almanac_maintenance_change_sets WHERE slo_id=$1 AND revision=2`, f.ids[0]).Scan(&empty); err != nil || empty != 0 {
		t.Fatalf("explicit metadata set:%d %v", empty, err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT indexed_through FROM almanac_maintenance_compatibility WHERE slo_id=$1`, f.ids[0]).Scan(&through); err != nil || through != 4 {
		t.Fatalf("through%d %v", through, err)
	}
	for _, sql := range []string{
		`UPDATE almanac_maintenance_change_sets SET range_count=1 WHERE slo_id=$1 AND revision=2`,
		`DELETE FROM almanac_maintenance_changed_inputs WHERE slo_id=$1`,
		`DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=1`,
	} {
		if _, err := f.conn.Exec(t.Context(), sql, f.ids[0]); err == nil {
			t.Fatalf("mutable proof: %s", sql)
		}
	}
}

func TestIntegrationMaintenanceCompatibilityProof(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("proof")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	// A real largest ordinal exercises the generated discrete range endpoint.
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_maintenance_compatibility(slo_id,scope_ordinal) OVERRIDING SYSTEM VALUE VALUES($1,9223372036854775807)`, f.ids[1]); err != nil {
		t.Fatal(err)
	}
	maximum := f.input("maximum-ordinal")
	maximum.SloIDs = []string{f.ids[1]}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, maximum); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	for _, c := range []struct {
		computed, requested int64
		start, end          time.Time
		kind                string
	}{
		{0, 1, input.Start, input.End, "changed"}, {0, 1, input.End, input.End.Add(time.Hour), "indexed_absence"}, {1, 1, input.Start, input.End, "same_revision"},
	} {
		p, err := readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), c.computed, c.requested, c.start, c.end)
		if err != nil || p.Kind != c.kind || p.ComputedRevision != c.computed || p.RequestedRevision != c.requested {
			t.Fatalf("proof %+v %v", p, err)
		}
	}
	if _, err := readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), 1, 0, input.Start, input.End); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatal(err)
	}
	if _, err := readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), 0, 2, input.Start, input.End); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatal(err)
	}
	maxHit, err := dbgen.New(tx).HasMaintenanceChangedInput(t.Context(), dbgen.HasMaintenanceChangedInputParams{SloID: mustPGUUID(f.ids[1]), ScopeOrdinal: math.MaxInt64, AfterRevision: 0, ThroughRevision: 1, WindowStart: timestamp(maximum.Start), WindowEnd: timestamp(maximum.End)})
	if err != nil || !maxHit {
		t.Fatalf("largest ordinal:%v %v", maxHit, err)
	}
	// The typed range query must not overflow at the largest positive revision.
	if _, err := dbgen.New(tx).HasMaintenanceChangedInput(t.Context(), dbgen.HasMaintenanceChangedInputParams{SloID: mustPGUUID(f.ids[0]), ScopeOrdinal: math.MaxInt64, AfterRevision: math.MaxInt64 - 1, ThroughRevision: math.MaxInt64, WindowStart: timestamp(input.Start), WindowEnd: timestamp(input.End)}); err != nil {
		t.Fatalf("max range: %v", err)
	}
}
