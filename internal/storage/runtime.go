package storage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TrafficClass keeps interactive and background work out of live connection slots.
type TrafficClass uint8

const (
	LiveTraffic TrafficClass = iota
	InteractiveTraffic
	DerivedTraffic
)

// Runtime owns the backend's pools. Callers release every acquired connection;
// the backend closes the runtime after stopping admission and draining requests.
type Runtime struct {
	pools       [3]*pgxpool.Pool
	databaseURL string
	compatible  atomic.Bool
	metrics     runtimeMetrics
}

type runtimeContextKey struct{}

// Bind gives one backend request its lifetime-owned admission policy. It carries
// no authorization decision: every storage operation still verifies authority.
// Explicit live/derived callers select their class independently of this default.
func (r *Runtime) Bind(ctx context.Context) context.Context {
	return context.WithValue(ctx, runtimeContextKey{}, r)
}

func boundRuntime(ctx context.Context) *Runtime {
	runtime, _ := ctx.Value(runtimeContextKey{}).(*Runtime)
	return runtime
}

// OpenRuntime checks compatibility once before admitting pooled application work.
// Explicit migrations and the backend ownership lock retain dedicated connections.
func OpenRuntime(ctx context.Context, databaseURL string) (*Runtime, error) {
	compatibility, err := CheckCompatibility(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if compatibility != CompatibilityCurrent {
		return nil, errors.New("storage: runtime requires a current database schema")
	}
	runtime := &Runtime{databaseURL: databaseURL}
	runtime.compatible.Store(true)
	for class := LiveTraffic; class <= DerivedTraffic; class++ {
		config, err := runtimePoolConfig(databaseURL, class)
		if err != nil {
			runtime.Close()
			return nil, err
		}
		config.ConnConfig.Tracer = runtimeQueryTracer{metrics: &runtime.metrics.queries[class]}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			runtime.Close()
			return nil, operationError(ctx, "storage: pool initialization failed", err)
		}
		runtime.pools[class] = pool
		if err := pool.Ping(ctx); err != nil {
			runtime.Close()
			return nil, operationError(ctx, "storage: pool readiness failed", err)
		}
	}
	return runtime, nil
}

type readConnection struct {
	*pgx.Conn
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func (c *readConnection) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		if c.release != nil {
			c.release()
		} else {
			c.closeErr = c.Conn.Close(ctx)
		}
	})
	return c.closeErr
}

func acquireReadConnection(ctx context.Context, databaseURL string, runtime *Runtime) (*readConnection, error) {
	return acquireConnection(ctx, databaseURL, runtime, InteractiveTraffic)
}

func runtimeOption(values []*Runtime) (*Runtime, error) {
	if len(values) > 1 {
		return nil, errors.New("storage: invalid runtime options")
	}
	if len(values) == 1 {
		return values[0], nil
	}
	return nil, nil
}

func acquireConnection(ctx context.Context, databaseURL string, runtime *Runtime, class TrafficClass) (*readConnection, error) {
	if runtime == nil {
		runtime = boundRuntime(ctx)
	}
	if runtime == nil {
		conn, err := connectStandaloneCurrentSchema(ctx, databaseURL)
		if err != nil {
			return nil, err
		}
		return &readConnection{Conn: conn}, nil
	}
	if runtime.databaseURL != databaseURL {
		return nil, errors.New("storage: runtime database mismatch")
	}
	conn, err := runtime.Acquire(ctx, class)
	if err != nil {
		return nil, err
	}
	return &readConnection{Conn: conn.Conn(), release: conn.Release}, nil
}

// Acquire has a finite admission wait even when the caller has no deadline.
func (r *Runtime) Acquire(ctx context.Context, class TrafficClass) (*pgxpool.Conn, error) {
	if !r.compatible.Load() {
		return nil, errors.New("storage: runtime schema is unavailable")
	}
	if class > DerivedTraffic || r.pools[class] == nil {
		return nil, errors.New("storage: invalid traffic class")
	}
	bounded, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	conn, err := r.pools[class].Acquire(bounded)
	if err != nil {
		return nil, operationError(bounded, "storage: database admission failed", err)
	}
	return conn, nil
}

// CheckReady refreshes the schema gate on health checks, rather than every read.
func (r *Runtime) CheckReady(ctx context.Context) error {
	// Readiness uses reserved live admission even while the schema gate is closed;
	// otherwise a repaired schema could never reopen it. It opens no extra socket.
	bounded, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	conn, err := r.pools[LiveTraffic].Acquire(bounded)
	if err != nil {
		// Capacity failure is not evidence that the schema changed. Preserve the
		// last compatibility decision so a brief full live pool cannot close all
		// other admission until the next readiness probe.
		return operationError(bounded, "storage: readiness admission failed", err)
	}
	defer conn.Release()
	compatibility, err := checkConnectionCompatibility(ctx, conn.Conn())
	r.compatible.Store(err == nil && compatibility == CompatibilityCurrent)
	if err != nil {
		return err
	}
	if compatibility != CompatibilityCurrent {
		return errors.New("storage: runtime schema is incompatible")
	}
	return nil
}

// Close waits for acquired connections to be released. The lifecycle owner must
// cancel/drain work first; pgxpool refuses new acquisitions once closing begins.
func (r *Runtime) Close() {
	for _, pool := range r.pools {
		if pool != nil {
			pool.Close()
		}
	}
}

func runtimePoolConfig(databaseURL string, class TrafficClass) (*pgxpool.Config, error) {
	if class > DerivedTraffic {
		return nil, errors.New("storage: invalid traffic class")
	}
	connConfig, err := ParseDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("storage: invalid pool configuration")
	}
	// pgxpool removes its own URL parameters before handing connection settings
	// to PostgreSQL. Replacing ConnConfig would reintroduce those as invalid GUCs.
	config.ConnConfig.RuntimeParams["timezone"] = connConfig.RuntimeParams["timezone"]
	config.ConnConfig.ConnectTimeout = connConfig.ConnectTimeout
	config.MinConns = 0
	config.MaxConns = [...]int32{4, 8, 2}[class]
	config.ConnConfig.RuntimeParams["application_name"] = [...]string{"almanac-live", "almanac-interactive", "almanac-derived"}[class]
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := setSessionBounds(ctx, conn); err != nil {
			return err
		}
		switch class {
		case InteractiveTraffic:
			_, err := conn.Exec(ctx, "SET statement_timeout='2s'; SET lock_timeout='250ms'")
			return err
		case DerivedTraffic:
			_, err := conn.Exec(ctx, "SET statement_timeout='5s'; SET lock_timeout='250ms'")
			return err
		default:
			return nil
		}
	}
	return config, nil
}
