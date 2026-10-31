//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRuntimeReservesLiveCapacityWhenInteractivePoolIsFull(t *testing.T) {
	f := newAgentFixture(t)
	runtime, err := OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	var held []*pgxpool.Conn
	t.Cleanup(func() {
		for _, conn := range held {
			conn.Release()
		}
	})
	for range 8 {
		conn, err := runtime.Acquire(t.Context(), InteractiveTraffic)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	if _, err := runtime.Acquire(t.Context(), InteractiveTraffic); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated interactive admission: %v", err)
	}
	live, err := runtime.Acquire(t.Context(), LiveTraffic)
	if err != nil {
		t.Fatalf("interactive saturation consumed live capacity: %v", err)
	}
	defer live.Release()
	var one int
	if err := live.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("reserved live connection is unusable: %d, %v", one, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.Acquire(cancelled, InteractiveTraffic); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission: %v", err)
	}
}

func TestIntegrationRuntimeReusesReleasedDatabaseConnection(t *testing.T) {
	f := newAgentFixture(t)
	runtime, err := OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var firstPID uint32
	for attempt := range 2 {
		conn, err := runtime.Acquire(t.Context(), InteractiveTraffic)
		if err != nil {
			t.Fatal(err)
		}
		pid := conn.Conn().PgConn().PID()
		conn.Release()
		if attempt == 0 {
			firstPID = pid
		} else if pid != firstPID {
			t.Fatalf("released connection was replaced: %d -> %d", firstPID, pid)
		}
	}
}

func TestIntegrationFullLiveAdmissionDoesNotCloseKnownSchemaGate(t *testing.T) {
	f := newAgentFixture(t)
	runtime, err := OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var held []*pgxpool.Conn
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	for range 4 {
		conn, err := runtime.Acquire(t.Context(), LiveTraffic)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	if err := runtime.CheckReady(t.Context()); err == nil {
		t.Fatal("full live admission passed readiness")
	}
	conn, err := runtime.Acquire(t.Context(), InteractiveTraffic)
	if err != nil {
		t.Fatalf("capacity failure closed an unrelated admission class: %v", err)
	}
	conn.Release()
}

func TestIntegrationReadinessRecoversSchemaGateWithoutAnotherConnection(t *testing.T) {
	f := newAgentFixture(t)
	runtime, err := OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var checksum []byte
	if err := conn.QueryRow(t.Context(), `SELECT checksum FROM public.almanac_schema_migrations WHERE version=1`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_schema_migrations SET checksum=decode(repeat('00',32),'hex') WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckReady(t.Context()); err == nil {
		t.Fatal("inconsistent schema passed readiness")
	}
	if c, err := runtime.Acquire(t.Context(), InteractiveTraffic); err == nil {
		c.Release()
		t.Fatal("closed schema gate still admitted interactive work")
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_schema_migrations SET checksum=$1 WHERE version=1`, checksum); err != nil {
		t.Fatal(err)
	}
	before := runtime.pools[LiveTraffic].Stat().TotalConns()
	if err := runtime.CheckReady(t.Context()); err != nil {
		t.Fatalf("readiness could not reopen the repaired schema gate: %v", err)
	}
	if after := runtime.pools[LiveTraffic].Stat().TotalConns(); after != before {
		t.Fatalf("readiness did not reuse reserved live capacity: before=%d after=%d", before, after)
	}
}
