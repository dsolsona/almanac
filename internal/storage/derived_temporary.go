package storage

import (
	"context"
	"errors"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgconn"
)

// derivedTemporary reports whether background derivation failed for a reason that passes by itself
// (ADR-0034): waiting for another derivation, a serialization conflict or deadlock, a lock or
// statement timeout, or the worker's own time limit while the process is not shutting down. Such work
// is rescheduled within seconds and does not count towards its failures.
func derivedTemporary(ctx context.Context, err error) bool {
	if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
		return true
	}
	if failure, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch failure.Code {
		case "40001", "40P01", "55P03", "57014":
			return true
		}
	}
	return ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

// derivedFailureReason names the class of a failed derivation for its stored reason, which the
// diagnostics view and the SLO's unavailable label show (ADR-0034). Unknown errors keep the queue's
// fallback reason; the full error is in the log line and the trace span.
func derivedFailureReason(err error, fallback string) string {
	switch {
	case errors.Is(err, slo.ErrOperationalFacts):
		return "inconsistent_source_data"
	case errors.Is(err, ErrOperationalResource):
		return "derived_resource_limit"
	case errors.Is(err, ErrOperationalRangeDense):
		return "history_range_too_dense"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	if failure, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch {
		case failure.Code == "54000":
			return "derived_cache_limit"
		case failure.Code == "40001" || failure.Code == "40P01":
			return "database_conflict"
		case failure.Code == "55P03" || failure.Code == "57014":
			return "database_timeout"
		default:
			return "database_error"
		}
	}
	return fallback
}
