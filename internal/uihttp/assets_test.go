package uihttp

import (
	"compress/gzip"
	"encoding/json/v2"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	webassets "github.com/dsolsona/almanac/web"
)

func TestColdNavigationPreloadsOnlyItsPublicRouteDependencies(t *testing.T) {
	handler, err := NewAssetHandler()
	if err != nil {
		t.Fatal(err)
	}
	files, err := webassets.Assets()
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(files, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]assetChunk
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/slos", "/slos/new", "/slos/00000000-0000-0000-0000-000000000001", "/slis", "/agents", "/settings", "/settings/account", "/settings/password", "/settings/users", "/settings/users/new", "/settings/users/00000000-0000-0000-0000-000000000001"} {
		for _, compressed := range []bool{false, true} {
			request := httptest.NewRequest(http.MethodGet, route, nil)
			if compressed {
				request.Header.Set("Accept-Encoding", "gzip")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var reader io.Reader = response.Body
			if compressed {
				gzipReader, err := gzip.NewReader(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				defer gzipReader.Close()
				reader = gzipReader
			}
			body, err := io.ReadAll(reader)
			if err != nil || response.Code != 200 || response.Header().Get("Cache-Control") != "no-cache" {
				t.Fatalf("document %s: status %d, %v", route, response.Code, err)
			}
			for module, want := range map[string]bool{
				"src/settings/SettingsPage.tsx":                 settingsPath.MatchString(route),
				"src/slos/SlosPage.tsx":                         strings.HasPrefix(route, "/slos"),
				"src/slis/SlisPage.tsx":                         route == "/slis",
				"src/components/charts/TimelineChartView.tsx":   sloDetailPath.MatchString(route),
				"src/components/charts/BudgetSparklinePlot.tsx": route == "/slos",
			} {
				link := `<link rel="modulepreload" crossorigin href="/` + manifest[module].File + `">`
				if strings.Contains(string(body), link) != want {
					t.Fatalf("%s preload on %s: want %v", module, route, want)
				}
			}
			if strings.Contains(string(body), "/api/") {
				t.Fatal("public code preload must not introduce protected-data requests")
			}
		}
	}
}

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
