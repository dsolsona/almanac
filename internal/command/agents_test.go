package command

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/agentclient"
	"github.com/dsolsona/almanac/internal/setupclient"
)

type agentCLICreateDocument struct {
	Result struct {
		Agent struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"agent"`
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	} `json:"result"`
}

func TestAgentCLICreateDocumentDecodesGeneratedEnvelope(t *testing.T) {
	const response = `{"schema_version":1,"operation":"agents create","result":{"agent":{"id":"00000000-0000-0000-0000-000000000001","version":"1"},"credential":{"id":"00000000-0000-0000-0000-000000000002"}}}`
	var document agentCLICreateDocument
	if err := json.Unmarshal([]byte(response), &document); err != nil {
		t.Fatal("decode create envelope failed")
	}
	if document.Result.Agent.ID != "00000000-0000-0000-0000-000000000001" || document.Result.Agent.Version != "1" || document.Result.Credential.ID != "00000000-0000-0000-0000-000000000002" {
		t.Fatal("create envelope metadata fields did not decode")
	}
}

type failingAgentWriter struct{}

func (failingAgentWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestAgentCommandsRejectIncompleteOrContradictoryFlags(t *testing.T) {
	cases := [][]string{
		{"agents", "list"},
		{"agents", "list", "--profile-file=x", "extra"},
		{"agents", "list", "--profile-file=x", "--limit=201"},
		{"agents", "get", "--profile-file=x", "--agent-id=not-a-uuid"},
		{"agents", "get", "--profile-file=x", "--agent-id=00000000-0000-0000-0000-000000000001", "--limit=2"},
		{"agents", "create", "--profile-file=x", "--name=n", "--display-name=d", "--current-password-file=p", "--idempotency-key=k"},
		{"agents", "create", "--profile-file=x", "--name=n", "--display-name=d", "--current-password-file=p", "--idempotency-key=k", "--show-token", "--token-file=t"},
		{"agents", "credentials", "revoke", "--profile-file=x", "--agent-id=00000000-0000-0000-0000-000000000001", "--credential-id=00000000-0000-0000-0000-000000000002"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run(context.Background(), args, &out, &errOut, BuildInfo{}); code != 2 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestAgentETagRequiresExactPositiveInt64AndAgentBinding(t *testing.T) {
	id := "00000000-0000-0000-0000-000000000001"
	for _, tc := range []struct {
		tag   string
		valid bool
	}{
		{`"agent:` + id + `:1"`, true},
		{`W/"agent:` + id + `:1"`, false},
		{`"agent:` + id + `:01"`, false},
		{`"agent:` + id + `:9223372036854775808"`, false},
		{`"agent:00000000-0000-0000-0000-000000000002:1"`, false},
	} {
		if got := validETag(tc.tag, id); got != tc.valid {
			t.Errorf("validETag(%q)=%t want %t", tc.tag, got, tc.valid)
		}
	}
}

func TestAgentOutputIsHumanReadableSafeAndReportsWriterFailures(t *testing.T) {
	result := jsontext.Value(`{"items":[{"id":"00000000-0000-0000-0000-000000000001","name":"line\n\u001b[31m","display_name":"shown\tname","enabled":true,"version":"9223372036854775807","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"}],"next_cursor":""}`)
	text := renderAgentText("agents list", result, "", false)
	if !strings.Contains(text, `"line\n\x1b[31m"`) || strings.ContainsRune(text, '\x1b') || strings.Contains(text, "schema_version") || !strings.Contains(text, "Agents (1)") {
		t.Fatalf("unsafe or non-human text output: %q", text)
	}
	var stdout bytes.Buffer
	doc := agentsDoc{SchemaVersion: 1, Operation: "agents list", Result: result}
	if code := writeAgentSuccess(&stdout, &bytes.Buffer{}, true, doc, "agents list", result, "", false); code != 0 || !strings.Contains(stdout.String(), `"operation":"agents list"`) {
		t.Fatalf("JSON operation envelope is wrong: code=%d output=%q", code, stdout.String())
	}
	if code := writeAgentSuccess(failingAgentWriter{}, &bytes.Buffer{}, false, doc, "agents list", result, "", false); code != 1 {
		t.Fatalf("text success writer failure returned %d", code)
	}
	if code := writeAgentSuccess(failingAgentWriter{}, &bytes.Buffer{}, true, doc, "agents list", result, "", false); code != 1 {
		t.Fatalf("JSON success writer failure returned %d", code)
	}
	if code := writeAgentFailure(&bytes.Buffer{}, failingAgentWriter{}, "agents list", false, agentInvalid("safe failure")); code != 1 {
		t.Fatalf("text error writer failure returned %d", code)
	}
}

func TestAgentDetailTextShowsRuntimeWithoutCombiningHealthSignals(t *testing.T) {
	result := jsontext.Value(`{"agent":{"id":"00000000-0000-0000-0000-000000000001","name":"east","display_name":"East","enabled":false,"version":"9223372036854775807","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z","runtime":{"reachability":"connected","protocol":"almanac-agent/1","software_version":"1.2.3","capabilities":[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["live_query"]}],"last_heartbeat_at":"2026-10-26T10:00:00.123456Z","desired_configuration_revision":"9223372036854775807","applied_configuration_revision":"0","scheduler_health":"ready","collection_progress":"running","last_attempt_at":"2026-10-26T09:59:00Z","last_successful_query_at":null,"next_expected_execution_at":null,"collection_explanation":"no_assignment"}},"etag":"\"agent:00000000-0000-0000-0000-000000000001:9223372036854775807\""}`)
	text := renderAgentText("agents get", result, "", false)
	for _, want := range []string{
		"Connection state: Disabled by administrator",
		"Reported reachability: connected (not operational while disabled)",
		`Protocol: "almanac-agent/1"`,
		`Software version: "1.2.3"`,
		"Desired configuration revision: 9223372036854775807",
		"Applied configuration revision: Not applied",
		"Scheduler health: ready",
		"Collection progress: running",
		"Last heartbeat: 2026-10-26T10:00:00.123456Z",
		"Last attempt: 2026-10-26T09:59:00Z",
		"Last successful query: Not reported",
		"No datasources are assigned to this agent.",
		"Observation acceptance freshness: Not available",
		"prometheus",
		"prometheus-http-v1",
		"live_query",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("agent detail text omitted %q: %s", want, text)
		}
	}
	if strings.Contains(text, "healthy") || strings.Contains(text, "grant_id") || strings.Contains(text, "token") {
		t.Fatalf("agent detail text claimed health or disclosed an unrelated identifier: %s", text)
	}
	emptyCapabilities := jsontext.Value(strings.Replace(string(result), `"capabilities":[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["live_query"]}]`, `"capabilities":[]`, 1))
	if emptyText := renderAgentText("agents get", emptyCapabilities, "", false); !strings.Contains(emptyText, "No provider capabilities were reported.") {
		t.Fatalf("empty capabilities were not explicit: %s", emptyText)
	}
	neverConnected := jsontext.Value(`{"agent":{"id":"00000000-0000-0000-0000-000000000001","name":"east","display_name":"East","enabled":true,"version":"1","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"},"etag":"\"agent:00000000-0000-0000-0000-000000000001:1\""}`)
	if neverConnectedText := renderAgentText("agents get", neverConnected, "", false); !strings.Contains(neverConnectedText, "Connection state: Never connected") || !strings.Contains(neverConnectedText, "No heartbeat has been received.") {
		t.Fatalf("absent runtime was not represented as never connected: %s", neverConnectedText)
	}
}

func TestCancellationTextDistinguishesPreDispatchFromUnknownMutation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		unknown bool
		warn    bool
		message string
	}{
		{name: "before dispatch", message: "agent operation was cancelled or timed out"},
		{name: "after dispatch", unknown: true, warn: true, message: "agent operation was cancelled or timed out; the mutation may have completed; inspect agent state before deciding what to do next"},
	} {
		err := &agentclient.Failure{Kind: setupclient.KindCancelled, Code: "cancelled", Message: tc.message, Unknown: tc.unknown}
		var stderr bytes.Buffer
		if exit := writeAgentFailure(&bytes.Buffer{}, &stderr, "agents create", false, err); exit != 6 {
			t.Fatalf("%s cancellation exit=%d", tc.name, exit)
		}
		if got := strings.Contains(stderr.String(), "may have completed"); got != tc.warn {
			t.Fatalf("%s cancellation warning=%t, want %t", tc.name, got, tc.warn)
		}
	}
}
