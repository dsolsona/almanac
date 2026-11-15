package alertclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
)

const policyID = "11111111-1111-4111-8111-111111111111"
const sloID = "22222222-2222-4222-8222-222222222222"
const requestID = "0123456789abcdef0123456789abcdef"
const policyJSON = `{"id":"` + policyID + `","slo_id":"` + sloID + `","name":"Checkout","severity":"high","enabled":true,"version":"9007199254740993","evaluation_revision":"1","created_at":"2026-11-15T12:00:00Z","updated_at":"2026-11-15T12:00:00Z","conditions":[{"id":"fast","short_window_seconds":300,"long_window_seconds":3600,"threshold":"14.4"}]}`
const createJSON = `{"name":"Checkout","slo_id":"` + sloID + `","conditions":[{"id":"fast","short_window_seconds":300,"long_window_seconds":3600,"threshold":"14.4"}]}`

func TestAlertCreatePreservesExactResultAndDisablesTransportReplay(t *testing.T) {
	calls := 0
	ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/alert-policies" || r.GetBody != nil || r.Header.Get("Idempotency-Key") != "original-key" || r.Header.Get("X-CSRF-Token") != "csrf" {
			t.Errorf("wrong request/replay settings: %s %s replay=%v", r.Method, r.URL.Path, r.GetBody != nil)
		}
		return &http.Response{StatusCode: 201, Header: http.Header{"Etag": {`"alert-policy:` + policyID + `:9007199254740993"`}}, Body: io.NopCloser(strings.NewReader(`{"operation":"alert-policy.create","request_id":"` + requestID + `","policy":` + policyJSON + `}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	editor := func(_ context.Context, r *http.Request) error {
		r.Header.Set("X-CSRF-Token", "csrf")
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("unsafe replay")), nil }
		return nil
	}
	data, tag, err := New(ops, editor).Do(t.Context(), Input{Kind: "alert-policies", Action: "create", Key: "original-key", Body: []byte(createJSON)})
	if err != nil || calls != 1 || !strings.Contains(string(data), `"9007199254740993"`) || tag != `"alert-policy:`+policyID+`:9007199254740993"` {
		t.Fatalf("create exact result: %s %s %v calls=%d", data, tag, err, calls)
	}
}

const snapshotJSON = `{"slo_id":"` + sloID + `","slo_revision":"1","sli_revision":"1","source_generation":"9007199254740993","exclusion_revision":"0","calculation_version":"canonical-v1","cutoff":"2026-11-15T12:00:00Z"}`
const recordJSON = `{"id":"` + policyID + `","policy_id":"` + policyID + `","state":"acknowledged","version":"2","sequence":"2","evaluation_revision":"1","firing_snapshot":` + snapshotJSON + `,"latest_snapshot":` + snapshotJSON + `,"latest_breaching_snapshot":` + snapshotJSON + `,"opened_detected_at":"2026-11-15T12:00:00Z","updated_at":"2026-11-15T12:01:00Z","acknowledged_at":"2026-11-15T12:01:00Z","acknowledged_by":"` + sloID + `","original_policy":{"name":"Original name","severity":"high","service_name":"Store","slo_name":"Checkout","target":"0.999","rolling_window_seconds":2592000}}`
const processingJSON = `{"policy_id":"` + policyID + `","revision":"1","schedulable":true,"next_boundary":"2026-11-15T12:01:00Z","available_at":"2026-11-15T12:01:00Z","lag_seconds":0,"failure_attempts":0,"exhausted":false}`
const windowJSON = `{"snapshot":` + snapshotJSON + `,"start":"2026-11-15T11:00:00Z","end":"2026-11-15T12:00:00Z","quality":"complete","good":"9007199254740992.125","total":"9007199254740993.125"}`
const evaluationJSON = `{"policy_id":"` + policyID + `","evaluation_revision":"1","snapshot":` + snapshotJSON + `,"truth":"breaching","conditions":[{"id":"fast","threshold":"14.4","outcome":"matching","short":` + windowJSON + `,"long":` + windowJSON + `}]}`

func TestAlertGeneratedOperations(t *testing.T) {
	for _, tc := range []struct{ kind, action, method, path, body, response, tag string }{
		{"alert-policies", "list", "GET", "/api/v1/alert-policies", "", `{"items":[{"policy":` + policyJSON + `,"processing":` + processingJSON + `,"slo_name":"Checkout"}],"total":"9007199254740993"}`, ""},
		{"alert-policies", "get", "GET", "/api/v1/alert-policies/" + policyID, "", policyJSON, `"alert-policy:` + policyID + `:9007199254740993"`},
		{"alert-policies", "update", "PATCH", "/api/v1/alert-policies/" + policyID, `{"name":"Renamed"}`, "", `"alert-policy:` + policyID + `:9007199254740993"`},
		{"alert-policies", "enable", "POST", "/api/v1/alert-policies/" + policyID + "/enable", `{"reason":"Resume"}`, "", `"alert-policy:` + policyID + `:9007199254740993"`},
		{"alert-policies", "disable", "POST", "/api/v1/alert-policies/" + policyID + "/disable", `{}`, "", `"alert-policy:` + policyID + `:9007199254740993"`},
		{"alert-policies", "retry", "POST", "/api/v1/alert-policies/" + policyID + "/retry", `{}`, "", `"alert-policy:` + policyID + `:9007199254740993"`},
		{"alert-policies", "ranges", "GET", "/api/v1/alert-policies/" + policyID + "/ranges", "", `{"items":[]}`, ""},
		{"alerts", "list", "GET", "/api/v1/alerts", "", `{"items":[{"alert":` + recordJSON + `,"evaluation":` + evaluationJSON + `,"processing":` + processingJSON + `,"evaluation_id":"` + sloID + `","policy_name":"Current name","severity":"high","slo_id":"` + sloID + `","slo_name":"Checkout","service_id":"` + sloID + `","service_name":"Store","scheduled_at":"2026-11-15T12:00:00Z","committed_at":"2026-11-15T12:00:01Z"}],"groups":[],"total":"1"}`, ""},
		{"alerts", "get", "GET", "/api/v1/alerts/" + policyID, "", recordJSON, `"alert-occurrence:` + policyID + `:2"`},
		{"alerts", "acknowledge", "POST", "/api/v1/alerts/" + policyID + "/acknowledge", `{}`, `{"operation":"alert.acknowledge","request_id":"` + requestID + `","alert":` + recordJSON + `}`, `"alert-occurrence:` + policyID + `:2"`},
		{"alerts", "evidence", "GET", "/api/v1/alerts/" + policyID + "/evidence", "", `{"id":"` + sloID + `","evaluation":` + evaluationJSON + `,"scheduled_at":"2026-11-15T12:00:00Z","committed_at":"2026-11-15T12:00:01Z","objective":{"target":"0.999","cadence_seconds":60,"definition_valid_from":"2026-10-01T00:00:00Z","measurement_valid_from":"2026-10-01T00:00:00Z","retention_start":"2025-11-15T12:00:00Z"}}`, ""},
		{"alerts", "transitions", "GET", "/api/v1/alerts/" + policyID + "/transitions", "", `{"items":[]}`, ""},
		{"alerts", "processing", "GET", "/api/v1/alert-processing", "", processingJSON, ""},
	} {
		t.Run(tc.kind+"-"+tc.action, func(t *testing.T) {
			input := Input{Kind: tc.kind, Action: tc.action, ID: policyID, Limit: 20, Body: []byte(tc.body)}
			if input.Mutation() {
				input.Key = "original,key"
				input.ETag = `"alert-policy:` + policyID + `:1"`
				if tc.action == "acknowledge" {
					input.ETag = `"alert-occurrence:` + policyID + `:1"`
				}
			}
			if tc.action == "ranges" {
				input.Revision = "9007199254740993"
			}
			if tc.action == "evidence" {
				input.Selection = "firing"
			}
			if tc.action == "list" {
				input.ID = ""
				input.SLOID = sloID
				input.Query = "checkout %_ & café"
				input.Severity = "high"
				if tc.kind == "alerts" {
					input.State = "firing,acknowledged"
					input.Severity = "high,medium"
					input.Scope = "all"
					input.GroupBy = "slo"
					input.Sort = "severity"
					input.ServiceID = sloID
				} else {
					input.Enabled = new(false)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("route %s %s, want %s %s", r.Method, r.URL.Path, tc.method, tc.path)
				}
				if input.Mutation() && (r.Header.Get("If-Match") != input.ETag || r.Header.Get("Idempotency-Key") != input.Key) {
					t.Error("lost version or idempotency key")
				}
				if tc.action == "list" && (r.URL.Query().Get("q") != input.Query || r.URL.Query().Get("limit") != "20" || r.URL.Query().Get("severity") != input.Severity) {
					t.Errorf("list query changed: %s", r.URL.RawQuery)
				}
				if tc.action == "ranges" && r.URL.Query().Get("revision") != input.Revision {
					t.Error("rounded revision")
				}
				if tc.action == "evidence" && r.URL.Query().Get("selection") != "firing" {
					t.Error("lost evidence selection")
				}
				if tc.action == "processing" && r.URL.Query().Get("policy_id") != policyID {
					t.Error("lost policy processing identity")
				}
				w.Header().Set("ETag", tc.tag)
				response := tc.response
				if response == "" {
					response = fmt.Sprintf(`{"operation":%q,"request_id":%q,"policy":%s}`, input.CommandOperation(), requestID, policyJSON)
				}
				io.WriteString(w, response)
			}))
			defer server.Close()
			ops, _ := api.NewClient(server.URL)
			data, tag, err := New(ops, nil).Do(t.Context(), input)
			if err != nil || tag != tc.tag || len(data) == 0 {
				t.Fatalf("operation: tag=%s err=%v", tag, err)
			}
		})
	}
}

func TestAlertMalformedResponsesAndServerFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		mutation   bool
	}{
		{"numeric version", `{"operation":"alert-policy.create","request_id":"` + requestID + `","policy":` + strings.Replace(policyJSON, `"9007199254740993"`, `9007199254740993`, 1) + `}`, 201, true},
		{"wrong operation", `{"operation":"alert-policy.update","request_id":"` + requestID + `","policy":` + policyJSON + `}`, 201, true},
		{"server secret", `{"code":"private-secret","message":"credential-secret"}`, 503, true},
		{"invalid JSON", `{`, 201, true},
		{"unexpected success", `{}`, 204, true},
		{"missing items", `{"total":"0"}`, 200, false},
		{"null items", `{"items":null,"total":"0"}`, 200, false},
		{"numeric total", `{"items":[],"total":9007199254740993}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("ETag", `"alert-policy:`+policyID+`:9007199254740993"`)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			ops, _ := api.NewClient(server.URL)
			in := Input{Kind: "alert-policies", Action: "list", Limit: 20}
			if tc.mutation {
				in.Action = "create"
				in.Key = "original"
				in.Body = []byte(createJSON)
			}
			_, _, err := New(ops, nil).Do(t.Context(), in)
			f, ok := errors.AsType[*datasourceclient.Failure](err)
			if !ok || f.Unknown != tc.mutation || calls != 1 || strings.Contains(f.Message, "secret") {
				t.Fatalf("bad failure/call count: %v %d", err, calls)
			}
		})
	}
}

func TestAlertBodyReadAndCancellationPreserveUnknownOutcome(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		t.Run(fmt.Sprint(mutation), func(t *testing.T) {
			in := Input{Kind: "alert-policies", Action: "list", Limit: 20}
			if mutation {
				in.Action = "create"
				in.Key = "original-key"
				in.Body = []byte(createJSON)
			}
			ops, _ := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: brokenBody{}}, nil
			})}))
			_, _, err := New(ops, nil).Do(t.Context(), in)
			f, ok := errors.AsType[*datasourceclient.Failure](err)
			if !ok || f.Unknown != mutation || strings.Contains(f.Message, "secret") {
				t.Fatalf("body failure: %v", err)
			}
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Consume the request so HTTP/1 can observe the peer disconnect;
				// leaving a POST body unread prevents that server-side read loop.
				_, _ = io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			ops, _ = api.NewClient(server.URL)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := New(ops, nil).Do(ctx, in); done <- err }()
			<-entered
			cancel()
			err = <-done
			f, ok = errors.AsType[*datasourceclient.Failure](err)
			if !ok || f.Unknown != mutation {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("read-secret") }
func (brokenBody) Close() error             { return nil }

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return r(request) }

func TestAlertMutationUnknownNeverEchoesTransportSecrets(t *testing.T) {
	ops, _ := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("credential-secret") })}))
	_, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alert-policies", Action: "disable", ID: policyID, ETag: `"alert-policy:` + policyID + `:1"`, Key: "original-key"})
	f, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || !f.Unknown || f.Code != "outcome_unknown" || strings.Contains(f.Message, "credential-secret") || !strings.Contains(f.Message, "alert-policy.disable") || !strings.Contains(f.Message, "original-key") {
		t.Fatalf("unsafe/insufficient reconciliation: %v", err)
	}
}

func TestAlertInvalidInputDoesNotReachTransport(t *testing.T) {
	ops, _ := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("invalid input reached network")
		return nil, errors.New("unexpected")
	})}))
	for _, body := range []string{`null`, `{}`, strings.Replace(createJSON, `"name":"Checkout"`, `"name":null`, 1), strings.Replace(createJSON, `"name":"Checkout"`, `"name":"Checkout","name":"duplicate"`, 1), strings.Replace(createJSON, `"threshold":"14.4"`, `"threshold":14.4`, 1), strings.Replace(createJSON, `"id":"fast"`, `"id":"fast","extra":1`, 1)} {
		_, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alert-policies", Action: "create", Key: "key", Body: []byte(body)})
		f, ok := errors.AsType[*datasourceclient.Failure](err)
		if !ok || f.Code != "invalid_request" || f.Unknown {
			t.Fatalf("invalid input %s: %v", body, err)
		}
	}
}

func TestAlertRetryRejectsDiscardedReason(t *testing.T) {
	err := Validate(Input{Kind: "alert-policies", Action: "retry", ID: policyID, ETag: `"alert-policy:` + policyID + `:1"`, Key: "key", Body: []byte(`{"reason":"must not be discarded"}`)})
	if err == nil {
		t.Fatal("retry accepted an unsupported reason")
	}
}

func TestAlertCommandLookupDistinguishesUnknownExpiredAndCommitted(t *testing.T) {
	for _, status := range []string{"unknown", "expired", "committed"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("operation") != "alert-policy.update" || r.URL.Query().Get("idempotency_key") != "original,key" {
					t.Error("lost original command identity")
				}
				body := `{"status":"` + status + `"}`
				if status == "committed" {
					body = `{"status":"committed","result":{"operation":"alert-policy.update","request_id":"` + requestID + `","policy":` + policyJSON + `}}`
				}
				io.WriteString(w, body)
			}))
			defer server.Close()
			ops, _ := api.NewClient(server.URL)
			data, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alerts", Action: "command-result", Operation: "alert-policy.update", Key: "original,key"})
			if err != nil || !strings.Contains(string(data), `"`+status+`"`) {
				t.Fatalf("lookup: %s %v", data, err)
			}
		})
	}
}
