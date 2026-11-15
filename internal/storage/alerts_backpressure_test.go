package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAlertBackpressureClassification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"wrapped admission deadline", fmt.Errorf("admission: %w", context.DeadlineExceeded), true},
		{"sanitized lock timeout", operationError(context.Background(), "safe", &pgconn.PgError{Code: "55P03"}), true},
		{"sanitized statement timeout", operationError(context.Background(), "safe", &pgconn.PgError{Code: "57014"}), true},
		{"safe read serialization", errReadSerialization, true},
		{"wrapped transaction serialization", fmt.Errorf("transaction: %w", &pgconn.PgError{Code: "40001"}), true},
		{"transaction deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"bounded calculation resource refusal", ErrOperationalResource, true},
		{"terminal source exhaustion", ErrDerivedSourceFailed, false},
		{"inconsistent canonical facts", slo.ErrOperationalFacts, false},
		{"constraint failure", &pgconn.PgError{Code: "23514"}, false},
		{"unclassified error", errors.New("unexpected calculation failure"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := alertBackpressure(test.err); got != test.want {
				t.Fatalf("backpressure=%v want=%v", got, test.want)
			}
		})
	}
}

func TestAlertBackpressurePreservesSanitizedConflicts(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			cause := &pgconn.PgError{Code: code, Message: "private-message", Detail: "private-detail", InternalQuery: "private-query"}
			const message = "storage: exact aggregate window batch read failed"
			err := operationError(t.Context(), message, fmt.Errorf("wrapped private-context: %w", cause))
			if !alertBackpressure(err) {
				t.Errorf("sanitization erased retryable conflict %s", code)
			}
			if _, exposed := errors.AsType[*pgconn.PgError](err); exposed || strings.Contains(err.Error(), "private") || err.Error() != message {
				t.Errorf("sanitization changed safe error contract: %v", err)
			}
		})
	}
}
