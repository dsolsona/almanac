package command

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// Metrics have their own listener and mux. They never inherit public auth/API
// routes, and neither public server mode gains a /metrics or profiling route.
type metricsServer struct {
	server *http.Server
	done   <-chan struct{}
	failed <-chan struct{}
}

func startMetricsServer(address string, handler http.Handler) (metricsServer, error) {
	if address == "" {
		return metricsServer{}, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return metricsServer{}, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	done, failed := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			close(failed)
		}
	}()
	return metricsServer{server: server, done: done, failed: failed}, nil
}

func (m metricsServer) close() {
	if m.server != nil {
		_ = m.server.Close()
		<-m.done
	}
}
