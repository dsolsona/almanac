package storage

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestParseDatabaseURLRequiresPostgreSQLAndDatabase(t *testing.T) {
	for _, raw := range []string{
		"mysql://user:secret@localhost/almanac",
		"postgres://user:secret@localhost/",
		"not a URL with secret",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseDatabaseURL(raw); err == nil {
				t.Fatal("ParseDatabaseURL accepted unsupported or incomplete URL")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatalf("database error exposed connection URL: %v", err)
			}
		})
	}
}

func TestOperationErrorClassifiesCallerAndDatabaseTimeouts(t *testing.T) {
	timeout := testTimeoutError{}
	for name, cause := range map[string]error{
		"caller deadline":          context.DeadlineExceeded,
		"server statement timeout": &pgconn.PgError{Code: "57014"},
		"socket timeout":           timeout,
	} {
		t.Run(name, func(t *testing.T) {
			if err := operationError(context.Background(), "storage: operation failed", cause); !errors.Is(err, ErrCancelled) {
				t.Fatalf("operation error = %v, want cancellation/timeout classification", err)
			}
		})
	}
	if err := operationError(context.Background(), "storage: operation failed", errors.New("connection refused")); errors.Is(err, ErrCancelled) {
		t.Fatalf("ordinary connection failure was classified as timeout: %v", err)
	}
}

type testTimeoutError struct{}

var _ net.Error = testTimeoutError{}

func (testTimeoutError) Error() string   { return "network timeout" }
func (testTimeoutError) Timeout() bool   { return true }
func (testTimeoutError) Temporary() bool { return true }

func TestParseDatabaseURLPreservesTLSVerificationAndUsesUTC(t *testing.T) {
	config, err := ParseDatabaseURL("postgres://almanac:secret@localhost/almanac?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify {
		t.Fatal("verify-full connection did not retain certificate verification")
	}
	if config.RuntimeParams["timezone"] != "UTC" {
		t.Fatalf("connection timezone = %q, want UTC", config.RuntimeParams["timezone"])
	}
}
