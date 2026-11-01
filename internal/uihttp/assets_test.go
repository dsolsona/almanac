package uihttp

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	webassets "github.com/dsolsona/almanac/web"
)

func TestEmbeddedAssetsNegotiateCachedGzipAndPreserveByteRanges(t *testing.T) {
	handler, err := NewAssetHandler()
	if err != nil {
		t.Fatal(err)
	}
	name := fingerprintedAssetPath(t)
	files, err := webassets.Assets()
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(files, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"gzip", "GZip; q=0.5", "gzip;q=0, *;q=1", "identity"} {
		request := httptest.NewRequest(http.MethodGet, "/"+name, nil)
		request.Header.Set("Accept-Encoding", header)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		compressed := header == "gzip" || header == "GZip; q=0.5"
		if response.Code != 200 || (response.Header().Get("Content-Encoding") == "gzip") != compressed || !strings.Contains(response.Header().Get("Vary"), "Accept-Encoding") || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("asset negotiation failed for %s: %d %v", header, response.Code, response.Header())
		}
		var body []byte
		if compressed {
			if response.Body.Len() >= len(want) {
				t.Fatal("compressed asset did not reduce transfer bytes")
			}
			reader, err := gzip.NewReader(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			body = response.Body.Bytes()
		}
		if string(body) != string(want) {
			t.Fatal("compression changed the production asset")
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/"+name, nil)
		request.Header.Set("Accept-Encoding", "gzip")
		request.Header.Set("Range", "bytes=0-11")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusPartialContent || response.Header().Get("Content-Encoding") != "" || response.Header().Get("Content-Range") != "bytes 0-11/"+strconv.Itoa(len(want)) {
			t.Fatalf("range representation changed: %d %v", response.Code, response.Header())
		}
		if method == http.MethodGet && response.Body.String() != string(want[:12]) || method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatal("incorrect asset range/HEAD body")
		}
	}
}
