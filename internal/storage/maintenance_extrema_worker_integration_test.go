//go:build integration

package storage

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func extremaWorkerFixture(t *testing.T, ready bool) (maintenanceFixture, maintenanceHourInput, dbgen.AlmanacMaintenancePreparation) {
	t.Helper()
	f, h := hourFixture(t)
	if ready {
		if _, err := hourPrepare(t, f, h); err != nil {
			t.Fatal(err)
		}
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	if _, err := dbgen.New(f.conn).EnsureSloExtremaCursors(t.Context(), dbgen.EnsureSloExtremaCursorsParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(f.now)}); err != nil {
		t.Fatal(err)
	}
	request := targetedPreparation(f, "extrema_hour", h.HourStart, 0)
	request.RequestedRevision = 1
	request.BuildID = h.BuildID
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{request}, maintenancePreparationLive)
	return f, h, preparationClaimOne(t, f)
}

func TestIntegrationMaintenanceExtremaHourWorkerPublish(t *testing.T) {
	t.Parallel()
	f, h, job := extremaWorkerFixture(t, true)
	before := targetQueueSourceSnapshot(t, f)
	state, err := f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), f.clock)
	if err != nil || state != "complete" {
		t.Fatalf("execute %q %v", state, err)
	}
	var payload []byte
	var gen, revision int64
	if err := f.conn.QueryRow(t.Context(), `SELECT lanes,source_generation,exclusion_revision FROM almanac_slo_extrema WHERE slo_id=$1 AND range_start=$2 AND resolution_seconds=3600`, h.SloID, h.HourStart).Scan(&payload, &gen, &revision); err != nil {
		t.Fatal(err)
	}
	var lanes extremaLanes
	if err := unmarshalReceiptMetadata(payload, &lanes); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || gen <= job.SourceGeneration.Int64 {
		t.Fatalf("actual snapshot relabelled as requested generation: gotG=%d requestedG=%d R=%d", gen, job.SourceGeneration.Int64, revision)
	}
	session, _, _, base := extremaProducerSession(t, f, h)
	defer rollbackTransaction(session.tx)
	masks := []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}}
	for i, lane := range lanes.list() {
		if lane == nil {
			continue
		}
		w := base
		w.Duration = []time.Duration{base.Duration, base.Cadence, time.Hour, 6 * time.Hour}[i]
		for _, mark := range lane.Marks {
			w.At = mark.At
			got, e := mark.result(t.Context(), w)
			if e != nil {
				t.Fatal(e)
			}
			want := evaluationRawWindow(t, w, masks)
			if !reflect.DeepEqual(got.Good, want.Good) || !reflect.DeepEqual(got.Total, want.Total) || got.Excluded != want.Excluded || got.Observed != want.Observed {
				t.Fatalf("stored replay differs from raw at %v lane%d", mark.At, i)
			}
			if e := mark.validateMaintenanceRowAuthority(w, maintenanceRetainedRowAuthority{Scope: preparationScope(f), SourceGeneration: gen, RequestedRevision: revision}); e != nil {
				t.Fatal(e)
			}
		}
	}
	rollbackTransaction(session.tx)
	if targetQueueSourceSnapshot(t, f) != before {
		t.Fatal("target publication advanced source cursor/dirty state")
	}
	var saved dbgen.AlmanacMaintenancePreparation
	compilerTx(t, f, func(tx pgx.Tx) error {
		var e error
		saved, e = dbgen.New(tx).LockMaintenancePreparation(t.Context(), job.ID)
		return e
	})
	if saved.State != "pending" || saved.LeaseToken != job.LeaseToken || saved.Attempts != 0 {
		t.Fatal("executor acknowledged outer claim")
	}
}

func TestIntegrationMaintenanceExtremaHourDispatch(t *testing.T) {
	t.Parallel()
	f, _, job := extremaWorkerFixture(t, true)
	state, err := f.runtime.processMaintenancePreparation(t.Context(), job, preparationOptions(f), f.clock)
	if err != nil || state != "complete" {
		t.Fatalf("durable hour dispatch did not complete: %s %v", state, err)
	}
	var saved string
	var leased bool
	var rows, attempts int
	if err := f.conn.QueryRow(t.Context(), `SELECT state,lease_token IS NOT NULL,attempts,(SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$2 AND exclusion_revision=1 AND range_start=$3) FROM public.almanac_maintenance_preparation WHERE id=$1`, job.ID, job.SloID, job.HourStart).Scan(&saved, &leased, &attempts, &rows); err != nil || saved != "complete" || leased || attempts != 0 || rows != 1 {
		t.Fatalf("dispatch lost artifact/ack: state=%s leased=%v attempts=%d rows=%d err=%v", saved, leased, attempts, rows, err)
	}
}

func assertNoTargetArtifact(t *testing.T, f maintenanceFixture, job dbgen.AlmanacMaintenancePreparation, children int) {
	t.Helper()
	var rows, childJobs int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1 AND exclusion_revision=$2),(SELECT count(*) FROM almanac_maintenance_preparation WHERE id<>$3)`, job.SloID, job.TargetRevision, job.ID).Scan(&rows, &childJobs); err != nil || rows != 0 || childJobs != children {
		t.Fatalf("partial publication rows=%d childJobs=%d: %v", rows, childJobs, err)
	}
}

func TestIntegrationMaintenanceExtremaHourWorkerPending(t *testing.T) {
	t.Parallel()
	f, _, job := extremaWorkerFixture(t, false)
	state, err := f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), f.clock)
	if err != nil || state != "pending" {
		t.Fatalf("missing scoped hour: %q %v", state, err)
	}
	assertNoTargetArtifact(t, f, job, 1)
	var kind string
	if err := f.conn.QueryRow(t.Context(), `SELECT kind FROM almanac_maintenance_preparation WHERE id<>$1`, job.ID).Scan(&kind); err != nil || kind != "scoped_hour" {
		t.Fatalf("wrong missing prerequisite: %s %v", kind, err)
	}
	turn, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock)
	if err != nil || turn.Complete != 1 {
		t.Fatalf("child execution: %+v %v", turn, err)
	}
	state, err = f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), f.clock)
	if err != nil || state != "complete" {
		t.Fatalf("prepared target never recovered: %q %v", state, err)
	}
}

func TestIntegrationMaintenanceExtremaHourWorkerGuards(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unadmitted", "missing_parent", "stale_claim", "future", "future_source"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, h, job := extremaWorkerFixture(t, true)
			switch mode {
			case "unadmitted":
				f.runtime.maintenanceConsumers.Store(nil)
			case "missing_parent":
				if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_slo_extrema_cursor WHERE slo_id=$1`, job.SloID); err != nil {
					t.Fatal(err)
				}
			case "stale_claim":
				job.LeaseToken = maintenanceCompilerID()
			case "future":
				job.HourStart = timestamp(f.now.Truncate(time.Hour))
			case "future_source":
				// A second real natural key preserves a captured source target not yet visible.
				req := targetedPreparation(f, "extrema_hour", h.HourStart, 100000)
				req.RequestedRevision = 1
				req.BuildID = h.BuildID
				preparationEnqueue(t, f, []maintenanceEvaluationPreparation{req}, maintenancePreparationLive)
				job = preparationClaimOne(t, f)
			}
			state, err := f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), f.clock)
			if mode == "stale_claim" {
				if !errors.Is(err, ErrMaintenancePreparationStale) {
					t.Fatalf("stale claim: %v", err)
				}
			} else if err != nil && !errors.Is(err, ErrDerivedPending) {
				t.Fatalf("guard %s: %q %v", mode, state, err)
			} else if err == nil && state != "pending" {
				t.Fatalf("guard escaped: %s %q", mode, state)
			}
			var rows int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1`, job.SloID).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("guard published %d: %v", rows, err)
			}
		})
	}
}

func TestIntegrationMaintenanceExtremaHourWorkerWaitFences(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"parent_version", "ready_lease", "pending_lease"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, _, job := extremaWorkerFixture(t, mode != "pending_lease")
			locker := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(locker)
			lock, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(lock)
			pattern := "%-- name: LockMaintenancePreparation %"
			if mode == "parent_version" {
				if _, err := lock.Exec(t.Context(), `SELECT id FROM almanac_slo_extrema_cursor WHERE slo_id=$1 FOR UPDATE`, job.SloID); err != nil {
					t.Fatal(err)
				}
				pattern = "%-- name: LockMaintenanceExtremaParent %"
			} else {
				if _, err := dbgen.New(lock).LockMaintenancePreparation(t.Context(), job.ID); err != nil {
					t.Fatal(err)
				}
			}
			var micros atomic.Int64
			micros.Store(f.now.UnixMicro())
			clock := func() time.Time { return time.UnixMicro(micros.Load()).UTC() }
			done := make(chan error, 1)
			go func() {
				_, e := f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), clock)
				done <- e
			}()
			waitForAgentQueryWait(t, f.conn, pattern)
			if mode == "parent_version" {
				if _, err := lock.Exec(t.Context(), `UPDATE almanac_slo_extrema_cursor SET version=version+1 WHERE slo_id=$1`, job.SloID); err != nil {
					t.Fatal(err)
				}
			} else {
				micros.Store(job.LeaseUntil.Time.UnixMicro())
			}
			if err := lock.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("executor did not finish after resource wait")
			}
			expected := ErrMaintenancePreparationStale
			if mode == "parent_version" {
				expected = ErrDerivedPending
			}
			if !errors.Is(err, expected) {
				t.Fatalf("post-wait fence %s: %v", mode, err)
			}
			assertNoTargetArtifact(t, f, job, 0)
		})
	}
}

func TestIntegrationMaintenanceExtremaHourWorkerSnapshotChanges(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"source", "exclusion"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, _, job := extremaWorkerFixture(t, true)
			calls := 0
			clock := func() time.Time {
				calls++
				// The second clock is after RR has released its connection and before RC.
				if calls == 2 {
					if kind == "source" {
						if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sli_source_generations SET generation=generation+1 WHERE sli_id=$1`, job.SliID); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("between-phases")); err != nil {
							t.Fatal(err)
						}
					}
				}
				return f.now
			}
			_, err := f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), clock)
			expected := ErrDerivedPending
			if kind == "exclusion" {
				expected = ErrMaintenanceHourStale
			}
			if !errors.Is(err, expected) || calls < 2 {
				t.Fatalf("snapshot change %s: %v calls%d", kind, err, calls)
			}
			assertNoTargetArtifact(t, f, job, 0)
		})
	}
}

func TestIntegrationMaintenanceExtremaHourWorkerInvalidClock(t *testing.T) {
	t.Parallel()
	f, _, job := extremaWorkerFixture(t, false)
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 1 {
			return f.now
		}
		return time.Time{}
	}
	_, err := f.runtime.executeMaintenanceExtremaHour(t.Context(), job, preparationOptions(f), clock)
	if !errors.Is(err, ErrMaintenancePreparationStale) {
		t.Fatalf("invalid completion clock classified as facts failure: %v", err)
	}
	assertNoTargetArtifact(t, f, job, 0)
}
