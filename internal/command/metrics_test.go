package command

import (
	"net"
	"net/http"
	"testing"
)

func TestMetricsListenerDisabledOrOwnedAndClosed(t *testing.T) {
	disabled, err := startMetricsServer("", http.NotFoundHandler())
	if err != nil || disabled.failed != nil || disabled.server != nil {
		t.Fatalf("disabled metrics opened a server: %v", err)
	}
	disabled.close()
	started, err := startMetricsServer("127.0.0.1:0", http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	started.close()
	select {
	case <-started.done:
	default:
		t.Fatal("metrics close did not join its listener")
	}
	select {
	case <-started.failed:
		t.Fatal("ordinary shutdown was reported as a listener failure")
	default:
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if unexpected, err := startMetricsServer(occupied.Addr().String(), http.NotFoundHandler()); err == nil {
		unexpected.close()
		t.Fatal("metrics silently adopted an occupied listener")
	}
}
