package agentcmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

func TestDiagnosticHTTPSUploadRetries(t *testing.T) {
	var calls atomic.Int32
	var bodyBytes atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent/v1/diagnostics/00000000-0000-4000-8000-000000000003/result" ||
			r.Header.Get("Authorization") != "Bearer test-credential" || r.Header.Get("X-Agent-Session-Generation") != "7" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("upload request metadata was invalid")
		}
		bodyBytes.Store(r.ContentLength)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	result := largeDiagnosticResult()
	body, err := result.CanonicalJSON()
	if err != nil || len(body) <= agentprotocol.MaxInlineDiagnosticResultBytes || len(body) > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		t.Fatalf("large canonical result size = %d, err=%v", len(body), err)
	}
	c := &client{opts: options{backendURL: server.URL}, token: "test-credential", generation: "8", http: server.Client()}
	// The backend clock is deliberately ahead. The local parent deadline still
	// caps the request through context.WithTimeout without mixing clock domains.
	c.setServerTime(time.Now().Add(2 * time.Hour))
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(5*time.Second))
	defer cancel()
	if !c.uploadDiagnostic(ctx, diagnosticUpload{result: result, deadlineAt: c.currentServerTime().Add(4 * time.Second), generation: "7", body: body}) {
		t.Fatal("HTTPS result upload did not succeed after retry")
	}
	if calls.Load() != 2 || bodyBytes.Load() != int64(len(body)) {
		t.Fatalf("upload calls=%d bytes=%d; want 2 calls and %d bytes", calls.Load(), bodyBytes.Load(), len(body))
	}
}

func TestDiagnosticUploadQueueRejectsBodiesOutsideTransferBounds(t *testing.T) {
	c := &client{diagnosticUploads: make(chan diagnosticUpload, 1)}
	result := agentprotocol.DiagnosticResult{DiagnosticID: "00000000-0000-4000-8000-000000000003", State: "succeeded", ResultClass: "complete"}
	tooLarge := make([]byte, agentprotocol.MaxHTTPSDiagnosticResultBytes+1)
	c.setServerTime(time.Now())
	if c.queueDiagnosticUpload(t.Context(), completedDiagnosticResult{result: result, deadlineAt: c.currentServerTime().Add(time.Second), generation: "7"}, tooLarge) {
		t.Fatal("oversized result was queued for transfer")
	}
	inline := make([]byte, agentprotocol.MaxInlineDiagnosticResultBytes)
	if c.queueDiagnosticUpload(t.Context(), completedDiagnosticResult{result: result, deadlineAt: c.currentServerTime().Add(time.Second), generation: "7"}, inline) {
		t.Fatal("inline result was queued for HTTPS transfer")
	}
}

func largeDiagnosticResult() agentprotocol.DiagnosticResult {
	series := make([]agentprotocol.DiagnosticSeries, 20)
	for index := range series {
		labels := make(map[string]string, 32)
		for label := range 32 {
			labels[fmt.Sprintf("label_%02d", label)] = strings.Repeat("x", 256)
		}
		series[index] = agentprotocol.DiagnosticSeries{Labels: labels, Time: "1700000000.000", Value: "1"}
	}
	return agentprotocol.DiagnosticResult{DiagnosticID: "00000000-0000-4000-8000-000000000003", State: "succeeded", ResultClass: "complete", Series: series}
}
