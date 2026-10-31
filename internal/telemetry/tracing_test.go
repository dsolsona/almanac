package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestBrowserParentConnectsHTTPAndDatabaseWithoutSensitiveAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		_ = provider.Shutdown(context.Background())
	})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, query := DatabaseQuery(r.Context(), "-- name: GetSlo :one\nSELECT secret_token FROM protected WHERE password='private'", "interactive")
		DatabaseResult(query, 1, errors.New("private credential and SQL error"))
		defer ResponseEncoding(w)()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/slos/00000000-0000-4000-8000-000000000001/operational?token=private", nil)
	request.Header.Set("Traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	request.Header.Set("Authorization", "Bearer private")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	spans := exporter.GetSpans()
	if len(spans) != 3 || spans[0].Name != "db.GetSlo" || spans[1].Name != "http.response.encode" || spans[2].Name != "GET /api/v1/slos/{id}/operational" {
		t.Fatalf("missing connected request/query spans: %d", len(spans))
	}
	if spans[2].Parent.SpanID().String() != "0123456789abcdef" || spans[0].Parent.SpanID() != spans[2].SpanContext.SpanID() || spans[1].Parent.SpanID() != spans[2].SpanContext.SpanID() || spans[0].SpanContext.TraceID() != spans[2].SpanContext.TraceID() {
		t.Fatal("browser, HTTP and query trace context was not preserved")
	}
	for _, span := range spans {
		for _, attribute := range span.Attributes {
			value := attribute.Value.Emit()
			if strings.Contains(value, "private") || strings.Contains(value, "secret_token") || strings.Contains(value, "000000000001") {
				t.Fatal("trace attributes retained sensitive SQL, URL or headers")
			}
		}
		if len(span.Events) != 0 || strings.Contains(span.Status.Description, "private") {
			t.Fatal("trace error retained sensitive diagnostics")
		}
	}
}

func TestTraceIntakeDoesNotTraceItsAuthenticationOrDatabaseWork(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		_ = provider.Shutdown(context.Background())
	})
	type requestValue struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), requestValue{}, "retained"))
	defer cancel()
	called := false
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Context().Value(requestValue{}) != "retained" {
			t.Fatal("intake lost request context values")
		}
		ctx, span := Tracer().Start(r.Context(), "auth.session.inspect")
		_, query := DatabaseQuery(ctx, "-- name: InspectSession :one\nSELECT 1", "interactive")
		DatabaseResult(query, 1, nil)
		span.End()
		cancel()
		if !errors.Is(r.Context().Err(), context.Canceled) {
			t.Fatal("intake lost request cancellation")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry/traces", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called || recorder.Code != http.StatusAccepted {
		t.Fatal("intake handler was bypassed")
	}
	if spans := exporter.GetSpans(); len(spans) != 0 {
		t.Fatalf("trace intake generated %d spans for exporter traffic", len(spans))
	}
}

func TestProductionSamplerRejectsInvalidRatiosAndPreservesParent(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	for _, value := range []string{"", "NaN", "+Inf", "-0.1", "1.1"} {
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", value)
		if _, err := configuredSampler(); err == nil {
			t.Fatalf("invalid ratio accepted: %q", value)
		}
	}
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
	sampler, err := configuredSampler()
	if err != nil {
		t.Fatal(err)
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true})
	if sampler.ShouldSample(sdktrace.SamplingParameters{ParentContext: trace.ContextWithRemoteSpanContext(t.Context(), parent)}).Decision != sdktrace.RecordAndSample {
		t.Fatal("sampled remote parent was disconnected")
	}
	if sampler.ShouldSample(sdktrace.SamplingParameters{ParentContext: t.Context()}).Decision != sdktrace.Drop {
		t.Fatal("zero root sampling ratio was ignored")
	}
}

func TestBlockedExporterDoesNotBlockSpansAndReportsQueueDrops(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", server.URL+"/v1/traces")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	provider, err := Start(t.Context(), "almanac-backend")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	shutdownCalled := false
	t.Cleanup(func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !shutdownCalled {
			if err := provider.Shutdown(ctx); err != nil {
				t.Error(err)
			}
		}
		otel.SetTracerProvider(noop.NewTracerProvider())
	})
	for range 256 {
		_, span := Tracer().Start(t.Context(), "fixture")
		span.End()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("export did not start")
	}
	for range 3000 {
		_, span := Tracer().Start(t.Context(), "fixture")
		span.End()
	}
	queue := TraceQueueMetrics(t.Context())
	if !queue.Available || queue.Capacity != 2048 || queue.Dropped <= 0 || queue.Size > queue.Capacity {
		t.Fatalf("missing bounded drop evidence: %+v", queue)
	}
	// Keep the endpoint blocked: shutdown must honor its caller's deadline,
	// rather than succeeding only after the fixture releases the exporter.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = provider.Shutdown(ctx)
	shutdownCalled = true
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("blocked tracing shutdown ignored its deadline")
	}
}

func TestTracingConfigurationCanBeDisabledAndRejectsCredentialURLs(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	disabled, err := Start(t.Context(), "almanac-backend")
	if err != nil || disabled.provider != nil {
		t.Fatal("unconfigured tracing did not remain disabled")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://private:secret@example.invalid/v1/traces")
	if _, err := Start(t.Context(), "almanac-backend"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("credential URL was accepted or disclosed")
	}
}

type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("private remote response")
}
func (failingExporter) Shutdown(context.Context) error { return nil }

func TestExporterFailureIsCountedWithoutLeakingRemoteDiagnostics(t *testing.T) {
	before := ExportFailures()
	err := (safeExporter{failingExporter{}}).ExportSpans(t.Context(), nil)
	if err == nil || ExportFailures() != before+1 || strings.Contains(err.Error(), "private") {
		t.Fatal("export failure was lost or disclosed remote diagnostics")
	}
}
