package agenthttp

import (
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
)

func TestAgentSessionGenerationRequiresOneCanonicalPositiveValue(t *testing.T) {
	for _, value := range []string{"1", "9007199254740993", strconv.FormatInt(int64(^uint64(0)>>1), 10)} {
		got, ok := parseAgentSessionGeneration([]string{value})
		want, _ := strconv.ParseInt(value, 10, 64)
		if !ok || got != want {
			t.Errorf("parseAgentSessionGeneration(%q) = %d,%t; want %d,true", value, got, ok, want)
		}
	}
	for _, values := range [][]string{{}, {"0"}, {"01"}, {"+1"}, {"-1"}, {"9223372036854775808"}, {"1", "2"}, {"1 "}} {
		if got, ok := parseAgentSessionGeneration(values); ok {
			t.Errorf("parseAgentSessionGeneration(%q) = %d,true; want false", values, got)
		}
	}
}

type diagnosticCompleterFunc func(context.Context, string, int64, agentprotocol.DiagnosticResult) error

func (f diagnosticCompleterFunc) CompleteDiagnostic(ctx context.Context, token string, generation int64, result agentprotocol.DiagnosticResult) error {
	return f(ctx, token, generation, result)
}

func TestDiagnosticResultHTTPSHandlerAcceptsBoundedLargePayload(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000003"
	result := largeAgentHTTPDiagnosticResult(id)
	body, err := json.Marshal(result)
	if err != nil || len(body) <= agentprotocol.MaxInlineDiagnosticResultBytes || len(body) > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		t.Fatalf("fixture payload size=%d err=%v", len(body), err)
	}
	var calls int
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://example.test", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewDiagnosticResultHandler(diagnosticCompleterFunc(func(_ context.Context, token string, generation int64, actual agentprotocol.DiagnosticResult) error {
		calls++
		if token != "protected-agent-token" || generation != 7 || actual.DiagnosticID != id || actual.State != "succeeded" {
			t.Errorf("completion binding token=%q generation=%d result=%+v", token, generation, actual)
		}
		return nil
	}), auth, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/agent/v1/diagnostics/"+id+"/result", strings.NewReader(string(body)))
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = "192.0.2.15:443"
	request.Header.Set("Authorization", "Bearer protected-agent-token")
	request.Header.Set("X-Agent-Session-Generation", "7")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 || calls != 1 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("large result response status=%d bytes=%d calls=%d", recorder.Code, recorder.Body.Len(), calls)
	}
}

func TestDiagnosticResultHTTPSHandlerRejectsInlineAndOversizedBodiesBeforeCompletion(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000003"
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://example.test", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler, err := NewDiagnosticResultHandler(diagnosticCompleterFunc(func(context.Context, string, int64, agentprotocol.DiagnosticResult) error {
		calls++
		return nil
	}), auth, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "inline", body: `{"diagnostic_id":"` + id + `","state":"succeeded","result_class":"empty","series":[]}`, want: http.StatusBadRequest},
		{name: "oversized", body: strings.Repeat("x", agentprotocol.MaxHTTPSDiagnosticResultBytes+1), want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://example.test/api/agent/v1/diagnostics/"+id+"/result", strings.NewReader(test.body))
			request.TLS = &tls.ConnectionState{}
			request.RemoteAddr = "192.0.2.15:443"
			request.Header.Set("Authorization", "Bearer protected-agent-token")
			request.Header.Set("X-Agent-Session-Generation", "7")
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want || strings.Contains(recorder.Body.String(), "protected-agent-token") || calls != 0 {
				t.Fatalf("response status=%d calls=%d body=%q", recorder.Code, calls, recorder.Body.String())
			}
		})
	}
}

func largeAgentHTTPDiagnosticResult(id string) agentprotocol.DiagnosticResult {
	series := make([]agentprotocol.DiagnosticSeries, 20)
	for index := range series {
		labels := make(map[string]string, 32)
		for label := range 32 {
			labels[fmt.Sprintf("label_%02d", label)] = strings.Repeat("x", 256)
		}
		series[index] = agentprotocol.DiagnosticSeries{Labels: labels, Time: "1700000000.000", Value: "1"}
	}
	return agentprotocol.DiagnosticResult{DiagnosticID: id, State: "succeeded", ResultClass: "complete", Series: series}
}
