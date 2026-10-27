// Package agentws serves the bounded, authenticated agent control channel.
package agentws

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/dsolsona/almanac/internal/agentaccess"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/agentruntime"
	"github.com/dsolsona/almanac/internal/authhttp"
)

const (
	writeTimeout      = 5 * time.Second
	helloTimeout      = 5 * time.Second
	revocationPolling = 2 * time.Second
)

type Handler struct {
	access   *agentaccess.Service
	runtime  *agentruntime.Service
	auth     *authhttp.Handler
	registry *Registry
}

type outbound struct {
	body        []byte
	closeAfter  bool
	closeCode   websocket.StatusCode
	closeReason string
}

type sessionControl struct {
	conn     *websocket.Conn
	cancel   context.CancelFunc
	shutdown chan struct{}
	queue    chan outbound
	pending  atomic.Int64
	readyMu  sync.RWMutex
	session  *agentruntime.Session
	done     chan struct{}
	stopOnce sync.Once
}

func New(access *agentaccess.Service, runtime *agentruntime.Service, auth *authhttp.Handler, registry *Registry) (*Handler, error) {
	if access == nil || runtime == nil || auth == nil || registry == nil {
		return nil, errors.New("agentws: invalid options")
	}
	return &Handler{access: access, runtime: runtime, auth: auth, registry: registry}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.auth.ValidateTLSRequest(r) != nil || r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.Error(w, "request rejected", http.StatusBadRequest)
		return
	}
	token, ok := bearerToken(r.Header.Values("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := h.access.Authenticate(r.Context(), token); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{agentprotocol.ProtocolV1},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(agentprotocol.MaxMessageBytes)
	ctx, cancel := context.WithCancel(r.Context())
	control := &sessionControl{
		conn: conn, cancel: cancel, shutdown: make(chan struct{}),
		queue: make(chan outbound, agentprotocol.MaxQueueMessages), done: make(chan struct{}),
	}
	if !h.registry.add(control) {
		cancel()
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer h.registry.remove(control)
	defer cancel()
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	if conn.Subprotocol() != agentprotocol.ProtocolV1 {
		_ = conn.Close(websocket.StatusProtocolError, "protocol required")
		return
	}
	readCtx, stopHello := context.WithTimeout(ctx, helloTimeout)
	messageType, data, err := conn.Read(readCtx)
	stopHello()
	if err != nil || messageType != websocket.MessageText {
		_ = conn.Close(websocket.StatusPolicyViolation, "hello required")
		return
	}
	envelope, err := agentprotocol.Decode(data)
	if err != nil || envelope.Kind != agentprotocol.KindHello {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid hello")
		return
	}
	var hello agentprotocol.Hello
	if json.Unmarshal(envelope.Payload, &hello) != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid hello")
		return
	}
	accepted, err := h.runtime.Open(ctx, token, agentprotocol.Envelope[agentprotocol.Hello]{
		Kind: envelope.Kind, MessageID: envelope.MessageID,
		SessionGeneration: envelope.SessionGeneration, Payload: hello,
	})
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "agent session rejected")
		return
	}
	control.readyMu.Lock()
	control.session = &accepted
	control.readyMu.Unlock()
	welcome, err := json.Marshal(accepted.Welcome)
	if err != nil || control.enqueue(ctx, outbound{body: welcome}) != nil {
		_ = conn.Close(websocket.StatusInternalError, "session unavailable")
		return
	}

	var workers sync.WaitGroup
	workers.Go(func() { h.writeLoop(ctx, control) })
	workers.Go(func() { h.monitorCredential(ctx, control, token) })
	h.readLoop(ctx, control, token)
	cancel()
	workers.Wait()
}

func (h *Handler) readLoop(ctx context.Context, control *sessionControl, token string) {
	for {
		messageType, data, err := control.conn.Read(ctx)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			_ = control.conn.Close(websocket.StatusUnsupportedData, "text messages required")
			return
		}
		envelope, err := agentprotocol.Decode(data)
		if err != nil {
			_ = control.conn.Close(websocket.StatusPolicyViolation, "invalid protocol message")
			return
		}
		control.readyMu.RLock()
		session := *control.session
		control.readyMu.RUnlock()
		switch envelope.Kind {
		case agentprotocol.KindHeartbeat:
			var heartbeat agentprotocol.Heartbeat
			if json.Unmarshal(envelope.Payload, &heartbeat) != nil {
				_ = control.conn.Close(websocket.StatusPolicyViolation, "invalid heartbeat")
				return
			}
			ack, err := h.runtime.Renew(ctx, token, session,
				agentprotocol.Envelope[agentprotocol.Heartbeat]{Kind: envelope.Kind, MessageID: envelope.MessageID,
					SessionGeneration: envelope.SessionGeneration, Payload: heartbeat}, time.Now().UTC())
			if err != nil {
				_ = control.conn.Close(websocket.StatusPolicyViolation, "heartbeat rejected")
				return
			}
			body, err := json.Marshal(ack)
			if err != nil || control.enqueue(ctx, outbound{body: body}) != nil {
				_ = control.conn.Close(websocket.StatusTryAgainLater, "session overloaded")
				return
			}
		case agentprotocol.KindConfigurationApplied:
			var applied agentprotocol.ConfigurationApplied
			if json.Unmarshal(envelope.Payload, &applied) != nil || h.runtime.Applied(ctx, token, session,
				agentprotocol.Envelope[agentprotocol.ConfigurationApplied]{Kind: envelope.Kind, MessageID: envelope.MessageID,
					SessionGeneration: envelope.SessionGeneration, Payload: applied}) != nil {
				_ = control.conn.Close(websocket.StatusPolicyViolation, "configuration acknowledgement rejected")
				return
			}
		default:
			_ = control.conn.Close(websocket.StatusPolicyViolation, "unsupported protocol operation")
			return
		}
	}
}

func (h *Handler) writeLoop(ctx context.Context, control *sessionControl) {
	for {
		select {
		case item := <-control.queue:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := control.conn.Write(writeCtx, websocket.MessageText, item.body)
			cancel()
			if err != nil {
				control.cancel()
				return
			}
			control.pending.Add(-int64(len(item.body)))
			if item.closeAfter {
				_ = control.conn.Close(item.closeCode, item.closeReason)
				control.cancel()
				return
			}
		case <-control.shutdown:
			control.readyMu.RLock()
			session := control.session
			control.readyMu.RUnlock()
			if session != nil {
				message := agentprotocol.Envelope[agentprotocol.Shutdown]{
					Kind: agentprotocol.KindShutdown, MessageID: "shutdown",
					SessionGeneration: strconv.FormatInt(session.Generation, 10),
					Payload:           agentprotocol.Shutdown{Reason: "server_shutdown"},
				}
				if body, err := json.Marshal(message); err == nil {
					writeCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
					_ = control.conn.Write(writeCtx, websocket.MessageText, body)
					cancel()
				}
			}
			_ = control.conn.Close(websocket.StatusGoingAway, "server shutdown")
			control.cancel()
			return
		case <-ctx.Done():
			return
		}
	}
}

func (h *Handler) monitorCredential(ctx context.Context, control *sessionControl, token string) {
	ticker := time.NewTicker(revocationPolling)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := h.access.Authenticate(ctx, token); err == nil {
				continue
			}
			control.readyMu.RLock()
			session := control.session
			control.readyMu.RUnlock()
			if session != nil {
				message := agentprotocol.Envelope[agentprotocol.Revoked]{
					Kind: agentprotocol.KindRevoked, MessageID: "revoked",
					SessionGeneration: strconv.FormatInt(session.Generation, 10),
					Payload:           agentprotocol.Revoked{Reason: "credential_revoked"},
				}
				if body, err := json.Marshal(message); err == nil {
					if err := control.enqueue(ctx, outbound{body: body, closeAfter: true,
						closeCode: websocket.StatusPolicyViolation, closeReason: "credential revoked"}); err != nil {
						control.forceClose()
					}
				}
			}
			return
		}
	}
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 || len(values[0]) > 2048 {
		return "", false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func (c *sessionControl) enqueue(ctx context.Context, value outbound) error {
	length := int64(len(value.body))
	if length == 0 || length > agentprotocol.MaxMessageBytes {
		return agentprotocol.ErrMessageTooLarge
	}
	for {
		current := c.pending.Load()
		if current+length > agentprotocol.MaxQueueBytes {
			return errors.New("agentws: outbound queue byte limit reached")
		}
		if c.pending.CompareAndSwap(current, current+length) {
			break
		}
	}
	select {
	case c.queue <- value:
		return nil
	case <-ctx.Done():
		c.pending.Add(-length)
		return ctx.Err()
	}
}
