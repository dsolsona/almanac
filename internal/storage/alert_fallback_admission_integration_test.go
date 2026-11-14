//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// Test-only consumer of the existing runtime admission policy. Calculations share
// bounded derived capacity; they cannot open extra connections after refusal.
func experimentAdmittedFallback(ctx context.Context, runtime *Runtime, p dbgen.ReadRetainedWindowBatchParams) ([]dbgen.ReadRetainedWindowBatchRow, error) {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := runtime.Acquire(bounded, DerivedTraffic)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.Join(ErrDerivedPending, err)
		}
		return nil, err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='1s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return nil, err
	}
	rows, err := experimentFallbackLimited(bounded, tx, p, 8, 7200)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !errors.Is(err, ErrDerivedSourceFailed) && derivedTemporary(ctx, err) {
			return nil, errors.Join(ErrDerivedPending, err)
		}
		return nil, err
	}
	if err := tx.Commit(bounded); err != nil {
		return nil, err
	}
	return rows, nil
}

func TestIntegrationAlertFallbackAdmission(t *testing.T) {
	t.Parallel()
	s := newFallbackFixture(t, time.Minute)
	runtime, err := OpenRuntime(t.Context(), s.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	first, err := runtime.Acquire(t.Context(), DerivedTraffic)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := runtime.Acquire(t.Context(), DerivedTraffic)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, fallbackInputs(s))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	rows, err := experimentAdmittedFallback(t.Context(), runtime, p)
	if !errors.Is(err, ErrDerivedPending) || rows != nil {
		t.Fatalf("saturated admission must defer without evidence: %+v, %v", rows, err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("250 ms admission failed to finish before operation deadline: %s", elapsed)
	} else {
		t.Logf("saturated admission returned pending after %s", elapsed)
	}
	if runtime.pools[DerivedTraffic].Stat().TotalConns() != 2 {
		t.Fatal("refusal opened extra derived connections")
	}
	live, err := runtime.Acquire(t.Context(), LiveTraffic)
	if err != nil {
		t.Fatalf("derived saturation consumed live admission: %v", err)
	}
	live.Release()
	second.Release()
	rows, err = experimentAdmittedFallback(t.Context(), runtime, p)
	if err != nil || len(rows) != 4 {
		t.Fatalf("released capacity did not restore a complete snapshot: %+v, %v", rows, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if rows, err := experimentAdmittedFallback(cancelled, runtime, p); !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatalf("cancelled work must produce no evidence: %+v, %v", rows, err)
	}
	lock, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lock)
	if _, err := lock.Exec(t.Context(), "LOCK TABLE public.almanac_sli_observations IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	if rows, err := experimentAdmittedFallback(t.Context(), runtime, p); !errors.Is(err, ErrDerivedPending) || rows != nil {
		t.Fatalf("lock deadline must defer without partial counts: %+v, %v", rows, err)
	}
	if err := lock.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), "UPDATE public.almanac_derived_dirty_ranges SET attempts=5,lease_until=NULL WHERE sli_id=$1", s.sliID); err != nil {
		t.Fatal(err)
	}
	if rows, err := experimentAdmittedFallback(t.Context(), runtime, p); !errors.Is(err, ErrDerivedSourceFailed) || errors.Is(err, ErrDerivedPending) || rows != nil {
		t.Fatalf("terminal failure must not become a budget deferral: %+v, %v", rows, err)
	}
}
