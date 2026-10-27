package prometheus

import (
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MaxQueryBytes = 4096
	MaxSeries     = 20
	MaxResponse   = 1 << 20
)

type Config struct {
	Endpoint            string
	AuthMode            string
	Username            string
	Password            string
	BearerToken         string
	TLSConfig           *tls.Config
	Timeout             time.Duration
	AllowedDestinations []string
}

type Client struct {
	endpoint *url.URL
	authMode string
	username string
	password string
	token    string
	http     *http.Client
	allowed  []string
}

type Series struct {
	Labels map[string]string `json:"labels"`
	Time   string            `json:"time"`
	Value  string            `json:"value"`
}

type QueryResult struct {
	Class   string   `json:"class"`
	Series  []Series `json:"series"`
	Message string   `json:"message"`
}

type SourceError struct {
	Code string
}

func (e *SourceError) Error() string { return "prometheus: " + e.Code }

func NewClient(config Config) (*Client, error) {
	if ValidateEndpoint(config.Endpoint) != nil ||
		(config.AuthMode != "" && config.AuthMode != "none" && config.AuthMode != "basic" && config.AuthMode != "bearer") ||
		(config.AuthMode == "basic" && (config.Username == "" || config.Password == "")) ||
		(config.AuthMode == "bearer" && config.BearerToken == "") ||
		(config.AuthMode != "basic" && config.Password != "") ||
		(config.AuthMode != "bearer" && config.BearerToken != "") {
		return nil, ErrInvalidEndpoint
	}
	u, _ := url.Parse(config.Endpoint)
	if u.RawQuery != "" || u.ForceQuery {
		return nil, ErrInvalidEndpoint
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	if timeout < time.Millisecond || timeout > 20*time.Second {
		return nil, ErrInvalidEndpoint
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12} // #nosec G402 -- source TLS policy is explicitly configured.
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	allowed := append([]string(nil), config.AllowedDestinations...)
	transport := &http.Transport{
		TLSClientConfig:   tlsConfig,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return safeDialContext(ctx, network, address, allowed)
		},
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Client{endpoint: u, authMode: config.AuthMode, username: config.Username, password: config.Password, token: config.BearerToken, http: client, allowed: allowed}, nil
}

func (c *Client) Query(ctx context.Context, query string) (QueryResult, error) {
	if c == nil || c.endpoint == nil || strings.TrimSpace(query) == "" || len(query) > MaxQueryBytes {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	u := c.endpoint.Clone()
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/query"
	values := u.Query()
	values.Set("query", query)
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	if c.authMode == "basic" {
		req.SetBasicAuth(c.username, c.password)
	} else if c.authMode == "bearer" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return QueryResult{}, &SourceError{Code: "timeout"}
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return QueryResult{}, &SourceError{Code: "timeout"}
		}
		var urlError *url.Error
		if errors.As(err, &urlError) && strings.Contains(strings.ToLower(urlError.Error()), "tls") {
			return QueryResult{}, &SourceError{Code: "tls"}
		}
		return QueryResult{}, &SourceError{Code: "network"}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return QueryResult{}, &SourceError{Code: "authentication"}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return QueryResult{}, &SourceError{Code: "rate_limited"}
	}
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return QueryResult{}, &SourceError{Code: "network"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
	if err != nil || len(body) > MaxResponse {
		return QueryResult{}, &SourceError{Code: "incomplete"}
	}
	return parseQueryResponse(body)
}

func parseQueryResponse(body []byte) (QueryResult, error) {
	var response struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Data     struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []jsontext.Value  `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || response.Status != "success" || response.Data.ResultType != "vector" {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	if len(response.Data.Result) > MaxSeries {
		return QueryResult{}, &SourceError{Code: "incomplete"}
	}
	if len(response.Warnings) != 0 {
		return QueryResult{Class: "incomplete", Series: []Series{}, Message: "The source returned an incomplete preview"}, nil
	}
	result := QueryResult{Class: "complete", Series: make([]Series, 0, len(response.Data.Result))}
	for _, item := range response.Data.Result {
		if len(item.Value) != 2 {
			return QueryResult{}, &SourceError{Code: "query"}
		}
		timestamp, err := strconv.ParseFloat(string(item.Value[0]), 64)
		if err != nil || timestamp < 0 || timestamp > 1e12 {
			return QueryResult{}, &SourceError{Code: "query"}
		}
		var value string
		if json.Unmarshal(item.Value[1], &value) != nil || value == "NaN" || strings.Contains(value, "Inf") {
			return QueryResult{}, &SourceError{Code: "query"}
		}
		result.Series = append(result.Series, Series{Labels: item.Metric, Time: strconv.FormatFloat(timestamp, 'f', 3, 64), Value: value})
	}
	if len(result.Series) == 0 {
		result.Class = "empty"
	}
	return result, nil
}

func safeDialContext(ctx context.Context, network, address string, allowed []string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("prometheus: destination lookup failed")
	}
	for _, candidate := range addresses {
		if deniedDestination(candidate.IP) || !destinationAllowed(host, candidate.IP, allowed) {
			return nil, errors.New("prometheus: destination denied")
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}

func deniedDestination(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	metadata := []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254"}
	for _, value := range metadata {
		if ip.Equal(net.ParseIP(value)) {
			return true
		}
	}
	return false
}

func destinationAllowed(host string, ip net.IP, allowed []string) bool {
	for _, entry := range allowed {
		if strings.EqualFold(entry, host) {
			return true
		}
		prefix, err := netip.ParsePrefix(entry)
		address, addressErr := netip.ParseAddr(ip.String())
		if err == nil && addressErr == nil && prefix.Contains(address) {
			return true
		}
	}
	return false
}
