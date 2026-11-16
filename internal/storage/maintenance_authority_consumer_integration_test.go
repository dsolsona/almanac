//go:build integration

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func authorityOptions(f maintenanceFixture) maintenanceAuthorityOptions {
	return maintenanceAuthorityOptions{MaxClaims: 1, MaxChanges: 1, MaxBuckets: 2, MaxScanHours: 2, MaxAttempts: 2, Lease: time.Minute, OperationBudget: 5 * time.Second, PendingRetry: time.Second, FailureRetry: 2 * time.Second, RetentionStart: f.historyStart}
}
func authorityClaim(t *testing.T, f maintenanceFixture, now time.Time, o maintenanceAuthorityOptions) dbgen.AlmanacSloExclusionWork {
	t.Helper()
	var got []dbgen.AlmanacSloExclusionWork
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		got, err = claimMaintenanceAuthority(t.Context(), tx, now, o, func() time.Time { return now })
		return err
	})
	if len(got) != 1 {
		t.Fatalf("claim count=%d at %v", len(got), now)
	}
	return got[0]
}
func authorityStep(t *testing.T, f maintenanceFixture, now time.Time, o maintenanceAuthorityOptions) dbgen.AlmanacSloExclusionWork {
	t.Helper()
	c := authorityClaim(t, f, now, o)
	compilerTx(t, f, func(tx pgx.Tx) error {
		return advanceMaintenanceAuthority(t.Context(), tx, c, o, time.Time{}, func() time.Time { return now })
	})
	got, err := dbgen.New(f.conn).ReadMaintenanceAuthorityWork(t.Context(), c.SloID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func authorityCreate(t *testing.T, f maintenanceFixture, key string, start, end time.Time) MaintenanceRecord {
	t.Helper()
	in := f.input(key)
	in.Start = start
	in.End = end
	m, err := CreateMaintenance(t.Context(), f.f.databaseURL, in)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func authorityMove(t *testing.T, f maintenanceFixture, m MaintenanceRecord, key string, start, end time.Time) MaintenanceRecord {
	t.Helper()
	got, err := EditMaintenance(t.Context(), f.f.databaseURL, EditMaintenanceInput{Proof: f.proof, ID: m.ID, Revision: m.Revision, Title: m.Title, Reason: "Move declaration", Start: start, End: end, SloIDs: m.SloIDs, IdempotencyKey: key, RequestID: key, Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func authorityHours(t *testing.T, f maintenanceFixture, revision int64) []time.Time {
	t.Helper()
	rows, err := f.conn.Query(t.Context(), `SELECT hour_start FROM almanac_maintenance_preparation WHERE slo_id=$1 AND kind='extrema_hour' AND target_revision=$2 ORDER BY hour_start`, f.ids[0], revision)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var h time.Time
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		out = append(out, h.UTC())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestIntegrationMaintenanceAuthorityConsumerSchema(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	var exists bool
	err := f.conn.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='almanac_slo_exclusion_work' AND column_name='processed_revision')`).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("authority work lacks durable consumer progress")
	}
}
func TestIntegrationMaintenanceAuthorityConsumerDisjointRestart(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	first := f.historyStart.Add(time.Hour).Truncate(time.Hour)
	distant := first.Add(10 * 24 * time.Hour)
	m := authorityCreate(t, f, "one", first, first.Add(time.Hour))
	authorityMove(t, f, m, "move", distant, distant.Add(time.Hour))
	now := first.Add(20 * 24 * time.Hour)
	o := authorityOptions(f)
	var progress dbgen.AlmanacSloExclusionWork
	for i := range 20 {
		before := len(authorityHours(t, f, 2))
		progress = authorityStep(t, f, now.Add(time.Duration(i)*time.Second), o)
		if added := len(authorityHours(t, f, 2)) - before; added > o.MaxBuckets {
			t.Fatalf("unbounded bucket turn %d", added)
		}
		if i == 1 {
			// Re-open the runtime as after process restart. Only durable state remains.
			f.runtime.Close()
			var err error
			f.runtime, err = OpenRuntime(t.Context(), f.f.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer f.runtime.Close()
		}
		if progress.ProcessedRevision == 2 {
			break
		}
	}
	if progress.ProcessedRevision != 2 {
		t.Fatalf("checkpoint not complete %+v", progress)
	}
	var want []time.Time
	for _, start := range []time.Time{first, distant} {
		for i := range 8 {
			want = append(want, start.Add(time.Duration(i)*time.Hour))
		}
	}
	if got := authorityHours(t, f, 2); !slices.Equal(got, want) {
		t.Fatalf("disjoint independent expected hours\ngot %v\nwant %v", got, want)
	}
	var days int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='extrema_day' AND target_revision=2`).Scan(&days); err != nil || days != 2 {
		t.Fatalf("daily composition=%d err=%v", days, err)
	}
	var untouched int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation WHERE kind LIKE 'extrema_%' AND hour_start>$1 AND hour_start<$2`, first.Add(24*time.Hour), distant.Truncate(24*time.Hour)).Scan(&untouched); err != nil || untouched != 0 {
		t.Fatalf("distant gap invalidated=%d err=%v", untouched, err)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerSupersession(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			first := f.historyStart.Add(time.Hour).Truncate(time.Hour)
			distant := first.Add(10 * 24 * time.Hour)
			m := authorityCreate(t, f, "one", first, first.Add(time.Hour))
			now := first.Add(20 * 24 * time.Hour)
			o := authorityOptions(f)
			turns := 4
			if partial {
				turns = 1
			}
			for i := range turns {
				authorityStep(t, f, now.Add(time.Duration(i)*time.Second), o)
			}
			before := authorityHours(t, f, 1)
			if len(before) != turns*2 {
				t.Fatalf("initial bounded target count %d", len(before))
			}
			authorityMove(t, f, m, "move", distant, distant.Add(time.Hour))
			// Superseded unfinished targets may already be obsolete before the new
			// authority consumer runs. Transfer must not depend on a viewer read.
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='obsolete' WHERE target_revision=1 AND kind LIKE 'extrema_%'`); err != nil {
				t.Fatal(err)
			}
			for i := range 30 {
				p := authorityStep(t, f, now.Add(time.Duration(10+i)*time.Second), o)
				if p.ProcessedRevision == 2 {
					break
				}
			}
			latest := authorityHours(t, f, 2)
			for _, h := range before {
				if !slices.Contains(latest, h) {
					t.Fatalf("lost checkpointed old bucket %v", h)
				}
			}
			if len(latest) != 16 {
				t.Fatalf("superseded progress lost old/new affected buckets: %v", latest)
			}
			// Complete the actual newest replacement; R3 must not rebuild it again.
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='complete' WHERE target_revision=2 AND kind LIKE 'extrema_%'`); err != nil {
				t.Fatal(err)
			}
			third := distant.Add(5 * 24 * time.Hour)
			authorityCreate(t, f, "third", third, third.Add(time.Hour))
			for i := range 12 {
				p := authorityStep(t, f, now.Add(time.Duration(50+i)*time.Second), o)
				if p.ProcessedRevision == 3 {
					break
				}
			}
			got := authorityHours(t, f, 3)
			if len(got) != 8 || !got[0].Equal(third) {
				t.Fatalf("completed replacement rebuilt across later revision: %v", got)
			}
		})
	}
}

func TestIntegrationMaintenanceAuthorityConsumerFutureAndNoWork(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	o := authorityOptions(f)
	got, err := f.runtime.runMaintenanceAuthorityTurn(t.Context(), f.now, o, f.clock)
	if err != nil || got.Claimed != 0 {
		t.Fatalf("empty turn %+v %v", got, err)
	}
	future := f.at.Add(100 * 24 * time.Hour).Truncate(time.Hour)
	authorityCreate(t, f, "future", future, future.Add(time.Hour))
	now := f.now
	authorityStep(t, f, now, o)
	if got := authorityHours(t, f, 1); len(got) != 0 {
		t.Fatalf("future generated closed work %v", got)
	}
	// A new historical declaration cannot wait behind that far-future one.
	past := f.historyStart.Add(time.Hour).Truncate(time.Hour)
	authorityCreate(t, f, "past", past, past.Add(time.Hour))
	for i := range 4 {
		authorityStep(t, f, now.Add(time.Duration(2+i)*time.Second), o)
	}
	if len(authorityHours(t, f, 2)) == 0 {
		t.Fatal("future change head-blocked historical edit")
	}
}

func TestIntegrationMaintenanceAuthorityConsumerLeaseAndNewEdit(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	m := authorityCreate(t, f, "one", f.historyStart, f.historyStart.Add(time.Hour))
	o := authorityOptions(f)
	claim := authorityClaim(t, f, f.now, o)
	authorityMove(t, f, m, "move", f.at.Add(time.Hour), f.at.Add(2*time.Hour))
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = advanceMaintenanceAuthority(t.Context(), tx, claim, o, time.Time{}, f.clock)
	rollbackTransaction(tx)
	if !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("new edit erased by old owner: %v", err)
	}
	newer := authorityClaim(t, f, f.now.Add(time.Second), o)
	if newer.DesiredRevision != 2 || newer.LeaseEpoch <= claim.LeaseEpoch {
		t.Fatal("new version could not replace old lease")
	}
	tx, err = f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expired := func() time.Time { return newer.LeaseUntil.Time }
	err = advanceMaintenanceAuthority(t.Context(), tx, newer, o, time.Time{}, expired)
	rollbackTransaction(tx)
	if !errors.Is(err, ErrMaintenancePreparationStale) {
		t.Fatalf("expired claim acknowledged: %v", err)
	}
	if got := authorityHours(t, f, 2); len(got) != 0 {
		t.Fatalf("expired owner published %v", got)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerPostWaitExpiry(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	authorityCreate(t, f, "one", f.historyStart, f.historyStart.Add(time.Hour))
	o := authorityOptions(f)
	claim := authorityClaim(t, f, f.now, o)
	locker := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(locker)
	lock, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lock)
	if _, err = dbgen.New(lock).LockMaintenanceAuthorityWork(t.Context(), claim.SloID); err != nil {
		t.Fatal(err)
	}
	var micros atomic.Int64
	micros.Store(f.now.UnixMicro())
	clock := func() time.Time { return time.UnixMicro(micros.Load()).UTC() }
	done := make(chan error, 1)
	go func() {
		done <- f.runtime.maintenancePreparationTransaction(t.Context(), o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
			return advanceMaintenanceAuthority(ctx, tx, claim, o, time.Time{}, clock)
		})
	}()
	waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenanceAuthorityWork %")
	micros.Store(claim.LeaseUntil.Time.UnixMicro())
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrMaintenancePreparationStale) {
		t.Fatalf("post-wait lease used old time: %v", err)
	}
	var jobs int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("stale transaction leaked jobs %d %v", jobs, err)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerFailureBudget(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	m := authorityCreate(t, f, "one", f.historyStart, f.historyStart.Add(time.Hour))
	o := authorityOptions(f)
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_authority_preparation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'authority fixture failure'; END $$; CREATE TRIGGER reject_authority_preparation BEFORE INSERT ON almanac_maintenance_preparation FOR EACH ROW EXECUTE FUNCTION reject_authority_preparation()`); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		now := f.now.Add(time.Duration(i) * o.FailureRetry)
		got, err := f.runtime.runMaintenanceAuthorityTurn(t.Context(), now, o, func() time.Time { return now })
		if err == nil || got.Claimed != 1 || got.Failed != 1 {
			t.Fatalf("failure budget turn%d %+v %v", i, got, err)
		}
	}
	now := f.now.Add(2 * o.FailureRetry)
	got, err := f.runtime.runMaintenanceAuthorityTurn(t.Context(), now, o, func() time.Time { return now })
	if err != nil || got.Claimed != 0 {
		t.Fatalf("exhausted claim retried %+v %v", got, err)
	}
	id, _ := canonicalUUID(f.ids[0])
	stopped, err := dbgen.New(f.conn).ReadMaintenanceAuthorityWork(t.Context(), id)
	if err != nil || stopped.Attempts != 2 || stopped.ProcessedRevision != 0 {
		t.Fatalf("failure not durable %+v %v", stopped, err)
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_authority_preparation ON almanac_maintenance_preparation`); err != nil {
		t.Fatal(err)
	}
	authorityMove(t, f, m, "retry-new-edit", f.at.Add(time.Hour), f.at.Add(2*time.Hour))
	newer := authorityStep(t, f, now.Add(time.Second), o)
	if newer.Attempts != 0 || newer.LeaseEpoch <= stopped.LeaseEpoch {
		t.Fatalf("new edit failed to restart exhausted work %+v", newer)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerFutureCurrentWithoutIngestion(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "authority future")
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	start := f.at.Add(time.Hour)
	end := start.Add(time.Hour)
	authorityCreate(t, f, "future", start, end)
	build := compilerCapture(t, f, nil)
	compilerFinish(t, f, build.ID, 10)
	o := authorityOptions(f)
	o.MaxBuckets = 16
	o.MaxScanHours = 16
	id, _ := canonicalUUID(f.ids[0])
	row, err := dbgen.New(f.conn).GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	initialGeneration, err := dbgen.New(f.conn).GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{f.now, start, end} {
		got, err := f.runtime.runMaintenanceAuthorityTurn(t.Context(), now, o, func() time.Time { return now })
		if err != nil || got.Advanced != 1 {
			t.Fatalf("future boundary %v: %+v %v", now, got, err)
		}
		published, err := dbgen.New(f.conn).GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: id, Now: timestamp(now), CalculationVersion: operationalCalculationVersion})
		if err != nil || !published.Current {
			t.Fatalf("future boundary current missing %v: %+v %v", now, published, err)
		}
		value, err := publishedCurrentStatus(published)
		if err != nil {
			t.Fatal(err)
		}
		if !value.Current.EvaluationAt.Equal(now) {
			t.Fatalf("boundary did not refresh current: %v want %v", value.Current.EvaluationAt, now)
		}
		if now.Equal(end) && (value.Current.Quality.ExcludedIntervals == nil || *value.Current.Quality.ExcludedIntervals != 57 || value.Current.Quality.ExpectedIntervals != 3 || value.Current.Quality.MissingIntervals != 3 || value.Current.Performance.Total != nil) {
			t.Fatalf("no-ingestion boundary failed exclusion: %+v", value.Current)
		}
	}
	generation, err := dbgen.New(f.conn).GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: 1})
	if err != nil || generation != initialGeneration {
		t.Fatalf("boundary changed source generation %d→%d %v", initialGeneration, generation, err)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerClaimsStartImmediately(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	in := f.input("both")
	in.Start = f.historyStart
	in.End = f.historyStart.Add(time.Hour)
	in.SloIDs = slices.Clone(f.ids)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, in); err != nil {
		t.Fatal(err)
	}
	o := authorityOptions(f)
	o.MaxClaims = 2
	o.MaxBuckets = 16
	var first string
	if err := f.conn.QueryRow(t.Context(), `SELECT slo_id::text FROM almanac_slo_exclusion_work ORDER BY next_attempt_at,slo_id LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time {
		var finished bool
		if err := f.conn.QueryRow(t.Context(), `SELECT processed_revision>0 FROM almanac_slo_exclusion_work WHERE slo_id=$1`, first).Scan(&finished); err != nil {
			return time.Time{}
		}
		if finished {
			return f.now.Add(2 * o.Lease)
		}
		return f.now
	}
	got, err := f.runtime.runMaintenanceAuthorityTurn(t.Context(), f.now, o, clock)
	if err != nil || got.Claimed != 2 || got.Advanced != 2 || got.Stale != 0 {
		t.Fatalf("leased work before it could start: %+v %v", got, err)
	}
	var leases int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_exclusion_work WHERE lease_until IS NOT NULL`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("successful work retained %d leases: %v", leases, err)
	}
	invalid := o
	invalid.OperationBudget = 6 * time.Second
	if invalid.valid() {
		t.Fatal("authority increased existing five-second operation ceiling")
	}
}

func TestIntegrationMaintenanceAuthorityConsumerSourceFenceAndSafeRelease(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	authorityCreate(t, f, "one", f.historyStart, f.historyStart.Add(time.Hour))
	o := authorityOptions(f)
	claim := authorityClaim(t, f, f.now, o)
	locker := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(locker)
	lock, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lock)
	sliID, _ := canonicalUUID(f.sliID)
	if _, err = lock.Exec(t.Context(), `SELECT id FROM almanac_slis WHERE id=$1 FOR UPDATE`, sliID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- f.runtime.maintenancePreparationTransaction(t.Context(), o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
			return advanceMaintenanceAuthority(ctx, tx, claim, o, time.Time{}, f.clock)
		})
	}()
	waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenanceHourSli %")
	if _, err = lock.Exec(t.Context(), `UPDATE almanac_sli_source_generations SET generation=generation+1 WHERE sli_id=$1`, sliID); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("source race lacked actual generation fence: %v", err)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		return finishMaintenanceAuthorityFailure(t.Context(), tx, claim, o, false, f.clock)
	})
	row, err := dbgen.New(f.conn).ReadMaintenanceAuthorityWork(t.Context(), claim.SloID)
	if err != nil || row.Attempts != 0 || row.LeaseUntil.Valid || !row.NextAttemptAt.Time.Equal(f.now.Add(o.PendingRetry)) {
		t.Fatalf("source stale owner not safely deferred: %+v %v", row, err)
	}
	var jobs int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("source-obsolete work published targets %d %v", jobs, err)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerEditDuringCurrentRefresh(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "authority refresh race")
	authorityCreate(t, f, "one", f.historyStart, f.historyStart.Add(time.Hour))
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	o := authorityOptions(f)
	claim := authorityClaim(t, f, f.now, o)
	locker := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(locker)
	lock, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lock)
	if _, err = dbgen.New(lock).LockMaintenanceSlo(t.Context(), claim.SloID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.runtime.processMaintenanceAuthority(t.Context(), claim, o, f.clock) }()
	waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenanceSlo %")
	if _, err = lock.Exec(t.Context(), `UPDATE almanac_slo_exclusion_state SET revision=2 WHERE slo_id=$1`, claim.SloID); err != nil {
		t.Fatal(err)
	}
	if _, err = lock.Exec(t.Context(), `UPDATE almanac_slo_exclusion_work SET desired_revision=2,work_version=work_version+1 WHERE slo_id=$1`, claim.SloID); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("refresh race acknowledged old target: %v", err)
	}
	row, err := dbgen.New(f.conn).ReadMaintenanceAuthorityWork(t.Context(), claim.SloID)
	if err != nil || row.CurrentRevision != 0 || row.ProcessedRevision != 0 || row.DesiredRevision != 2 {
		t.Fatalf("old refresh consumed newest work: %+v %v", row, err)
	}
}

func TestIntegrationMaintenanceAuthorityConsumerBoundaryCrossedDuringRefresh(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	start := f.at.Add(time.Minute)
	authorityCreate(t, f, "future", start, start.Add(time.Minute))
	o := authorityOptions(f)
	began := start.Add(-time.Second)
	finished := start.Add(time.Second)
	claim := authorityClaim(t, f, began, o)
	compilerTx(t, f, func(tx pgx.Tx) error {
		return advanceMaintenanceAuthority(t.Context(), tx, claim, o, began, func() time.Time { return finished })
	})
	row, err := dbgen.New(f.conn).ReadMaintenanceAuthorityWork(t.Context(), claim.SloID)
	if err != nil || !row.BoundaryThrough.Time.Equal(began) || !row.NextAttemptAt.Time.Equal(start) {
		t.Fatalf("boundary crossed during current refresh was skipped: %+v %v", row, err)
	}
}
