package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
