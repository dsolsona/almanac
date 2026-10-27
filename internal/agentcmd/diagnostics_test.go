package agentcmd

import (
	"errors"
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
	agent := testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment})
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
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case replay := <-runner.results:
		if replay.result.DiagnosticID != request.DiagnosticID || replay.result.State != "succeeded" {
			t.Fatalf("cached diagnostic replay = %+v", replay)
		}
	case <-time.After(time.Second):
		t.Fatal("cached duplicate result was not replayed")
	}
	if calls.Load() != 1 {
		t.Fatalf("source calls = %d; duplicate delivery must not execute again", calls.Load())
	}
}

func TestDiagnosticRunnerDoesNotReexecuteDuplicateAfterResultBodyEviction(t *testing.T) {
	var calls atomic.Int32
	secondCall := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 2 {
			secondCall <- struct{}{}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"api"},"value":[1793102400.125,"1"]}]}}`))
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	runner := newDiagnosticRunner(testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment}))
	defer runner.close()
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000013", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: time.Now().Add(10 * time.Second).UTC().Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.results:
	case <-time.After(2 * time.Second):
		t.Fatal("first diagnostic did not complete")
	}
	runner.mu.Lock()
	for index := range maxDiagnosticReceipts {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+100)
		result := agentprotocol.DiagnosticResult{DiagnosticID: id, State: "succeeded", ResultClass: "empty", Series: []agentprotocol.DiagnosticSeries{}}
		runner.commandIDs[id] = request.DeadlineAt
		runner.commandOrder = append(runner.commandOrder, id)
		runner.rememberLocked(id, completedDiagnosticResult{result: result, deadlineAt: request.DeadlineAt}, time.Now())
	}
	_, cached := runner.completed[request.DiagnosticID]
	_, tracked := runner.commandIDs[request.DiagnosticID]
	runner.mu.Unlock()
	if cached || !tracked {
		t.Fatalf("result cache eviction removed command tombstone: cached=%t tracked=%t", cached, tracked)
	}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatalf("duplicate diagnostic delivery returned error: %v", err)
	}
	select {
	case <-secondCall:
		t.Fatal("uncached duplicate reexecuted the provider query")
	case <-time.After(100 * time.Millisecond):
	}
	if calls.Load() != 1 || len(runner.results) != 0 {
		t.Fatalf("duplicate reexecuted or emitted uncached result: provider calls=%d queued results=%d", calls.Load(), len(runner.results))
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
	runner := newDiagnosticRunner(testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment}))
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
	runner := newDiagnosticRunner(testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment}))
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

func TestDiagnosticRunnerUsesBackendTimeEstimateWhenLocalClockIsSkewed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	agent := testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment})
	backendNow := time.Now().Add(-2 * time.Hour).UTC()
	agent.setServerTime(backendNow)
	runner := newDiagnosticRunner(agent)
	defer runner.close()
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000019", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: backendNow.Add(5 * time.Second).Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatalf("backend-valid deadline rejected with local clock skew: %v", err)
	}
	select {
	case result := <-runner.results:
		if result.result.State != "succeeded" || result.generation != "7" {
			t.Fatalf("diagnostic result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend-deadline diagnostic did not complete")
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
	runner := newDiagnosticRunner(testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment}))
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
		runner.commandIDs[result.DiagnosticID] = time.Now().Add(time.Minute)
		runner.commandOrder = append(runner.commandOrder, result.DiagnosticID)
		runner.rememberLocked(result.DiagnosticID, completedDiagnosticResult{result: result, deadlineAt: time.Now().Add(time.Second)}, time.Now())
		runner.mu.Unlock()
	}
	if runner.receiptBytes > maxDiagnosticReceiptBytes || len(runner.completed) >= 60 {
		t.Fatalf("diagnostic cache retained %d bytes across %d receipts", runner.receiptBytes, len(runner.completed))
	}
}

func TestDiagnosticRunnerRejectsNewIDsAtCapacityAndReclaimsExpiredTombstones(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()
	assignment := testDiagnosticAssignment(server.URL)
	runner := newDiagnosticRunner(testDiagnosticClient(testAgentID, []agentprotocol.DatasourceAssignment{assignment}))
	defer runner.close()
	now := time.Now()
	runner.mu.Lock()
	for index := range maxDiagnosticCommandIDs {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		deadline := now.Add(10 * time.Second)
		runner.commandIDs[id] = deadline
		runner.commandOrder = append(runner.commandOrder, id)
	}
	runner.mu.Unlock()

	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-999999999999", AgentID: testAgentID,
		Operation: "query_preview", DeadlineAt: now.Add(5 * time.Second).UTC().Truncate(time.Microsecond), Datasource: assignment, Query: "up"}
	if err := runner.start(t.Context(), request); !errors.Is(err, errDiagnosticCapacity) {
		t.Fatalf("new diagnostic at full ID capacity error = %v", err)
	}
	runner.mu.Lock()
	if _, retained := runner.commandIDs["00000000-0000-4000-8000-000000000001"]; !retained || len(runner.commandIDs) != maxDiagnosticCommandIDs {
		runner.mu.Unlock()
		t.Fatalf("capacity rejection evicted a valid tombstone: %d IDs", len(runner.commandIDs))
	}
	runner.commandIDs["00000000-0000-4000-8000-000000000001"] = now.Add(-time.Second)
	runner.mu.Unlock()
	if err := runner.start(t.Context(), request); err != nil {
		t.Fatalf("diagnostic after tombstone expiry was rejected: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted diagnostic did not reach provider")
	}
	runner.mu.Lock()
	_, retained := runner.commandIDs[request.DiagnosticID]
	count := len(runner.commandIDs)
	runner.mu.Unlock()
	if !retained || count != maxDiagnosticCommandIDs {
		t.Fatalf("admitted command tombstone retained=%t count=%d", retained, count)
	}
}

func TestDiagnosticCapacityFailureUsesSafeRateLimitedResult(t *testing.T) {
	request := agentprotocol.DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000099", DeadlineAt: time.Now().Add(time.Second)}
	completed := diagnosticCapacityResult(request)
	if completed.result.ErrorCode != "rate_limited" || completed.result.Validate() != nil || !completed.deadlineAt.Equal(request.DeadlineAt) {
		t.Fatalf("capacity response = %+v", completed)
	}
}

func testDiagnosticAssignment(endpoint string) agentprotocol.DatasourceAssignment {
	return agentprotocol.DatasourceAssignment{DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "1",
		Name: "metrics-east", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: endpoint,
		TLS: agentprotocol.DatasourceTLS{}, AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
}

func testDiagnosticClient(agentID string, assignments []agentprotocol.DatasourceAssignment) *client {
	now := time.Now().UTC()
	agent := &client{agentID: agentID, assignments: assignments, generation: "7"}
	agent.setServerTime(now)
	return agent
}
