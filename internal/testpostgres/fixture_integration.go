//go:build integration

package testpostgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const FixtureURL = "postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable"

func Database(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv("ALMANAC_TEST_DATABASE_URL")
	if adminURL != FixtureURL {
		t.Fatal("ALMANAC_TEST_DATABASE_URL must select the pinned local PostgreSQL fixture")
	}
	parsed, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal("test database URL is invalid")
	}
	connectCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	admin, err := connect(connectCtx, adminURL)
	cancel()
	if err != nil {
		t.Fatal("connect to verified local PostgreSQL fixture failed")
	}
	var database, user string
	var version int
	verifyCtx, cancelVerify := context.WithTimeout(t.Context(), 5*time.Second)
	if err := admin.QueryRow(verifyCtx, "SELECT current_database(), current_user, current_setting('server_version_num')::integer").Scan(&database, &user, &version); err != nil {
		cancelVerify()
		closeConn(admin)
		t.Fatal("verify PostgreSQL fixture identity failed")
	}
	cancelVerify()
	if database != "almanac_f1_storage" || user != "almanac" || version != 180006 {
		closeConn(admin)
		t.Fatal("connected server is not the expected PostgreSQL 18.6 fixture")
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		closeConn(admin)
		t.Fatal("create disposable database name failed")
	}
	name := "almanac_test_" + hex.EncodeToString(suffix[:])
	child := parsed.Clone()
	child.Path = "/" + name
	createCtx, cancelCreate := context.WithTimeout(t.Context(), 5*time.Second)
	_, err = admin.Exec(createCtx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	cancelCreate()
	if err != nil {
		closeConn(admin)
		t.Fatal("create disposable database failed")
	}
	t.Cleanup(func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test-owned database %s failed", name)
		}
		_ = admin.Close(cleanup)
	})
	return child.String()
}

func Connect(t *testing.T, databaseURL string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	conn, err := connect(ctx, databaseURL)
	cancel()
	if err != nil {
		t.Fatal("connect to test-owned database failed")
	}
	t.Cleanup(func() { closeConn(conn) })
	return conn
}

func connect(ctx context.Context, databaseURL string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.ConnectTimeout = 5 * time.Second
	config.RuntimeParams["timezone"] = "UTC"
	return pgx.ConnectConfig(ctx, config)
}

func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}
