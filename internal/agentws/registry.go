package agentws

import (
	"context"
	"errors"
	"sync"
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

func (c *sessionControl) forceClose() {
	c.cancel()
	_ = c.conn.CloseNow()
}
