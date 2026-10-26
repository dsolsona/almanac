package backendhttp

import (
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dsolsona/almanac/internal/agenthttp"
	"github.com/dsolsona/almanac/internal/agentmanagement"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/config"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/storage"
)

type Server struct {
	http        *http.Server
	databaseURL string
	ready       atomic.Bool
}

func LoadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New("backendhttp: TLS certificate and key are required together")
	}
	cert, err := secretfile.ReadCertificate(certFile)
	if err != nil {
		return nil, errors.New("backendhttp: TLS certificate file is invalid")
	}
	key, err := secretfile.Read(keyFile, 16384)
	if err != nil {
		return nil, errors.New("backendhttp: TLS key file is invalid")
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, errors.New("backendhttp: TLS key pair is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}

func New(ctx context.Context, cfg config.Config, service *authentication.Service, databaseURL string, tlsConfig *tls.Config, assets http.Handler) (*Server, error) {
	if ctx == nil || service == nil || databaseURL == "" || (cfg.Components != "backend" && !cfg.BackendUI()) || (cfg.BackendUI() && assets == nil) || (cfg.Components == "backend" && assets != nil) {
		return nil, errors.New("backendhttp: invalid configuration")
	}
	s := &Server{databaseURL: databaseURL}
	options := authhttp.Options{PublicOrigin: cfg.PublicOrigin, TrustedProxyCIDRs: cfg.TrustedProxyCIDRs, Now: time.Now, Preflight: s.authenticationPreflight}
	auth, err := authhttp.New(service, options)
	if err != nil {
		return nil, errors.New("backendhttp: invalid authentication configuration")
	}
	management, err := agentmanagement.New(databaseURL, service, time.Now)
	if err != nil {
		return nil, errors.New("backendhttp: invalid agent management configuration")
	}
	agents, err := agenthttp.New(management, auth, agenthttp.Options{Preflight: s.authenticationPreflight})
	if err != nil {
		return nil, errors.New("backendhttp: invalid agent HTTP configuration")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { writeHealth(w, http.StatusOK) })
	mux.HandleFunc("GET /readyz", s.readiness)
	for _, path := range []string{"/api/v1/auth/context", "/api/v1/auth/login", "/api/v1/auth/password", "/api/v1/auth/logout"} {
		mux.Handle(path, auth)
	}
	mux.Handle("/api/v1/agents", agents)
	mux.Handle("/api/v1/", agents)
	if cfg.BackendUI() {
		mux.Handle("/", assets)
	}
	var clonedTLS *tls.Config
	if tlsConfig != nil {
		clonedTLS = tlsConfig.Clone()
	}
	s.http = &http.Server{Handler: strictPaths(mux, agents), TLSConfig: clonedTLS, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	return s, nil
}

func (s *Server) SetProvisioned(value bool) { s.ready.Store(value) }
func (s *Server) Serve(listener net.Listener) error {
	if s.http.TLSConfig != nil {
		listener = tls.NewListener(listener, s.http.TLSConfig)
	}
	return s.http.Serve(listener)
}
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) Close() error                       { return s.http.Close() }

// RegisterOnShutdown registers an asynchronous notification after listeners close.
// Callbacks must not wait for requests to finish draining.
func (s *Server) RegisterOnShutdown(callback func()) { s.http.RegisterOnShutdown(callback) }

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if !s.ready.Load() {
		writeHealth(w, http.StatusServiceUnavailable)
		return
	}
	owner, err := storage.ReadInitialOwnerState(ctx, s.databaseURL)
	if err != nil || !owner.Provisioned {
		writeHealth(w, http.StatusServiceUnavailable)
		return
	}
	writeHealth(w, http.StatusOK)
}

func (s *Server) authenticationPreflight(requestCtx context.Context) error {
	ctx, cancel := context.WithTimeout(requestCtx, 2*time.Second)
	defer cancel()
	owner, err := storage.ReadInitialOwnerState(ctx, s.databaseURL)
	if err != nil || !owner.Provisioned || !s.ready.Load() {
		return errors.New("backendhttp: authentication setup unavailable")
	}
	return nil
}

type unwrapWriter struct{ http.ResponseWriter }

func (w unwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func strictPaths(next http.Handler, managed ...http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathValue := r.URL.Path
		if pathValue != r.URL.EscapedPath() || path.Clean(pathValue) != pathValue || strings.ContainsAny(pathValue, "\\%") {
			if len(managed) > 0 && (pathValue == "/api/v1/agents" || strings.HasPrefix(pathValue, "/api/v1/agents/")) {
				managed[0].ServeHTTP(unwrapWriter{w}, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(unwrapWriter{w}, r)
	})
}

func writeHealth(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
		_ = json.MarshalWrite(w, api.HealthResponse{Status: api.Unavailable, Component: api.Backend})
		return
	}
	_ = json.MarshalWrite(w, api.HealthResponse{Status: api.Ok, Component: api.Backend})
}
