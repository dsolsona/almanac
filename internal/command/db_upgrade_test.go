package command

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage"
)

func TestResolveDatabaseURLUsesExplicitFileThenEnvironmentPrecedence(t *testing.T) {
	first := protectedURLFile(t, "postgres://explicit:secret@127.0.0.1/db\n")
	second := protectedURLFile(t, "postgres://environment:secret@127.0.0.1/db")
	got, err := resolveDatabaseURL(true, first, []string{
		"ALMANAC_DATABASE_URL_FILE=" + second,
		"ALMANAC_DATABASE_URL=postgres://raw:secret@127.0.0.1/db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://explicit:secret@127.0.0.1/db" {
		t.Fatalf("selected explicit URL = %q", got)
	}

	got, err = resolveDatabaseURL(false, "", []string{
		"ALMANAC_DATABASE_URL_FILE=" + second,
		"ALMANAC_DATABASE_URL=postgres://raw:secret@127.0.0.1/db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://environment:secret@127.0.0.1/db" {
		t.Fatalf("selected environment file URL = %q", got)
	}
}

func TestResolveDatabaseURLRejectsEmptySelectedSourceWithoutFallback(t *testing.T) {
	valid := protectedURLFile(t, "postgres://fallback:secret@127.0.0.1/db")
	for _, test := range []struct {
		name     string
		explicit bool
		path     string
		environ  []string
	}{
		{name: "explicit empty", explicit: true, environ: []string{"ALMANAC_DATABASE_URL_FILE=" + valid}},
		{name: "environment file empty", environ: []string{"ALMANAC_DATABASE_URL_FILE=", "ALMANAC_DATABASE_URL=postgres://fallback:secret@127.0.0.1/db"}},
		{name: "raw URL empty", environ: []string{"ALMANAC_DATABASE_URL="}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveDatabaseURL(test.explicit, test.path, test.environ); err == nil {
				t.Fatal("empty selected source fell back or was accepted")
			}
		})
	}
}

func TestReadDatabaseURLFileChecksActualFilePermissionsAndBounds(t *testing.T) {
	path := protectedURLFile(t, "  postgres://mounted:secret@127.0.0.1/db \n")
	got, err := readDatabaseURLFile(path)
	if err != nil || got != "postgres://mounted:secret@127.0.0.1/db" {
		t.Fatalf("read protected URL = %q, error = %v", got, err)
	}

	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("postgres://mounted:secret@127.0.0.1/db"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "secret-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readDatabaseURLFile(link); err != nil {
		t.Fatalf("protected regular file reached through symlink was rejected: %v", err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readDatabaseURLFile(link); err == nil {
		t.Fatal("permissions on the opened file were not checked")
	}

	large := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(large, []byte("postgres://user:secret@127.0.0.1/db"+strings.Repeat("x", maxDatabaseURLBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDatabaseURLFile(large); err == nil {
		t.Fatal("oversized database URL file was accepted")
	}
}

func TestReadDatabaseURLFileRejectsFIFOPromptly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-url")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readDatabaseURLFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO database URL source was accepted")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("FIFO read blocked instead of rejecting a non-regular file")
	}
}

func TestDBUpgradeJSONConfigurationFailureIsOneRedactedDocument(t *testing.T) {
	t.Setenv("ALMANAC_DATABASE_URL", "postgres://operator:sentinel-secret@127.0.0.1/")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"db", "upgrade", "--output=json"}, &stdout, &stderr, BuildInfo{})
	if code != 2 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "sentinel-secret") || strings.Contains(stdout.String(), "operator") {
		t.Fatalf("JSON configuration error exposed credentials: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"schema_version":1`) || !strings.Contains(stdout.String(), `"operation":"db.upgrade"`) || !strings.Contains(stdout.String(), `"error"`) {
		t.Fatalf("unexpected JSON failure document %q", stdout.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("stdout is not one JSON document: %v", err)
	}
}

func TestDBUpgradeUnknownFlagDoesNotEchoFlagValue(t *testing.T) {
	secretArg := "--database-url=postgres://operator:sentinel-secret@127.0.0.1/db"
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"db", "upgrade", secretArg}, &stdout, &stderr, BuildInfo{})
	if code != 2 || !strings.Contains(stderr.String(), "invalid flags") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "sentinel-secret") || strings.Contains(stderr.String(), "operator") {
		t.Fatalf("usage diagnostic exposed flag value: %q", stderr.String())
	}
}

func TestDBUpgradeJSONFlagAndPositionalFailuresRemainJSON(t *testing.T) {
	for _, args := range [][]string{
		{"db", "upgrade", "--bad", "--output=json"},
		{"db", "upgrade", "--output=json", "--timeout=not-a-duration"},
		{"db", "upgrade", "unexpected", "--output=json"},
	} {
		t.Run(strings.Join(args[2:], "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, &stdout, &stderr, BuildInfo{}); code != 2 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			var document map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
				t.Fatalf("stdout is not one JSON error document: %v; output=%q", err, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("JSON failure wrote diagnostics to stderr: %q", stderr.String())
			}
		})
	}
}

func TestWriteUpgradeResultReturnsExitOneWhenHumanOutputFails(t *testing.T) {
	var stderr bytes.Buffer
	code := writeUpgradeResult(alwaysFailWriter{}, &stderr, false, storage.UpgradeResult{DatabaseSchemaVersion: 1, Applied: 1})
	if code != 1 || !strings.Contains(stderr.String(), "output failed") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestWriteUpgradeResultReturnsExitOneWhenJSONNewlineFails(t *testing.T) {
	var stderr bytes.Buffer
	stdout := failOnNewlineWriter{}
	code := writeUpgradeResult(&stdout, &stderr, true, storage.UpgradeResult{DatabaseSchemaVersion: 1, Applied: 1})
	if code != 1 || !strings.Contains(stderr.String(), "output failed") {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
}

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }

type failOnNewlineWriter struct{ bytes.Buffer }

func (w *failOnNewlineWriter) Write(data []byte) (int, error) {
	if bytes.Equal(data, []byte("\n")) {
		return 0, errors.New("newline write failed")
	}
	return w.Buffer.Write(data)
}

func protectedURLFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
