package backendhttp

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperationalCompressionPreservesExactBytesAndHonorsNegotiation(t *testing.T) {
	body := `{"exact":"99999999999999999999999999.123456789012","samples":"` + strings.Repeat("unchanged-exact-value", 1000) + `"}`
	handler := compressOperationalReads(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	for _, test := range []struct {
		encoding   string
		compressed bool
	}{{"gzip, deflate, br", true}, {"gzip;q=0", false}, {"*;q=1", true}, {"gzip;q=0, *;q=1", false}, {"gzip;q=broken", false}, {"identity", false}} {
		request := httptest.NewRequest("GET", "/api/v1/slos/overview", nil)
		request.Header.Set("Accept-Encoding", test.encoding)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		value := response.Body.Bytes()
		if got := response.Header().Get("Content-Encoding") == "gzip"; got != test.compressed {
			t.Fatalf("encoding %q compressed=%t", test.encoding, got)
		}
		if test.compressed {
			if len(value) >= len(body)/4 {
				t.Fatal("repetitive operational payload did not shrink")
			}
			reader, err := gzip.NewReader(strings.NewReader(string(value)))
			if err != nil {
				t.Fatal(err)
			}
			value, err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		if string(value) != body || response.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatal("compression changed exact values or omitted negotiation metadata")
		}
	}
}

func TestCompressionLeavesCredentialAndStrongETagRepresentationsAlone(t *testing.T) {
	for _, path := range []string{"/api/auth/session", "/api/v1/agents/new-credential", "/api/agent/v1/configuration", "/api/v1/slos/11111111-1111-1111-1111-111111111111"} {
		handler := compressOperationalReads(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"resource:1"`)
			io.WriteString(w, `{"unchanged":true}`)
		}))
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Accept-Encoding", "gzip")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Header().Get("Content-Encoding") != "" || response.Body.String() != `{"unchanged":true}` || response.Header().Get("ETag") != `"resource:1"` {
			t.Fatalf("compression changed %s", path)
		}
	}
}
