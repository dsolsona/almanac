//go:build integration

package storage

import (
	"errors"
	"fmt"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
	"time"
)

func TestIntegrationMaintenanceCompilerSchema(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	var present bool
	if err := f.conn.QueryRow(t.Context(), `SELECT to_regclass('public.almanac_maintenance_builds') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("durable compiler identity/input pin table is absent")
	}
}

func compilerTx(t *testing.T, f maintenanceFixture, fn func(pgx.Tx) error) {
	t.Helper()
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := fn(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func compilerCapture(t *testing.T, f maintenanceFixture, revision *int64) dbgen.AlmanacMaintenanceBuild {
	t.Helper()
	var build dbgen.AlmanacMaintenanceBuild
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		build, err = captureMaintenanceBuild(t.Context(), tx, mustPGUUID(f.ids[0]), revision, f.now)
		return err
	})
	return build
}
func compilerStep(t *testing.T, f maintenanceFixture, id pgtype.UUID, limit int) maintenanceCompilerProgress {
	t.Helper()
	var claim maintenanceBuildClaim
	var result maintenanceCompilerProgress
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		claim, err = claimMaintenanceBuild(t.Context(), tx, id, time.Minute, f.clock)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		result, err = advanceMaintenanceBuild(t.Context(), tx, claim, limit, f.clock)
		return err
	})
	if result.Scanned > limit {
		t.Fatalf("unbounded turn: %+v", result)
	}
	return result
}
func compilerFinish(t *testing.T, f maintenanceFixture, id pgtype.UUID, limit int) {
	t.Helper()
	for range 40 {
		p := compilerStep(t, f, id, limit)
		if p.Phase == "complete" {
			return
		}
		if p.Phase == "unavailable" {
			t.Fatal("compiler history unavailable")
		}
	}
	t.Fatal("finite input did not complete")
}
func TestIntegrationMaintenanceCompilerFiniteSnapshot(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	for i, bounds := range [][2]int{{0, 4}, {2, 7}, {7, 8}, {10, 12}} {
		input := f.input(fmt.Sprintf("window-%d", i))
		input.Start = f.at.Add(time.Hour + time.Duration(bounds[0])*time.Minute)
		input.End = f.at.Add(time.Hour + time.Duration(bounds[1])*time.Minute)
		if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
			t.Fatal(err)
		}
	}
	build := compilerCapture(t, f, nil)
	if build.TargetRevision != 4 {
		t.Fatal(build.TargetRevision)
	}
	input := f.input("later")
	input.Start = f.at.Add(2 * time.Hour)
	input.End = f.at.Add(3 * time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), build.ID, f.at, f.at.Add(4*time.Hour)); !errors.Is(err, ErrMaintenanceCompilePending) {
		t.Fatalf("partial is readable: %v", err)
	}
	compilerFinish(t, f, build.ID, 1)
	var rows int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_members WHERE build_id=$1`, build.ID).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("fixed R membership %d: %v", rows, err)
	}
	// Independent minute bitmap oracle, including clipped edges and gaps.
	for a := 0; a < 14; a++ {
		for b := a + 1; b <= 14; b++ {
			excluded := int64(0)
			latest := b
			for minute := a; minute < b; minute++ {
				if minute < 8 || minute >= 10 && minute < 12 {
					excluded += 60 * 1000000
				}
			}
			if b <= 8 {
				latest = 0
			} else if b > 10 && b <= 12 {
				latest = 10
			}
			got, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), build.ID, f.at.Add(time.Hour+time.Duration(a)*time.Minute), f.at.Add(time.Hour+time.Duration(b)*time.Minute))
			if err != nil || got.ExcludedMicroseconds != excluded || !got.LatestEligibleEnd.Equal(f.at.Add(time.Hour+time.Duration(latest)*time.Minute)) || got.Revision != 4 {
				t.Fatalf("[%d,%d) got %+v: %v", a, b, got, err)
			}
		}
	}
	// A completed historical mask is not silently promoted to revision 5.
	again := compilerCapture(t, f, new(int64(4)))
	if again.ID != build.ID {
		t.Fatal("capture rebuilt historical identity")
	}
}

func TestIntegrationMaintenanceCompilerIdentityImmutable(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	b := compilerCapture(t, f, nil)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_builds SET target_revision=1 WHERE id=$1`, b.ID); compilerPGCode(err) != "23514" {
		t.Fatalf("captured revision can change: %v", err)
	}
	compilerFinish(t, f, b.ID, 1)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_builds SET phase='replay',completed_at=NULL WHERE id=$1`, b.ID); compilerPGCode(err) != "23514" {
		t.Fatalf("published build can resume mutation: %v", err)
	}
}

func TestIntegrationMaintenanceCompilerMembershipAndRange(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	first, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("original"))
	if err != nil {
		t.Fatal(err)
	}
	edit := f.input("metadata")
	edit.ID = first.ID
	edit.Revision = 1
	edit.Title = "Only metadata changed"
	second, err := EditMaintenance(t.Context(), f.f.databaseURL, edit)
	if err != nil {
		t.Fatal(err)
	}
	old := compilerCapture(t, f, nil)
	edit = f.input("scope")
	edit.ID = first.ID
	edit.Revision = 2
	edit.SloIDs = []string{f.ids[1]}
	if _, err := EditMaintenance(t.Context(), f.f.databaseURL, edit); err != nil {
		t.Fatal(err)
	}
	later := compilerCapture(t, f, nil)
	compilerFinish(t, f, old.ID, 1)
	compilerFinish(t, f, later.ID, 1)
	var revision int64
	if err := f.conn.QueryRow(t.Context(), `SELECT exclusion_revision FROM almanac_maintenance_members WHERE build_id=$1`, old.ID).Scan(&revision); err != nil || revision != second.Revision {
		t.Fatalf("metadata provenance lost: %d %v", revision, err)
	}
	got, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), later.ID, first.Start, first.End)
	if err != nil || got.ExcludedMicroseconds != 0 || !got.LatestEligibleEnd.Equal(first.End) {
		t.Fatalf("scope removal: %+v %v", got, err)
	}
	// A second SLO sharing the SLI gets its own membership; cancelling the
	// moved declaration removes it without changing the first SLO's R=3.
	var other dbgen.AlmanacMaintenanceBuild
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		other, err = captureMaintenanceBuild(t.Context(), tx, mustPGUUID(f.ids[1]), nil, f.now)
		return err
	})
	compilerFinish(t, f, other.ID, 1)
	covered, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), other.ID, first.Start, first.End)
	if err != nil || covered.Revision != 1 || covered.ExcludedMicroseconds != 3600000000 {
		t.Fatalf("independent scope: %+v %v", covered, err)
	}
	if _, err := CancelMaintenance(t.Context(), f.f.databaseURL, MaintenanceCommandInput{Proof: f.proof, ID: first.ID, Revision: 3, Reason: "cancel scheduled work", IdempotencyKey: "cancel", RequestID: "cancel", Now: f.clock}); err != nil {
		t.Fatal(err)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		other, err = captureMaintenanceBuild(t.Context(), tx, mustPGUUID(f.ids[1]), nil, f.now)
		return err
	})
	compilerFinish(t, f, other.ID, 1)
	covered, err = readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), other.ID, first.Start, first.End)
	if err != nil || covered.Revision != 2 || covered.ExcludedMicroseconds != 0 {
		t.Fatalf("cancelled scope: %+v %v", covered, err)
	}
	// Supported full timestamp span exceeds Go time.Duration, but not int64 µs.
	wide := f.input("wide")
	wide.Start = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	wide.End = time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, wide); err != nil {
		t.Fatal(err)
	}
	all := compilerCapture(t, f, nil)
	compilerFinish(t, f, all.ID, 2)
	full, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), all.ID, wide.Start, wide.End)
	if err != nil || full.ExcludedMicroseconds != wide.End.UnixMicro()-wide.Start.UnixMicro() || !full.LatestEligibleEnd.Equal(wide.Start) {
		t.Fatalf("full range overflow: %+v %v", full, err)
	}
	for _, bounds := range [][2]time.Time{{wide.Start, wide.Start}, {wide.End, wide.Start}, {wide.Start.Add(time.Microsecond), wide.End}, {wide.Start.In(time.FixedZone("offset", 3600)), wide.End}} {
		if _, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), all.ID, bounds[0], bounds[1]); !errors.Is(err, ErrMaintenanceInvalid) {
			t.Fatalf("invalid range accepted: %v", err)
		}
	}
}
