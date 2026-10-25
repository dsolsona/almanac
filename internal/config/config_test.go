package config

import (
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
	for _, value := range []string{"http://user:secret@host", "https://host/path", "http://host?q=secret", "ftp://host", "http://host/#x"} {
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
}
