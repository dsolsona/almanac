//go:build integration

package command

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type renderedTrace struct {
	TraceID  string  `json:"trace_id"`
	Journey  string  `json:"journey"`
	Result   string  `json:"result"`
	Duration float64 `json:"duration_ms"`
	Stage    string  `json:"stage"`
}

type traceLookup struct {
	ElapsedMS int64 `json:"elapsed_ms"`
	Status    int   `json:"status"`
	Spans     int   `json:"spans"`
	Decoded   bool  `json:"decoded"`
	Connected bool  `json:"connected"`
}

type journeyTraceDocument struct {
	Data []struct {
		TraceID string `json:"traceID"`
		Spans   []struct {
			TraceID    string `json:"traceID"`
			SpanID     string `json:"spanID"`
			Operation  string `json:"operationName"`
			Duration   int64  `json:"duration"`
			ProcessID  string `json:"processID"`
			References []struct {
				TraceID string `json:"traceID"`
				SpanID  string `json:"spanID"`
			} `json:"references"`
		} `json:"spans"`
		Processes map[string]struct {
			Service string `json:"serviceName"`
		} `json:"processes"`
	} `json:"data"`
}

// Retrieve complete traces from the real local sink. The fixture's UI and
// independently checked counts remain the acceptance path; tracing adds evidence
// to that same journey without introducing another browser matrix.
func verifyRenderedJourneyTraces(t *testing.T, artifacts string, journeys ...string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(artifacts, "journey-traces.json"))
	var events []renderedTrace
	if err != nil || json.Unmarshal(body, &events) != nil {
		t.Fatalf("rendered journey evidence missing: %s", artifacts)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	for _, journey := range journeys {
		var event *renderedTrace
		for index := range events {
			if events[index].Journey == journey && events[index].Result == "ready" {
				event = &events[index]
				break
			}
		}
		if event == nil || len(event.TraceID) != 32 || strings.Trim(event.TraceID, "0123456789abcdef") != "" || event.Duration < 0 {
			t.Fatalf("missing successful rendered %s journey: %s", journey, artifacts)
		}
		// The local sink's measured search.latencyOffset is 30s and its
		// insert.indexFlushInterval is 20s. Jaeger intentionally hides recent
		// spans; this observation window is not a browser performance budget.
		// https://docs.victoriametrics.com/victoriatraces/querying/#search-latency
		started := time.Now()
		deadline := started.Add(40 * time.Second)
		var evidence []byte
		var connected bool
		var lookups []traceLookup
		for time.Now().Before(deadline) {
			lookup := traceLookup{}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:19428/select/jaeger/api/traces/"+event.TraceID, nil)
			if err != nil {
				t.Fatal("construct trace lookup failed")
			}
			response, err := client.Do(request)
			if err == nil {
				lookup.Status = response.StatusCode
				evidence, err = io.ReadAll(io.LimitReader(response.Body, 4<<20))
				closeErr := response.Body.Close()
				var document journeyTraceDocument
				if err == nil && closeErr == nil && response.StatusCode == 200 && json.Unmarshal(evidence, &document) == nil {
					lookup.Decoded = true
					for _, data := range document.Data {
						lookup.Spans += len(data.Spans)
					}
					connected = connectedJourneyTrace(document, *event)
				}
			}
			lookup.ElapsedMS = time.Since(started).Milliseconds()
			lookup.Connected = connected
			lookups = append(lookups, lookup)
			if connected {
				break
			}
			select {
			case <-t.Context().Done():
				t.Fatal("trace capture cancelled")
			case <-time.After(200 * time.Millisecond):
			}
		}
		lookupBody, err := json.Marshal(lookups)
		if err != nil {
			t.Fatal("encode trace lookup diagnostics failed")
		}
		if err := os.WriteFile(filepath.Join(artifacts, "trace-lookups-"+journey+".json"), lookupBody, 0o600); err != nil {
			t.Fatal("save trace lookup diagnostics failed")
		}
		if err := os.WriteFile(filepath.Join(artifacts, "trace-"+journey+".json"), evidence, 0o600); err != nil {
			t.Fatal("save trace evidence failed")
		}
		if !connected {
			t.Fatalf("VictoriaTraces lacks connected browser/HTTP/database/render evidence for %s trace=%s; artifacts=%s", journey, event.TraceID, artifacts)
		}
		t.Logf("rendered journey=%s trace=%s duration_ms=%.3f artifacts=%s", journey, event.TraceID, event.Duration, artifacts)
	}
}

func connectedJourneyTrace(document journeyTraceDocument, event renderedTrace) bool {
	for _, data := range document.Data {
		if data.TraceID != event.TraceID {
			continue
		}
		root := ""
		parents := map[string][]string{}
		backend, database, chart := false, false, event.Journey != "slo.detail"
		for _, span := range data.Spans {
			if span.TraceID != event.TraceID {
				return false
			}
			for _, reference := range span.References {
				if reference.TraceID == event.TraceID {
					parents[span.SpanID] = append(parents[span.SpanID], reference.SpanID)
				}
			}
			service := data.Processes[span.ProcessID].Service
			if service == "almanac-web" && span.Operation == "ui."+event.Journey && float64(span.Duration)/1000 >= event.Duration-2 {
				root = span.SpanID
			}
		}
		if root == "" {
			continue
		}
		var descends func(string, map[string]bool) bool
		descends = func(id string, seen map[string]bool) bool {
			if id == root {
				return true
			}
			if seen[id] {
				return false
			}
			seen[id] = true
			for _, parent := range parents[id] {
				if descends(parent, seen) {
					return true
				}
			}
			return false
		}
		for _, span := range data.Spans {
			if !descends(span.SpanID, map[string]bool{}) {
				continue
			}
			service := data.Processes[span.ProcessID].Service
			backend = backend || service == "almanac-backend" && (strings.HasPrefix(span.Operation, "GET ") || strings.HasPrefix(span.Operation, "POST "))
			database = database || service == "almanac-backend" && strings.HasPrefix(span.Operation, "db.")
			chart = chart || service == "almanac-web" && span.Operation == "ui.chart.render"
		}
		if backend && database && chart {
			return true
		}
	}
	return false
}
