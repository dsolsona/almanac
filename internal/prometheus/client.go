package prometheus

import (
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/telemetry"
)

const (
	MaxQueryBytes = 4096
	MaxSeries     = 20
	MaxResponse   = 1 << 20
)

type Config struct {
	Provider            string
	CadenceSeconds      int
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
	provider string
	cadence  int
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

// NewClient constructs a provider-aware HTTP client with bounded requests, TLS and destination checks.
func NewClient(config Config) (*Client, error) {
	if ValidateEndpoint(config.Endpoint) != nil ||
		(config.AuthMode != "" && config.AuthMode != "none" && config.AuthMode != "basic" && config.AuthMode != "bearer") ||
		(config.AuthMode == "basic" && (config.Username == "" || config.Password == "")) ||
		(config.AuthMode == "bearer" && config.BearerToken == "") ||
		(config.AuthMode != "basic" && config.Password != "") ||
		(config.AuthMode != "bearer" && config.BearerToken != "") {
		return nil, ErrInvalidEndpoint
	}
	provider := config.Provider
	if provider == "" {
		provider = "prometheus"
	}
	if provider != "prometheus" && provider != "victoriatraces" && provider != "tempo" && provider != "loki" {
		return nil, ErrInvalidEndpoint
	}
	cadence := config.CadenceSeconds
	if cadence == 0 {
		cadence = 60
	}
	if cadence < 15 || cadence > 86400 {
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
		TLSClientConfig:     tlsConfig,
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return safeDialContext(ctx, network, address, allowed)
		},
	}
	client := &http.Client{Transport: telemetry.ClientTransport(transport, "provider."+provider+".request"), Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Client{provider: provider, cadence: cadence, endpoint: u, authMode: config.AuthMode, username: config.Username, password: config.Password, token: config.BearerToken, http: client, allowed: allowed}, nil
}

// CloseIdleConnections releases pooled connections when this client is retired.
// Requests already in flight are unaffected and remain governed by their contexts.
func (c *Client) CloseIdleConnections() {
	c.http.CloseIdleConnections()
}

// QueryAt evaluates a vector query at the supplied canonical interval end.
func (c *Client) QueryAt(ctx context.Context, query string, at time.Time) (QueryResult, error) {
	if at.IsZero() || at.Nanosecond()%1_000 != 0 {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	return c.query(ctx, query, at.UTC())
}

// query translates the canonical interval to the native API and rejects unsafe or incomplete responses.
func (c *Client) query(ctx context.Context, query string, at time.Time) (QueryResult, error) {
	if c == nil || c.endpoint == nil || strings.TrimSpace(query) == "" || len(query) > MaxQueryBytes {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	if c.provider == "victoriatraces" && hasLogsQLOptions(query) {
		return QueryResult{}, &SourceError{Code: "query"}
	}

	u := c.endpoint.Clone()
	path := "/api/v1/query"
	switch c.provider {
	case "loki":
		path = "/loki/api/v1/query"
	case "victoriatraces":
		path = "/select/logsql/stats_query"
	case "tempo":
		path = "/api/metrics/query"
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	values := u.Query()
	if c.provider == "loki" {
		// $interval is a literal template token; replacement also applies inside quoted query text.
		query = strings.ReplaceAll(query, "$interval", strconv.Itoa(c.cadence)+"s")
	}
	values.Set("query", query)
	if c.provider == "tempo" {
		values.Del("query")
		values.Set("q", query)
	}
	if c.provider == "victoriatraces" || c.provider == "tempo" {
		values.Set("start", at.Add(-time.Duration(c.cadence)*time.Second).Format(time.RFC3339Nano))
		values.Set("end", at.Format(time.RFC3339Nano))
	}
	if c.provider == "victoriatraces" {
		values.Set("allow_partial_response", "0")
	}
	if !at.IsZero() {
		evaluation := at
		if c.provider == "loki" {
			evaluation = at.Add(-time.Nanosecond)
		}
		values.Set("time", evaluation.UTC().Format(time.RFC3339Nano))
	}
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
	if c.provider == "tempo" {
		return parseTempoInstant(body, at)
	}
	result, parseErr := parseQueryResponse(body)
	// Loki evaluates one nanosecond before the canonical end to preserve [start,end).
	// Expose the canonical endpoint used by observation alignment, not its wire shift.
	if parseErr == nil && c.provider == "loki" {
		expected := strconv.FormatFloat(float64(at.Add(-time.Nanosecond).UnixMilli())/1000, 'f', 3, 64)
		for i := range result.Series {
			if result.Series[i].Time != expected {
				return QueryResult{}, &SourceError{Code: "query"}
			}
			result.Series[i].Time = strconv.FormatInt(at.Unix(), 10)
		}
	}
	return result, parseErr
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
		if len(item.Metric) > 32 {
			return QueryResult{}, &SourceError{Code: "incomplete"}
		}
		for name, value := range item.Metric {
			if name == "" || len(name) > 128 || len(value) > 256 {
				return QueryResult{}, &SourceError{Code: "incomplete"}
			}
		}
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
	var lastErr error
	for _, candidate := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		return nil, errors.New("prometheus: destination unavailable")
	}
	return nil, lastErr
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

// ConnectionQuery is a bounded, read-only query in the selected provider language.
func ConnectionQuery(provider string) string {
	switch provider {
	case "victoriatraces":
		return "* | stats count() as events"
	case "tempo":
		return "{} | count_over_time()"
	default:
		return "vector(1)"
	}
}

// parseTempoInstant validates complete, safe integer counts and anchors them to the requested interval end.
func parseTempoInstant(body []byte, at time.Time) (QueryResult, error) {
	var response struct {
		Series []struct {
			Labels []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue string `json:"stringValue"`
				} `json:"value"`
			} `json:"labels"`
			Value *float64 `json:"value"`
		} `json:"series"`
		Status  jsontext.Value `json:"status"`
		Metrics *struct {
			TotalJobs     uint64 `json:"totalJobs"`
			CompletedJobs uint64 `json:"completedJobs"`
		} `json:"metrics"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Series) > MaxSeries || response.Series == nil && response.Metrics == nil {
		return QueryResult{}, &SourceError{Code: "query"}
	}
	if len(response.Status) > 0 && string(response.Status) != "0" && string(response.Status) != `"COMPLETE"` || response.Message != "" || response.Metrics != nil && response.Metrics.TotalJobs != response.Metrics.CompletedJobs {
		return QueryResult{Class: "incomplete", Series: []Series{}, Message: "The source returned an incomplete preview"}, nil
	}
	result := QueryResult{Class: "complete", Series: []Series{}}
	for _, series := range response.Series {
		// Protobuf JSON omits scalar zero. Accept it only for an identified count series.
		if series.Value == nil {
			for _, label := range series.Labels {
				if label.Key == "__name__" && label.Value.StringValue == "count_over_time" {
					series.Value = new(float64(0))
				}
			}
		}
		if series.Value == nil || math.IsNaN(*series.Value) || math.IsInf(*series.Value, 0) || *series.Value < 0 || *series.Value > 9007199254740991 || math.Trunc(*series.Value) != *series.Value || len(series.Labels) > 32 {
			return QueryResult{}, &SourceError{Code: "query"}
		}
		labels := make(map[string]string, len(series.Labels))
		for _, label := range series.Labels {
			if label.Key == "" || len(label.Key) > 128 || len(label.Value.StringValue) > 256 {
				return QueryResult{}, &SourceError{Code: "incomplete"}
			}
			labels[label.Key] = label.Value.StringValue
		}
		result.Series = append(result.Series, Series{Labels: labels, Time: strconv.FormatInt(at.Unix(), 10), Value: strconv.FormatFloat(*series.Value, 'f', -1, 64)})
	}
	if len(result.Series) == 0 {
		result.Class = "empty"
	}
	return result, nil
}

// hasLogsQLOptions rejects query-level configuration overrides, including in
// subqueries. The initial count profile delegates interval and completeness
// policy to the agent. Quoted search text does not configure query options.
func hasLogsQLOptions(query string) bool {
	for i := 0; i < len(query); {
		if query[i] == '#' {
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if query[i] == '\'' || query[i] == '"' || query[i] == '`' {
			quote := query[i]
			i++
			for i < len(query) {
				if query[i] == '\\' && quote != '`' {
					i += 2
					continue
				}
				if query[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		start := i
		for i < len(query) {
			r, size := utf8.DecodeRuneInString(query[i:])
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				break
			}
			i += size
		}
		if start == i {
			i++
			continue
		}
		if strings.ToLower(query[start:i]) == "options" {
			return true
		}
	}
	return false
}
