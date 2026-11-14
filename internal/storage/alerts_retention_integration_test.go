//go:build integration

package storage

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/jackc/pgx/v5"
)

func alertRetentionTestOptions(now time.Time, batch int) alertRetentionOptions {
	return alertRetentionOptions{Batch: batch, CompactBefore: now.Add(-24 * time.Hour), HistoryBefore: now.Add(-365 * 24 * time.Hour)}
}

func TestIntegrationAlertRetentionActivePins(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Old active evidence")
	var ids []string
	for index, quality := range []alert.Quality{alert.Complete, alert.Complete, alert.Missing} {
		f.now = f.at.Add(time.Duration(index) * time.Minute)
		claim := f.claim(t, f.now)
		result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, quality, true), f.limits, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.EvaluationID)
	}
	f.now = f.at.Add(800 * 24 * time.Hour)
	for range 3 {
		if _, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var lastBreach string
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations),evaluation_id::text FROM almanac_alert_breach_pins WHERE policy_id=$1`, policy).Scan(&count, &lastBreach); err != nil || count != 3 || lastBreach != ids[1] {
		t.Fatalf("old active evidence lost: evals=%d breach=%s want=%s %v", count, lastBreach, ids[1], err)
	}
}

func seedAlertRetentionClear(t *testing.T, f alertTestFixture, count int, delay time.Duration) {
	t.Helper()
	f.policy(t, "Quiet history")
	for index := range count {
		f.now = f.at.Add(delay + time.Duration(index)*time.Minute)
		claim := f.claim(t, f.now)
		if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, f.clock); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationAlertRetentionCompaction(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	seedAlertRetentionClear(t, f, 8, 0)
	f.now = f.at.Add(3 * 24 * time.Hour)
	var compacted int
	for range 4 {
		result, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 4), f.clock)
		if err != nil {
			t.Fatal(err)
		}
		compacted += result.CompactedEvaluations
	}
	var evaluations, ranges int
	var fakeCounts bool
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations),(SELECT count(*) FROM almanac_alert_ranges WHERE kind='compacted_evaluations'),EXISTS(SELECT 1 FROM almanac_alert_ranges WHERE summary::text LIKE '%Good%' OR summary::text LIKE '%Total%')`).Scan(&evaluations, &ranges, &fakeCounts); err != nil || compacted != 7 || evaluations != 1 || ranges != 2 || fakeCounts {
		t.Fatalf("compaction outcome: removed=%d eval=%d ranges=%d fake=%v %v", compacted, evaluations, ranges, fakeCounts, err)
	}
}

func TestIntegrationAlertRetentionMinimumBatch(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	seedAlertRetentionClear(t, f, 3, 0)
	f.now = f.at.Add(3 * 24 * time.Hour)
	result, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 1), f.clock)
	if !errors.Is(err, ErrAlertInvalid) || result != (alertRetentionResult{}) {
		t.Fatalf("batch incapable of compaction was accepted: %+v %v", result, err)
	}
	var untouched bool
	if err := f.conn.QueryRow(t.Context(), `SELECT bool_and(version=1) FROM almanac_alert_retention_work`).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("invalid batch advanced work: %v %v", untouched, err)
	}
	result, err = f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 2), f.clock)
	if err != nil || result.CompactedEvaluations != 2 || result.Scanned > 6 {
		t.Fatalf("minimum valid batch did not compact within three bounded pages: %+v %v", result, err)
	}
	var evaluations, ranges, pinned int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations),(SELECT count(*) FROM almanac_alert_ranges WHERE kind='compacted_evaluations'),(SELECT count(*) FROM almanac_alert_evaluations e JOIN almanac_alert_cursors c ON c.last_evaluation_id=e.id)`).Scan(&evaluations, &ranges, &pinned); err != nil || evaluations != 1 || ranges != 1 || pinned != 1 {
		t.Fatalf("minimum batch lost retained evidence: evaluations=%d ranges=%d pinned=%d %v", evaluations, ranges, pinned, err)
	}
}

func TestIntegrationAlertRetentionReplayHorizon(t *testing.T) {
	for _, delay := range []time.Duration{0, 23 * time.Hour} {
		t.Run(delay.String(), func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			seedAlertRetentionClear(t, f, 4, delay)
			f.now = f.at.Add(24 * time.Hour)
			if delay > 0 {
				f.now = f.at.Add(25 * time.Hour)
			}
			if _, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations`).Scan(&count); err != nil || count != 4 {
				t.Fatalf("replay horizon/catch-up receipt lost: %d %v", count, err)
			}
		})
	}
}

func TestIntegrationAlertRetentionAtomicCompaction(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	seedAlertRetentionClear(t, f, 6, 0)
	f.now = f.at.Add(3 * 24 * time.Hour)
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_compaction_range() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.kind='compacted_evaluations' THEN RAISE EXCEPTION 'fixture compaction failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_compaction BEFORE INSERT ON almanac_alert_ranges FOR EACH ROW EXECUTE FUNCTION reject_compaction_range()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock); err == nil {
		t.Fatal("compaction fixture did not fail")
	}
	var count, ranges int
	var revision int64
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations),(SELECT count(*) FROM almanac_alert_ranges),(SELECT version FROM almanac_alert_retention_work WHERE kind='evaluation')`).Scan(&count, &ranges, &revision); err != nil || count != 6 || ranges != 0 || revision != 1 {
		t.Fatalf("partial compaction: eval=%d ranges=%d version=%d %v", count, ranges, revision, err)
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_compaction ON almanac_alert_ranges`); err != nil {
		t.Fatal(err)
	}
	result, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock)
	if err != nil || result.CompactedEvaluations != 5 {
		t.Fatalf("rolled-back work did not retry: %+v %v", result, err)
	}
}

func TestIntegrationAlertRetentionTerminalClocks(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		offset time.Duration
		keep   bool
	}{{"expired", time.Minute, false}, {"recent detection", 800 * 24 * time.Hour, true}, {"exact retention boundary", 435 * 24 * time.Hour, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			policy := f.policy(t, "Terminal history")
			claim := f.claim(t, f.at)
			if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock); err != nil {
				t.Fatal(err)
			}
			detected := f.at.Add(scenario.offset)
			alertTestTx(t, f.conn, func(tx pgx.Tx) error {
				_, err := disableAlertPolicyTx(t.Context(), tx, alertDisableInput{PolicyID: policy, ActorID: f.f.userID, RequestID: "old-close", Reason: "Retired", ExpectedVersion: 1, EffectiveAt: f.at.Add(time.Minute), Now: func() time.Time { return detected }})
				return err
			})
			f.now = f.at.Add(800 * 24 * time.Hour)
			if _, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock); err != nil {
				t.Fatal(err)
			}
			var occurrences, transitions, pins int
			if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_occurrences),(SELECT count(*) FROM almanac_alert_transitions),(SELECT count(*) FROM almanac_alert_breach_pins)`).Scan(&occurrences, &transitions, &pins); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario.keep {
				want = 1
			}
			if occurrences != want || pins != want || scenario.keep && transitions != 2 || !scenario.keep && transitions != 0 {
				t.Fatalf("terminal clock pruning: keep=%v occ=%d transitions=%d pins=%d", scenario.keep, occurrences, transitions, pins)
			}
		})
	}
}

func TestIntegrationAlertRetentionConcurrentBatches(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	seedAlertRetentionClear(t, f, 10, 0)
	f.now = f.at.Add(3 * 24 * time.Hour)
	var workers sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		workers.Go(func() {
			_, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 4), f.clock)
			errors <- err
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var retained int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations e JOIN almanac_alert_cursors c ON c.last_evaluation_id=e.id`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("concurrent maintenance deleted cursor evidence: %d %v", retained, err)
	}
	var overlaps int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_ranges a JOIN almanac_alert_ranges b ON a.id<b.id AND a.policy_id=b.policy_id AND a.revision=b.revision AND a.starts_at<b.ends_at AND b.starts_at<a.ends_at`).Scan(&overlaps); err != nil || overlaps != 0 {
		t.Fatalf("duplicate compaction ranges: %d %v", overlaps, err)
	}
}

func TestIntegrationAlertRetentionPinnedPrefixProgress(t *testing.T) {
	t.Parallel()
	verifyAlertRetentionPinnedPrefix(t, 4)
}

func TestIntegrationAlertRetentionPageBoundaryPair(t *testing.T) {
	t.Parallel()
	// Three pinned rows put the following two unpinned evaluations on
	// different two-row pages. A complete sweep must still consider the pair.
	verifyAlertRetentionPinnedPrefix(t, 3)
}

func verifyAlertRetentionPinnedPrefix(t *testing.T, quietCount int) {
	t.Helper()
	f := newAlertTestFixture(t)
	register := func(id, name string, first time.Time) {
		t.Helper()
		alertTestTx(t, f.conn, func(tx pgx.Tx) error {
			return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: id, Revision: 1, Conditions: []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "1"}}}, SLOID: f.sloID, Name: name, Severity: "high", FirstBoundary: first, Now: f.now, Limits: f.limits})
		})
	}
	low := "00000000-0000-4000-8000-000000000001"
	register(low, "Pinned prefix", f.at)
	for index, quality := range []alert.Quality{alert.Complete, alert.Complete, alert.Missing} {
		f.now = f.at.Add(time.Duration(index) * time.Minute)
		claim := f.claim(t, f.now)
		if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, quality, true), f.limits, f.clock); err != nil {
			t.Fatal(err)
		}
	}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		_, err := disableAlertPolicyTx(t.Context(), tx, alertDisableInput{PolicyID: low, ActorID: f.f.userID, ExpectedVersion: 1, Reason: "Retain history", RequestID: "prefix-disable", EffectiveAt: f.now, Now: f.clock})
		return err
	})
	register("ffffffff-ffff-4fff-8fff-ffffffffffff", "Later quiet", f.at.Add(3*time.Minute))
	for index := 3; index < 3+quietCount; index++ {
		f.now = f.at.Add(time.Duration(index) * time.Minute)
		claim := f.claim(t, f.now)
		if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, f.clock); err != nil {
			t.Fatal(err)
		}
	}
	f.now = f.at.Add(3 * 24 * time.Hour)
	compacted := 0
	for range 4 {
		result, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 2), f.clock)
		if err != nil {
			t.Fatal(err)
		}
		if result.Scanned > 6 {
			t.Fatalf("raw work exceeded three bounded stage pages: %+v", result)
		}
		compacted += result.CompactedEvaluations
	}
	var protected int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1`, low).Scan(&protected); err != nil || protected != 3 || compacted != 2 {
		t.Fatalf("pinned prefix starved later work: protected=%d compacted=%d %v", protected, compacted, err)
	}
}
