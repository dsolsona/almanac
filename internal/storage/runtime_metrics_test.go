package storage

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestMetricsScrapesNeedNoDatabaseAndDoNotInventLag(t *testing.T) {
	runtime := &Runtime{}
	handler := runtime.MetricsHandler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "almanac_derived_metrics_ready 0\n") {
		t.Fatalf("metrics unavailable before initial sample: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "almanac_derived_current_lag_seconds") {
		t.Fatal("unsampled lag was represented as a known value")
	}
	// The schema gate is closed and every pool is nil. Serving a known sample
	// must still work; an outage cannot turn every scrape into database I/O.
	runtime.metrics.diagnostics.Store(&derivedMetricsSnapshot{At: time.Unix(100, 0), ReadDerivedMetricsRow: dbgen.ReadDerivedMetricsRow{CurrentLagSeconds: 9, PendingCurrent: 3, CacheBytes: 42, CanonicalBytes: 1000, DerivedBytes: 200}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, expected := range []string{"almanac_derived_metrics_ready 1\n", "almanac_derived_current_lag_seconds 9\n", "almanac_derived_pending_current 3\n", "almanac_storage_relation_bytes{kind=\"canonical\"} 1000\n"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("missing known sample %q", expected)
		}
	}
	for _, path := range []string{"/api/v1/slos", "/debug/pprof/", "/healthz"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("internal metrics listener exposed %s", path)
		}
	}
}

func TestQueryMetricsCountFailuresWithoutKeepingSensitiveInput(t *testing.T) {
	runtime := &Runtime{}
	query := &runtime.metrics.queries[InteractiveTraffic]
	query.observe(5*time.Millisecond, nil)
	query.observe(2*time.Second, errors.New("secret query and password"))
	tracer := runtimeQueryTracer{metrics: query}
	ctx := tracer.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT secret", Args: []any{"password"}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	response := httptest.NewRecorder()
	runtime.MetricsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{`almanac_db_query_duration_seconds_count{class="interactive"} 3`, `almanac_db_query_errors_total{class="interactive"} 1`, `almanac_db_query_duration_seconds_bucket{class="interactive",le="5"} 3`} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing counter %q", expected)
		}
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "password") || strings.Contains(body, "SELECT") {
		t.Fatal("query metrics disclosed sensitive input")
	}
}

func TestIngestionMetricsSeparateAcceptedDuplicateAndUnsuccessfulBatches(t *testing.T) {
	runtime := &Runtime{}
	runtime.ObserveIngestionBatch(100*time.Millisecond, true, false)
	runtime.ObserveIngestionBatch(200*time.Millisecond, false, true)
	runtime.ObserveIngestionBatch(300*time.Millisecond, false, false)
	for index, expected := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond} {
		snapshot := runtime.metrics.ingestion[index].snapshot()
		if snapshot.count != 1 || snapshot.nanoseconds != uint64(expected) {
			t.Fatalf("ingestion result class %d mixed different outcomes: %+v", index, snapshot)
		}
		previous := uint64(0)
		for index, count := range snapshot.buckets {
			if count < previous || count > snapshot.count || (ingestionDurationBounds[index] < expected && count != 0) || (ingestionDurationBounds[index] >= expected && count != 1) {
				t.Fatal("ingestion histogram lost a duration or produced inconsistent percentile bounds")
			}
			previous = count
		}
	}
	response := httptest.NewRecorder()
	runtime.MetricsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, result := range []string{"accepted", "duplicate", "unsuccessful"} {
		if !strings.Contains(response.Body.String(), `almanac_ingestion_batch_duration_seconds_count{result="`+result+`"} 1`) {
			t.Fatalf("missing separate ingestion counter for %s", result)
		}
	}
	var absent *Runtime
	absent.ObserveIngestionBatch(time.Millisecond, true, false)
}
