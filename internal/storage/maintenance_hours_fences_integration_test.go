//go:build integration

package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationMaintenanceHourSourceLockWait(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"relevant", "unrelated", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, in := hourFixture(t)
			blocker, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(blocker)
			// Actual accepted ingestion locks the SLI before inserting observations and
			// its dirty marker. The reader must see a commit during this initial wait.
			if _, err := dbgen.New(blocker).LockSliMetadata(t.Context(), in.SliID); err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(conn)
			var micros atomic.Int64
			micros.Store(f.now.UnixMicro())
			clock := func() time.Time { return time.UnixMicro(micros.Load()).UTC() }
			done := make(chan error, 1)
			go func() {
				tx, err := conn.Begin(t.Context())
				if err != nil {
					done <- err
					return
				}
				_, err = prepareMaintenanceHour(t.Context(), tx, in, clock)
				if err == nil {
					err = tx.Commit(t.Context())
				}
				rollbackTransaction(tx)
				done <- err
			}()
			if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "LockMaintenanceHourSli"); err != nil {
				t.Fatal(err)
			}
			if kind == "deadline" {
				micros.Store(in.Deadline.UnixMicro())
			} else {
				hour := in.HourStart
				if kind == "unrelated" {
					hour = hour.Add(2 * time.Hour)
				}
				if _, err := blocker.Exec(t.Context(), `UPDATE almanac_sli_observations SET ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, hour); err != nil {
					t.Fatal(err)
				}
				if err := markObservationFixture(t.Context(), blocker, f.sliID); err != nil {
					t.Fatal(err)
				}
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if kind == "unrelated" {
				if err != nil {
					t.Fatalf("unrelated hour restarted preparation: %v", err)
				}
			} else if !errors.Is(err, ErrMaintenanceHourPending) {
				t.Fatalf("%s did not defer: %v", kind, err)
			}
			var n int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_maintenance_hours`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if kind == "unrelated" && n != 1 || kind != "unrelated" && n != 0 {
				t.Fatalf("%s publication count=%d", kind, n)
			}
		})
	}
}
func TestIntegrationMaintenanceHourRollbackAndFinalClock(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	for _, kind := range []string{"rollback", "deadline", "rr", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			options := pgx.TxOptions{}
			if kind == "rr" {
				options.IsoLevel = pgx.RepeatableRead
			}
			tx, err := f.conn.BeginTx(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			clock := func() time.Time {
				calls++
				if kind == "deadline" && calls >= 4 {
					return in.Deadline
				}
				return f.now
			}
			ctx := t.Context()
			if kind == "cancel" {
				var cancel func()
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err = prepareMaintenanceHour(ctx, tx, in, clock)
			switch kind {
			case "rollback":
				if err != nil {
					t.Fatal(err)
				}
			case "deadline":
				if !errors.Is(err, ErrMaintenanceHourPending) {
					t.Fatalf("final deadline: %v", err)
				}
			case "rr":
				if !errors.Is(err, ErrMaintenanceInvalid) {
					t.Fatalf("unsafe RR accepted: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			}
			rollbackTransaction(tx)
			var n int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_maintenance_hours`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("partial survived: %d %v", n, err)
			}
		})
	}
}
func TestIntegrationMaintenanceHourAbsentFenceAndExhaustion(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	in.HourStart = in.HourStart.Add(time.Hour)
	clearMaintenanceHourFixture(t, f, in.HourStart)
	in.RetentionStart = in.HourStart
	in.SourceGeneration = 0
	if _, err := hourPrepare(t, f, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at) SELECT sli_id,measurement_revision,generation,$2,$2::timestamptz+interval '1 hour',$3,$3 FROM almanac_sli_source_generations WHERE sli_id=$1`, f.sliID, in.HourStart, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now); !errors.Is(err, ErrMaintenanceHourPending) {
		t.Fatalf("absence survived dirty input: %v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_derived_dirty_ranges SET attempts=5,lease_until=$3 WHERE sli_id=$1 AND range_start=$2`, f.sliID, in.HourStart, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now); !errors.Is(err, ErrMaintenanceHourPending) {
		t.Fatalf("active final lease became exhausted: %v", err)
	}
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now.Add(time.Minute)); !errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("terminal source became pending: %v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_derived_dirty_ranges WHERE sli_id=$1 AND range_start=$2`, f.sliID, in.HourStart); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at,ingestion_generation)
 SELECT $2,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at,$3,1 FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2::timestamptz-interval '1 hour'`, f.sliID, in.HourStart, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now); !errors.Is(err, ErrMaintenanceHourPending) {
		t.Fatalf("absence survived new raw input: %v", err)
	}
	if _, err := dbgen.New(f.conn).RebuildDerivedBatch(t.Context(), dbgen.RebuildDerivedBatchParams{SliIds: []pgtype.UUID{in.SliID}, Revisions: []int64{1}, Generations: []int64{1}, Starts: []pgtype.Timestamptz{timestamp(in.HourStart)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now); !errors.Is(err, ErrMaintenanceHourStale) {
		t.Fatalf("absence survived published hour: %v", err)
	}
}

func TestIntegrationMaintenanceHourFreshReadIdentity(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	old, err := hourPrepare(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=900,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, in.HourStart); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now); !errors.Is(err, ErrMaintenanceHourPending) {
		t.Fatalf("old population escaped dirty fence: %v", err)
	}
	f.drain(t, "changed source")
	if _, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now); !errors.Is(err, ErrMaintenanceHourStale) {
		t.Fatalf("old source generation was relabelled: %v", err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, in.HourStart).Scan(&in.SourceGeneration); err != nil {
		t.Fatal(err)
	}
	fresh, err := hourPrepare(t, f, in)
	if err != nil || fresh.Eligible.Good.RatString() != "49410" {
		t.Fatalf("new exact population: %+v %v", fresh, err)
	}
	stored, err := readStoredMaintenanceHour(t.Context(), dbgen.New(f.conn), old.Key, old.ExclusionRevision)
	if err != nil || !equalMaintenancePopulation(stored.Eligible, old.Eligible) {
		t.Fatalf("old retained population changed: %+v %v", stored, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_maintenance_hours SET eligible_good=0 WHERE source_generation=$1`, in.SourceGeneration); compilerPGCode(err) != "23514" {
		t.Fatalf("immutable prepared population changed: %v", err)
	}
	pending := f.input("pending-mask")
	pending.Start = in.HourStart.Add(30 * time.Minute)
	pending.End = in.HourStart.Add(40 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, pending); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	in.BuildID = b.ID
	if _, err := hourPrepare(t, f, in); !errors.Is(err, ErrMaintenanceCompilePending) {
		t.Fatalf("partial mask readable: %v", err)
	}
	compilerFinish(t, f, b.ID, 10)
	in.SloID = mustPGUUID(f.ids[1])
	if _, err := hourPrepare(t, f, in); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("other SLO mask leaked: %v", err)
	}
}

func TestIntegrationMaintenanceHourSourcePublicationRace(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	blocker, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(blocker)
	if _, err := blocker.Exec(t.Context(), `SELECT 1 FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2 FOR UPDATE`, f.sliID, in.HourStart); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(conn)
	done := make(chan error, 1)
	go func() {
		tx, err := conn.Begin(t.Context())
		if err != nil {
			done <- err
			return
		}
		_, err = prepareMaintenanceHour(t.Context(), tx, in, f.clock)
		if err == nil {
			err = tx.Commit(t.Context())
		}
		rollbackTransaction(tx)
		done <- err
	}()
	if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "LockMaintenanceHourSource"); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET source_generation=source_generation+1 WHERE sli_id=$1 AND hour_start=$2`, f.sliID, in.HourStart); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrMaintenanceHourStale) {
		t.Fatalf("source published during wait escaped fence: %v", err)
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_maintenance_hours`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale publication %d %v", count, err)
	}
}
