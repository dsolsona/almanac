package agentws

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"sync"
	"uuid"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

// Registry owns upgraded sessions because net/http does not drain hijacked
// WebSocket connections during Server.Shutdown.
type Registry struct {
	mu       sync.Mutex
	sessions map[*sessionControl]struct{}
	stopping bool
}

func NewRegistry() *Registry { return &Registry{sessions: make(map[*sessionControl]struct{})} }

func (r *Registry) add(session *sessionControl) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping {
		return false
	}
	r.sessions[session] = struct{}{}
	return true
}

func (r *Registry) remove(session *sessionControl) {
	r.mu.Lock()
	delete(r.sessions, session)
	r.mu.Unlock()
	close(session.done)
}

func (r *Registry) Drain(ctx context.Context) error {
	r.mu.Lock()
	r.stopping = true
	sessions := make([]*sessionControl, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, session := range sessions {
		session.readyMu.RLock()
		ready := session.ready
		session.readyMu.RUnlock()
		if ready {
			session.stopOnce.Do(func() { close(session.shutdown) })
		} else {
			session.forceClose()
		}
	}
	for _, session := range sessions {
		select {
		case <-session.done:
		case <-ctx.Done():
			r.Close()
			return ctx.Err()
		}
	}
	return nil
}

func (r *Registry) Close() error {
	r.mu.Lock()
	r.stopping = true
	sessions := make([]*sessionControl, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	var closeErr error
	for _, session := range sessions {
		session.stopOnce.Do(func() { close(session.shutdown) })
		session.cancel()
		if err := session.conn.CloseNow(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

func (r *Registry) NotifyConfigurationChanged(ctx context.Context, agentID string, revision int64) error {
	parsed, err := uuid.Parse(agentID)
	if r == nil || err != nil || parsed.String() != agentID || revision <= 0 {
		return errors.New("agentws: invalid configuration notification")
	}
	r.mu.Lock()
	sessions := make([]*sessionControl, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, control := range sessions {
		control.readyMu.RLock()
		session := control.session
		ready := control.ready
		if !ready || session == nil || session.AgentID != agentID {
			control.readyMu.RUnlock()
			continue
		}
		generation := strconv.FormatInt(session.Generation, 10)
		control.readyMu.RUnlock()
		message := agentprotocol.Envelope[agentprotocol.ConfigurationChanged]{
			Kind: agentprotocol.KindConfigurationChanged, MessageID: "configuration-" + strconv.FormatInt(revision, 10),
			SessionGeneration: generation, Payload: agentprotocol.ConfigurationChanged{DesiredRevision: strconv.FormatInt(revision, 10)},
		}
		body, err := json.Marshal(message)
		if err != nil {
			return errors.New("agentws: configuration notification encoding failed")
		}
		return control.enqueue(ctx, outbound{body: body})
	}
	return nil
}

func (r *Registry) SessionGeneration(agentID string) (int64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	sessions := make([]*sessionControl, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, control := range sessions {
		control.readyMu.RLock()
		session, ready := control.session, control.ready
		if ready && session != nil && session.AgentID == agentID {
			generation := session.Generation
			control.readyMu.RUnlock()
			return generation, true
		}
		control.readyMu.RUnlock()
	}
	return 0, false
}

func (r *Registry) SendDiagnostic(ctx context.Context, agentID string, generation int64, request agentprotocol.DiagnosticRequest) error {
	if r == nil || generation <= 0 || request.AgentID != agentID || request.DiagnosticID == "" {
		return errors.New("agentws: invalid diagnostic dispatch")
	}
	r.mu.Lock()
	sessions := make([]*sessionControl, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, control := range sessions {
		control.readyMu.RLock()
		session, ready := control.session, control.ready
		if !ready || session == nil || session.AgentID != agentID || session.Generation != generation {
			control.readyMu.RUnlock()
			continue
		}
		control.readyMu.RUnlock()
		message := agentprotocol.Envelope[agentprotocol.DiagnosticRequest]{Kind: agentprotocol.KindDiagnosticRequest,
			MessageID: "diagnostic-" + request.DiagnosticID, SessionGeneration: strconv.FormatInt(generation, 10), Payload: request}
		if err := message.Validate(); err != nil {
			return errors.New("agentws: invalid diagnostic message")
		}
		body, err := json.Marshal(message)
		if err != nil {
			return errors.New("agentws: diagnostic message encoding failed")
		}
		return control.enqueue(ctx, outbound{body: body})
	}
	return errors.New("agentws: diagnostic agent session unavailable")
}

func (r *Registry) CancelDiagnostic(ctx context.Context, agentID string, generation int64, diagnosticID string) error {
	if r == nil || generation <= 0 || diagnosticID == "" {
		return errors.New("agentws: invalid diagnostic cancellation")
	}
	r.mu.Lock()
	sessions := make([]*sessionControl, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, control := range sessions {
		control.readyMu.RLock()
		session, ready := control.session, control.ready
		if !ready || session == nil || session.AgentID != agentID || session.Generation != generation {
			control.readyMu.RUnlock()
			continue
		}
		control.readyMu.RUnlock()
		message := agentprotocol.Envelope[agentprotocol.DiagnosticCancel]{Kind: agentprotocol.KindDiagnosticCancel,
			MessageID: "cancel-" + diagnosticID, SessionGeneration: strconv.FormatInt(generation, 10), Payload: agentprotocol.DiagnosticCancel{DiagnosticID: diagnosticID}}
		if err := message.Validate(); err != nil {
			return errors.New("agentws: invalid diagnostic cancellation message")
		}
		body, err := json.Marshal(message)
		if err != nil {
			return errors.New("agentws: diagnostic cancellation encoding failed")
		}
		return control.enqueue(ctx, outbound{body: body})
	}
	return errors.New("agentws: diagnostic agent session unavailable")
}

func (c *sessionControl) forceClose() {
	c.cancel()
	_ = c.conn.CloseNow()
}
