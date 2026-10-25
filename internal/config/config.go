package config

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxConfigBytes = 64 << 10

type Config struct {
	Listen          string        `json:"listen"`
	Components      string        `json:"components"`
	BackendURL      string        `json:"backend_url"`
	ShutdownTimeout time.Duration `json:"-"`
}

type fileConfig struct {
	Listen          *string `json:"listen"`
	Components      *string `json:"components"`
	BackendURL      *string `json:"backend_url"`
	ShutdownTimeout *string `json:"shutdown_timeout"`
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

func (c Config) UIOnly() bool { return c.Components == "ui" }
