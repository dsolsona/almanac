package agentcmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

func TestDiagnosticRunnerExecutesPrometheusPreviewAndDeduplicates(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != "up" {
			t.Errorf("unexpected source request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"api"},"value":[1793102400.125,"1"]}]}}`))
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	agent := &client{agentID: testAgentID, assignments: []agentprotocol.DatasourceAssignment{assignment}}
	runner := newDiagnosticRunner(agent)
	defer runner.close()
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000003", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: time.Now().Add(5 * time.Second).UTC().Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-runner.results:
		if err := result.result.Validate(); err != nil || result.result.State != "succeeded" || result.result.ResultClass != "complete" || len(result.result.Series) != 1 || result.result.Series[0].Labels["job"] != "api" || result.result.Series[0].Value != "1" {
			t.Fatalf("diagnostic result = %+v, validation %v", result, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("diagnostic request did not complete")
	}
	if calls.Load() != 1 {
		t.Fatalf("source calls = %d; duplicate delivery must not execute again", calls.Load())
	}
}

func TestDiagnosticRunnerCancellationStopsProviderRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	runner := newDiagnosticRunner(&client{agentID: testAgentID, assignments: []agentprotocol.DatasourceAssignment{assignment}})
	defer runner.close()
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000004", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: time.Now().Add(10 * time.Second).UTC().Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not start")
	}
	runner.cancel(request.DiagnosticID)
	select {
	case result := <-runner.results:
		if result.result.State != "canceled" || result.result.DiagnosticID != request.DiagnosticID {
			t.Fatalf("cancellation result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled provider request did not finish")
	}
}

func TestDiagnosticRunnerDoesNotExecuteExpiredRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	runner := newDiagnosticRunner(&client{agentID: testAgentID, assignments: []agentprotocol.DatasourceAssignment{assignment}})
	defer runner.close()
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000005", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-runner.results:
		if result.result.State != "timed_out" || result.result.DiagnosticID != request.DiagnosticID {
			t.Fatalf("expired request result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("expired request did not return timed_out")
	}
	if calls.Load() != 0 {
		t.Fatalf("expired request reached provider %d times", calls.Load())
	}
}

func TestDiagnosticRunnerCancelsWhenAssignmentIsRevoked(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	runner := newDiagnosticRunner(&client{agentID: testAgentID, assignments: []agentprotocol.DatasourceAssignment{assignment}})
	defer runner.close()
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000006", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: time.Now().Add(10 * time.Second).UTC().Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not start")
	}
	runner.reconcileAssignments(nil)
	select {
	case result := <-runner.results:
		if result.result.State != "canceled" || result.result.DiagnosticID != request.DiagnosticID {
			t.Fatalf("revoked assignment diagnostic = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked assignment did not cancel provider request")
	}
}

func TestDiagnosticReceiptCacheBoundsTotalResultBytes(t *testing.T) {
	runner := newDiagnosticRunner(&client{})
	defer runner.close()
	for index := range 60 {
		result := largeDiagnosticResult()
		result.DiagnosticID = fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		runner.mu.Lock()
		runner.rememberLocked(result.DiagnosticID, completedDiagnosticResult{result: result, deadlineAt: time.Now().Add(time.Second)}, time.Now())
		runner.mu.Unlock()
	}
	if runner.receiptBytes > maxDiagnosticReceiptBytes || len(runner.completed) >= 60 {
		t.Fatalf("diagnostic cache retained %d bytes across %d receipts", runner.receiptBytes, len(runner.completed))
	}
}

func testDiagnosticAssignment(endpoint string) agentprotocol.DatasourceAssignment {
	return agentprotocol.DatasourceAssignment{DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "1",
		Name: "metrics-east", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: endpoint,
		TLS: agentprotocol.DatasourceTLS{}, AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
}
