package telemetry

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// DatabaseQuery records generated statement names only. Values and arbitrary SQL
// text never become attributes, span names, events or error descriptions.
func DatabaseQuery(ctx context.Context, sql, class string) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient))
	if span.IsRecording() {
		name := queryName(sql)
		span.SetName("db." + name)
		span.SetAttributes(attribute.String("db.system.name", "postgresql"), attribute.String("db.query.summary", name), attribute.String("almanac.traffic.class", class))
	}
	return ctx, span
}

func queryName(sql string) string {
	line, _, _ := strings.Cut(sql, "\n")
	if rest, ok := strings.CutPrefix(line, "-- name: "); ok {
		name, _, _ := strings.Cut(rest, " ")
		if len(name) > 0 && len(name) <= 80 {
			valid := true
			for _, character := range name {
				if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_') {
					valid = false
				}
			}
			if valid {
				return name
			}
		}
	}
	return "statement"
}

func DatabaseResult(span trace.Span, rows int64, err error) {
	span.SetAttributes(attribute.Int64("db.response.returned_rows", rows))
	if err != nil {
		span.SetStatus(codes.Error, "database operation failed")
	}
	span.End()
}
