package telemetry

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Captured from the pinned browser SDK, including zero-duration chart spans
// and its choice of intValue for an integral duration. The authenticated relay
// must accept the driver's actual wire format, not only hand-written payloads.
func TestBrowserIntakeAcceptsPinnedSDKWireFormat(t *testing.T) {
	body, err := os.ReadFile("testdata/browser-sdk.json")
	if err != nil {
		t.Fatal(err)
	}
	var batches []jsontext.Value
	if err := json.Unmarshal(body, &batches); err != nil {
		t.Fatal(err)
	}
	for index, batch := range batches {
		if _, err := SanitizeBrowser(batch, time.Unix(1793478007, 0)); err != nil {
			t.Fatalf("SDK batch %d rejected: %v", index, err)
		}
	}
}

func TestBrowserIntakeReconstructsPayloadWithoutSecrets(t *testing.T) {
	now := time.Now()
	span := browserSpan{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", Name: "ui.slo.detail", Kind: 1, Start: "1793456062000000000", End: "1793456062100000000"}
	// Use explicit fixture time: validation cannot silently accept stale spans.
	now = time.Unix(1793456063, 0)
	span.Attributes = []browserAttribute{{Key: "http.route", Value: browserValue{String: "/api/v1/slos/{id}/operational"}}, {Key: "authorization", Value: browserValue{String: "private-session"}}, {Key: "almanac.result", Value: browserValue{String: "ready"}}, {Key: "almanac.duration_ms", Value: browserValue{Integer: "42"}}}
	request := browserRequest{Resources: []browserResource{{Scopes: []browserScope{{Spans: []browserSpan{span}}}}}}
	request.Resources[0].Resource.Attributes = []browserAttribute{{Key: "password", Value: browserValue{String: "private-session"}}}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	safe, err := SanitizeBrowser(body, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(safe), "private-session") || strings.Contains(string(safe), "authorization") || !strings.Contains(string(safe), "/api/v1/slos/{id}/operational") {
		t.Fatal("intake leaked secret data or removed safe attribution")
	}
	if !strings.Contains(string(safe), "almanac.duration_ms") {
		t.Fatal("integral SDK duration was stripped from the trace")
	}
	request.Resources[0].Scopes[0].Spans[0].Name = "http.GET /api/v1/private-session"
	body, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SanitizeBrowser(body, now); err == nil {
		t.Fatal("dynamic span name was accepted")
	}
	if _, err := SanitizeBrowser(safe, now.Add(25*time.Hour)); err == nil {
		t.Fatal("expired browser batch was accepted")
	}
}

func TestBrowserPartialExportRejectionIsCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partialSuccess":{"rejectedSpans":"1","errorMessage":"private remote diagnostic"}}`))
	}))
	defer server.Close()
	exporter := newBrowserExporter(server.URL)
	before := BrowserExportFailures()
	fixture, err := os.ReadFile("testdata/browser-sdk.json")
	if err != nil {
		t.Fatal(err)
	}
	var batches []jsontext.Value
	if err := json.Unmarshal(fixture, &batches); err != nil || len(batches) == 0 {
		t.Fatal("SDK fixture missing")
	}
	body, err := SanitizeBrowser(batches[0], time.Unix(1793478007, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.enqueue(body); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := exporter.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if BrowserExportFailures() != before+1 {
		t.Fatal("collector partial rejection was reported as successful export")
	}
}

func TestBrowserAdmissionIsBoundedAndRejectsAfterClose(t *testing.T) {
	queue := &browserExporter{queue: make(chan []byte, 1)}
	before := BrowserDroppedBatches()
	if err := queue.enqueue([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := queue.enqueue([]byte(`{}`)); err == nil {
		t.Fatal("full queue accepted another batch")
	}
	queue.mu.Lock()
	queue.closed = true
	close(queue.queue)
	queue.mu.Unlock()
	if err := queue.enqueue([]byte(`{}`)); err == nil {
		t.Fatal("closed queue accepted a batch")
	}
	if BrowserDroppedBatches() != before+2 {
		t.Fatal("queue drop accounting is incomplete")
	}
}
