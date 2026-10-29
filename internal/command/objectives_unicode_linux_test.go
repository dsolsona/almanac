//go:build linux

package command

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/cliprofile"
)

func TestOperationalOverviewCommandSendsMultibyteQueryThroughClient(t *testing.T) {
	query := strings.Repeat("é", 65)
	var received string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			_, _ = w.Write([]byte(`{"csrf_token":"csrf","session":{"user_id":"user-1","login":"owner","role":"administrator","restricted":false}}`))
		case "/api/v1/slos/overview":
			received = r.URL.Query().Get("q")
			_, _ = w.Write([]byte(`{"snapshot_id":"11111111-1111-1111-1111-111111111111","items":[],"group_counts":{"needs_attention":0,"watch":0,"healthy":0},"next_cursor":"","previous_cursor":""}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	profilePath := filepath.Join(t.TempDir(), "profile.json")
	profile, err := cliprofile.Open(t.Context(), profilePath, true)
	if err != nil {
		t.Fatal(err)
	}
	validSession := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if err := profile.Save(cliprofile.Record{SchemaVersion: 1, BackendURL: server.URL, CAPEM: string(certificate), Session: validSession}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := objectivesCommand(t.Context(), "slos", []string{"overview", "--profile-file=" + profilePath, "--output=json", "--q=" + query}, &stdout, &stderr)
	if code != 0 || received != query || !strings.Contains(stdout.String(), `"group_counts"`) {
		t.Fatalf("CLI overview code=%d received query runes=%d want=%d stdout=%s stderr=%s", code, len([]rune(received)), len([]rune(query)), stdout.String(), stderr.String())
	}
}
