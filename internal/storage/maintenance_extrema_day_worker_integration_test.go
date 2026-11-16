//go:build integration

package storage

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func extremaDayWorkerFixture(t *testing.T) (maintenanceFixture, dbgen.AlmanacMaintenancePreparation, time.Time) {
	t.Helper()
	s := newSelfHealFixture(t, "day-worker", true)
	end := s.historyStart.Truncate(24 * time.Hour).Add(24 * time.Hour)
	shift := int64(end.Add(-2*time.Hour).Sub(s.historyStart) / time.Second)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+make_interval(secs=>$2),sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+make_interval(secs=>$2),recorded_at+make_interval(secs=>$2) FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start<$3`, s.sliID, shift, s.historyStart.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.now = end.Add(time.Minute)
	s.at = s.now
	id := s.createSLOWithHistory(t, "day-worker", time.Hour, end.Add(-2*time.Hour))
	var verifier [32]byte
	verifier[0] = 98
	f := maintenanceFixture{selfHealFixture: s, ids: []string{id}, proof: DatasourceProof{SessionVerifier: verifier}}
	m := f.input("day-mask")
	m.Start = end.Add(-50 * time.Minute)
	m.End = end.Add(-40 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	f.drain(t, "day source")
	var gen int64
	if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, end.Add(-time.Hour)).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	h := maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SloID: mustPGUUID(id), SliID: mustPGUUID(f.sliID), BuildID: b.ID, DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: gen, HourStart: end.Add(-time.Hour)}, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	if _, err := dbgen.New(f.conn).EnsureSloExtremaCursors(t.Context(), dbgen.EnsureSloExtremaCursorsParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(f.now)}); err != nil {
		t.Fatal(err)
	}
	// Source cursor readiness is a separate producer responsibility. This
	// fixture models ready source history with missing targeted artifacts.
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_extrema_cursor SET built_through=$2,dirty_from=NULL,dirty_to=NULL WHERE slo_id=$1`, id, end); err != nil {
		t.Fatal(err)
	}
	request := targetedPreparation(f, "extrema_day", end.Add(-24*time.Hour), 0)
	request.RequestedRevision = 1
	request.BuildID = b.ID
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{request}, maintenancePreparationLive)
	return f, preparationClaimOne(t, f), end
}

func TestIntegrationMaintenanceExtremaDayProgress(t *testing.T) {
	t.Parallel()
	f, job, end := extremaDayWorkerFixture(t)
	before := targetQueueSourceSnapshot(t, f)
	for round := range 2 {
		for range 2 {
			state, err := f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), f.clock)
			if err != nil || state != "pending" {
				t.Fatalf("day pending: %s %v", state, err)
			}
		}
		var children int
		if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='extrema_hour'`).Scan(&children); err != nil || children != round+1 {
			t.Fatalf("bounded missing child/dedup: got%d want%d err%v", children, round+1, err)
		}
		child := preparationClaimOne(t, f)
		state, err := f.runtime.processMaintenancePreparation(t.Context(), child, preparationOptions(f), f.clock)
		if err != nil || state != "complete" {
			t.Fatalf("hour child: %s %v", state, err)
		}
		if round == 0 {
			// A legitimate legacy R0 row remains source-ready. The day must
			// prove compatibility of the original selection's full windows.
			session, row, source, _ := extremaProducerSession(t, f, maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SloID: job.SloID}})
			base := extremaWindowInput(row, source, f.now, preparationOptions(f).RetentionStart)
			legacy, e := buildExtremaHour(t.Context(), dbgen.New(session.tx), row, base, end.Add(-2*time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			payload, e := marshalReceiptMetadata(legacy)
			if e != nil {
				t.Fatal(e)
			}
			rollbackTransaction(session.tx)
			if _, e = f.conn.Exec(t.Context(), `UPDATE almanac_slo_extrema SET exclusion_revision=0,lanes=$3 WHERE slo_id=$1 AND range_start=$2 AND resolution_seconds=3600`, job.SloID, end.Add(-2*time.Hour), payload); e != nil {
				t.Fatal(e)
			}
		}
	}
	state, err := f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), f.clock)
	if err != nil || state != "complete" {
		t.Fatalf("day publication: %s %v", state, err)
	}
	var payload []byte
	var revision, generation int64
	if err := f.conn.QueryRow(t.Context(), `SELECT lanes,exclusion_revision,source_generation FROM almanac_slo_extrema WHERE slo_id=$1 AND range_start=$2 AND resolution_seconds=86400`, job.SloID, job.HourStart).Scan(&payload, &revision, &generation); err != nil {
		t.Fatal(err)
	}
	var lanes extremaLanes
	if err := unmarshalReceiptMetadata(payload, &lanes); err != nil {
		t.Fatal(err)
	}
	if lanes.Compliance == nil || !lanes.Compliance.Mark.At.Equal(end) || revision != 1 || generation <= 0 {
		t.Fatal("day identity/compliance cutoff lost")
	}
	row, err := dbgen.New(f.conn).GetSlo(t.Context(), job.SloID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := dbgen.New(f.conn).GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: job.SliID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	base := extremaWindowInput(row, source, f.now, preparationOptions(f).RetentionStart)
	value, err := lanes.Compliance.Mark.result(t.Context(), base)
	if err != nil || !terminalCount(value.Good, 49500) || !terminalCount(value.Total, 50000) || value.Excluded != 10 || value.Observed != 50 {
		t.Fatalf("independent 50 eligible interval counts: %+v %v", value, err)
	}
	if err := lanes.Compliance.Mark.validateMaintenanceRowAuthority(base, maintenanceRetainedRowAuthority{Scope: preparationScope(f), SourceGeneration: generation, RequestedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	if targetQueueSourceSnapshot(t, f) != before {
		t.Fatal("target day advanced/cleared parent source cursor")
	}
	found := false
	for _, mark := range lanes.Window.Marks {
		if mark.At.After(end.Add(-2*time.Hour)) && !mark.At.After(end.Add(-time.Hour)) {
			found = true
			if mark.Maintenance == nil || mark.Maintenance.ComputedRevision != 0 || mark.Maintenance.RequestedRevision != 1 || mark.Maintenance.Proof == nil || mark.Maintenance.Proof.Kind != "indexed_absence" {
				t.Fatal("legacy selected value relabelled/lost compatibility proof")
			}
		}
	}
	if !found {
		t.Fatal("no eligible legacy selection retained")
	}
	var stateSaved string
	var leased bool
	if err := f.conn.QueryRow(t.Context(), `SELECT state,lease_token IS NOT NULL FROM almanac_maintenance_preparation WHERE id=$1`, job.ID).Scan(&stateSaved, &leased); err != nil || stateSaved != "pending" || !leased {
		t.Fatalf("executor acknowledged outer job: %s %v %v", stateSaved, leased, err)
	}
}

func prepareExtremaDayChildren(t *testing.T, f maintenanceFixture, job dbgen.AlmanacMaintenancePreparation) {
	t.Helper()
	for range 2 {
		state, err := f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), f.clock)
		if err != nil || state != "pending" {
			t.Fatalf("child scheduling: %s %v", state, err)
		}
		child := preparationClaimOne(t, f)
		state, err = f.runtime.processMaintenancePreparation(t.Context(), child, preparationOptions(f), f.clock)
		if err != nil || state != "complete" {
			t.Fatalf("child publication: %s %v", state, err)
		}
	}
}

func assertNoMaintenanceDay(t *testing.T, f maintenanceFixture) {
	t.Helper()
	var rows int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=86400`, f.ids[0]).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("stale day published %d rows: %v", rows, err)
	}
}

func TestIntegrationMaintenanceExtremaDaySnapshotFences(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"source", "exclusion", "unadmitted"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, job, end := extremaDayWorkerFixture(t)
			prepareExtremaDayChildren(t, f, job)
			if kind == "unadmitted" {
				f.runtime.maintenanceConsumers.Store(nil)
			}
			calls := 0
			clock := func() time.Time {
				calls++
				if calls == 2 {
					if kind == "source" {
						if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, end.Add(-time.Minute)); err != nil {
							t.Fatal(err)
						}
					} else if kind == "exclusion" {
						m := f.input("day-race")
						m.Start = end.Add(time.Hour)
						m.End = m.Start.Add(time.Hour)
						if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
							t.Fatal(err)
						}
					}
				}
				return f.now
			}
			state, err := f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), clock)
			if kind == "source" && !errors.Is(err, ErrDerivedPending) || kind == "exclusion" && !errors.Is(err, ErrMaintenanceHourStale) || kind == "unadmitted" && (err != nil || state != "pending") {
				t.Fatalf("%s fence: %s %v", kind, state, err)
			}
			assertNoMaintenanceDay(t, f)
		})
	}
}

func TestIntegrationMaintenanceExtremaDayWaitFences(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"parent_version", "lease"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, job, _ := extremaDayWorkerFixture(t)
			prepareExtremaDayChildren(t, f, job)
			locker := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(locker)
			lock, err := locker.BeginTx(t.Context(), pgx.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(lock)
			pattern := "%-- name: LockMaintenancePreparation %"
			if kind == "parent_version" {
				if _, err := lock.Exec(t.Context(), `SELECT id FROM almanac_slo_extrema_cursor WHERE slo_id=$1 FOR UPDATE`, job.SloID); err != nil {
					t.Fatal(err)
				}
				pattern = "%-- name: LockMaintenanceExtremaParent %"
			} else if _, err := dbgen.New(lock).LockMaintenancePreparation(t.Context(), job.ID); err != nil {
				t.Fatal(err)
			}
			var micros atomic.Int64
			micros.Store(f.now.UnixMicro())
			clock := func() time.Time { return time.UnixMicro(micros.Load()).UTC() }
			done := make(chan error, 1)
			go func() {
				_, e := f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), clock)
				done <- e
			}()
			waitForAgentQueryWait(t, f.conn, pattern)
			if kind == "parent_version" {
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
				t.Fatal("executor failed to release after lock")
			}
			want := ErrMaintenancePreparationStale
			if kind == "parent_version" {
				want = ErrDerivedPending
			}
			if !errors.Is(err, want) {
				t.Fatalf("post-lock %s: %v", kind, err)
			}
			assertNoMaintenanceDay(t, f)
		})
	}
}

func TestIntegrationMaintenanceExtremaDayTerminalChild(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"unavailable", "exhausted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f, job, _ := extremaDayWorkerFixture(t)
			result, err := f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), f.clock)
			if err != nil || result != "pending" {
				t.Fatalf("initial missing child: %s %v", result, err)
			}
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state=$1,reason='history_unavailable' WHERE kind='extrema_hour'`, state); err != nil {
				t.Fatal(err)
			}
			result, err = f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), f.clock)
			if !errors.Is(err, ErrMaintenanceHourUnavailable) {
				t.Fatalf("terminal child left day pending: %s %v", result, err)
			}
			assertNoMaintenanceDay(t, f)
			// A newer actual source generation has a distinct repair key. The
			// old terminal result must not suppress its fresh child request.
			if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, f.historyStart); err != nil {
				t.Fatal(err)
			}
			result, err = f.runtime.executeMaintenanceExtremaDay(t.Context(), job, preparationOptions(f), f.clock)
			if err != nil || result != "pending" {
				t.Fatalf("old source terminal poisoned newer repair: %s %v", result, err)
			}
			var pending, terminal int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state='pending'),count(*) FILTER(WHERE state IN ('unavailable','exhausted')) FROM almanac_maintenance_preparation WHERE kind='extrema_hour'`).Scan(&pending, &terminal); err != nil || pending != 1 || terminal != 1 {
				t.Fatalf("fresh source key: pending%d terminal%d err%v", pending, terminal, err)
			}
		})
	}
}
