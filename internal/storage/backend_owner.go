package storage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrBackendAlreadyRunning = errors.New("storage: backend already running")
	ErrBackendOwnershipLost  = errors.New("storage: backend ownership lost")
)

const backendOwnerNamespace int32 = 0x414c0003
const backendOwnerKey int32 = 1

// BackendOwner holds the database session advisory lock for one backend process.
type BackendOwner struct {
	conn          *pgx.Conn
	stop          chan struct{}
	done          chan struct{}
	finished      chan struct{}
	closeOnce     sync.Once
	closeDeadline time.Time
	closeErr      error
	err           error
}

// AcquireBackendOwner validates the current schema and obtains the single-backend lock.
func AcquireBackendOwner(ctx context.Context, databaseURL string) (*BackendOwner, error) {
	compatibility, err := CheckCompatibility(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if compatibility != CompatibilityCurrent {
		return nil, errors.New("storage: database schema is incompatible")
	}
	cfg, err := ParseDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["application_name"] = "almanac-backend-owner"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("storage: backend ownership connection failed")
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", backendOwnerNamespace, backendOwnerKey).Scan(&acquired); err != nil {
		closeInitialOwnerConnection(conn)
		return nil, errors.New("storage: backend ownership acquisition failed")
	}
	if !acquired {
		closeInitialOwnerConnection(conn)
		return nil, ErrBackendAlreadyRunning
	}
	owner := &BackendOwner{conn: conn, stop: make(chan struct{}), done: make(chan struct{}), finished: make(chan struct{})}
	go owner.monitor()
	return owner, nil
}

func (o *BackendOwner) Done() <-chan struct{} { return o.done }

func (o *BackendOwner) Err() error {
	select {
	case <-o.done:
		return o.err
	default:
		return nil
	}
}

func (o *BackendOwner) monitor() {
	defer close(o.finished)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-o.stop:
			o.release()
			return
		case <-ticker.C:
			if err := o.probe(time.Second); err != nil {
				o.recordLoss()
				deadline := time.Now().Add(5 * time.Second)
				select {
				case <-o.stop:
					deadline = o.closeDeadline
				default:
				}
				_ = o.closeConnection(deadline)
				o.closeErr = ErrBackendOwnershipLost
				return
			}
		}
	}
}

func (o *BackendOwner) probe(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := o.conn.Ping(ctx)
	cancel()
	return err
}

func (o *BackendOwner) recordLoss() {
	if o.Err() == nil {
		o.err = ErrBackendOwnershipLost
		close(o.done)
	}
}

// release performs final verification and closes the owned connection from the
// monitor goroutine, keeping it the sole owner of connection I/O.
func (o *BackendOwner) release() {
	remaining := time.Until(o.closeDeadline)
	if remaining <= 0 {
		o.recordLoss()
		o.closeErr = ErrBackendOwnershipLost
		_ = o.closeConnection(o.closeDeadline)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), min(time.Second, remaining))
	var unlocked bool
	err := o.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1, $2)", backendOwnerNamespace, backendOwnerKey).Scan(&unlocked)
	cancel()
	if err != nil || !unlocked {
		o.recordLoss()
		o.closeErr = ErrBackendOwnershipLost
		_ = o.closeConnection(o.closeDeadline)
		return
	}
	o.closeErr = o.closeConnection(o.closeDeadline)
}

func (o *BackendOwner) closeConnection(deadline time.Time) error {
	if !deadline.After(time.Now()) {
		if conn := o.conn.PgConn().Conn(); conn != nil {
			_ = conn.Close()
		}
		return errors.New("storage: backend ownership cleanup deadline exceeded")
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	err := o.conn.Close(ctx)
	cancel()
	if err == nil {
		return nil
	}
	if conn := o.conn.PgConn().Conn(); conn != nil {
		_ = conn.Close()
	}
	return errors.New("storage: backend ownership cleanup failed")
}

// Close asks the monitor to verify and release the connection. It is idempotent
// and joins the monitor before returning.
func (o *BackendOwner) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		o.closeDeadline = time.Now().Add(5 * time.Second)
		close(o.stop)
	})
	<-o.finished
	return o.closeErr
}
