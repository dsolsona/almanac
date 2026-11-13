//go:build integration

package storage

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestIntegrationQueryTraceBackendPID(t *testing.T) {
	t.Parallel()
	conn := integrationConnect(t, integrationDatabase(t))
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	var expected int64
	if err := conn.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&expected); err != nil {
		t.Fatal(err)
	}
	_, span := provider.Tracer("fixture").Start(t.Context(), "db.fixture")
	recordQueryConnection(span, conn)
	span.End()
	spans := exporter.GetSpans()
	if len(spans) != 1 || len(spans[0].Attributes) != 1 || string(spans[0].Attributes[0].Key) != "db.postgresql.backend_pid" || spans[0].Attributes[0].Value.AsInt64() != expected || expected <= 0 {
		t.Fatal("query trace cannot correlate to PostgreSQL backend")
	}
}
