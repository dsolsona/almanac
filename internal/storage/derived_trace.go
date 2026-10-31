package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Correlate admission and durable execution using the existing immutable job
// identity. No query values are exported, no extra SQL is issued, and neither
// an HTTP span nor an in-memory trace-context cache must survive the queue wait.
func historyTraceKey(key dbgen.FindPreparedSloHistoryParams) string {
	value := fmt.Appendf(nil, "%x:%d:%d:%d:%d:%d:%d:%d:%s:%s:%d", key.SloID.Bytes, key.DefinitionRevision, key.MeasurementRevision, key.SourceGeneration,
		key.EvaluatedThrough.Time.UnixMicro(), key.RangeStart.Time.UnixMicro(), key.RangeEnd.Time.UnixMicro(), key.BurnWindowSeconds, key.Kind, key.CalculationVersion, key.RetentionStart.Time.UnixMicro())
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func finishDerivedTrace(ctx context.Context, span trace.Span, err error, success string) {
	result := success
	switch {
	case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		result = "cancelled"
		span.SetStatus(codes.Error, "background work cancelled")
	case errors.Is(err, ErrDerivedPending), errors.Is(err, ErrDerivedSourceFailed):
		result = "waiting"
	case err != nil:
		result = "failed"
		span.SetStatus(codes.Error, "background work failed")
	}
	span.SetAttributes(attribute.String("almanac.result", result))
	span.End()
}
