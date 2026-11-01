package agentcmd

import (
	"context"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

func writeCollectionHTTPResponse(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("time"))
	if err != nil {
		t.Error(err)
		return
	}
	_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%d,"1"]}]}}`, at.Unix())
}

func newHTTPCollectionRunner(t *testing.T, endpoint string) (*collectionRunner, agentprotocol.ConfigurationSnapshot) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Minute)
	runner := newCollectionRunner(testClient(now, testNonce(91)))
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", Name: "source",
		Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: endpoint,
		AuthenticationMode: "bearer", BearerToken: "first-token", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
	definition := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000091", MeasurementRevision: "1",
		DatasourceID: "source", DatasourceVersion: "1", MeasurementKind: agentprotocol.MeasurementKindEventCounts,
		ComponentMode: agentprotocol.ComponentModeIntervalCount, GoodQuery: "vector(1)", TotalQuery: "vector(1)", EffectiveFrom: now.Add(-5 * time.Minute)}
	snapshot := agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{definition}}
	lease := testLease(now.Add(-time.Minute), "lease", now.Add(time.Hour))
	lease.ConfigurationRevision = "1"
	lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: definition.SLIID, MeasurementRevision: "1"}}
	runner.SetLease(lease)
	runner.SetConfiguration(snapshot)
	t.Cleanup(runner.closeSources)
	return runner, snapshot
}

func prepareHTTPInterval(runner *collectionRunner, workers *sync.WaitGroup) {
	runner.mu.Lock()
	runner.workers = workers
	for _, definition := range runner.slis {
		key := collectionKey(definition)
		runner.nextIntervalEnd[key] = runner.client.currentServerTime().Add(-time.Minute).Truncate(time.Minute)
		runner.initialPending[key] = true
	}
	runner.mu.Unlock()
}

func collectHTTPInterval(t *testing.T, runner *collectionRunner) {
	t.Helper()
	before := len(runner.pending)
	var workers sync.WaitGroup
	prepareHTTPInterval(runner, &workers)
	runner.dispatchDue(t.Context())
	workers.Wait()
	for _, entry := range runner.pending {
		var result struct {
			Outcome struct {
				State string `json:"state"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(entry.raw, &result); err != nil || result.Outcome.State != "observed" {
			t.Fatalf("collection result state = %q, decode error = %v", result.Outcome.State, err)
		}
	}
	if len(runner.pending) != before+len(runner.slis) {
		t.Fatal("collection did not produce an observation for each SLI")
	}
}

func TestSuccessiveCollectionsReuseDatasourceConnection(t *testing.T) {
	var connections, requests atomic.Int32
	closed := make(chan struct{}, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer first-token" {
			t.Error("collection used unexpected credentials")
		}
		writeCollectionHTTPResponse(t, w, r)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			connections.Add(1)
		case http.StateClosed:
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	runner, snapshot := newHTTPCollectionRunner(t, server.URL)
	collectHTTPInterval(t, runner)
	// Reapplying an unchanged snapshot must preserve the datasource's pool.
	runner.SetConfiguration(snapshot)
	collectHTTPInterval(t, runner)
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests = %d, want 4", got)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("two collections (four queries) opened %d connections, want 1", got)
	}
	// Shutdown releases pooled connections after collection workers have drained.
	runner.closeSources()
	awaitHTTPConnectionClosed(t, closed)
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{})
}

func TestCollectionCancellationReachesDatasource(t *testing.T) {
	var block atomic.Bool
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !block.Load() {
			writeCollectionHTTPResponse(t, w, r)
			return
		}
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	runner, _ := newHTTPCollectionRunner(t, server.URL)
	collectHTTPInterval(t, runner)
	block.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var workers sync.WaitGroup
	prepareHTTPInterval(runner, &workers)
	runner.dispatchDue(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not reach datasource")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("collection cancellation did not reach datasource")
	}
	workers.Wait()
}

func awaitHTTPConnectionClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("retired client left an idle connection open")
	}
}

func TestDatasourceRevisionReplacesCredentialsSettingsAndClosesIdleConnections(t *testing.T) {
	var connections atomic.Int32
	var revised atomic.Bool
	closed := make(chan struct{}, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revised.Load() {
			user, password, ok := r.BasicAuth()
			if !ok || user != "new-user" || password != "new-password" || r.URL.Path != "/new/api/v1/query" {
				t.Error("revised collection did not use the new credentials and endpoint path")
			}
		} else if r.Header.Get("Authorization") != "Bearer first-token" || r.URL.Path != "/api/v1/query" {
			t.Error("initial collection used unexpected credentials or endpoint path")
		}
		writeCollectionHTTPResponse(t, w, r)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			connections.Add(1)
		case http.StateClosed:
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	runner, snapshot := newHTTPCollectionRunner(t, server.URL)
	collectHTTPInterval(t, runner)
	snapshot.Assignments[0].DatasourceVersion = "2"
	snapshot.Assignments[0].AuthenticationMode = "basic"
	snapshot.Assignments[0].BearerToken = ""
	snapshot.Assignments[0].Username = "new-user"
	snapshot.Assignments[0].Password = "new-password"
	snapshot.Assignments[0].Endpoint = server.URL + "/new"
	snapshot.SLIs[0].DatasourceVersion = "2"
	revised.Store(true)
	runner.SetConfiguration(snapshot)
	awaitHTTPConnectionClosed(t, closed)
	collectHTTPInterval(t, runner)
	collectHTTPInterval(t, runner)
	if got := connections.Load(); got != 2 {
		t.Fatalf("three collections across two revisions opened %d connections, want 2", got)
	}
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{})
	awaitHTTPConnectionClosed(t, closed)
}

func TestDatasourcesAtSameOriginKeepCredentialsAndConnectionsIsolated(t *testing.T) {
	var connections atomic.Int32
	var first, second atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer first-token":
			first.Add(1)
		case "Bearer second-token":
			second.Add(1)
		default:
			t.Error("unexpected credentials")
		}
		writeCollectionHTTPResponse(t, w, r)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	runner, snapshot := newHTTPCollectionRunner(t, server.URL)
	other := snapshot.Assignments[0]
	other.DatasourceID, other.Name, other.BearerToken = "second-source", "second-source", "second-token"
	definition := snapshot.SLIs[0]
	definition.SLIID, definition.DatasourceID = "00000000-0000-4000-8000-000000000092", "second-source"
	snapshot.Assignments = append(snapshot.Assignments, other)
	snapshot.SLIs = append(snapshot.SLIs, definition)
	lease := runner.lease
	lease.AllowedMeasurements = append(lease.AllowedMeasurements, agentprotocol.MeasurementGrant{SLIID: definition.SLIID, MeasurementRevision: "1"})
	runner.SetLease(lease)
	runner.SetConfiguration(snapshot)
	collectHTTPInterval(t, runner)
	collectHTTPInterval(t, runner)
	if connections.Load() != 2 || first.Load() != 4 || second.Load() != 4 {
		t.Fatalf("connections = %d, credential request counts = %d/%d, want 2 and 4/4", connections.Load(), first.Load(), second.Load())
	}
}

func TestDatasourceTLSRevisionUsesNewVerificationSettings(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCollectionHTTPResponse(t, w, r)
	}))
	defer server.Close()
	runner, snapshot := newHTTPCollectionRunner(t, server.URL)
	snapshot.Assignments[0].TLS.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	runner.SetConfiguration(snapshot)
	collectHTTPInterval(t, runner)
	// Removing the trusted CA must not reuse the previously verified TLS session.
	snapshot.Assignments[0].DatasourceVersion = "2"
	snapshot.Assignments[0].TLS.CAPEM = ""
	snapshot.SLIs[0].DatasourceVersion = "2"
	runner.SetConfiguration(snapshot)
	var workers sync.WaitGroup
	prepareHTTPInterval(runner, &workers)
	runner.dispatchDue(t.Context())
	workers.Wait()
	var failures int
	for _, entry := range runner.pending {
		var result struct {
			Outcome struct {
				State     string `json:"state"`
				ErrorCode string `json:"error_code"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(entry.raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.Outcome.State == "failed" && result.Outcome.ErrorCode == "tls" {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("TLS verification failures = %d, want 1", failures)
	}
}

func TestDatasourceRevisionCancelsActiveCollectionWithUnchangedSLIKey(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	runner, snapshot := newHTTPCollectionRunner(t, server.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var workers sync.WaitGroup
	prepareHTTPInterval(runner, &workers)
	runner.dispatchDue(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	snapshot.Assignments[0].DatasourceVersion = "2"
	snapshot.Assignments[0].BearerToken = "replacement-token"
	snapshot.SLIs[0].DatasourceVersion = "2"
	drainCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := runner.SetConfigurationAndDrain(drainCtx, snapshot); err != nil {
		t.Fatalf("configuration did not drain retired collection: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("configuration change did not cancel the source request")
	}
	workers.Wait()
}

func TestReusedClientHonorsCurrentCollectionLeaseDeadline(t *testing.T) {
	var block atomic.Bool
	started, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !block.Load() {
			writeCollectionHTTPResponse(t, w, r)
			return
		}
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	runner, _ := newHTTPCollectionRunner(t, server.URL)
	collectHTTPInterval(t, runner)
	block.Store(true)
	runner.leaseDeadline = time.Now().Add(200 * time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var workers sync.WaitGroup
	prepareHTTPInterval(runner, &workers)
	runner.dispatchDue(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("reused client ignored the collection's shorter lease deadline")
	}
	workers.Wait()
	if len(runner.pending) != 1 {
		t.Fatal("expired collection submitted an observation")
	}
	// A later lease must work with the same client after that deadline expires.
	block.Store(false)
	runner.leaseDeadline = time.Now().Add(time.Minute)
	collectHTTPInterval(t, runner)
}
