package telemetry

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTP traces all server paths without recording literal URLs, headers or body
// data. Unwrap preserves ResponseController's websocket/hijack capability.
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		TraceHeaders(w)
		if strings.HasPrefix(r.URL.Path, "/api/v1/telemetry/") {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), suppressTracing{}, true)))
			return
		}
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := Tracer().Start(ctx, "http.server", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		writer := &responseWriter{ResponseWriter: w, ctx: ctx}
		defer func() {
			route := requestRoute(r)
			method := r.Method
			if !strings.Contains(" GET HEAD POST PUT PATCH DELETE OPTIONS CONNECT TRACE ", " "+method+" ") {
				method = "OTHER"
			}
			span.SetName(method + " " + route)
			span.SetAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route), attribute.Int("http.response.status_code", writer.statusCode()))
			if writer.statusCode() >= 500 {
				span.SetStatus(codes.Error, "server error")
			}
			if r.Context().Err() != nil {
				span.SetStatus(codes.Error, "request cancelled")
			}
		}()
		request := r.WithContext(ctx)
		next.ServeHTTP(writer, request)
		// ServeMux fills Pattern on the request it receives, not on its caller's
		// pre-context copy. Route patterns contain placeholders rather than IDs.
		r.Pattern = request.Pattern
	})
}

// Outer middleware can receive a different request copy from ServeMux. Preserve
// useful bounded route names in that case without retaining path IDs or secrets.
func requestRoute(r *http.Request) string {
	if r.Pattern != "" {
		if _, route, ok := strings.Cut(r.Pattern, " "); ok {
			return route
		}
		return r.Pattern
	}
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	if len(parts) > 8 {
		return "unmatched"
	}
	for index, part := range parts {
		switch part {
		case "api", "v1", "agent", "auth", "context", "login", "logout", "password", "agents", "providers", "datasources", "diagnostics", "slis", "slos", "services", "overview", "operational", "status", "history", "values", "preparation", "retry", "evaluation", "observations", "configuration", "connect", "result", "livez", "readyz":
		default:
			if len(part) != 36 {
				return "unmatched"
			}
			for _, character := range part {
				if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || character == '-') {
					return "unmatched"
				}
			}
			parts[index] = "{id}"
		}
	}
	return "/" + strings.Join(parts, "/")
}

type responseWriter struct {
	http.ResponseWriter
	status int
	ctx    context.Context
}

func (w *responseWriter) TraceContext() context.Context { return w.ctx }

// ResponseEncoding measures JSON encoding and writes inside the request trace.
// Standard Unwrap traversal preserves middleware layering and avoids changing
// handler signatures solely to pass tracing state to response helpers.
func ResponseEncoding(w http.ResponseWriter) func() {
	for range 16 {
		if carrier, ok := w.(interface{ TraceContext() context.Context }); ok {
			_, span := Tracer().Start(carrier.TraceContext(), "http.response.encode")
			return func() { span.End() }
		}
		if wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
			w = wrapper.Unwrap()
		} else {
			break
		}
	}
	return func() {}
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *responseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
