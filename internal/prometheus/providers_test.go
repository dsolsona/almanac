package prometheus

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestProviderIntervalRequests verifies native paths, interval bounds and timestamp normalization against independent expected requests.
func TestProviderIntervalRequests(t *testing.T) {
	end := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, provider := range []string{"victoriatraces", "loki", "tempo"} {
		t.Run(provider, func(t *testing.T) {
			var query url.Values
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, query = r.URL.Path, r.URL.Query()
				if provider == "tempo" {
					w.Write([]byte(`{"series":[{"value":99}],"metrics":{"totalJobs":1,"completedJobs":1}}`))
					return
				}
				if provider == "loki" {
					w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1794398399.999,"99"]}]}}`))
					return
				}
				w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1794398400,"99"]}]}}`))
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			client, err := NewClient(Config{Endpoint: server.URL + "/gateway", Provider: provider, CadenceSeconds: 15, AllowedDestinations: []string{host.Hostname()}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			result, err := client.QueryAt(t.Context(), "sum(count_over_time({app=\"test\"}[$interval]))", end)
			if err != nil || len(result.Series) != 1 || result.Series[0].Value != "99" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			switch provider {
			case "loki":
				if path != "/gateway/loki/api/v1/query" || query.Get("time") != "2026-11-11T11:59:59.999999999Z" || !strings.Contains(query.Get("query"), "[15s]") {
					t.Fatalf("wrong Loki interval: %s %v", path, query)
				}
				if result.Series[0].Time != "1794398400" {
					t.Fatalf("canonical time=%s", result.Series[0].Time)
				}
			case "victoriatraces":
				if path != "/gateway/select/logsql/stats_query" || query.Get("start") != "2026-11-11T11:59:45Z" || query.Get("end") != "2026-11-11T12:00:00Z" || query.Get("allow_partial_response") != "0" {
					t.Fatalf("wrong native interval: %s %v", path, query)
				}
			case "tempo":
				if path != "/gateway/api/metrics/query" || query.Get("start") != "2026-11-11T11:59:45Z" || query.Get("end") != "2026-11-11T12:00:00Z" || query.Get("q") == "" || query.Has("query") {
					t.Fatalf("wrong Tempo interval: %s %v", path, query)
				}
			}
		})
	}
}

// TestTempoRejectsIncompleteAndInvalidCounts ensures partial jobs and unsafe or malformed numeric values never become observations.
func TestTempoRejectsIncompleteAndInvalidCounts(t *testing.T) {
	for _, body := range []string{
		`{"series":[{"value":99}],"status":"PARTIAL"}`,
		`{"series":[{"value":99}],"metrics":{"totalJobs":2,"completedJobs":1}}`,
		`{"series":[{"value":99}],"message":"query limited"}`,
		`{"series":[{"value":0.5}]}`, `{"series":[{"value":9007199254740992}]}`, `{"series":[{}]}`, `{}`, `{"error":"bad query"}`,
	} {
		result, err := parseTempoInstant([]byte(body), time.Now())
		if err == nil && result.Class != "incomplete" {
			t.Fatalf("accepted %s: %+v", body, result)
		}
	}
	result, err := parseTempoInstant([]byte(`{"series":[{"labels":[{"key":"__name__","value":{"stringValue":"count_over_time"}}],"value":0}]}`), time.Now())
	if err != nil || result.Class != "complete" || result.Series[0].Value != "0" {
		t.Fatalf("explicit zero=%+v %v", result, err)
	}
}

// TestTempoProtobufOmittedZero distinguishes an identified protobuf default zero from a malformed empty series.
func TestTempoProtobufOmittedZero(t *testing.T) {
	result, err := parseTempoInstant([]byte(`{"series":[{"labels":[{"key":"__name__","value":{"stringValue":"count_over_time"}}]}],"metrics":{"completedJobs":1,"totalJobs":1}}`), time.Now())
	if err != nil || len(result.Series) != 1 || result.Series[0].Value != "0" {
		t.Fatalf("protobuf zero=%+v %v", result, err)
	}
}

// TestLokiRejectsStaleEvaluationPoint prevents a past count from being relabelled as the current interval.
func TestLokiRejectsStaleEvaluationPoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1794398384.999,"99"]}]}}`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client, err := NewClient(Config{Endpoint: server.URL, Provider: "loki", CadenceSeconds: 15, AllowedDestinations: []string{endpoint.Hostname()}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if _, err := client.QueryAt(t.Context(), "vector(99)", time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("stale Loki count was relabelled as current")
	}
}

// TestVictoriaTracesRejectsQueryOptionsBeforeIO prevents interval/completeness overrides, including Unicode spellings, from reaching the source.
func TestVictoriaTracesRejectsQueryOptionsBeforeIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("query options reached the source") }))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client, err := NewClient(Config{Endpoint: server.URL, Provider: "victoriatraces", CadenceSeconds: 15, AllowedDestinations: []string{endpoint.Hostname()}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, query := range []string{
		"options(time_offset=1h) * | stats count()",
		"optİons(ignore_global_time_filter=true) * | stats count()",
		"options(ignore_global_time_filter=true) * | stats count()",
		"options(allow_partial_response=true) * | stats count()",
		"x:in(options(time_offset=1h) * | keep x) | stats count()",
		"# \"\noptions(time_offset=1h) * | stats count()",
		"OPTIONS # comment\n (allow_partial_response=true) * | stats count()",
		"options\u2003(time_offset=1h) * | stats count()",
	} {
		if _, err := client.QueryAt(t.Context(), query, time.Now().Truncate(time.Second)); err == nil {
			t.Errorf("accepted %q", query)
		}
	}
	for _, query := range []string{`name:="options(time_offset=1h)" | stats count()`, "name:=`options(ignore_global_time_filter=true)` | stats count()", `name:='options' | stats count()`} {
		if hasLogsQLOptions(query) {
			t.Errorf("rejected quoted search text %q", query)
		}
	}
}
