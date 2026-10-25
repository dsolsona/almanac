package command

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/config"
	"github.com/dsolsona/almanac/internal/uihttp"
)

func TestVersionJSONIsOneDocumentAndDiagnosticsStayOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"version", "--output=json"}, &stdout, &stderr, BuildInfo{Version: "1.2.3", Commit: "abc", Date: "today"})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Count(stdout.String(), "{") != 1 || !strings.Contains(stdout.String(), `"schema_version":1`) || !strings.HasSuffix(stdout.String(), "\n") {
		t.Fatalf("unexpected JSON output %q", stdout.String())
	}
}

func TestHelpAndUnsupportedModes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--help"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "backend/ui/alerts") {
		t.Fatalf("help omits limitations: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"serve", "--components=backend"}, &stdout, &stderr, BuildInfo{}); code != 2 {
		t.Fatalf("unsupported mode exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "no listener was opened") || stdout.Len() != 0 {
		t.Fatalf("unexpected unsupported-mode output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestUsageErrorsUseExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"serve", "--unknown"}, &stdout, &stderr, BuildInfo{}); code != 2 {
		t.Fatalf("usage exit = %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("usage diagnostic missing")
	}
}

func TestRunServerForcesClosedInFlightProxyOnCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(backend.Close)

	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Listen:          address,
		Components:      "ui",
		BackendURL:      backend.URL,
		ShutdownTimeout: 100 * time.Millisecond,
	}
	server, err := uihttp.New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { done <- runServer(ctx, server, cfg, slog.New(slog.NewTextHandler(&stderr, nil)), &stderr) }()

	var connection net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err = net.DialTimeout("tcp", address, 20*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server did not listen: %v", err)
	}
	connection.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	responseDone := make(chan error, 1)
	go func() {
		response, err := client.Get(fmt.Sprintf("http://%s/api/v1/blocked", address))
		if response != nil {
			response.Body.Close()
		}
		responseDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("backend request did not start")
	}
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("runServer exit = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("runServer did not force close the active request within its bound")
	}
	select {
	case err := <-responseDone:
		if err == nil {
			t.Fatal("proxy request unexpectedly completed successfully after forced shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy request remained open after server shutdown")
	}
}
