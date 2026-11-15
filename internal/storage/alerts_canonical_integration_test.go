//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationAlertCanonicalCounts(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Canonical burn")
	f.drain(t, "canonical counts")
	claim := f.claim(t, f.at)
	prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
	if err != nil || !prepared.Cutoff.Equal(f.at) || len(prepared.Input.Windows) != 2 {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	for _, window := range prepared.Input.Windows {
		good, total := "4950", "5000"
		if window.End.Sub(window.Start) == time.Hour {
			good, total = "59400", "60000"
		}
		if window.Quality != alert.Complete || window.Good != good || window.Total != total || window.Snapshot != prepared.Input.Snapshot {
			t.Fatalf("independent canonical counts: %+v want %s/%s", window, good, total)
		}
	}
	committed, err := f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil || committed.Occurrence == nil || committed.Occurrence.State != alert.Firing {
		t.Fatalf("canonical evidence did not fire: %+v %v", committed, err)
	}
	var pinned bool
	if err := f.conn.QueryRow(t.Context(), `SELECT pending_cutoff IS NOT NULL FROM almanac_alert_cursors WHERE policy_id=$1 AND revision=1`, claim.PolicyID).Scan(&pinned); err != nil || pinned {
		t.Fatalf("completed boundary retained pin: %v %v", pinned, err)
	}
}

func TestIntegrationAlertCanonicalPending(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Pending canonical burn")
	claim := f.claim(t, f.at)
	prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
	if !errors.Is(err, ErrDerivedPending) || !prepared.Cutoff.Equal(f.at) || len(prepared.Input.Windows) != 0 {
		t.Fatalf("pending became partial evidence: %+v %v", prepared, err)
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pending committed evaluation: %d %v", count, err)
	}
	f.drain(t, "pending canonical recovery")
	f.now = f.at.Add(20 * time.Minute)
	reclaimed := f.claim(t, f.now)
	ready, err := f.runtime.readAlertEvidence(t.Context(), reclaimed, prepared.Cutoff, f.limits, f.clock)
	if err != nil || !ready.Cutoff.Equal(prepared.Cutoff) || len(ready.Input.Windows) != 2 || ready.Input.Windows[0].Quality != alert.Complete {
		t.Fatalf("retry moved cutoff or aged old evidence: %+v %v", ready, err)
	}
}

func TestIntegrationAlertCanonicalQuality(t *testing.T) {
	for _, test := range []struct {
		name, mutation string
		want           alert.Quality
	}{
		{"zero", `UPDATE almanac_sli_observations SET good_count=0,total_count=0,ingestion_generation=0 WHERE sli_id=$1`, alert.ZeroTraffic},
		{"empty", `UPDATE almanac_sli_observations SET outcome='complete_empty',good_count=NULL,total_count=NULL,ingestion_generation=0 WHERE sli_id=$1`, alert.Missing},
		{"missing", `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start >= $2::timestamptz-interval '4 minutes' AND interval_start < $2::timestamptz-interval '3 minutes'`, alert.Partial},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			f.policy(t, test.name+" canonical burn")
			args := []any{f.sliID}
			if test.name == "missing" {
				args = append(args, f.at)
			}
			if _, err := execObservationFixture(t, f.conn, f.sliID, test.mutation, args...); err != nil {
				t.Fatal(err)
			}
			f.drain(t, "canonical quality")
			claim := f.claim(t, f.at)
			prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
			if err != nil {
				t.Fatal(err)
			}
			for _, window := range prepared.Input.Windows {
				if window.Quality != test.want {
					t.Fatalf("quality=%s want=%s: %+v", window.Quality, test.want, window)
				}
			}
			result, err := alert.Evaluate(prepared.Input, f.limits)
			if err != nil || result.Truth != alert.Indeterminate {
				t.Fatalf("quality became clear: %+v %v", result, err)
			}
		})
	}
}

func TestIntegrationAlertCanonicalExclusionGate(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Maintenance gate")
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_exclusion_state SET revision=1 WHERE slo_id=$1`, f.sloID); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.runtime.readAlertEvidence(t.Context(), f.claim(t, f.at), time.Time{}, f.limits, f.clock)
	if !errors.Is(err, ErrAlertExclusionsPending) || len(prepared.Input.Windows) != 0 {
		t.Fatalf("mask silently ignored: %+v %v", prepared, err)
	}
}

func TestIntegrationAlertCanonicalSourceFence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Source fence")
	f.drain(t, "source fence")
	claim := f.claim(t, f.at)
	prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=1000,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2::timestamptz-interval '1 minute'`, f.sliID, f.at); err != nil {
		t.Fatal(err)
	}
	_, err = f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, func() time.Time { return f.at.Add(time.Second) })
	if !errors.Is(err, ErrAlertStale) {
		t.Fatalf("source change published old evidence: %v", err)
	}
	row, err := dbgen.New(f.conn).LockAlertCursor(t.Context(), dbgen.LockAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision})
	if err != nil || !row.NextScheduledAt.Time.Equal(claim.ScheduledAt) {
		t.Fatalf("fenced publication advanced: %+v %v", row, err)
	}
}

func TestIntegrationAlertCanonicalCustomWindows(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.drain(t, "custom windows")
	id := uuid.New().String()
	conditions := []alert.Condition{
		{ID: "a", Short: 5 * time.Minute, Long: time.Hour, Threshold: "1"},
		{ID: "b", Short: 10 * time.Minute, Long: 2 * time.Hour, Threshold: "1"},
		{ID: "c", Short: 15 * time.Minute, Long: 3 * time.Hour, Threshold: "1"},
	}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: id, Revision: 1, Conditions: conditions}, SLOID: f.sloID, Name: "Three custom burns", Severity: "medium", FirstBoundary: f.at, Now: f.now, Limits: f.limits})
	})
	prepared, err := f.runtime.readAlertEvidence(t.Context(), f.claim(t, f.at), time.Time{}, f.limits, f.clock)
	if err != nil || len(prepared.Input.Windows) != 6 {
		t.Fatalf("six distinct windows: %+v %v", prepared, err)
	}
	for _, window := range prepared.Input.Windows {
		if window.Quality != alert.Complete || window.Snapshot != prepared.Input.Snapshot {
			t.Fatalf("inconsistent window: %+v", window)
		}
	}
	result, err := alert.Evaluate(prepared.Input, f.limits)
	if err != nil || len(result.Conditions) != 3 || result.Truth != alert.Breaching {
		t.Fatalf("custom conditions: %+v %v", result, err)
	}
}

func TestIntegrationAlertCanonicalClockOnly(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Clock outage")
	f.drain(t, "clock outage")
	claim := f.claim(t, f.at)
	first, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(20 * time.Minute)
	claim.ScheduledAt = f.now
	// Exercise canonical eligibility at a later scheduled boundary without
	// pretending this synthetic boundary is the journal's oldest due claim.
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	later, err := prepareAlertEvidence(t.Context(), dbgen.New(tx), claim, time.Time{}, f.limits, f.now)
	if err != nil || !later.Cutoff.After(first.Cutoff) {
		t.Fatalf("clock did not expose outage: %+v %v", later, err)
	}
	result, err := alert.Evaluate(later.Input, f.limits)
	if err != nil || result.Truth != alert.Indeterminate {
		t.Fatalf("outage became clear: %+v %v", result, err)
	}
	for _, window := range later.Input.Windows {
		if window.Quality == alert.Complete {
			t.Fatalf("old data appeared complete: %+v", window)
		}
	}
}

func TestIntegrationAlertCanonicalWarmup(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.drain(t, "warmup")
	// A one-hour alert cannot use the shortened ten-minute SLO population.
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_definitions SET evaluation_valid_from=$2 WHERE slo_id=$1 AND revision=1`, f.sloID, f.at.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.policy(t, "Warmup burn")
	prepared, err := f.runtime.readAlertEvidence(t.Context(), f.claim(t, f.at), time.Time{}, f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := alert.Evaluate(prepared.Input, f.limits)
	if err != nil || result.Truth != alert.Indeterminate || result.Conditions[0].Long.Quality != alert.Warmup || result.Conditions[0].Short.Quality != alert.Complete {
		t.Fatalf("shortened window fired: %+v %v", result, err)
	}
}
