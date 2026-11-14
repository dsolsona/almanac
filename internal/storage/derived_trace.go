package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Unlike a publication key, this identity exists before acquisition or calculation
// and remains stable across retries and revisions of the same objective.
func recordCurrentWork(span trace.Span, id pgtype.UUID) {
	if !span.IsRecording() {
		return
	}
	key := sha256.Sum256(append([]byte("almanac.current.work:"), id.Bytes[:]...))
	span.SetAttributes(attribute.String("almanac.current.work.key", hex.EncodeToString(key[:])))
}

func currentPreparationCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// Preserve only trace parentage, not cancellation, deadlines or other values.
	parent := trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	return context.WithTimeout(parent, 2*time.Second)
}

// Correlate admission and durable execution using the existing immutable job
// identity. No query values are exported, no extra SQL is issued, and neither
// an HTTP span nor an in-memory trace-context cache must survive the queue wait.
func historyTraceKey(key dbgen.FindPreparedSloHistoryParams) string {
	value := fmt.Appendf(nil, "%x:%d:%d:%d:%d:%d:%d:%d:%s:%s:%d", key.SloID.Bytes, key.DefinitionRevision, key.MeasurementRevision, key.SourceGeneration,
		key.EvaluatedThrough.Time.UnixMicro(), key.RangeStart.Time.UnixMicro(), key.RangeEnd.Time.UnixMicro(), key.BurnWindowSeconds, key.Kind, key.CalculationVersion, key.RetentionStart.Time.UnixMicro())
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func recordCurrentPublication(span trace.Span, generationID string, source int64, at time.Time) {
	if !span.IsRecording() {
		return
	}
	key := sha256.Sum256([]byte(generationID))
	span.SetAttributes(attribute.String("almanac.publication.key", hex.EncodeToString(key[:])),
		attribute.String("almanac.source.generation", strconv.FormatInt(source, 10)),
		attribute.String("almanac.evaluated.through", at.UTC().Format(time.RFC3339Nano)))
}

// derivedErrorDetail describes a background calculation failure for operators: the SQLSTATE and message of
// a database error, or the error text. Calculation inputs carry no credentials; the text is bounded.
func derivedErrorDetail(err error) string {
	if failure, ok := errors.AsType[*pgconn.PgError](err); ok {
		return "sqlstate " + failure.Code + ": " + failure.Message
	}
	detail := err.Error()
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return detail
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
		// ADR-0034: the span carries the cause, so a failing work item can be diagnosed from its trace.
		result = "failed"
		detail := derivedErrorDetail(err)
		span.RecordError(err, trace.WithAttributes(attribute.String("almanac.error.detail", detail)))
		span.SetStatus(codes.Error, detail)
	}
	span.SetAttributes(attribute.String("almanac.result", result))
	span.End()
}
