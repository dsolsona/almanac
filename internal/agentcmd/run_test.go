package agentcmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dsolsona/almanac/internal/agentprotocol"
)

const testAgentID = "00000000-0000-4000-8000-000000000001"

func TestHandleHeartbeatAckBindingsAreAtomic(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	nonce := testNonce(1)
	baseline := testAck(now, nonce)
	if err := baseline.Validate(); err != nil {
		t.Fatalf("valid heartbeat-ack fixture rejected before mutation: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*client, *agentprotocol.Envelope[agentprotocol.HeartbeatAck])
		wantOK bool
	}{
		{name: "valid ack", wantOK: true},
		{name: "wrong nonce", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.RequestNonce = testNonce(2)
		}},
		{name: "unrelated generation", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.SessionGeneration = "2"
			e.Payload.ExecutionLease.SessionGeneration = "2"
		}},
		{name: "different agent", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.ExecutionLease.AgentID = "00000000-0000-4000-8000-000000000002"
		}},
		{name: "configuration revision regression", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.ExecutionLease.ConfigurationRevision = "0"
		}},
		{name: "duplicate sequence", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.RenewalSequence = "0"
		}},
		{name: "skipped sequence", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.RenewalSequence = "2"
		}},
		{name: "expired lease", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.ExecutionLease.ValidFrom = now.Add(-30 * time.Minute)
			e.Payload.ExecutionLease.ValidUntil = now.Add(-time.Second)
		}},
		{name: "lease expiry regressed", mutate: func(_ *client, e *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			e.Payload.ExecutionLease.ValidUntil = now.Add(30 * time.Minute)
		}},
		{name: "no pending heartbeat", mutate: func(c *client, _ *agentprotocol.Envelope[agentprotocol.HeartbeatAck]) {
			c.pending = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := testClient(now, nonce)
			ack := testAck(now, nonce)
			if test.mutate != nil {
				test.mutate(c, &ack)
			}
			beforeLease, beforeSequence := c.lease, c.sequence
			var beforePending *agentprotocol.Heartbeat
			if c.pending != nil {
				copy := *c.pending
				beforePending = &copy
			}
			wire, err := json.Marshal(ack)
			if err != nil {
				t.Fatal("marshal ack fixture")
			}
			err = c.handleMessage(context.Background(), nil, jsontext.Value(wire))
			if test.wantOK {
				if err != nil || c.pending != nil || c.sequence != 1 || c.lease.LeaseID != "lease-next" {
					t.Fatalf("valid ack state: err=%v seq=%d pending=%t lease=%q", err, c.sequence, c.pending != nil, c.lease.LeaseID)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid ack was accepted")
			}
			if !reflect.DeepEqual(c.lease, beforeLease) || c.sequence != beforeSequence || !reflect.DeepEqual(c.pending, beforePending) {
				t.Fatal("rejected ack partially changed client state")
			}
		})
	}
}

func TestHeartbeatGrantCanAdvanceBeforeNewConfigurationApplies(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	nonce := testNonce(21)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Service Unavailable","status":503,"code":"agent_configuration_unavailable"}`))
	}))
	defer server.Close()

	c := testClient(now, nonce)
	c.opts.backendURL = server.URL
	c.http = server.Client()
	c.configurationPending = true
	c.assignments = []agentprotocol.DatasourceAssignment{{DatasourceID: "00000000-0000-4000-8000-000000000020", DatasourceVersion: "1", Name: "current-source",
		Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test",
		TLS: agentprotocol.DatasourceTLS{}, AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}}
	oldAssignments := append([]agentprotocol.DatasourceAssignment(nil), c.assignments...)
	ack := testAck(now, nonce)
	ack.Payload.ExecutionLease.ConfigurationRevision = "2"
	wire, err := json.Marshal(ack)
	if err != nil {
		t.Fatal("marshal revision-advance ack")
	}
	if err := c.handleMessage(t.Context(), nil, jsontext.Value(wire)); err != nil {
		t.Fatalf("accept newer heartbeat grant while configuration is pending: %v", err)
	}
	if c.lease.ConfigurationRevision != "2" || c.revision != "1" || !c.configurationPending || c.pending != nil || !reflect.DeepEqual(c.assignments, oldAssignments) {
		t.Fatalf("grant advance changed applied configuration: lease=%s applied=%s pending=%t assignments=%+v", c.lease.ConfigurationRevision, c.revision, c.configurationPending, c.assignments)
	}
}

func TestWelcomeRenewalSequenceMatchesPendingState(t *testing.T) {
	for _, test := range []struct {
		name       string
		current    int64
		pending    string
		welcomed   int64
		wantReject bool
	}{
		{name: "initial generation zero", current: 0, welcomed: 0},
		{name: "resume without pending renewal", current: 4, welcomed: 4},
		{name: "reject regression without pending renewal", current: 4, welcomed: 3, wantReject: true},
		{name: "pending renewal not committed", current: 4, pending: "4", welcomed: 4},
		{name: "pending renewal committed but ack lost", current: 4, pending: "4", welcomed: 5},
		{name: "reject sequence beyond lost ack", current: 4, pending: "4", welcomed: 6, wantReject: true},
		{name: "reject local pending mismatch", current: 4, pending: "3", welcomed: 4, wantReject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var pending *agentprotocol.Heartbeat
			if test.pending != "" {
				pending = &agentprotocol.Heartbeat{RenewalSequence: test.pending}
			}
			err := validateWelcomeSequence(test.current, pending, test.welcomed)
			if (err != nil) != test.wantReject {
				t.Fatalf("welcome sequence validation error=%v, wantReject=%t", err, test.wantReject)
			}
		})
	}
}

func TestRetryBackoffCapsAndStopsOnCancellation(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second,
		8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	delay := time.Second
	for index, expected := range want {
		if delay != expected {
			t.Fatalf("backoff[%d]=%s, want %s", index, delay, expected)
		}
		delay = nextBackoff(delay)
	}
	if nextBackoff(0) != time.Second {
		t.Fatal("backoff below minimum was not raised to one second")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if waitRetry(ctx, time.Hour) {
		t.Fatal("retry wait continued after cancellation")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("retry cancellation was not prompt: %s", elapsed)
	}
}

func TestRetryDelayResetsOnlyAfterCurrentAttemptEstablished(t *testing.T) {
	if got := retryDelay(16*time.Second, false); got != 16*time.Second {
		t.Fatalf("failed connection attempt reset retry delay to %s", got)
	}
	if got := retryDelay(16*time.Second, true); got != time.Second {
		t.Fatalf("established connection attempt retry delay = %s, want 1s", got)
	}
}

func TestSessionRejectionIsActionableAndTerminal(t *testing.T) {
	err := closeReasonError("session_rejected")
	if !errors.Is(err, ErrSessionRejected) || !strings.Contains(err.Error(), "ownership lease") {
		t.Fatalf("session rejection classification = %v", err)
	}
	if err := closeReasonError("server_unavailable"); err != nil {
		t.Fatalf("retryable close reason became terminal: %v", err)
	}
}

func TestRunSessionReplaysPendingHeartbeatAfterLostAck(t *testing.T) {
	baseTime := time.Now().UTC().Add(-time.Minute)
	firstLease := testLease(baseTime, "lease-current", baseTime.Add(time.Hour))
	committedLease := testLease(baseTime.Add(time.Minute), "lease-next", baseTime.Add(time.Hour+time.Minute))
	committedLease.ConfigurationRevision = "2"
	if err := firstLease.Validate(); err != nil {
		t.Fatalf("initial lease fixture invalid before connection test: %v", err)
	}
	if err := committedLease.Validate(); err != nil {
		t.Fatalf("renewed lease fixture invalid before connection test: %v", err)
	}
	var connections atomic.Int32
	heartbeats := make(chan agentprotocol.Heartbeat, 2)
	serverError := make(chan error, 2)
	handler := http.NewServeMux()
	handler.HandleFunc("/api/agent/v1/configuration", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := agentprotocol.ConfigurationSnapshot{
			AgentID: testAgentID, ConfigurationRevision: "2", Assignments: []agentprotocol.DatasourceAssignment{},
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			serverError <- errors.New("marshal test snapshot")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", "\"agent-config:"+testAgentID+":2\"")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	handler.HandleFunc("/api/agent/v1/connect", func(w http.ResponseWriter, r *http.Request) {
		attempt := connections.Add(1)
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{agentprotocol.ProtocolV1},
		})
		if err != nil {
			serverError <- errors.New("accept test websocket")
			return
		}
		defer conn.CloseNow()
		_, helloBytes, err := conn.Read(r.Context())
		if err != nil {
			serverError <- errors.New("read test hello")
			return
		}
		var hello agentprotocol.Envelope[agentprotocol.Hello]
		if json.Unmarshal(helloBytes, &hello) != nil || hello.Validate() != nil {
			serverError <- errors.New("decode test hello")
			return
		}
		lease, renewalSequence, configurationRevision := firstLease, "0", "1"
		if attempt == 2 {
			lease, renewalSequence, configurationRevision = committedLease, "1", "2"
		}
		welcome := agentprotocol.Envelope[agentprotocol.Welcome]{
			Kind: agentprotocol.KindWelcome, MessageID: "welcome-test", SessionGeneration: "1",
			Payload: agentprotocol.Welcome{
				Protocol: agentprotocol.ProtocolV1, RequestNonce: hello.Payload.RequestNonce,
				ServerTime: time.Now().UTC(), AgentID: testAgentID, ConfigurationRevision: configurationRevision,
				RenewalSequence: renewalSequence, HeartbeatIntervalMS: 10_000,
				ExecutionLease: lease,
			},
		}
		if err := welcome.Validate(); err != nil {
			serverError <- errors.New("test welcome baseline is invalid")
			return
		}
		if err := writeTestMessage(r.Context(), conn, welcome); err != nil {
			serverError <- errors.New("write test welcome")
			return
		}
		_, heartbeatBytes, err := conn.Read(r.Context())
		if err != nil {
			serverError <- errors.New("read heartbeat")
			return
		}
		var heartbeat agentprotocol.Envelope[agentprotocol.Heartbeat]
		if json.Unmarshal(heartbeatBytes, &heartbeat) != nil || heartbeat.Validate() != nil {
			serverError <- errors.New("decode heartbeat")
			return
		}
		heartbeats <- heartbeat.Payload
		if attempt == 1 {
			// Model a durable renewal whose acknowledgement is lost in transit.
			return
		}
		if attempt != 2 {
			serverError <- errors.New("unexpected third websocket connection")
			return
		}
		ack := agentprotocol.Envelope[agentprotocol.HeartbeatAck]{
			Kind: agentprotocol.KindHeartbeatAck, MessageID: "ack-test", SessionGeneration: "1",
			Payload: agentprotocol.HeartbeatAck{
				RequestNonce: heartbeat.Payload.RequestNonce, RenewalSequence: "1",
				ExecutionLease: committedLease,
			},
		}
		if err := ack.Validate(); err != nil {
			serverError <- errors.New("test heartbeat-ack baseline is invalid")
			return
		}
		if err := writeTestMessage(r.Context(), conn, ack); err != nil {
			serverError <- errors.New("write heartbeat ack")
			return
		}
		_, appliedBytes, err := conn.Read(r.Context())
		if err != nil {
			serverError <- errors.New("read configuration-applied after newer grant")
			return
		}
		var applied agentprotocol.Envelope[agentprotocol.ConfigurationApplied]
		if json.Unmarshal(appliedBytes, &applied) != nil || applied.Validate() != nil || applied.Payload.ConfigurationRevision != "2" {
			serverError <- errors.New("decode configuration-applied after newer grant")
			return
		}
		shutdown := agentprotocol.Envelope[agentprotocol.Shutdown]{
			Kind: agentprotocol.KindShutdown, MessageID: "shutdown-test", SessionGeneration: "1",
			Payload: agentprotocol.Shutdown{Reason: "server_shutdown"},
		}
		if err := shutdown.Validate(); err != nil {
			serverError <- errors.New("test shutdown baseline is invalid")
			return
		}
		if err := writeTestMessage(r.Context(), conn, shutdown); err != nil {
			serverError <- errors.New("write shutdown")
		}
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	c := testClient(baseTime, testNonce(1))
	c.opts.backendURL = server.URL
	c.http = server.Client()
	if err := c.prepareHeartbeat(); err != nil {
		t.Fatal("prepare pending heartbeat")
	}
	original := *c.pending
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.runSession(ctx); err == nil {
		t.Fatal("first session did not report lost acknowledgement")
	}
	if c.pending == nil || c.pending.RequestNonce != original.RequestNonce || c.pending.RenewalSequence != original.RenewalSequence {
		t.Fatal("lost acknowledgement did not preserve exact pending heartbeat")
	}
	if err := c.runSession(ctx); !errors.Is(err, errReconnect) {
		t.Fatalf("second session result=%v, want backend reconnect", err)
	}
	first, second := <-heartbeats, <-heartbeats
	if first.RequestNonce != original.RequestNonce || second.RequestNonce != original.RequestNonce ||
		first.RenewalSequence != original.RenewalSequence || second.RenewalSequence != original.RenewalSequence {
		t.Fatal("reconnect replaced pending heartbeat nonce or sequence")
	}
	if c.pending != nil || c.sequence != 1 || c.lease.LeaseID != committedLease.LeaseID || c.revision != "2" ||
		!c.lease.ValidUntil.Equal(committedLease.ValidUntil) {
		t.Fatal("replayed acknowledgement did not commit matching lease state")
	}
	select {
	case err := <-serverError:
		t.Fatalf("test backend: %v", err)
	default:
	}
}

func writeTestMessage(ctx context.Context, conn *websocket.Conn, message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func testClient(now time.Time, nonce string) *client {
	lease := testLease(now, "lease-current", now.Add(time.Hour))
	return &client{
		agentID: testAgentID, generation: "1", revision: "1", resume: testNonce(3),
		lease: lease, sequence: 0,
		pending: &agentprotocol.Heartbeat{
			RequestNonce: nonce, AppliedConfigurationRevision: "1",
			Scheduler: "ready", CollectionProgress: "idle",
			LeaseID: lease.LeaseID, RenewalSequence: "0",
		},
		serverClock: &serverClockEstimate{serverTime: now, serverAt: time.Now()},
	}
}

func testAck(now time.Time, nonce string) agentprotocol.Envelope[agentprotocol.HeartbeatAck] {
	return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{
		Kind: agentprotocol.KindHeartbeatAck, MessageID: "ack-test-1", SessionGeneration: "1",
		Payload: agentprotocol.HeartbeatAck{
			RequestNonce: nonce, RenewalSequence: "1",
			ExecutionLease: testLease(now, "lease-next", now.Add(time.Hour)),
		},
	}
}

func testLease(now time.Time, id string, validUntil time.Time) agentprotocol.Lease {
	return agentprotocol.Lease{
		LeaseID: id, AgentID: testAgentID, SessionGeneration: "1",
		ConfigurationRevision: "1", AllowedMeasurements: []agentprotocol.MeasurementGrant{},
		ValidFrom: now, ValidUntil: validUntil,
	}
}

func testNonce(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}
