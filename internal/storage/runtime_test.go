package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBoundStorageDoesNotBypassTheSchemaGateOrDatabaseIdentity(t *testing.T) {
	runtime := &Runtime{databaseURL: "postgres://example:example@localhost/example?sslmode=disable"}
	ctx := runtime.Bind(t.Context())
	if boundRuntime(context.Background()) != nil {
		t.Fatal("backend storage policy leaked into another context")
	}
	if conn, err := connectCurrentSchema(ctx, runtime.databaseURL); err == nil {
		conn.Close(t.Context())
		t.Fatal("bound operation bypassed the closed schema gate")
	} else if !strings.Contains(err.Error(), "runtime schema") {
		t.Fatalf("bound operation used the standalone connection path: %v", err)
	}
	if conn, err := connectCurrentSchema(ctx, "postgres://example:example@localhost/other?sslmode=disable"); err == nil {
		conn.Close(t.Context())
		t.Fatal("runtime was reused for another database")
	} else if !strings.Contains(err.Error(), "database mismatch") {
		t.Fatalf("database identity was not checked: %v", err)
	}
}

func TestRuntimePoolBudgetsCannotBeOverriddenByConnectionURL(t *testing.T) {
	for class, limit := range map[TrafficClass]int32{LiveTraffic: 4, InteractiveTraffic: 8, DerivedTraffic: 2} {
		config, err := runtimePoolConfig("postgres://example:example@localhost/example?sslmode=disable&pool_max_conns=99&pool_min_conns=50", class)
		if err != nil {
			t.Fatal(err)
		}
		if config.MaxConns != limit || config.MinConns != 0 || config.ConnConfig.ConnectTimeout != 5*time.Second || config.ConnConfig.RuntimeParams["timezone"] != "UTC" {
			t.Fatalf("class %d lost its bounded configuration", class)
		}
		for _, key := range []string{"pool_max_conns", "pool_min_conns"} {
			if _, exists := config.ConnConfig.RuntimeParams[key]; exists {
				t.Fatalf("pool parameter %s leaked into PostgreSQL session settings", key)
			}
		}
	}
}

func TestPooledReadConnectionReleasesOnce(t *testing.T) {
	releases := 0
	conn := &readConnection{release: func() { releases++ }}
	for range 2 {
		if err := conn.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if releases != 1 {
		t.Fatalf("connection returned to its pool %d times", releases)
	}
}
