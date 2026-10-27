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
	return &sessionControl{queue: make(chan outbound, agentprotocol.MaxQueueMessages)}
}

func TestOpenFailureReasonsAreStableAndSanitized(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{err: agentownership.ErrConflict, want: "active_owner"},
		{err: agentownership.ErrInvalidCredential, want: "credential_revoked"},
		{err: agentruntime.ErrIncompatible, want: "incompatible_protocol"},
		{err: errors.New("private database detail"), want: "session_rejected"},
	} {
		if got := openFailureReason(test.err); got != test.want || strings.Contains(got, "private") {
			t.Fatalf("open failure reason = %q, want %q", got, test.want)
		}
	}
}
