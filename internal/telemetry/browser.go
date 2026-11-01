package telemetry

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const BrowserBodyLimit = 128 << 10

// BrowserValidationError contains a fixed classification, never payload data.
// It lets operators distinguish an SDK-format failure from rejected metadata.
type BrowserValidationError string

func (e BrowserValidationError) Error() string { return "telemetry: invalid browser " + string(e) }

type browserValue struct {
	String  string   `json:"stringValue,omitempty"`
	Double  *float64 `json:"doubleValue,omitempty"`
	Integer any      `json:"intValue,omitempty"`
}
type browserAttribute struct {
	Key   string       `json:"key"`
	Value browserValue `json:"value"`
}
type browserSpan struct {
	TraceID    string             `json:"traceId"`
	SpanID     string             `json:"spanId"`
	ParentID   string             `json:"parentSpanId,omitempty"`
	Name       string             `json:"name"`
	Kind       int                `json:"kind"`
	Start      string             `json:"startTimeUnixNano"`
	End        string             `json:"endTimeUnixNano"`
	Attributes []browserAttribute `json:"attributes"`
	Status     struct {
		Code int `json:"code"`
	} `json:"status"`
}
type browserScope struct {
	Scope struct {
		Name string `json:"name"`
	} `json:"scope"`
	Spans []browserSpan `json:"spans"`
}
type browserResource struct {
	Resource struct {
		Attributes []browserAttribute `json:"attributes"`
	} `json:"resource"`
	Scopes []browserScope `json:"scopeSpans"`
}
type browserRequest struct {
	Resources []browserResource `json:"resourceSpans"`
}

// SanitizeBrowser accepts a bounded OTLP/JSON subset and constructs a new payload.
// Unknown fields (events, links, URL/query, error text, resource labels) cannot be
// relayed accidentally: they have no representation in this output structure.
func SanitizeBrowser(body []byte, now time.Time) ([]byte, error) {
	var request browserRequest
	if len(body) > BrowserBodyLimit || json.Unmarshal(body, &request) != nil || len(request.Resources) != 1 || len(request.Resources[0].Scopes) != 1 {
		return nil, BrowserValidationError("batch")
	}
	resource := &request.Resources[0]
	scope := &resource.Scopes[0]
	if len(scope.Spans) == 0 || len(scope.Spans) > 64 {
		return nil, BrowserValidationError("span_count")
	}
	attributes := []browserAttribute{{Key: "service.name", Value: browserValue{String: "almanac-web"}}}
	for _, attr := range resource.Resource.Attributes {
		if attr.Key == "service.version" && safeBrowserVersion(attr.Value.String) && attr.Value.Double == nil && attr.Value.Integer == nil {
			attributes = append(attributes, attr)
			break
		}
	}
	if provider := activeProvider.Load(); provider != nil {
		for _, attr := range provider.attributes {
			key := string(attr.Key)
			if key == "deployment.environment.name" || key == "almanac.build.sha256" {
				attributes = append(attributes, browserAttribute{Key: key, Value: browserValue{String: attr.Value.AsString()}})
			}
		}
	}
	resource.Resource.Attributes = attributes
	scope.Scope.Name = "almanac-web"
	for index := range scope.Spans {
		span := &scope.Spans[index]
		start, startErr := strconv.ParseInt(span.Start, 10, 64)
		end, endErr := strconv.ParseInt(span.End, 10, 64)
		switch {
		case !validHexID(span.TraceID, 16) || !validHexID(span.SpanID, 8) || (span.ParentID != "" && !validHexID(span.ParentID, 8)):
			return nil, BrowserValidationError("span_id")
		case !safeBrowserSpanName(span.Name):
			return nil, BrowserValidationError("span_name")
		case span.Kind < 1 || span.Kind > 5:
			return nil, BrowserValidationError("span_kind")
		case startErr != nil || endErr != nil || start <= 0 || end < start || end-start > int64(5*time.Minute) || time.Unix(0, end).After(now.Add(time.Minute)) || time.Unix(0, start).Before(now.Add(-24*time.Hour)):
			return nil, BrowserValidationError("span_time")
		case len(span.Attributes) > 16:
			return nil, BrowserValidationError("span_attributes")
		case span.Status.Code < 0 || span.Status.Code > 2:
			return nil, BrowserValidationError("span_status")
		}
		attributes := make([]browserAttribute, 0, len(span.Attributes))
		for _, attr := range span.Attributes {
			if safeBrowserAttribute(attr) {
				attributes = append(attributes, attr)
			}
		}
		span.Attributes = attributes
	}
	return json.Marshal(request)
}

func safeBrowserVersion(value string) bool {
	if len(value) < 1 || len(value) > 80 {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_') {
			return false
		}
	}
	return strings.HasSuffix(value, ".js") || strings.HasSuffix(value, ".ts")
}

func validHexID(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size || strings.ToLower(value) != value {
		return false
	}
	for _, b := range decoded {
		if b != 0 {
			return true
		}
	}
	return false
}

func safeBrowserSpanName(name string) bool {
	switch name {
	case "ui.slo.list", "ui.slo.detail", "ui.sli.list", "ui.sli.detail", "ui.management", "ui.chart.render", "ui.fonts.wait", "ui.json.parse", "http.response.read":
		return true
	}
	if !strings.HasPrefix(name, "http.") {
		return false
	}
	method, route, ok := strings.Cut(strings.TrimPrefix(name, "http."), " ")
	return ok && safeHTTPMethod(method) && safeBrowserRoute(route)
}

func safeHTTPMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	}
	return false
}

func safeBrowserRoute(route string) bool {
	if route == "unmatched" {
		return true
	}
	if len(route) > 160 || !strings.HasPrefix(route, "/api/") {
		return false
	}
	parts := strings.Split(strings.Trim(route, "/"), "/")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		switch part {
		case "api", "v1", "agent", "auth", "context", "login", "logout", "password", "agents", "providers", "datasources", "diagnostics", "slis", "slos", "services", "overview", "operational", "status", "history", "values", "preparation", "retry", "evaluation", "observations", "configuration", "connect", "result", "{id}", "{value}":
		default:
			return false
		}
	}
	return true
}

func safeBrowserAttribute(attr browserAttribute) bool {
	value := attr.Value
	if value.String != "" && (value.Double != nil || value.Integer != nil) {
		return false
	}
	switch attr.Key {
	case "almanac.journey":
		return value.String == "slo.list" || value.String == "slo.detail" || value.String == "sli.list" || value.String == "sli.detail" || value.String == "management"
	case "almanac.action":
		return value.String == "document" || value.String == "navigation" || value.String == "refresh" || value.String == "filter" || value.String == "interaction"
	case "almanac.result":
		return value.String == "ready" || value.String == "cancelled" || value.String == "timeout" || value.String == "error"
	case "almanac.duration_ms":
		number, valid := browserNumber(value)
		return valid && number >= 0 && number <= 300_000
	case "http.request.method":
		return safeHTTPMethod(value.String)
	case "http.route":
		return safeBrowserRoute(value.String)
	case "http.response.status_code":
		number, valid := browserNumber(value)
		return valid && number >= 100 && number <= 599 && math.Trunc(number) == number && value.Double == nil
	}
	return false
}

func browserNumber(value browserValue) (float64, bool) {
	if value.String != "" {
		return 0, false
	}
	var number float64
	if value.Double != nil {
		if value.Integer != nil {
			return 0, false
		}
		number = *value.Double
	} else {
		switch integer := value.Integer.(type) {
		case string:
			parsed, err := strconv.ParseInt(integer, 10, 64)
			if err != nil {
				return 0, false
			}
			number = float64(parsed)
		case float64:
			if math.Trunc(integer) != integer {
				return 0, false
			}
			number = integer
		default:
			return 0, false
		}
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

// BrowserSink is an internal, fixed-destination queue. The authenticated HTTP
// boundary performs authorization and rate limiting before calling it.
func BrowserSink(ctx context.Context, body []byte) error {
	provider := activeProvider.Load()
	if provider == nil || provider.browser == nil {
		return errors.New("telemetry: browser export disabled")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return provider.browser.enqueue(body)
}

func Enabled() bool { return activeProvider.Load() != nil }

// Headers are public capability metadata; no exporter destination or credentials.
func TraceHeaders(w http.ResponseWriter) {
	if Enabled() {
		w.Header().Set("X-Almanac-Tracing", "1")
	} else {
		w.Header().Set("X-Almanac-Tracing", "0")
	}
}
