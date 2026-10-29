package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSloEvaluationRetry(t *testing.T) {
	t.Run("serialization succeeds", func(t *testing.T) {
		calls := 0
		result, err := retrySloEvaluation(t.Context(), func() (api.SloEvaluation, error) {
			calls++
			if calls == 1 {
				return api.SloEvaluation{}, &pgconn.PgError{Code: "40001", Detail: "private"}
			}
			return api.SloEvaluation{CalculationVersion: "success"}, nil
		})
		if err != nil || calls != 2 || result.CalculationVersion != "success" {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	})
	t.Run("exhaustion is bounded and sanitized", func(t *testing.T) {
		calls := 0
		_, err := retrySloEvaluation(t.Context(), func() (api.SloEvaluation, error) {
			calls++
			return api.SloEvaluation{}, &pgconn.PgError{Code: "40001", Detail: "private"}
		})
		if calls != sloEvaluationAttempts || !errors.Is(err, errReadSerialization) || err.Error() != errReadSerialization.Error() {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
	t.Run("other error is not retried", func(t *testing.T) {
		calls := 0
		failure := errors.New("failure")
		_, err := retrySloEvaluation(t.Context(), func() (api.SloEvaluation, error) {
			calls++
			return api.SloEvaluation{}, failure
		})
		if calls != 1 || !errors.Is(err, failure) {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
	t.Run("cancellation stops retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		_, err := retrySloEvaluation(ctx, func() (api.SloEvaluation, error) {
			calls++
			cancel()
			return api.SloEvaluation{}, errReadSerialization
		})
		if calls != 1 || !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}
