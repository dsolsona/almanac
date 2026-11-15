package alerthttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
)

func TestAlertRoutesRequireSessionAndCSRF(t *testing.T) {
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://example.test", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New("postgres://example.test/db", auth, Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/alert-policies", "/alerts", "/alert-processing", "/alert-commands", "/alerts/11111111-1111-4111-8111-111111111111/acknowledge"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			r := httptest.NewRequest(method, "https://example.test/api/v1"+path, nil)
			r.TLS = &tls.ConnectionState{}
			r.RemoteAddr = "192.0.2.20:443"
			want := http.StatusUnauthorized
			if method == http.MethodPost {
				r.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: secret})
				r.Header.Set("Origin", "https://example.test")
				want = http.StatusForbidden
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
				t.Fatalf("%s %s: status=%d headers=%v body=%s", method, path, w.Code, w.Header(), w.Body.String())
			}
		}
	}
}

func TestAlertPolicyInputProfile(t *testing.T) {
	conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4"}, {ID: "sustained", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: "6"}, {ID: "custom", Short: 2 * time.Minute, Long: 2 * time.Hour, Threshold: "2"}}
	if field := validateConditions(conditions); field != "" {
		t.Fatalf("more than two conditions rejected: %s", field)
	}
	for _, test := range []struct {
		name   string
		change func([]alert.Condition) []alert.Condition
	}{
		{"empty", func(c []alert.Condition) []alert.Condition { return nil }},
		{"many", func(c []alert.Condition) []alert.Condition { return make([]alert.Condition, 9) }},
		{"duplicate", func(c []alert.Condition) []alert.Condition { c[1].ID = c[0].ID; return c }},
		{"threshold exponent", func(c []alert.Condition) []alert.Condition { c[0].Threshold = "1e1"; return c }},
		{"threshold zero", func(c []alert.Condition) []alert.Condition { c[0].Threshold = "0"; return c }},
		{"threshold length", func(c []alert.Condition) []alert.Condition { c[0].Threshold = strings.Repeat("1", 33); return c }},
		{"short after long", func(c []alert.Condition) []alert.Condition { c[0].Short = 2 * time.Hour; return c }},
		{"unaligned", func(c []alert.Condition) []alert.Condition { c[0].Short = 61 * time.Second; return c }},
		{"too long", func(c []alert.Condition) []alert.Condition { c[0].Long = 31 * 24 * time.Hour; return c }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if field := validateConditions(test.change(append([]alert.Condition{}, conditions...))); field == "" {
				t.Fatal("invalid condition accepted")
			}
		})
	}
	for _, name := range []string{"", " ", "Checkout\n", strings.Repeat("x", 257)} {
		if validName(name) {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	if !validName(strings.Repeat("é", 256)) {
		t.Fatal("valid Unicode name rejected")
	}
}

func TestAlertStrictBodyBoundary(t *testing.T) {
	for _, test := range []struct {
		name, contentType, body string
		want                    int
	}{
		{"valid", "application/json", `{"name":"Checkout"}`, 0},
		{"unknown", "application/json", `{"name":"Checkout","extra":true}`, 400},
		{"duplicate", "application/json", `{"name":"Checkout","name":"Other"}`, 400},
		{"trailing", "application/json", `{"name":"Checkout"}{}`, 400},
		{"null", "application/json", `null`, 400},
		{"null field", "application/json", `{"name":null}`, 400},
		{"array", "application/json", `[]`, 400},
		{"empty", "application/json", ``, 400},
		{"oversize", "application/json", `{"name":"` + strings.Repeat("x", 65536) + `"}`, 413},
		{"media", "text/plain", `{}`, 415},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/alert-policies", strings.NewReader(test.body))
			r.Header.Set("Content-Type", test.contentType)
			var value struct {
				Name string `json:"name"`
			}
			if status := decodeBody(r, &value); status != test.want {
				t.Fatalf("status=%d want=%d", status, test.want)
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/alert-policies", strings.NewReader(`{}`))
	r.Header.Add("Content-Type", "application/json")
	r.Header.Add("Content-Type", "application/json")
	var value struct{}
	if status := decodeBody(r, &value); status != 400 {
		t.Fatalf("duplicate media header accepted: %d", status)
	}
}

func TestAlertPolicyValidationProblemIsSafe(t *testing.T) {
	w := httptest.NewRecorder()
	storageError(w, alert.ErrPolicy, "request")
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"field":"conditions"`) {
		t.Fatalf("invalid policy is not actionable: %d %s", w.Code, w.Body.String())
	}
}

func TestAlertVersionAndQueryBoundary(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	if version, ok := parseETag([]string{etag("alert-policy", id, 2)}, "alert-policy", id); !ok || version != 2 {
		t.Fatalf("valid ETag rejected: %d %v", version, ok)
	}
	for _, headers := range [][]string{
		{}, {`*`}, {`W/"alert-policy:` + id + `:2"`}, {`"alert-policy:` + id + `:01"`},
		{`"alert-policy:` + id + `:0"`}, {`"alert-occurrence:` + id + `:1"`},
		{etag("alert-policy", id, 1), etag("alert-policy", id, 2)},
	} {
		if _, valid := parseETag(headers, "alert-policy", id); valid {
			t.Fatalf("invalid ETag accepted: %v", headers)
		}
	}
	if limit, cursor, valid := pageQuery(url.Values{}, "q"); !valid || limit != 20 || cursor != "" {
		t.Fatalf("default page: %d %q %v", limit, cursor, valid)
	}
	for _, query := range []url.Values{
		{"limit": {"0"}}, {"limit": {"201"}}, {"limit": {"020"}}, {"limit": {"20", "20"}},
		{"cursor": {strings.Repeat("x", 1025)}}, {"q": {""}}, {"q": {strings.Repeat("x", 257)}}, {"unknown": {"x"}},
		{"q": {string(rune(0))}},
	} {
		if _, _, valid := pageQuery(query, "q"); valid {
			t.Fatalf("invalid query accepted: %v", query)
		}
	}
}
