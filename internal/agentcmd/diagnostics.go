package agentcmd

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/prometheus"
)

const (
	maxActiveDiagnostics    = 4
	maxDiagnosticReceipts   = 256
	diagnosticReceiptPeriod = 24 * time.Hour
)

var errDiagnosticCapacity = errors.New("agent diagnostic capacity reached")

type diagnosticRunner struct {
	mu        sync.Mutex
	client    *client
	results   chan agentprotocol.DiagnosticResult
	active    map[string]context.CancelFunc
	completed map[string]diagnosticReceipt
	order     []string
}

type diagnosticReceipt struct {
	result  agentprotocol.DiagnosticResult
	expires time.Time
}

func newDiagnosticRunner(agent *client) *diagnosticRunner {
	return &diagnosticRunner{client: agent, results: make(chan agentprotocol.DiagnosticResult, maxActiveDiagnostics), active: make(map[string]context.CancelFunc), completed: make(map[string]diagnosticReceipt)}
}

func (r *diagnosticRunner) start(parent context.Context, request agentprotocol.DiagnosticRequest) error {
	if r == nil || r.client == nil || request.Validate() != nil || request.AgentID != r.client.agentID || request.DeadlineAt.After(time.Now().Add(30*time.Second)) {
		return errors.New("agent diagnostic request rejected")
	}
	if request.Operation == "query_preview" && !slices.Contains(r.client.assignments, request.Datasource) {
		return errors.New("agent diagnostic assignment rejected")
	}
	capabilities := []agentprotocol.Capability{{Provider: datasource.PrometheusProvider,
		ConfigurationFormats: []string{datasource.PrometheusConfiguration}, Operations: []string{"connection_test", "query_preview"}}}
	if !datasource.SupportsOperation(capabilities, request.Operation) {
		return errors.New("agent diagnostic capability rejected")
	}
	r.mu.Lock()
	r.pruneLocked(time.Now())
	if _, ok := r.active[request.DiagnosticID]; ok {
		r.mu.Unlock()
		return nil
	}
	if saved, ok := r.completed[request.DiagnosticID]; ok {
		r.mu.Unlock()
		r.offer(saved.result)
		return nil
	}
	if len(r.active) >= maxActiveDiagnostics {
		r.mu.Unlock()
		return errDiagnosticCapacity
	}
	deadline := request.DeadlineAt
	if maxProviderDeadline := time.Now().Add(20 * time.Second); deadline.After(maxProviderDeadline) {
		deadline = maxProviderDeadline
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	r.active[request.DiagnosticID] = cancel
	r.mu.Unlock()
	go r.execute(ctx, request)
	return nil
}

func (r *diagnosticRunner) cancel(id string) {
	r.mu.Lock()
	cancel := r.active[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *diagnosticRunner) close() {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.active))
	for _, cancel := range r.active {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (r *diagnosticRunner) execute(ctx context.Context, request agentprotocol.DiagnosticRequest) {
	result := agentprotocol.DiagnosticResult{DiagnosticID: request.DiagnosticID}
	settings := datasource.Settings{Name: request.Datasource.Name, DisplayName: request.Datasource.Name,
		Provider: request.Datasource.Provider, ConfigurationFormat: request.Datasource.ConfigurationFormat,
		Endpoint: request.Datasource.Endpoint, TLS: datasource.TLSSettings{InsecureSkipVerify: request.Datasource.TLS.InsecureSkipVerify, CAPEM: request.Datasource.TLS.CAPEM},
		AuthenticationMode: request.Datasource.AuthenticationMode, AuthenticationUser: request.Datasource.Username,
		PollingIntervalSeconds: request.Datasource.PollingIntervalSeconds, SettlingDelaySeconds: request.Datasource.SettlingDelaySeconds,
		AgentID: request.AgentID}
	tlsConfig, err := settings.ClientTLSConfig()
	if err != nil {
		result = diagnosticFailure(request.DiagnosticID, "unsupported")
	} else {
		endpoint, _ := url.Parse(request.Datasource.Endpoint)
		source, clientErr := prometheus.NewClient(prometheus.Config{Endpoint: request.Datasource.Endpoint,
			AuthMode: request.Datasource.AuthenticationMode, Username: request.Datasource.Username,
			Password: request.Datasource.Password, BearerToken: request.Datasource.BearerToken,
			TLSConfig: tlsConfig, Timeout: 20 * time.Second, AllowedDestinations: []string{endpoint.Hostname()}})
		if clientErr != nil {
			result = diagnosticFailure(request.DiagnosticID, "unsupported")
		} else {
			query := request.Query
			if request.Operation == "connection_test" {
				query = "vector(1)"
			}
			observed, queryErr := source.Query(ctx, query)
			if queryErr != nil {
				var sourceErr *prometheus.SourceError
				if errors.As(queryErr, &sourceErr) {
					result = diagnosticFailure(request.DiagnosticID, sourceErr.Code)
				} else {
					result = diagnosticFailure(request.DiagnosticID, "network")
				}
			} else {
				series := make([]agentprotocol.DiagnosticSeries, 0, len(observed.Series))
				for _, item := range observed.Series {
					series = append(series, agentprotocol.DiagnosticSeries{Labels: item.Labels, Time: item.Time, Value: item.Value})
				}
				result = agentprotocol.DiagnosticResult{DiagnosticID: request.DiagnosticID, State: "succeeded", ResultClass: observed.Class, Series: series}
			}
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		result = agentprotocol.DiagnosticResult{DiagnosticID: request.DiagnosticID, State: "canceled"}
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result = agentprotocol.DiagnosticResult{DiagnosticID: request.DiagnosticID, State: "timed_out"}
	}
	r.mu.Lock()
	delete(r.active, request.DiagnosticID)
	r.completed[request.DiagnosticID] = diagnosticReceipt{result: result, expires: time.Now().Add(diagnosticReceiptPeriod)}
	r.order = append(r.order, request.DiagnosticID)
	for len(r.order) > maxDiagnosticReceipts {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.completed, oldest)
	}
	r.mu.Unlock()
	r.offer(result)
}

func (r *diagnosticRunner) pruneLocked(now time.Time) {
	kept := r.order[:0]
	for _, id := range r.order {
		receipt, ok := r.completed[id]
		if ok && now.Before(receipt.expires) {
			kept = append(kept, id)
		} else {
			delete(r.completed, id)
		}
	}
	r.order = kept
}

func (r *diagnosticRunner) offer(result agentprotocol.DiagnosticResult) {
	select {
	case r.results <- result:
	default:
	}
}

func diagnosticFailure(id, code string) agentprotocol.DiagnosticResult {
	if !agentprotocol.ValidDiagnosticErrorCode(code) {
		code = "network"
	}
	return agentprotocol.DiagnosticResult{DiagnosticID: id, State: "failed", ErrorCode: code, Message: diagnosticMessage(code)}
}

func diagnosticMessage(code string) string {
	switch code {
	case "authentication":
		return "The datasource rejected authentication"
	case "incomplete":
		return "The datasource returned an incomplete result"
	case "network":
		return "The agent could not reach the datasource"
	case "query":
		return "The datasource rejected the query"
	case "rate_limited":
		return "The datasource rate limited the request"
	case "tls":
		return "The agent could not verify the datasource TLS connection"
	case "timeout":
		return "The datasource request timed out"
	default:
		return "The datasource diagnostic is unsupported"
	}
}
