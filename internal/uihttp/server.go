package uihttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/config"
)

var ErrServerClosed = http.ErrServerClosed

type Server struct {
	http    *http.Server
	log     *slog.Logger
	backend *url.URL
	client  *http.Client
}

func New(cfg config.Config, logger *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	target, err := url.Parse(cfg.BackendURL)
	if err != nil {
		return nil, err
	}
	assets, err := NewAssetHandler()
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	s := &Server{log: logger, backend: target, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	mux := http.NewServeMux()
	health := healthHandler{server: s}
	mux.HandleFunc("GET /livez", health.GetLiveness)
	mux.HandleFunc("GET /readyz", health.GetReadiness)
	mux.Handle("/api/v1/", s.proxy())
	mux.Handle("/", assets)
	s.http = &http.Server{Handler: guardPaths(mux), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s, nil
}

func (s *Server) Serve(listener net.Listener) error { return s.http.Serve(listener) }

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) Close() error                       { return s.http.Close() }

type healthHandler struct{ server *Server }

func (h healthHandler) GetLiveness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeHealth(w, api.HealthResponse{Status: api.HealthResponseStatusOk, Component: api.Ui})
}

func (h healthHandler) GetReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	check, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.backend.ResolveReference(&url.URL{Path: "/readyz"}).String(), nil)
	if err != nil {
		writeUnavailable(w)
		return
	}
	response, err := h.server.client.Do(check)
	if err != nil {
		writeUnavailable(w)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeUnavailable(w)
		return
	}
	writeHealth(w, api.HealthResponse{Status: api.HealthResponseStatusOk, Component: api.Ui})
}

func writeHealth(w http.ResponseWriter, value api.HealthResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, value)
}

func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.MarshalWrite(w, api.HealthResponse{Status: api.HealthResponseStatusUnavailable, Component: api.Ui})
}

func (s *Server) proxy() http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: s.client.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := s.backend.Clone()
			target.RawQuery = ""
			pr.SetURL(target)
			pr.Out.Host = s.backend.Host
			for name := range pr.Out.Header {
				lowerName := strings.ToLower(name)
				if strings.HasPrefix(lowerName, "x-forwarded-") || slices.Contains([]string{"forwarded", "x-real-ip", "x-client-ip", "client-ip", "true-client-ip", "cf-connecting-ip"}, lowerName) {
					pr.Out.Header.Del(name)
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				s.log.Error("cannot create request id")
				id = [16]byte{}
			}
			requestID := hex.EncodeToString(id[:])
			s.log.Warn("backend proxy unavailable", "method", r.Method, "request_id", requestID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.MarshalWrite(w, api.ProxyProblem{Code: api.BackendUnavailable, RequestId: requestID, Message: "Configured backend is unavailable"})
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedProxyMethod(r.Method) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !safeAPIPath(r) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func allowedProxyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func safeAPIPath(r *http.Request) bool {
	decoded, ok := decodePath(r.URL.EscapedPath())
	if !ok || !strings.HasPrefix(decoded, "/api/v1/") {
		return false
	}
	return true
}

func guardPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoded, ok := decodePath(r.URL.EscapedPath())
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && (!strings.HasPrefix(decoded, "/api/v1/") || hasDotSegment(decoded)) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodePath(value string) (string, bool) {
	decoded := value
	for range 3 {
		next, err := url.PathUnescape(decoded)
		if err != nil || strings.Contains(next, "\\") {
			return "", false
		}
		if next == decoded {
			return decoded, !hasDotSegment(decoded)
		}
		decoded = next
	}
	if strings.Contains(decoded, "%") {
		return "", false
	}
	return decoded, !hasDotSegment(decoded)
}

func hasDotSegment(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
