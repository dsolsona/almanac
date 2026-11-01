package command

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"
	"strings"
	"testing"
	"time"
)

type journeyTraceTag struct {
	Key   string         `json:"key"`
	Value jsontext.Value `json:"value"`
}
type journeyTraceReference struct {
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}
type journeyTraceSpan struct {
	TraceID    string                  `json:"traceID"`
	SpanID     string                  `json:"spanID"`
	Operation  string                  `json:"operationName"`
	Duration   int64                   `json:"duration"`
	Start      int64                   `json:"startTime"`
	ProcessID  string                  `json:"processID"`
	References []journeyTraceReference `json:"references"`
	Tags       []journeyTraceTag       `json:"tags"`
}
type journeyTraceProcess struct {
	Service string            `json:"serviceName"`
	Tags    []journeyTraceTag `json:"tags"`
}
type journeyTraceData struct {
	TraceID   string                         `json:"traceID"`
	Spans     []journeyTraceSpan             `json:"spans"`
	Processes map[string]journeyTraceProcess `json:"processes"`
}
type journeyTraceDocument struct {
	Data []journeyTraceData `json:"data"`
}

type performanceWorkTraceWant struct {
	Kind, Key, Source, Through, BackendSHA, AgentSHA string
	NotBefore                                        time.Time
}

func traceTagValue(tags []journeyTraceTag, key string) string {
	for _, tag := range tags {
		if tag.Key == key {
			var text string
			if json.Unmarshal(tag.Value, &text) == nil {
				return text
			}
			var number int64
			if json.Unmarshal(tag.Value, &number) == nil {
				return strconv.FormatInt(number, 10)
			}
			var flag bool
			if json.Unmarshal(tag.Value, &flag) == nil {
				return strconv.FormatBool(flag)
			}
		}
	}
	return ""
}

func connectedPerformanceWork(document journeyTraceDocument, want performanceWorkTraceWant) (string, int64, bool) {
	for _, data := range document.Data {
		spans := make(map[string]journeyTraceSpan, len(data.Spans))
		for _, span := range data.Spans {
			spans[span.SpanID] = span
		}
		backend := func(span journeyTraceSpan) bool {
			p := data.Processes[span.ProcessID]
			return p.Service == "almanac-backend" && traceTagValue(p.Tags, "almanac.build.sha256") == want.BackendSHA && traceTagValue(p.Tags, "deployment.environment.name") == "acceptance"
		}
		var descends func(string, string, map[string]bool) bool
		descends = func(id, ancestor string, seen map[string]bool) bool {
			if id == ancestor {
				return true
			}
			if seen[id] {
				return false
			}
			seen[id] = true
			for _, parent := range spans[id].References {
				if parent.TraceID == data.TraceID && descends(parent.SpanID, ancestor, seen) {
					return true
				}
			}
			return false
		}
		for _, root := range data.Spans {
			if !backend(root) || root.TraceID != data.TraceID || root.Start < want.NotBefore.UnixMicro() || root.Duration <= 0 || traceTagValue(root.Tags, "error") == "true" {
				continue
			}
			match := false
			switch want.Kind {
			case "publication":
				match = root.Operation == "derived.current.evaluate" && traceTagValue(root.Tags, "almanac.result") == "published" && traceTagValue(root.Tags, "almanac.publication.key") == want.Key && traceTagValue(root.Tags, "almanac.source.generation") == want.Source && traceTagValue(root.Tags, "almanac.evaluated.through") == want.Through
			case "source":
				n, _ := strconv.Atoi(traceTagValue(root.Tags, "almanac.batch.count"))
				match = root.Operation == "derived.source.batch" && traceTagValue(root.Tags, "almanac.result") == "published" && n > 0
			case "ingestion":
				match = root.Operation == "POST /api/agent/v1/observations" && traceTagValue(root.Tags, "http.response.status_code") == "200"
			}
			if !match {
				continue
			}
			operations := make(map[string]bool)
			agent := false
			for _, span := range data.Spans {
				if backend(span) && span.TraceID == data.TraceID && descends(span.SpanID, root.SpanID, make(map[string]bool)) && traceTagValue(span.Tags, "error") != "true" {
					operations[span.Operation] = true
				}
				p := data.Processes[span.ProcessID]
				if p.Service == "almanac-agent" && traceTagValue(p.Tags, "almanac.build.sha256") == want.AgentSHA && traceTagValue(p.Tags, "deployment.environment.name") == "acceptance" && span.Operation == "agent.backend.request" && descends(root.SpanID, span.SpanID, make(map[string]bool)) {
					agent = true
				}
			}
			connected := want.Kind == "publication" && operations["db.PublishDerivedCurrentProjection"] && operations["db.ReadRetainedWindowBatch"] || want.Kind == "source" && operations["db.RebuildDerivedBatch"] && operations["db.AcknowledgeDerivedBatch"] || want.Kind == "ingestion" && agent && operations["db.InsertSliCoverage"] && operations["db.InsertSliObservationValue"] && operations["db.InsertSliDeliveryReceipt"]
			if connected && len(data.TraceID) == 32 && strings.Trim(data.TraceID, "0123456789abcdef") == "" {
				return data.TraceID, root.Duration, true
			}
		}
	}
	return "", 0, false
}

func TestPublicationTraceRequiresExactCommittedGenerationAndConnectedQueries(t *testing.T) {
	tag := func(key, value string) journeyTraceTag {
		body, _ := json.Marshal(value)
		return journeyTraceTag{Key: key, Value: body}
	}
	traceID := "0123456789abcdef0123456789abcdef"
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	want := performanceWorkTraceWant{Kind: "publication", Key: "opaque", Source: "7", Through: at.Format(time.RFC3339Nano), BackendSHA: "build", NotBefore: at}
	document := journeyTraceDocument{Data: []journeyTraceData{{TraceID: traceID, Processes: map[string]journeyTraceProcess{"backend": {Service: "almanac-backend", Tags: []journeyTraceTag{tag("almanac.build.sha256", "build"), tag("deployment.environment.name", "acceptance")}}}, Spans: []journeyTraceSpan{
		{TraceID: traceID, SpanID: "root", Operation: "derived.current.evaluate", ProcessID: "backend", Start: at.UnixMicro(), Duration: 1000, Tags: []journeyTraceTag{tag("almanac.result", "published"), tag("almanac.publication.key", "opaque"), tag("almanac.source.generation", "7"), tag("almanac.evaluated.through", want.Through)}},
		{TraceID: traceID, SpanID: "write", Operation: "db.PublishDerivedCurrentProjection", ProcessID: "backend", References: []journeyTraceReference{{TraceID: traceID, SpanID: "root"}}},
		{TraceID: traceID, SpanID: "read", Operation: "db.ReadRetainedWindowBatch", ProcessID: "backend", References: []journeyTraceReference{{TraceID: traceID, SpanID: "root"}}},
	}}}}
	if _, _, ok := connectedPerformanceWork(document, want); !ok {
		t.Fatal("complete committed publication rejected")
	}
	for _, mutate := range []func(*performanceWorkTraceWant){func(w *performanceWorkTraceWant) { w.Key = "wrong" }, func(w *performanceWorkTraceWant) { w.Source = "8" }, func(w *performanceWorkTraceWant) { w.BackendSHA = "other" }, func(w *performanceWorkTraceWant) { w.Through = at.Add(time.Minute).Format(time.RFC3339Nano) }, func(w *performanceWorkTraceWant) { w.NotBefore = at.Add(time.Second) }} {
		changed := want
		mutate(&changed)
		if _, _, ok := connectedPerformanceWork(document, changed); ok {
			t.Fatal("unmatched publication accepted")
		}
	}
	document.Data[0].Spans[2].References = nil
	if _, _, ok := connectedPerformanceWork(document, want); ok {
		t.Fatal("orphan database query accepted as publication evidence")
	}
}
