package agentws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dsolsona/almanac/internal/agentaccess"
	"github.com/dsolsona/almanac/internal/agentownership"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/agentruntime"
)

func TestEnqueueBoundsMessagesIncludingInFlight(t *testing.T) {
	control := newTestControl()
	ctx := t.Context()
	for range agentprotocol.MaxQueueMessages {
		if err := control.enqueue(ctx, outbound{body: []byte("x")}); err != nil {
			t.Fatalf("enqueue within message budget: %v", err)
		}
	}
	first := <-control.queue
	if control.pending.Load() != agentprotocol.MaxQueueMessages || control.queued.Load() != agentprotocol.MaxQueueMessages {
		t.Fatalf("in-flight message left budget early: bytes=%d count=%d", control.pending.Load(), control.queued.Load())
	}
	if err := control.enqueue(ctx, outbound{body: []byte("x")}); err == nil {
		t.Fatal("message budget overflow was accepted")
	}
	control.pending.Add(-int64(len(first.body)))
	control.queued.Add(-1)
	if err := control.enqueue(ctx, outbound{body: []byte("x")}); err != nil {
		t.Fatalf("enqueue after writer released in-flight message: %v", err)
	}
}

func TestRegistryPublicationCannotRegressAfterRemoval(t *testing.T) {
	registry := NewRegistry()
	newer, older, reconnect := newTestControl(), newTestControl(), newTestControl()
	const id = "00000000-0000-4000-8000-000000000001"
	if _, ok := registry.publish(newer, agentruntime.Session{AgentID: id, Generation: 2}); !ok {
		t.Fatal("newer publication rejected")
	}
	if _, ok := registry.publish(older, agentruntime.Session{AgentID: id, Generation: 1}); ok {
		t.Fatal("older publication accepted")
	}
	registry.remove(older)
	if got, ok := registry.SessionGeneration(id); !ok || got != 2 {
		t.Fatalf("current generation = %d, %v", got, ok)
	}
	registry.remove(newer)
	if _, ok := registry.publish(older, agentruntime.Session{AgentID: id, Generation: 1}); ok {
		t.Fatal("older generation resurrected")
	}
	if _, ok := registry.publish(reconnect, agentruntime.Session{AgentID: id, Generation: 2}); !ok {
		t.Fatal("same-generation reconnect rejected")
	}
}

func TestReplacementReasonNegotiation(t *testing.T) {
	legacy := newTestControl()
	if got := legacy.replacementReason(); got != "session_rejected" {
		t.Fatalf("legacy reason = %q", got)
	}
	legacy.supportsOwnerReplaced = true
	if got := legacy.replacementReason(); got != "owner_replaced" {
		t.Fatalf("capable reason = %q", got)
	}
}

func TestRegistryReplacementClosesLegacyAndCapableWebSockets(t *testing.T) {
	for _, test := range []struct {
		capable bool
		want    string
	}{{false, "session_rejected"}, {true, "owner_replaced"}} {
		t.Run(test.want, func(t *testing.T) {
			r := NewRegistry()
			ready := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				conn, err := websocket.Accept(w, req, nil)
				if err != nil {
					return
				}
				ctx, cancel := context.WithCancel(req.Context())
				defer cancel()
				old := newTestControl()
				old.conn, old.cancel, old.supportsOwnerReplaced = conn, cancel, test.capable
				r.publish(old, agentruntime.Session{AgentID: "agent", Generation: 1})
				close(ready)
				<-ctx.Done()
			}))
			defer server.Close()
			conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			<-ready
			r.publish(newTestControl(), agentruntime.Session{AgentID: "agent", Generation: 2})
			_, _, err = conn.Read(t.Context())
			var closeErr websocket.CloseError
			if !errors.As(err, &closeErr) || closeErr.Reason != test.want {
				t.Fatalf("replacement close = %v, want %s", err, test.want)
			}
			r.closeWorkers.Wait()
		})
	}
}

func TestRegistryRemoveOldSessionPreservesCurrentOwner(t *testing.T) {
	r := NewRegistry()
	id := "agent"
	old, current := newTestControl(), newTestControl()
	old.session = &agentruntime.Session{AgentID: id, Generation: 1}
	current.session = &agentruntime.Session{AgentID: id, Generation: 2}
	r.owners[id] = registryOwner{generation: 2, control: current}
	r.remove(old)
	if got, gen := r.current(id); got != current || gen != 2 {
		t.Fatalf("owner = %p generation %d", got, gen)
	}
	r.remove(current)
	if got, gen := r.current(id); got != nil || gen != 2 {
		t.Fatalf("removed owner = %p generation %d", got, gen)
	}
}

func TestEnqueueBoundsBytesAndRollsBackCancelledReservation(t *testing.T) {
	control := newTestControl()
	body := make([]byte, agentprotocol.MaxMessageBytes)
	for range agentprotocol.MaxQueueBytes / agentprotocol.MaxMessageBytes {
		if err := control.enqueue(t.Context(), outbound{body: body}); err != nil {
			t.Fatalf("enqueue within byte budget: %v", err)
		}
	}
	if err := control.enqueue(t.Context(), outbound{body: []byte("x")}); err == nil {
		t.Fatal("byte budget overflow was accepted")
	}
	cancelled := newTestControl()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cancelled.enqueue(ctx, outbound{body: []byte("y")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enqueue error = %v", err)
	}
	if cancelled.pending.Load() != 0 || cancelled.queued.Load() != 0 {
		t.Fatalf("failed reservations leaked budget: bytes=%d count=%d", cancelled.pending.Load(), cancelled.queued.Load())
	}
}

func TestDrainBoundsReadySessionWhenPeerDoesNotRead(t *testing.T) {
	for _, reading := range []bool{true, false} {
		name := "handler_processing"
		if reading {
			name = "handler_reading"
		}
		t.Run(name, func(t *testing.T) {
			registry := NewRegistry()
			added, handlerDone := make(chan struct{}), make(chan struct{})
			handler := &Handler{registry: registry}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upgrade := &upgradeCapture{ResponseWriter: w}
				conn, err := websocket.Accept(upgrade, r, nil)
				if err != nil {
					close(handlerDone)
					return
				}
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				control := &sessionControl{conn: conn, transport: upgrade.transport, cancel: cancel, ready: true, shutdown: make(chan struct{}), queue: make(chan outbound, agentprotocol.MaxQueueMessages), done: make(chan struct{})}
				if !registry.add(control) {
					conn.CloseNow()
					close(handlerDone)
					return
				}
				writerDone := make(chan struct{})
				go func() { defer close(writerDone); handler.writeLoop(ctx, control) }()
				close(added)
				if reading {
					_, _, _ = conn.Read(ctx)
				} else {
					<-ctx.Done()
				}
				cancel()
				<-writerDone
				registry.remove(control)
				close(handlerDone)
			}))
			defer server.Close()
			client, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseNow()
			<-added
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := registry.Drain(ctx); err != nil {
				t.Fatalf("ready session consumed the entire shutdown deadline waiting for a non-reading peer: %v", err)
			}
			select {
			case <-handlerDone:
			case <-ctx.Done():
				t.Fatal("drain did not join the ready session handler")
			}
		})
	}
}

func TestDrainClosesPreHelloSessionAndJoinsHandler(t *testing.T) {
	registry := NewRegistry()
	added := make(chan struct{})
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			close(handlerDone)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		control := &sessionControl{conn: conn, cancel: cancel, shutdown: make(chan struct{}), queue: make(chan outbound, agentprotocol.MaxQueueMessages), done: make(chan struct{})}
		if !registry.add(control) {
			_ = conn.Close(websocket.StatusGoingAway, "stopping")
			close(handlerDone)
			return
		}
		close(added)
		_, _, _ = conn.Read(ctx)
		registry.remove(control)
		close(handlerDone)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal("parse test server URL failed")
	}
	serverURL.Scheme = "ws"
	client, _, err := websocket.Dial(t.Context(), serverURL.String(), nil)
	if err != nil {
		t.Fatal("open pre-hello session failed")
	}
	defer client.CloseNow()
	select {
	case <-added:
	case <-time.After(time.Second):
		t.Fatal("pre-hello session was not registered")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	if err := registry.Drain(ctx); err != nil {
		t.Fatalf("drain pre-hello session: %v", err)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("pre-hello drain waited for the hello timeout")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("drain returned before the session handler exited")
	}
	control := &sessionControl{done: make(chan struct{})}
	if registry.add(control) {
		t.Fatal("stopping registry accepted a new session")
	}
}

func newTestControl() *sessionControl {
	return &sessionControl{queue: make(chan outbound, agentprotocol.MaxQueueMessages), done: make(chan struct{})}
}

func TestOpenFailureReasonsAreStableAndSanitized(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{err: agentownership.ErrConflict, want: "active_owner"},
		{err: agentownership.ErrReplaced, want: "owner_replaced"},
		{err: agentownership.ErrInvalidCredential, want: "credential_revoked"},
		{err: agentruntime.ErrIncompatible, want: "incompatible_protocol"},
		{err: errors.New("private database detail"), want: "session_rejected"},
	} {
		if got := openFailureReason(test.err); got != test.want || strings.Contains(got, "private") {
			t.Fatalf("open failure reason = %q, want %q", got, test.want)
		}
	}
}

func TestAuthenticationFailureStatusSeparatesInvalidCredentialFromUnavailable(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{err: agentaccess.ErrInvalidCredential, want: http.StatusUnauthorized},
		{err: agentaccess.ErrUnavailable, want: http.StatusServiceUnavailable},
		{err: agentaccess.ErrCancelled, want: http.StatusServiceUnavailable},
		{err: errors.New("temporary storage fault"), want: http.StatusServiceUnavailable},
	} {
		if got := authenticationFailureStatus(test.err); got != test.want {
			t.Errorf("authentication failure %v returned status %d, want %d", test.err, got, test.want)
		}
	}
}

func TestOpenFailureRetriesWhenOwnershipStorageIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		err      error
		wantCode websocket.StatusCode
		wantText string
	}{
		{err: agentownership.ErrUnavailable, wantCode: websocket.StatusTryAgainLater, wantText: "server_unavailable"},
		{err: agentownership.ErrCancelled, wantCode: websocket.StatusTryAgainLater, wantText: "server_unavailable"},
		{err: agentownership.ErrDenied, wantCode: websocket.StatusPolicyViolation, wantText: "session_rejected"},
	} {
		code, text := openFailure(test.err)
		if code != test.wantCode || text != test.wantText {
			t.Errorf("openFailure(%v) = %d,%q; want %d,%q", test.err, code, text, test.wantCode, test.wantText)
		}
	}
}
