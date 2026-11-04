package storage

import (
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCurrentPublicationTraceHasOpaqueJoinAndExactProvenance(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	_, span := provider.Tracer("fixture").Start(t.Context(), "derived.current.evaluate")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recordCurrentPublication(span, "abc", 7, at)
	span.End()
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatal("publication span missing")
	}
	got := make(map[string]string)
	for _, value := range spans[0].Attributes {
		got[string(value.Key)] = value.Value.AsString()
	}
	if len(got) != 3 || got["almanac.publication.key"] != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || got["almanac.source.generation"] != "7" || got["almanac.evaluated.through"] != "2026-01-01T00:00:00Z" {
		t.Fatal("publication join lost exact provenance or exported raw identity")
	}
}
