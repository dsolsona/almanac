//go:build integration

package command

import (
	"io"
	"net/http"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Only the fixture coordinator owns lifecycle mutations. Agents and readers
// reconnect to the same HTTPS origin and PostgreSQL database after a restart.
type objectiveJourneyBackend struct {
	binary, baseURL string
	args            []string
	client          *http.Client
	logs            *runtimeLog
	process         *exec.Cmd
	done            chan struct{}
	err             error
}

func (b *objectiveJourneyBackend) start(t *testing.T) time.Duration {
	t.Helper()
	if b.process != nil {
		t.Fatal("isolated backend was started without joining the preceding process")
	}
	started := time.Now()
	process := exec.Command(b.binary, b.args...)
	process.Stdout, process.Stderr = io.Discard, b.logs
	if err := process.Start(); err != nil {
		t.Fatal("start isolated SLI journey backend executable failed")
	}
	b.process, b.done, b.err = process, make(chan struct{}), nil
	go func() { b.err = process.Wait(); close(b.done) }()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case <-b.done:
			t.Fatalf("SLI journey backend exited before readiness (failed=%t)", b.err != nil)
		default:
		}
		response, err := b.client.Get(b.baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return time.Since(started)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("SLI journey backend did not become live")
	return 0
}

func (b *objectiveJourneyBackend) stop(t *testing.T) time.Duration {
	t.Helper()
	if b.process == nil {
		return 0
	}
	started := time.Now()
	b.client.CloseIdleConnections()
	_ = b.process.Process.Signal(syscall.SIGTERM)
	select {
	case <-b.done:
		if b.err != nil {
			t.Error("SLI journey backend did not shut down cleanly")
		}
	case <-time.After(8 * time.Second):
		_ = b.process.Process.Kill()
		<-b.done
		t.Error("SLI journey backend exceeded its shutdown bound")
	}
	b.process = nil
	return time.Since(started)
}
