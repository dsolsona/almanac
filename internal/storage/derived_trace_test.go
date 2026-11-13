package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestCurrentWorkTraceSurvivesWaitingAndFailure(t *testing.T) {
	t.Parallel()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	id := pgtype.UUID{Bytes: [16]byte{1, 2, 3}, Valid: true}
	other := pgtype.UUID{Bytes: [16]byte{4, 5, 6}, Valid: true}
	for index, err := range []error{ErrDerivedPending, errors.New("preparation failed"), nil} {
		ctx, span := provider.Tracer("fixture").Start(t.Context(), "derived.current.evaluate")
		work := id
		if index == 2 {
			work = other
		}
		recordCurrentWork(span, work)
		finishDerivedTrace(ctx, span, err, "published")
	}
	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatal("work spans missing")
	}
	keys := make([]string, 3)
	for index, span := range spans {
		for _, attr := range span.Attributes {
			switch string(attr.Key) {
			case "almanac.current.work.key":
				keys[index] = attr.Value.AsString()
			case "almanac.result":
				if attr.Value.AsString() != []string{"waiting", "failed", "published"}[index] {
					t.Fatal("work result changed")
				}
			default:
				t.Fatal("unexpected work attribute")
			}
		}
		if raw, err := hex.DecodeString(keys[index]); err != nil || len(raw) != 32 {
			t.Fatal("work identity is not an opaque SHA256 key")
		}
	}
	if keys[0] != keys[1] || keys[0] == keys[2] {
		t.Fatal("work identity did not correlate retries or distinguish objectives")
	}
}

func TestCurrentPreparationCleanupTraceRetainsOnlyParentAndOwnDeadline(t *testing.T) {
	t.Parallel()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	type credentialKey struct{}
	ctx, cancelParent := context.WithCancel(context.WithValue(t.Context(), credentialKey{}, "private"))
	ctx, parent := provider.Tracer("fixture").Start(ctx, "derived.current.evaluate")
	defer parent.End()
	cancelParent()
	before := time.Now()
	cleanup, cancel := currentPreparationCleanupContext(ctx)
	defer cancel()
	after := time.Now()
	if cleanup.Err() != nil || cleanup.Value(credentialKey{}) != nil {
		t.Fatal("cleanup inherited cancellation or unrelated values")
	}
	deadline, ok := cleanup.Deadline()
	if !ok || deadline.Before(before.Add(2*time.Second)) || deadline.After(after.Add(2*time.Second)) {
		t.Fatal("cleanup lost its independent two-second deadline")
	}
	if !trace.SpanContextFromContext(cleanup).Equal(parent.SpanContext()) {
		t.Fatal("cleanup lost trace parent")
	}
	_, child := provider.Tracer("fixture").Start(cleanup, "db.DeferCurrentPreparation")
	child.End()
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Parent.SpanID() != parent.SpanContext().SpanID() || spans[0].SpanContext.TraceID() != parent.SpanContext().TraceID() {
		t.Fatal("cleanup query is detached from work trace")
	}
	cancel()
	if !errors.Is(cleanup.Err(), context.Canceled) {
		t.Fatal("cleanup cancellation stopped working")
	}
}

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
