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
	maxActiveDiagnostics      = 4
	maxDiagnosticCommandIDs   = 512
	maxDiagnosticReceipts     = 256
	maxDiagnosticReceiptBytes = 8 << 20
)

var errDiagnosticCapacity = errors.New("agent diagnostic capacity reached")

type diagnosticRunner struct {
	mu             sync.Mutex
	client         *client
	results        chan completedDiagnosticResult
	active         map[string]activeDiagnostic
	commandIDs     map[string]time.Time
	commandOrder   []string
	completed      map[string]diagnosticReceipt
	completedOrder []string
	receiptBytes   int
}

type completedDiagnosticResult struct {
	result     agentprotocol.DiagnosticResult
	deadlineAt time.Time
	generation string
}

type activeDiagnostic struct {
	cancel       context.CancelFunc
	datasourceID string
	datasource   *agentprotocol.DatasourceAssignment
}

type diagnosticReceipt struct {
	completedDiagnosticResult
	expires time.Time
	size    int
}

func newDiagnosticRunner(agent *client) *diagnosticRunner {
	return &diagnosticRunner{client: agent, results: make(chan completedDiagnosticResult, maxActiveDiagnostics), active: make(map[string]activeDiagnostic),
		commandIDs: make(map[string]time.Time), completed: make(map[string]diagnosticReceipt)}
}

func (r *diagnosticRunner) start(parent context.Context, request agentprotocol.DiagnosticRequest) error {
	if r == nil || r.client == nil || request.Validate() != nil || request.AgentID != r.client.agentID {
		return errors.New("agent diagnostic request rejected")
	}
	now := r.client.currentServerTime()
	if now.IsZero() || request.DeadlineAt.After(now.Add(30*time.Second)) {
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
	r.pruneLocked(now)
	if _, ok := r.active[request.DiagnosticID]; ok {
		r.mu.Unlock()
		return nil
	}
	if _, ok := r.commandIDs[request.DiagnosticID]; ok {
		if saved, cached := r.completed[request.DiagnosticID]; cached {
			r.mu.Unlock()
			r.offer(saved.completedDiagnosticResult)
			return nil
		}
		r.mu.Unlock()
		return nil
	}
	if !request.DeadlineAt.After(now) {
		result := agentprotocol.DiagnosticResult{DiagnosticID: request.DiagnosticID, State: "timed_out"}
		completed := completedDiagnosticResult{result: result, deadlineAt: request.DeadlineAt, generation: r.client.generation}
		r.mu.Unlock()
		r.offer(completed)
		return nil
	}
	if len(r.commandIDs) >= maxDiagnosticCommandIDs || len(r.active) >= maxActiveDiagnostics {
		r.mu.Unlock()
		return errDiagnosticCapacity
	}
	r.commandIDs[request.DiagnosticID] = request.DeadlineAt
	r.commandOrder = append(r.commandOrder, request.DiagnosticID)
	deadline := request.DeadlineAt
	if maxProviderDeadline := now.Add(20 * time.Second); deadline.After(maxProviderDeadline) {
		deadline = maxProviderDeadline
	}
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		r.mu.Unlock()
		return errors.New("agent diagnostic request expired")
	}
	ctx, cancel := context.WithTimeout(parent, remaining)
	active := activeDiagnostic{cancel: cancel}
	if request.Operation == "query_preview" {
		assignment := request.Datasource
		active.datasourceID = assignment.DatasourceID
		active.datasource = &assignment
	}
	r.active[request.DiagnosticID] = active
	generation := r.client.generation
	r.mu.Unlock()
	go r.execute(ctx, request, generation)
	return nil
}

func (r *diagnosticRunner) cancel(id string) {
	r.mu.Lock()
	active := r.active[id]
	r.mu.Unlock()
	if active.cancel != nil {
		active.cancel()
	}
}

func (r *diagnosticRunner) reconcileAssignments(assignments []agentprotocol.DatasourceAssignment) {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0)
	for _, active := range r.active {
		if active.datasource != nil && !slices.Contains(assignments, *active.datasource) {
			cancels = append(cancels, active.cancel)
		}
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (r *diagnosticRunner) close() {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.active))
	for _, active := range r.active {
		cancels = append(cancels, active.cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (r *diagnosticRunner) execute(ctx context.Context, request agentprotocol.DiagnosticRequest, generation string) {
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
	completed := completedDiagnosticResult{result: result, deadlineAt: request.DeadlineAt, generation: generation}
	r.rememberLocked(request.DiagnosticID, completed, r.client.currentServerTime())
	r.mu.Unlock()
	r.offer(completed)
}

func (r *diagnosticRunner) pruneLocked(now time.Time) {
	kept := r.commandOrder[:0]
	for _, id := range r.commandOrder {
		deadline, ok := r.commandIDs[id]
		if ok && now.Before(deadline) {
			kept = append(kept, id)
			continue
		}
		delete(r.commandIDs, id)
		if receipt, exists := r.completed[id]; exists {
			r.receiptBytes -= receipt.size
			delete(r.completed, id)
		}
	}
	r.commandOrder = kept
	kept = r.completedOrder[:0]
	for _, id := range r.completedOrder {
		receipt, ok := r.completed[id]
		if ok && now.Before(receipt.expires) {
			kept = append(kept, id)
		} else {
			delete(r.completed, id)
		}
	}
	r.completedOrder = kept
}

func (r *diagnosticRunner) rememberLocked(id string, completed completedDiagnosticResult, now time.Time) {
	deadline, tracked := r.commandIDs[id]
	if !tracked || !now.Before(deadline) {
		return
	}
	encoded, err := completed.result.CanonicalJSON()
	if err != nil {
		completed.result = diagnosticFailure(id, "incomplete")
		encoded, _ = completed.result.CanonicalJSON()
	}
	size := len(encoded)
	if prior, ok := r.completed[id]; ok {
		r.receiptBytes -= prior.size
	} else {
		r.completedOrder = append(r.completedOrder, id)
	}
	r.completed[id] = diagnosticReceipt{completedDiagnosticResult: completed, expires: deadline, size: size}
	r.receiptBytes += size
	for len(r.completedOrder) > maxDiagnosticReceipts || r.receiptBytes > maxDiagnosticReceiptBytes {
		oldest := r.completedOrder[0]
		r.completedOrder = r.completedOrder[1:]
		if receipt, ok := r.completed[oldest]; ok {
			r.receiptBytes -= receipt.size
			delete(r.completed, oldest)
		}
	}
}

func (r *diagnosticRunner) offer(result completedDiagnosticResult) {
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

func diagnosticCapacityResult(request agentprotocol.DiagnosticRequest) completedDiagnosticResult {
	return completedDiagnosticResult{result: diagnosticFailure(request.DiagnosticID, "rate_limited"), deadlineAt: request.DeadlineAt}
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
