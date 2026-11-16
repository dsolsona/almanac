//go:build alertacceptance

package storage

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAlertAcceptanceSourcePauseAcknowledgement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pause-source")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	pause := alertAcceptanceSourcePause(t.Context(), logger, path)
	if pause() {
		t.Fatal("source paused without a request")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Calling the normal turn must observe the marker before touching a pool.
	// The acknowledgement therefore identifies the between-turn pause gate.
	runtime := &Runtime{}
	runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{pauseSource: pause})
	now := time.Date(2026, 11, 16, 22, 59, 0, 0, time.UTC)
	for range 2 {
		_, next := runtime.runDerivedTurn(t.Context(), logger, func() time.Time { return now }, time.Time{})
		if !next.Equal(now.Add(time.Second)) {
			t.Fatalf("paused normal worker did not defer: %v", next)
		}
	}
	if strings.Count(logs.String(), `"msg":"alert acceptance source worker paused"`) != 1 {
		t.Fatalf("normal source worker did not acknowledge its pause once: %s", logs.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if pause() || !strings.Contains(logs.String(), `"msg":"alert acceptance source worker resumed"`) {
		t.Fatalf("source did not resume explicitly: %s", logs.String())
	}
}

func TestAlertAcceptanceSourcePauseUnreadableDoesNotAcknowledge(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(path, path); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	pause := alertAcceptanceSourcePause(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), path)
	if !pause() || strings.Contains(logs.String(), `"msg":"alert acceptance source worker paused"`) || !strings.Contains(logs.String(), "pause_file_unreadable") {
		t.Fatalf("unreadable control became confirmed pause: %s", logs.String())
	}
}
