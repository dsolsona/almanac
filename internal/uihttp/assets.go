package uihttp

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/dsolsona/almanac/internal/httpgzip"
	webassets "github.com/dsolsona/almanac/web"
)

var fingerprintedAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.`)

// NewAssetHandler loads and serves the embedded production UI assets.
func NewAssetHandler() (http.Handler, error) {
	return embeddedAssetHandler()
}

var embeddedAssetHandler = sync.OnceValues(func() (http.Handler, error) {
	assets, err := webassets.Assets()
	if err != nil {
		return nil, err
	}
	handler, err := staticHandler(assets)
	if err != nil {
		return nil, err
	}
	return guardPaths(handler), nil
})

func staticHandler(assets fs.FS) (http.Handler, error) {
	// Immutable text assets are compressed once, never on the request path.
	compressed := make(map[string][]byte)
	if err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (path.Ext(name) != ".js" && path.Ext(name) != ".css" && path.Ext(name) != ".html") {
			return nil
		}
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write(body); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		if buffer.Len() < len(body) {
			compressed[name] = buffer.Bytes()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if r.URL.Path == "/" || r.URL.Path == "/agents" || r.URL.Path == "/datasources" || r.URL.Path == "/datasources/new" || datasourceDetailPath.MatchString(r.URL.Path) || r.URL.Path == "/slis" || r.URL.Path == "/slis/new" || sliDetailPath.MatchString(r.URL.Path) || r.URL.Path == "/slos" || r.URL.Path == "/slos/new" || sloDetailPath.MatchString(r.URL.Path) || serviceDetailPath.MatchString(r.URL.Path) {
			name = "index.html"
		}
		file, err := assets.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if fingerprintedAsset.MatchString(path.Base(name)) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		reader, ok := file.(io.ReadSeeker)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if body, exists := compressed[name]; exists {
			w.Header().Add("Vary", "Accept-Encoding")
			if r.Header.Get("Range") == "" && httpgzip.Accepted(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
				w.Header().Set("Content-Encoding", "gzip")
				reader = bytes.NewReader(body)
			}
		}
		http.ServeContent(w, r, name, info.ModTime(), reader)
	}), nil
}

var datasourceDetailPath = regexp.MustCompile(`^/datasources/[0-9a-fA-F-]{36}$`)
var sliDetailPath = regexp.MustCompile(`^/slis/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var sloDetailPath = regexp.MustCompile(`^/slos/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var serviceDetailPath = regexp.MustCompile(`^/services/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
