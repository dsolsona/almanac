package config

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxConfigBytes = 64 << 10

type Config struct {
	Listen                   string         `json:"listen"`
	Components               string         `json:"components"`
	BackendURL               string         `json:"backend_url"`
	DatabaseURLFile          string         `json:"database_url_file"`
	InitialAdminLogin        string         `json:"initial_admin_login"`
	InitialAdminPasswordFile string         `json:"initial_admin_password_file"`
	PublicOrigin             string         `json:"public_origin"`
	TLSCertFile              string         `json:"tls_cert_file"`
	TLSKeyFile               string         `json:"tls_key_file"`
	TrustedProxyCIDRs        []netip.Prefix `json:"trusted_proxy_cidrs"`
	ShutdownTimeout          time.Duration  `json:"-"`
}

type fileConfig struct {
	Listen                   *string   `json:"listen"`
	Components               *string   `json:"components"`
	BackendURL               *string   `json:"backend_url"`
	DatabaseURLFile          *string   `json:"database_url_file"`
	InitialAdminLogin        *string   `json:"initial_admin_login"`
	InitialAdminPasswordFile *string   `json:"initial_admin_password_file"`
	PublicOrigin             *string   `json:"public_origin"`
	TLSCertFile              *string   `json:"tls_cert_file"`
	TLSKeyFile               *string   `json:"tls_key_file"`
	TrustedProxyCIDRs        *[]string `json:"trusted_proxy_cidrs"`
	ShutdownTimeout          *string   `json:"shutdown_timeout"`
}

func Defaults() Config {
	return Config{Listen: "127.0.0.1:8080", Components: "backend,ui,alerts", ShutdownTimeout: 30 * time.Second}
}

func Load(path string, environ []string, explicit map[string]string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: cannot read file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
		if err != nil {
			return Config{}, fmt.Errorf("config: cannot read file")
		}
		if len(data) > maxConfigBytes {
			return Config{}, fmt.Errorf("config: file exceeds 64 KiB")
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return Config{}, fmt.Errorf("config: expected a JSON object")
		}
		var file fileConfig
		if err := json.Unmarshal(data, &file, json.RejectUnknownMembers(true)); err != nil {
			return Config{}, fmt.Errorf("config: invalid JSON or field")
		}
		if file.Listen != nil {
			cfg.Listen = *file.Listen
		}
		if file.Components != nil {
			cfg.Components = *file.Components
		}
		if file.BackendURL != nil {
			cfg.BackendURL = *file.BackendURL
		}
		if file.DatabaseURLFile != nil {
			cfg.DatabaseURLFile = *file.DatabaseURLFile
		}
		if file.InitialAdminLogin != nil {
			cfg.InitialAdminLogin = *file.InitialAdminLogin
		}
		if file.InitialAdminPasswordFile != nil {
			cfg.InitialAdminPasswordFile = *file.InitialAdminPasswordFile
		}
		if file.PublicOrigin != nil {
			cfg.PublicOrigin = *file.PublicOrigin
		}
		if file.TLSCertFile != nil {
			cfg.TLSCertFile = *file.TLSCertFile
		}
		if file.TLSKeyFile != nil {
			cfg.TLSKeyFile = *file.TLSKeyFile
		}
		if file.TrustedProxyCIDRs != nil {
			if err := cfg.setTrustedProxies(*file.TrustedProxyCIDRs); err != nil {
				return Config{}, err
			}
		}
		if file.ShutdownTimeout != nil {
			cfg.ShutdownTimeout, err = time.ParseDuration(*file.ShutdownTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("config: invalid shutdown_timeout")
			}
		}
	}
	for _, pair := range environ {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		switch key {
		case "ALMANAC_LISTEN":
			cfg.Listen = value
		case "ALMANAC_COMPONENTS":
			cfg.Components = value
		case "ALMANAC_BACKEND_URL":
			cfg.BackendURL = value
		case "ALMANAC_DATABASE_URL_FILE":
			cfg.DatabaseURLFile = value
		case "ALMANAC_INITIAL_ADMIN_LOGIN":
			cfg.InitialAdminLogin = value
		case "ALMANAC_INITIAL_ADMIN_PASSWORD_FILE":
			cfg.InitialAdminPasswordFile = value
		case "ALMANAC_PUBLIC_ORIGIN":
			cfg.PublicOrigin = value
		case "ALMANAC_TLS_CERT_FILE":
			cfg.TLSCertFile = value
		case "ALMANAC_TLS_KEY_FILE":
			cfg.TLSKeyFile = value
		case "ALMANAC_TRUSTED_PROXY_CIDRS":
			if err := cfg.setTrustedProxyList(value); err != nil {
				return Config{}, err
			}
		case "ALMANAC_SHUTDOWN_TIMEOUT":
			d, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("config: invalid ALMANAC_SHUTDOWN_TIMEOUT")
			}
			cfg.ShutdownTimeout = d
		}
	}
	for key, value := range explicit {
		switch key {
		case "listen":
			cfg.Listen = value
		case "components":
			cfg.Components = value
		case "backend-url":
			cfg.BackendURL = value
		case "database-url-file":
			cfg.DatabaseURLFile = value
		case "initial-admin-login":
			cfg.InitialAdminLogin = value
		case "initial-admin-password-file":
			cfg.InitialAdminPasswordFile = value
		case "public-origin":
			cfg.PublicOrigin = value
		case "tls-cert-file":
			cfg.TLSCertFile = value
		case "tls-key-file":
			cfg.TLSKeyFile = value
		case "trusted-proxy-cidrs":
			if err := cfg.setTrustedProxyList(value); err != nil {
				return Config{}, err
			}
		case "shutdown-timeout":
			d, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("config: invalid --shutdown-timeout")
			}
			cfg.ShutdownTimeout = d
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("config: listen must not be empty")
	}
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return errors.New("config: listen must be a host:port address")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber > 65535 {
		return errors.New("config: listen port must be between 0 and 65535")
	}
	parts := strings.Split(c.Components, ",")
	if len(parts) == 0 {
		return errors.New("config: components must not be empty")
	}
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if part == "" {
			return errors.New("config: empty component name")
		}
		if seen[part] {
			return errors.New("config: duplicate component name")
		}
		seen[part] = true
		if !slices.Contains([]string{"backend", "ui", "alerts"}, part) {
			return fmt.Errorf("config: unknown component")
		}
	}
	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout > 2*time.Minute {
		return errors.New("config: shutdown_timeout must be positive and at most 2m")
	}
	if c.Components == "backend" || c.BackendUI() {
		if strings.TrimSpace(c.DatabaseURLFile) == "" {
			return errors.New("config: database_url_file is required for backend")
		}
		if strings.TrimSpace(c.PublicOrigin) == "" {
			return errors.New("config: public_origin is required for backend")
		}
		if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
			return errors.New("config: tls_cert_file and tls_key_file must be set together")
		}
		if c.TLSCertFile == "" && len(c.TrustedProxyCIDRs) == 0 {
			return errors.New("config: trusted_proxy_cidrs are required when TLS terminates at a proxy")
		}
	}
	if c.Components != "backend" && !c.BackendUI() && (c.DatabaseURLFile != "" || c.InitialAdminLogin != "" || c.InitialAdminPasswordFile != "" || c.PublicOrigin != "" || c.TLSCertFile != "" || c.TLSKeyFile != "" || len(c.TrustedProxyCIDRs) != 0) {
		return errors.New("config: backend-only fields require components=backend")
	}
	if c.Components == "ui" {
		u, err := url.Parse(c.BackendURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return errors.New("config: backend_url must be an http(s) origin without credentials, path, query, or fragment")
		}
		if port := u.Port(); port != "" {
			portNumber, err := strconv.ParseUint(port, 10, 16)
			if err != nil || portNumber == 0 {
				return errors.New("config: backend_url port must be between 1 and 65535")
			}
		}
	}
	return nil
}

func (c *Config) setTrustedProxies(values []string) error {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Zone() != "" {
			return errors.New("config: invalid trusted_proxy_cidrs")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	c.TrustedProxyCIDRs = prefixes
	return nil
}

func (c *Config) setTrustedProxyList(value string) error {
	parts := strings.Split(value, ",")
	for _, part := range parts {
		if strings.TrimSpace(part) != part || part == "" {
			return errors.New("config: invalid trusted_proxy_cidrs")
		}
	}
	return c.setTrustedProxies(parts)
}

func (c Config) UIOnly() bool { return c.Components == "ui" }

func (c Config) BackendUI() bool { return c.Components == "backend,ui" || c.Components == "ui,backend" }
