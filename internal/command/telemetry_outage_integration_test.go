//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
)

type traceOutageProgress struct {
	Observations    int64 `json:"new_observations"`
	Invalid         int64 `json:"invalid_values"`
	Gaps            int64 `json:"interval_gaps"`
	Failures        int64 `json:"sink_failures"`
	BackendFailures int64 `json:"backend_export_failures"`
	BrowserFailures int64 `json:"browser_export_failures"`
}

// This fixture forwards only to the existing isolated trace sink. Its private
// control endpoint makes an exporter outage observable without stopping the
// operator's collector, changing credentials, or mocking product requests.
type journeyTraceSink struct {
	server   *httptest.Server
	key      string
	outage   atomic.Bool
	started  atomic.Int64
	failures atomic.Int64
	probeMu  sync.Mutex
	probe    func(context.Context, time.Time) (traceOutageProgress, error)
}

func startJourneyTraceSink(t *testing.T) *journeyTraceSink {
	t.Helper()
	key := make([]byte, 24)
	if _, err := rand.Read(key); err != nil {
		t.Fatal("create isolated trace control key failed")
	}
	sink := &journeyTraceSink{key: hex.EncodeToString(key)}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(client.CloseIdleConnections)
	sink.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			if r.Header.Get("X-Fixture-Control") != sink.key {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/control/outage":
				sink.started.Store(time.Now().UnixNano())
				sink.outage.Store(true)
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodPost && r.URL.Path == "/control/recover":
				sink.outage.Store(false)
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodGet && r.URL.Path == "/control/progress":
				sink.probeMu.Lock()
				defer sink.probeMu.Unlock()
				if sink.probe == nil || !sink.outage.Load() {
					w.WriteHeader(http.StatusConflict)
					return
				}
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				defer cancel()
				progress, err := sink.probe(ctx, time.Unix(0, sink.started.Load()))
				if err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				progress.Failures = sink.failures.Load()
				_ = json.MarshalWrite(w, progress)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if sink.outage.Load() {
			sink.failures.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://127.0.0.1:19428/insert/opentelemetry/v1/traces", bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		if encoding := r.Header.Get("Content-Encoding"); encoding != "" {
			request.Header.Set("Content-Encoding", encoding)
		}
		response, err := client.Do(request)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, 64<<10))
	}))
	t.Cleanup(sink.server.Close)
	return sink
}

func (sink *journeyTraceSink) observeCollection(t *testing.T, databaseURL, sliID, agentID, metricsURL string) {
	t.Helper()
	db := testpostgres.Connect(t, databaseURL)
	sink.probeMu.Lock()
	defer sink.probeMu.Unlock()
	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	sink.probe = func(ctx context.Context, since time.Time) (progress traceOutageProgress, err error) {
		err = db.QueryRow(ctx, `WITH previous AS (
			SELECT max(interval_start) AS start FROM public.almanac_sli_observations
			WHERE sli_id=$1 AND recorded_at < $2
		), samples AS (
			SELECT c.*,(c.interval_start+source.polling_interval_seconds*interval '1 second') AS interval_end,
			lag(c.interval_start+source.polling_interval_seconds*interval '1 second') OVER (ORDER BY c.interval_start) AS prior_end
			FROM public.almanac_sli_observations c
			JOIN public.almanac_sli_measurements measurement ON measurement.sli_id=c.sli_id AND measurement.revision=c.measurement_revision
			JOIN public.almanac_datasources source ON source.id=measurement.datasource_id
			WHERE c.sli_id=$1 AND c.interval_start >= (SELECT start FROM previous)
		) SELECT count(*) FILTER (WHERE recorded_at >= $2), count(*) FILTER (WHERE recorded_at >= $2 AND (
			c.outcome <> 'observed' OR c.agent_id <> $3::uuid OR
			c.good_count IS DISTINCT FROM 99 OR c.total_count IS DISTINCT FROM 100 OR
			c.interval_end-c.interval_start <> interval '15 seconds')),
			count(*) FILTER (WHERE recorded_at >= $2 AND prior_end IS DISTINCT FROM interval_start)
			FROM samples c`, sliID, since, agentID).Scan(&progress.Observations, &progress.Invalid, &progress.Gaps)
		if err != nil {
			return progress, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
		if err != nil {
			return progress, err
		}
		response, err := client.Do(request)
		if err != nil {
			return progress, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != http.StatusOK {
			return progress, io.ErrUnexpectedEOF
		}
		for line := range strings.SplitSeq(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			switch fields[0] {
			case "almanac_trace_export_failures_total":
				progress.BackendFailures, err = strconv.ParseInt(fields[1], 10, 64)
			case "almanac_browser_trace_export_failures_total":
				progress.BrowserFailures, err = strconv.ParseInt(fields[1], 10, 64)
			}
			if err != nil {
				return progress, err
			}
		}
		return progress, nil
	}
}
