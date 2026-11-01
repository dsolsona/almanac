package backendhttp

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// Only operational reads without resource ETags are compressed. Authentication,
// credentials, agent traffic, websocket upgrades and embedded assets retain their
// existing transport/security contracts.
func compressedReadPath(path string) bool {
	if path == "/api/v1/slis" || path == "/api/v1/slos" || path == "/api/v1/services" || path == "/api/v1/slos/overview" {
		return true
	}
	if !strings.HasPrefix(path, "/api/v1/slos/") {
		return false
	}
	return strings.HasSuffix(path, "/operational") || strings.HasSuffix(path, "/status") || strings.HasSuffix(path, "/values") || strings.HasSuffix(path, "/preparation")
}

func acceptsGzip(header string) bool {
	wildcard := false
	for item := range strings.SplitSeq(header, ",") {
		parts := strings.Split(item, ";")
		encoding := strings.TrimSpace(parts[0])
		if !strings.EqualFold(encoding, "gzip") && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, param := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if strings.EqualFold(encoding, "gzip") {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}

func compressOperationalReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !compressedReadPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
			next.ServeHTTP(w, r)
			return
		}
		writer := &operationalGzipWriter{ResponseWriter: w}
		defer func() {
			if writer.gzip != nil {
				_ = writer.gzip.Close()
			}
		}()
		next.ServeHTTP(writer, r)
	})
}

type operationalGzipWriter struct {
	http.ResponseWriter
	gzip        *gzip.Writer
	wroteHeader bool
}

func (w *operationalGzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *operationalGzipWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	contentType := w.Header().Get("Content-Type")
	if status >= 200 && status != 204 && status != 304 && status != 206 && w.Header().Get("Content-Encoding") == "" && w.Header().Get("ETag") == "" && (strings.HasPrefix(contentType, "application/json") || strings.HasPrefix(contentType, "application/problem+json")) {
		compressor, err := gzip.NewWriterLevel(w.ResponseWriter, gzip.BestSpeed)
		if err != nil {
			panic("backendhttp: invalid fixed gzip level")
		}
		w.gzip = compressor
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *operationalGzipWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(200)
	}
	if w.gzip != nil {
		return w.gzip.Write(body)
	}
	return w.ResponseWriter.Write(body)
}
