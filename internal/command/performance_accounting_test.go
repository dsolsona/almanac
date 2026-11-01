package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPerformanceNearestRank(t *testing.T) {
	values := make([]time.Duration, 100)
	for index := range values {
		values[index] = time.Duration(100-index) * time.Millisecond
	}
	before := slices.Clone(values)
	got := performancePercentiles(values)
	if got.Count != 100 || got.P50 != 50*time.Millisecond || got.P95 != 95*time.Millisecond || got.P99 != 99*time.Millisecond || got.Max != 100*time.Millisecond || !slices.Equal(values, before) {
		t.Fatalf("incorrect nearest-rank distribution or mutated source: %+v", got)
	}
	if performancePercentiles(nil).Count != 0 {
		t.Fatal("empty distribution acquired samples")
	}
}

func TestPerformanceDiskAdmissionUsesMeasuredSizeAndKeepsReserve(t *testing.T) {
	required, err := performanceDiskRequired(8064000000, 8064000, 57600000-8064000)
	if err != nil || required != performanceDiskReserve+2*1000*(57600000-8064000) {
		t.Fatalf("incorrect measured fixture disk admission: %d %v", required, err)
	}
	if _, err := performanceDiskRequired(0, 100, 1); err == nil {
		t.Fatal("missing measured fixture cost was admitted")
	}
	if _, err := performanceDiskRequired(1<<62, 1, 1<<62); err == nil {
		t.Fatal("overflowing fixture disk estimate was admitted")
	}
}

func TestPerformanceReadyReadAccounting(t *testing.T) {
	var perActor [performanceUsers]atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := strconv.Atoi(r.Header.Get("X-Fixture-Actor"))
		if err != nil || actor < 0 || actor >= len(perActor) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		perActor[actor].Add(1)
		if r.URL.Path == "/pending" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write([]byte(`{"generation":"fixture"}`))
	}))
	defer server.Close()
	readers := make([]performanceReader, performanceUsers)
	for actor := range readers {
		readers[actor] = performanceReader{Identity: fmt.Sprintf("fixture-%d", actor), Client: server.Client(), Request: func(ctx context.Context, path string) (*http.Request, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
			if err == nil {
				request.Header.Set("X-Fixture-Actor", strconv.Itoa(actor))
			}
			return request, err
		}}
	}
	operation := performanceOperation{Name: "ready", Paths: []string{"/ready"}, Budget: time.Second, Check: func(_ int, body []byte) error {
		if string(body) != `{"generation":"fixture"}` {
			return errors.New("incorrect generation")
		}
		return nil
	}}
	report, err := samplePerformanceReads(t.Context(), readers, operation)
	if err != nil || report.Failures != 0 || report.Latency.Count != 100 || report.Wall <= 0 || report.Invocation <= 0 || report.Ended.Before(report.Started) {
		t.Fatalf("ready accounting failed: report=%+v err=%v", report, err)
	}
	for actor := range readers {
		if perActor[actor].Load() != 10 {
			t.Fatalf("actor %d did not issue exactly ten reads", actor)
		}
	}
	operation.Paths = []string{"/pending", "/ready"}
	report, err = samplePerformanceReads(t.Context(), readers, operation)
	if err == nil || report.Failures != 50 || report.Latency.Count != 100 {
		t.Fatalf("pending responses were retried or hidden: failures=%d count=%d err=%v", report.Failures, report.Latency.Count, err)
	}
	operation.Paths = []string{"/ready"}
	operation.Check = func(int, []byte) error { return errors.New("malformed provenance") }
	report, err = samplePerformanceReads(t.Context(), readers, operation)
	if err == nil || report.Failures != 100 || report.Samples[0].Failure != "response_contract" {
		t.Fatal("successful status hid invalid response data")
	}
	readers[1].Identity = readers[0].Identity
	if _, err := samplePerformanceReads(t.Context(), readers, operation); err == nil {
		t.Fatal("duplicate users accepted as ten-user concurrency")
	}
}

func TestPerformanceIngestionDoesNotInterpolateAwayAnIsolationFailure(t *testing.T) {
	baseline := performancePercentileBounds{Count: 100, Lower: .010, Upper: .011}
	loaded := performancePercentileBounds{Count: 100, Lower: .011, Upper: .012}
	if err := checkPerformanceIngestionIsolation(baseline, loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Upper = .013
	if checkPerformanceIngestionIsolation(baseline, loaded) == nil {
		t.Fatal("overlapping approximate percentiles manufactured a passing ratio")
	}
	baseline.Lower = .300
	loaded.Upper = .251
	if checkPerformanceIngestionIsolation(baseline, loaded) == nil {
		t.Fatal("absolute accepted-ingestion budget was ignored")
	}
}

func TestPerformanceIngestionRequiresCompleteMonotonicPhaseEvidence(t *testing.T) {
	const metrics = `almanac_ingestion_batch_duration_seconds_count{result="accepted"} 100
almanac_ingestion_batch_duration_seconds_count{result="unsuccessful"} 0
almanac_ingestion_batch_duration_seconds_bucket{result="accepted",le="0.010"} 94
almanac_ingestion_batch_duration_seconds_bucket{result="accepted",le="0.011"} 95
almanac_ingestion_batch_duration_seconds_bucket{result="accepted",le="0.012"} 100
almanac_ingestion_batch_duration_seconds_bucket{result="accepted",le="+Inf"} 100
`
	after, err := parsePerformanceIngestionMetrics(metrics)
	if err != nil {
		t.Fatal(err)
	}
	before := performanceIngestionHistogram{Bounds: slices.Clone(after.Bounds), Cumulative: make([]uint64, len(after.Cumulative))}
	bounds, err := performanceIngestionDelta(before, after)
	if err != nil || bounds.Count != 100 || bounds.Lower != .010 || bounds.Upper != .011 {
		t.Fatalf("wrong nearest-rank bounds: %+v %v", bounds, err)
	}
	after.Unsuccessful = 1
	if _, err := performanceIngestionDelta(before, after); err == nil {
		t.Fatal("unsuccessful ingestion was excluded from acceptance")
	}
	after.Unsuccessful = 0
	after.Count = 99
	if _, err := performanceIngestionDelta(before, after); err == nil {
		t.Fatal("undersampled ingestion phase was accepted")
	}
	for _, invalid := range []string{
		strings.Replace(metrics, "le=\"0.010\"} 94", "le=\"0.010\"} 96", 1),
		strings.Replace(metrics, "le=\"+Inf\"} 100", "le=\"+Inf\"} 99", 1),
		strings.Replace(metrics, `almanac_ingestion_batch_duration_seconds_bucket{result="accepted",le="+Inf"} 100`+"\n", "", 1),
		metrics + `almanac_ingestion_batch_duration_seconds_count{result="accepted"} 100` + "\n",
	} {
		if _, err := parsePerformanceIngestionMetrics(invalid); err == nil {
			t.Fatal("incomplete or inconsistent histogram was accepted")
		}
	}
}
