// Package telemetry owns bounded application tracing. Instrumentation records
// fixed operation names, never request bodies, credentials or SQL parameters.
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

var exportFailures atomic.Uint64

// ExportFailures is cumulative process-local evidence of failed export batches.
func ExportFailures() uint64 { return exportFailures.Load() }

type suppressTracing struct{}

var unrecordedTracer = noop.NewTracerProvider().Tracer("github.com/dsolsona/almanac")

// Tracer keeps exporter intake work out of the application's own traces. The
// request still runs normally with its values, deadlines and cancellation intact.
func Tracer() trace.Tracer {
	return scopedTracer{Tracer: otel.Tracer("github.com/dsolsona/almanac")}
}

type scopedTracer struct{ trace.Tracer }

func (t scopedTracer) Start(ctx context.Context, name string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	if suppressed, _ := ctx.Value(suppressTracing{}).(bool); suppressed {
		return unrecordedTracer.Start(ctx, name, options...)
	}
	return t.Tracer.Start(ctx, name, options...)
}

type Provider struct {
	provider   *sdktrace.TracerProvider
	browser    *browserExporter
	meter      *sdkmetric.MeterProvider
	reader     *sdkmetric.ManualReader
	attributes []attribute.KeyValue
}

// Start enables tracing only with an explicit OTLP endpoint. It does not contact
// that endpoint during startup or put export I/O on an application request path.
func Start(ctx context.Context, service string) (*Provider, error) {
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return &Provider{}, nil
	}
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		if base := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); base != "" {
			endpoint = strings.TrimRight(base, "/") + "/v1/traces"
		}
	}
	if endpoint == "" {
		return &Provider{}, nil
	}
	sampler, err := configuredSampler()
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("telemetry: invalid OTLP endpoint")
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithTimeout(2*time.Second), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, errors.New("telemetry: exporter initialization failed")
	}
	attributes := resourceAttributes(service)
	// The pinned SDK's self-observation counts queue-full drops. Use its standard
	// processor rather than replacing its batching/concurrency implementation.
	if err := os.Setenv("OTEL_GO_X_OBSERVABILITY", "true"); err != nil {
		_ = exporter.Shutdown(ctx)
		return nil, errors.New("telemetry: SDK drop accounting unavailable")
	}
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(meter)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attributes...)),
		sdktrace.WithSampler(sampler),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{AttributeValueLengthLimit: 256, AttributeCountLimit: 32, EventCountLimit: 16, LinkCountLimit: 32, AttributePerEventCountLimit: 8, AttributePerLinkCountLimit: 8}),
		sdktrace.WithBatcher(safeExporter{exporter}, sdktrace.WithMaxQueueSize(2048), sdktrace.WithMaxExportBatchSize(256), sdktrace.WithBatchTimeout(250*time.Millisecond), sdktrace.WithExportTimeout(2*time.Second)),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	owned := &Provider{provider: provider, browser: newBrowserExporter(endpoint), meter: meter, reader: reader, attributes: attributes}
	activeProvider.Store(owned)
	return owned, nil
}

// Local acceptance defaults to complete traces. Production can choose a root
// ratio while preserving every incoming parent's sampling decision.
func configuredSampler() (sdktrace.Sampler, error) {
	switch os.Getenv("OTEL_TRACES_SAMPLER") {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "parentbased_traceidratio":
		ratio, err := strconv.ParseFloat(os.Getenv("OTEL_TRACES_SAMPLER_ARG"), 64)
		if err != nil || !(ratio >= 0 && ratio <= 1) {
			return nil, errors.New("telemetry: sampling ratio must be between zero and one")
		}
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio)), nil
	default:
		return nil, errors.New("telemetry: unsupported sampler")
	}
}

func resourceAttributes(service string) []attribute.KeyValue {
	attributes := []attribute.KeyValue{attribute.String("service.name", service)}
	if environment := os.Getenv("ALMANAC_ENVIRONMENT"); environment != "" && len(environment) <= 64 {
		attributes = append(attributes, attribute.String("deployment.environment.name", environment))
	}
	version := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				version = setting.Value
			}
			if setting.Key == "vcs.modified" {
				attributes = append(attributes, attribute.Bool("almanac.build.modified", setting.Value == "true"))
			}
		}
	}
	// Container builds may omit .git. Identify the actual executable rather than
	// inventing a source revision or trusting a caller-supplied revision label.
	if path, err := os.Executable(); err == nil {
		if file, err := os.Open(path); err == nil {
			hash := sha256.New()
			_, readErr := io.Copy(hash, file)
			closeErr := file.Close()
			if readErr == nil && closeErr == nil {
				digest := hex.EncodeToString(hash.Sum(nil))
				attributes = append(attributes, attribute.String("almanac.build.sha256", digest))
				if version == "" {
					version = "sha256:" + digest
				}
			}
		}
	}
	if version != "" {
		attributes = append(attributes, attribute.String("service.version", version))
	}
	return attributes
}

func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.provider == nil {
		return nil
	}
	activeProvider.CompareAndSwap(p, nil)
	var browserErr error
	if p.browser != nil {
		browserErr = p.browser.shutdown(ctx)
	}
	traceErr := p.provider.Shutdown(ctx)
	var meterErr error
	if p.meter != nil {
		meterErr = p.meter.Shutdown(ctx)
	}
	return errors.Join(browserErr, traceErr, meterErr)
}

// The SDK may log exporter errors. Do not let remote response bodies, endpoint
// headers or transport diagnostics become application logs through that path.
type safeExporter struct{ sdktrace.SpanExporter }

func (s safeExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if err := s.SpanExporter.ExportSpans(ctx, spans); err != nil {
		exportFailures.Add(1)
		return errors.New("telemetry: trace export failed")
	}
	return nil
}
