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

func TestIntegrationAlertCanonicalTerminalFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                             string
		fast, pending, leased, allFailed bool
		want                             alert.Truth
	}{
		{name: "healthy fast condition still fires", fast: true, want: alert.Breaching},
		{name: "only failed condition is indeterminate", want: alert.Indeterminate},
		{name: "first window exhaustion is indeterminate", allFailed: true, want: alert.Indeterminate},
		{name: "unfinished window defers the whole boundary", fast: true, pending: true},
		{name: "leased final attempt remains pending", fast: true, pending: true, leased: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			f.drain(t, "terminal failure fixture")
			conditions := []alert.Condition{{ID: "sustained", Short: time.Hour, Long: 2 * time.Hour, Threshold: "1"}}
			if test.allFailed {
				conditions[0].Short, conditions[0].Long = 2*time.Hour, 3*time.Hour
			}
			if test.fast {
				conditions = append([]alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: 10 * time.Minute, Threshold: "1"}}, conditions...)
			}
			id := uuid.New().String()
			alertTestTx(t, f.conn, func(tx pgx.Tx) error {
				return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: id, Revision: 1, Conditions: conditions}, SLOID: f.sloID, Name: test.name, Severity: "high", FirstBoundary: f.at, Now: f.now, Limits: f.limits})
			})
			// The failed older hour belongs only to windows of two hours or
			// longer. Shorter windows retain known 990/1,000 counts.
			if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,attempts,next_attempt_at,updated_at)
VALUES($1,1,1,$2,$2::timestamptz+interval '1 hour',5,$3,$3)`, f.sliID, f.at.Add(-2*time.Hour), f.at); err != nil {
				t.Fatal(err)
			}
			if test.pending {
				attempts := 0
				var lease *time.Time
				if test.leased {
					attempts, lease = 5, new(f.at.Add(time.Minute))
				}
				if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,attempts,lease_until,next_attempt_at,updated_at)
VALUES($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$4,$5,$5)`, f.sliID, f.at.Add(-time.Hour), attempts, lease, f.at); err != nil {
					t.Fatal(err)
				}
			}
			claim := f.claim(t, f.at)
			prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
			if test.pending {
				if !errors.Is(err, ErrDerivedPending) || len(prepared.Input.Windows) != 0 || !prepared.Cutoff.Equal(f.at) {
					t.Fatalf("unfinished preparation became evidence: %+v %v", prepared, err)
				}
				cursor, err := dbgen.New(f.conn).LockAlertCursor(t.Context(), dbgen.LockAlertCursorParams{PolicyID: mustPGUUID(id), Revision: 1})
				if err != nil || !cursor.NextScheduledAt.Time.Equal(f.at) || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || cursor.LastEvaluationID.Valid || cursor.OpenOccurrenceID.Valid {
					t.Fatalf("pending advanced or lost its pin: %+v %v", cursor, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("terminal failure prevented complete policy evidence: %v", err)
			}
			failed := 0
			for _, window := range prepared.Input.Windows {
				if window.Snapshot != prepared.Input.Snapshot {
					t.Fatalf("mixed snapshot: %+v", window)
				}
				if window.End.Sub(window.Start) >= 2*time.Hour {
					failed++
					if window.Quality != alert.CalculationFailed || window.Reason != "calculation_failed" || window.Good != "" || window.Total != "" {
						t.Fatalf("failed counts became usable: %+v", window)
					}
				} else {
					counts := map[time.Duration][2]string{5 * time.Minute: {"4950", "5000"}, 10 * time.Minute: {"9900", "10000"}, time.Hour: {"59400", "60000"}}
					want := counts[window.End.Sub(window.Start)]
					if window.Quality != alert.Complete || window.Good != want[0] || window.Total != want[1] {
						t.Fatalf("healthy window lost exact evidence: %+v want %v", window, want)
					}
				}
			}
			wantFailed := 1
			if test.allFailed {
				wantFailed = 2
			}
			if failed != wantFailed {
				t.Fatalf("got %d failed windows, want %d", failed, wantFailed)
			}
			evaluation, err := alert.Evaluate(prepared.Input, f.limits)
			if err != nil || evaluation.Truth != test.want || evaluation.Conditions[len(evaluation.Conditions)-1].Outcome != alert.Unavailable {
				t.Fatalf("terminal outcome: %+v %v", evaluation, err)
			}
			committed, err := f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, f.clock)
			if err != nil || test.fast && (committed.Occurrence == nil || committed.Occurrence.State != alert.Firing) || !test.fast && committed.Occurrence != nil {
				t.Fatalf("terminal evidence journal result: %+v %v", committed, err)
			}
		})
	}
}

func TestIntegrationAlertCanonicalDatabaseFailure(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Database failure")
	f.drain(t, "database failure fixture")
	claim := f.claim(t, f.at)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `ALTER TABLE almanac_sli_hourly_aggregates RENAME TO temporarily_unavailable_aggregates`); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareAlertEvidence(t.Context(), dbgen.New(tx), claim, f.at, f.limits, f.at)
	if err == nil || errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) || len(prepared.Input.Windows) != 0 {
		t.Fatalf("database failure became SLO truth: %+v %v", prepared, err)
	}
}
