package backendhttp

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/telemetry"
)

type browserAllowance struct {
	at    time.Time
	count int
}
type browserLimits struct {
	mu    sync.Mutex
	users map[[32]byte]browserAllowance
}

func (l *browserLimits) allow(user [32]byte, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, value := range l.users {
		if now.Sub(value.at) >= time.Minute {
			delete(l.users, key)
		}
	}
	value, exists := l.users[user]
	if !exists && len(l.users) >= 256 {
		return false
	}
	if value.count >= 60 {
		return false
	}
	if !exists {
		value.at = now
	}
	value.count++
	l.users[user] = value
	return true
}

func browserTelemetry(auth *authhttp.Handler, service *authentication.Service) http.Handler {
	limits := &browserLimits{users: make(map[[32]byte]browserAllowance)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !telemetry.Enabled() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		proof, err := auth.ValidateSessionRequest(r)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		access, err := service.Inspect(r.Context(), proof.SessionSecret)
		if err != nil || access.Restricted {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		verifier, err := identity.SessionSecretVerifier(proof.SessionSecret)
		if err != nil || !limits.allow(verifier, time.Now()) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" || len(r.Header.Values("Content-Encoding")) != 0 {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, telemetry.BrowserBodyLimit))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		clean, err := telemetry.SanitizeBrowser(body, time.Now())
		if err != nil {
			if reason, ok := errors.AsType[telemetry.BrowserValidationError](err); ok {
				w.Header().Set("X-Almanac-Telemetry-Rejection", string(reason))
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := telemetry.BrowserSink(r.Context(), clean); err != nil {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{}"))
	})
}
