// Package prometheus contains the bounded Prometheus HTTP adapter used by agents.
package prometheus

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

var ErrInvalidEndpoint = errors.New("prometheus: invalid endpoint")

// ValidateEndpoint accepts absolute HTTP(S) endpoints, including private hosts
// reachable from an agent, and rejects embedded credentials and fragments.
func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return ErrInvalidEndpoint
	}
	if u.RawPath != "" && u.EscapedPath() != u.RawPath {
		return ErrInvalidEndpoint
	}
	if port := u.Port(); port != "" {
		n, parseErr := strconv.Atoi(port)
		if parseErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return ErrInvalidEndpoint
		}
	}
	if strings.ContainsAny(u.Host, "\\\r\n\t") {
		return ErrInvalidEndpoint
	}
	return nil
}
