package telemetry

import (
	"io"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type clientTransport struct {
	next      http.RoundTripper
	operation string
}

// ClientTransport preserves the client's timeout, trust, replay and redirect
// policy. It records no literal URL, credential header, query or response body.
func ClientTransport(next http.RoundTripper, operation string) http.RoundTripper {
	return &clientTransport{next: next, operation: operation}
}

func (t *clientTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, span := Tracer().Start(request.Context(), t.operation, trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(attribute.String("http.request.method", request.Method))
	copy := request.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(copy.Header))
	response, err := t.next.RoundTrip(copy)
	if err != nil {
		span.SetStatus(codes.Error, "HTTP transport failed")
		span.End()
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode >= 500 {
		span.SetStatus(codes.Error, "HTTP dependency failed")
	}
	if response.Body == nil || response.StatusCode == http.StatusSwitchingProtocols {
		span.End()
	} else {
		response.Body = &traceBody{ReadCloser: response.Body, span: span}
	}
	return response, nil
}

func (t *clientTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type traceBody struct {
	io.ReadCloser
	span trace.Span
	once sync.Once
}

func (b *traceBody) Read(data []byte) (int, error) {
	count, err := b.ReadCloser.Read(data)
	if err != nil {
		b.once.Do(func() {
			if err != io.EOF {
				b.span.SetStatus(codes.Error, "HTTP body read failed")
			}
			b.span.End()
		})
	}
	return count, err
}
func (b *traceBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		if err != nil {
			b.span.SetStatus(codes.Error, "HTTP body close failed")
		}
		b.span.End()
	})
	return err
}
