//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAlertCutoffRestart(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Pinned boundary")
	// Withhold the not-yet-due tail so canonical eligibility initially uses the
	// source allowance rather than the early completed interval at f.at.
	var settling int32
	if err := f.conn.QueryRow(t.Context(), `SELECT d.settling_delay_seconds FROM almanac_datasources d JOIN almanac_sli_measurements m ON m.datasource_id=d.id WHERE m.sli_id=$1 AND m.revision=1`, f.sliID).Scan(&settling); err != nil {
		t.Fatal(err)
	}
	due := sli.LatestDueEnd(f.at, f.sliID, int(f.cadence/time.Second), int(settling))
	if !due.Before(f.at) {
		t.Fatal("fixture requires a withheld early tail")
	}
	if _, err := f.conn.Exec(t.Context(), `CREATE TEMP TABLE held_tail AS SELECT * FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start >= $2`, f.sliID, due); err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, f.conn, f.sliID, `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start >= $2`, f.sliID, due); err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	cutoff, err := f.runtime.pinAlertCutoff(t.Context(), claim, f.clock)
	if err != nil || !cutoff.Equal(due) {
		t.Fatalf("initial cutoff=%s want=%s: %v", cutoff, due, err)
	}
	if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO almanac_sli_observations SELECT * FROM held_tail`); err != nil {
		t.Fatal(err)
	}
	definition, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	unpinned, err := liveSloEvaluationEnd(t.Context(), dbgen.New(f.conn), definition, claim.ScheduledAt, settling)
	if err != nil || !unpinned.After(cutoff) {
		t.Fatalf("late tail did not change unpinned cutoff: %s %v", unpinned, err)
	}
	// Reclaim after expiry through a new Runtime, representing evaluator restart.
	reopened, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	now := f.at.Add(2 * time.Minute)
	claims, err := reopened.claimAlertWork(t.Context(), now, now, time.Minute, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("reclaim: %+v %v", claims, err)
	}
	kept, err := reopened.pinAlertCutoff(t.Context(), claims[0], func() time.Time { return now })
	if err != nil || !kept.Equal(cutoff) {
		t.Fatalf("restart moved pending cutoff=%s original=%s: %v", kept, cutoff, err)
	}
	if _, err := f.runtime.pinAlertCutoff(t.Context(), claim, func() time.Time { return now }); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("expired worker rewrote pin: %v", err)
	}
	row, err := dbgen.New(f.conn).LockAlertCursor(t.Context(), dbgen.LockAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: 1})
	if err != nil || !row.PendingCutoff.Valid || !row.PendingCutoff.Time.Equal(cutoff) || !row.NextScheduledAt.Time.Equal(f.at) {
		t.Fatalf("lost durable pending boundary: %+v %v", row, err)
	}
}
