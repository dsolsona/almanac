package uihttp

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	webassets "github.com/dsolsona/almanac/web"
)

var fingerprintedAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.`)

// NewAssetHandler loads and serves the embedded production UI assets.
func NewAssetHandler() (http.Handler, error) {
	assets, err := webassets.Assets()
	if err != nil {
		return nil, err
	}
	return guardPaths(staticHandler(assets)), nil
}

func staticHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if r.URL.Path == "/" || r.URL.Path == "/agents" {
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
		http.ServeContent(w, r, name, info.ModTime(), reader)
	})
}
