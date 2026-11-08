package userhttp

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
)

func TestAccountTransportBoundaryRejectsBeforeDatabaseAccess(t *testing.T) {
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://example.test", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := New("invalid-database", "https://example.test", auth, nil, time.Now, nil)
	for _, tc := range []struct {
		method, path, origin, cookie string
		tls                          bool
		status                       int
	}{
		{"GET", "/api/v1/users", "", "", true, 401},
		{"GET", "/api/v1/account", "", "", false, 403},
		{"POST", "/api/v1/users", "https://foreign.test", "", true, 403},
		{"POST", "/api/v1/account/activation", "https://example.test", "", true, 403},
		{"POST", "/api/v1/account/activation", "https://example.test", "", false, 403},
		{"POST", "/api/v1/account/activation/context", "https://example.test", "", true, 403},
	} {
		t.Run(tc.method+tc.path+tc.origin, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://example.test"+tc.path, strings.NewReader(`{}`))
			r.TLS = nil
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.cookie != "" {
				r.Header.Set("Cookie", tc.cookie)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "database") {
				t.Fatalf("boundary status=%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestUserBodyStrictAndBounded(t *testing.T) {
	h := &Handler{}
	for _, body := range []string{`null`, `[]`, `{"name":"one","name":"two"}`, `{"unknown":"x"}`, `{"name":"x"} {}`, `{"name":"` + strings.Repeat("x", 17<<10) + `"}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var value struct {
			Name string `json:"name"`
		}
		if h.decode(w, r, &value, "request") || w.Code < 400 {
			t.Fatal("accepted invalid or unbounded JSON")
		}
	}
}

func TestUserETagIsSingleStrongTargetBoundVersion(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, tag := range []string{`W/"user-` + id + `-1"`, `*`, `"user-00000000-0000-4000-8000-000000000002-1"`, `"user-` + id + `-1", "user-` + id + `-2"`} {
		r := httptest.NewRequest("PUT", "/", nil)
		r.Header.Set("If-Match", tag)
		if got := version(r, id); got != "" {
			t.Fatalf("invalid ETag accepted: %q", got)
		}
	}
	r := httptest.NewRequest("PUT", "/", nil)
	r.Header.Set("If-Match", `"user-`+id+`-1"`)
	if version(r, id) != "1" {
		t.Fatal("strong target version rejected")
	}
	r.Header.Add("If-Match", `"user-`+id+`-2"`)
	if version(r, id) != "" {
		t.Fatal("duplicate header accepted")
	}
}
