package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPrecedenceAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:9000","components":"ui","backend_url":"http://127.0.0.1:8081","shutdown_timeout":"45s"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, []string{"ALMANAC_LISTEN=127.0.0.1:9001", "ALMANAC_SHUTDOWN_TIMEOUT=1m"}, map[string]string{"listen": "127.0.0.1:9002"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9002" || cfg.ShutdownTimeout != time.Minute {
		t.Fatalf("unexpected precedence: %+v", cfg)
	}
}

func TestLoadRejectsInvalidFiles(t *testing.T) {
	cases := []struct{ name, body string }{
		{"unknown", `{"secret":"do-not-print"}`},
		{"duplicate", `{"listen":"a","listen":"b"}`},
		{"empty components", `{"components":""}`},
		{"empty listen", `{"listen":""}`},
		{"trailing", `{} {}`},
		{"malformed", `{"listen":`},
		{"not object", `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path, nil, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.name == "unknown" && strings.Contains(err.Error(), "do-not-print") {
				t.Fatal("error disclosed config contents")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, make([]byte, maxConfigBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil, nil); err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestValidateComponentsAndBackendOrigin(t *testing.T) {
	for _, value := range []string{"", "ui,", "ui,ui", "backend,,ui", "bogus"} {
		cfg := Defaults()
		cfg.Components = value
		if err := cfg.Validate(); err == nil {
			t.Errorf("components %q accepted", value)
		}
	}
	for _, value := range []string{"host", "host:service", "host:65536"} {
		cfg := Defaults()
		cfg.Listen = value
		if err := cfg.Validate(); err == nil {
			t.Errorf("listen address %q accepted", value)
		}
	}
	for _, value := range []string{
		"http://user:secret@host", "https://host/path", "http://host?q=secret", "http://host?",
		"ftp://host", "http://host/#x", "http://:8081", "http://host:", "http://host:0", "http://host:65536",
	} {
		cfg := Defaults()
		cfg.Components = "ui"
		cfg.BackendURL = value
		if err := cfg.Validate(); err == nil {
			t.Errorf("backend origin %q accepted", value)
		}
	}
	cfg := Defaults()
	cfg.Components = "ui"
	cfg.BackendURL = "https://backend.example"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.BackendURL = "https://backend.example:65535"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("maximum valid port rejected: %v", err)
	}
}

func TestBackendUIConfiguration(t *testing.T) {
	for _, components := range []string{"backend,ui", "ui,backend"} {
		t.Run(components, func(t *testing.T) {
			cfg := Defaults()
			cfg.Components = components
			cfg.DatabaseURLFile = "/db"
			cfg.PublicOrigin = "https://example.test"
			cfg.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if !cfg.BackendUI() {
				t.Fatal("combined mode not recognized")
			}
		})
	}
	for _, mutate := range []func(*Config){
		func(cfg *Config) { cfg.DatabaseURLFile = "" },
		func(cfg *Config) { cfg.PublicOrigin = "" },
		func(cfg *Config) { cfg.TrustedProxyCIDRs = nil },
	} {
		cfg := Defaults()
		cfg.Components = "backend,ui"
		cfg.DatabaseURLFile = "/db"
		cfg.PublicOrigin = "https://example.test"
		cfg.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatal("invalid combined security/database configuration accepted")
		}
	}
}

func TestBackendConfigurationPrecedenceAndProxyParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.json")
	body := `{"components":"backend","database_url_file":"/file/db","initial_admin_login":"file-owner","initial_admin_password_file":"/file/password","public_origin":"https://example.test","tls_cert_file":"/file/cert","tls_key_file":"/file/key","trusted_proxy_cidrs":["10.0.0.0/24"]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	environ := []string{"ALMANAC_DATABASE_URL_FILE=/env/db", "ALMANAC_INITIAL_ADMIN_LOGIN=env-owner", "ALMANAC_INITIAL_ADMIN_PASSWORD_FILE=/env/password", "ALMANAC_PUBLIC_ORIGIN=https://env.example.test", "ALMANAC_TLS_CERT_FILE=/env/cert", "ALMANAC_TLS_KEY_FILE=/env/key", "ALMANAC_TRUSTED_PROXY_CIDRS=192.0.2.0/24"}
	explicit := map[string]string{"database-url-file": "/flag/db", "initial-admin-login": "flag-owner", "initial-admin-password-file": "/flag/password", "public-origin": "https://flag.example.test", "tls-cert-file": "/flag/cert", "tls-key-file": "/flag/key", "trusted-proxy-cidrs": "198.51.100.0/24"}
	cfg, err := Load(path, environ, explicit)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURLFile != "/flag/db" || cfg.InitialAdminLogin != "flag-owner" || cfg.InitialAdminPasswordFile != "/flag/password" || cfg.PublicOrigin != "https://flag.example.test" || cfg.TLSCertFile != "/flag/cert" || cfg.TLSKeyFile != "/flag/key" || len(cfg.TrustedProxyCIDRs) != 1 || cfg.TrustedProxyCIDRs[0].String() != "198.51.100.0/24" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestSourceSecretKeysetConfigurationRequiresBackendPairAndHonorsPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.json")
	body := `{"components":"backend","database_url_file":"/file/db","public_origin":"https://example.test","trusted_proxy_cidrs":["127.0.0.1/32"],"source_secret_keyset_file":"/file/keyset","source_secret_keyset_id":"00000000-0000-4000-8000-000000000001"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, []string{"ALMANAC_SOURCE_SECRET_KEYSET_FILE=/env/keyset"}, map[string]string{"source-secret-keyset-file": "/flag/keyset"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceSecretKeysetFile != "/flag/keyset" || cfg.SourceSecretKeysetID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("source-secret keyset precedence mismatch: %+v", cfg)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.SourceSecretKeysetID = "" },
		func(c *Config) { c.SourceSecretKeysetFile = "" },
	} {
		invalid := cfg
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatal("partial source-secret keyset configuration was accepted")
		}
	}
	ui := Defaults()
	ui.Components = "ui"
	ui.BackendURL = "http://127.0.0.1:8080"
	ui.SourceSecretKeysetFile = "/keyset"
	ui.SourceSecretKeysetID = "00000000-0000-4000-8000-000000000001"
	if err := ui.Validate(); err == nil {
		t.Fatal("backend key material was accepted by UI-only configuration")
	}
}

func TestBackendJSONFieldsRejectWrongTypes(t *testing.T) {
	for _, field := range []string{"database_url_file", "initial_admin_login", "initial_admin_password_file", "public_origin", "tls_cert_file", "tls_key_file", "trusted_proxy_cidrs"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			value := `[]`
			if field == "trusted_proxy_cidrs" {
				value = `{}`
			}
			body := `{"` + field + `":` + value + `}`
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, nil, nil); err == nil {
				t.Fatal("wrong JSON field type was accepted")
			}
		})
	}
}

func TestBackendConfigurationRejectsInvalidAndIncompatibleFields(t *testing.T) {
	cases := []struct {
		name     string
		environ  []string
		explicit map[string]string
	}{
		{"empty proxy", []string{"ALMANAC_COMPONENTS=backend", "ALMANAC_DATABASE_URL_FILE=/db", "ALMANAC_PUBLIC_ORIGIN=https://example.test", "ALMANAC_TLS_CERT_FILE=/cert", "ALMANAC_TLS_KEY_FILE=/key", "ALMANAC_TRUSTED_PROXY_CIDRS="}, nil},
		{"invalid proxy", []string{"ALMANAC_COMPONENTS=backend", "ALMANAC_DATABASE_URL_FILE=/db", "ALMANAC_PUBLIC_ORIGIN=https://example.test", "ALMANAC_TLS_CERT_FILE=/cert", "ALMANAC_TLS_KEY_FILE=/key", "ALMANAC_TRUSTED_PROXY_CIDRS=bogus"}, nil},
		{"missing trust", []string{"ALMANAC_COMPONENTS=backend", "ALMANAC_DATABASE_URL_FILE=/db", "ALMANAC_PUBLIC_ORIGIN=https://example.test"}, nil},
		{"partial tls", []string{"ALMANAC_COMPONENTS=backend", "ALMANAC_DATABASE_URL_FILE=/db", "ALMANAC_PUBLIC_ORIGIN=https://example.test", "ALMANAC_TLS_CERT_FILE=/cert", "ALMANAC_TRUSTED_PROXY_CIDRS=10.0.0.0/8"}, nil},
		{"ui backend secret", nil, map[string]string{"database-url-file": "/secret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load("", tc.environ, tc.explicit); err == nil {
				t.Fatal("expected configuration rejection")
			}
		})
	}
}
