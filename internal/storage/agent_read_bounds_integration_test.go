//go:build integration

package storage

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type transactionSetupCounter struct{ calls atomic.Int64 }

func (c *transactionSetupCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(data.SQL)), "SET LOCAL ") {
		c.calls.Add(1)
	}
	return ctx
}

func (*transactionSetupCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Explicit-runtime callers must not first apply standalone bounds; normal HTTP
// callers supply both runtime forms and must not configure the transaction twice.
func TestIntegrationPooledAgentReadConfiguresBoundsOnce(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	for _, mode := range []string{"context", "explicit", "both"} {
		t.Run(mode, func(t *testing.T) {
			counter := new(transactionSetupCounter)
			config, err := runtimePoolConfig(f.databaseURL, InteractiveTraffic)
			if err != nil {
				t.Fatal(err)
			}
			config.ConnConfig.Tracer = counter
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			runtime := &Runtime{databaseURL: f.databaseURL}
			runtime.pools[InteractiveTraffic] = pool
			runtime.compatible.Store(true)
			ctx := t.Context()
			if mode != "explicit" {
				ctx = runtime.Bind(ctx)
			}
			var explicit *Runtime
			if mode != "context" {
				explicit = runtime
			}
			clock := func() time.Time { return f.base.Add(time.Minute) }
			conn, _, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, f.databaseURL, explicit, f.verifier, clock, pgx.TxOptions{}, "administrator")
			if err != nil {
				t.Fatal(err)
			}
			defer closeInitialOwnerConnection(conn)
			defer rollbackTransaction(tx)
			var lock, statement, jit string
			if err := tx.QueryRow(ctx, "SELECT current_setting('lock_timeout'),current_setting('statement_timeout'),current_setting('jit')").Scan(&lock, &statement, &jit); err != nil {
				t.Fatal(err)
			}
			if lock != "250ms" || statement != "2s" || jit != "off" {
				t.Errorf("pooled bounds = %s/%s/%s, want 250ms/2s/off", lock, statement, jit)
			}
			if err := finishAgentRead(ctx, tx, clock, started, user, installation, session); err != nil {
				t.Fatal(err)
			}
			if calls := counter.calls.Load(); calls != 1 {
				t.Errorf("protected read made %d transaction-configuration round trips, want 1", calls)
			}
		})
	}
}
