package agentcmd

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/prometheus"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/google/uuid"
)

const (
	maxCollectionQueries   = 8
	maxCollectionPerSource = 4
	maxLiveSubmissions     = 2
	maxLiveResultBytes     = 64 << 20
	maxUploadBatchItems    = 32
	maxCollectionQueryTime = 20 * time.Second
	collectionRetryMax     = 30 * time.Second
)

type collectionRunner struct {
	client *client

	mu              sync.Mutex
	assignments     map[string]agentprotocol.DatasourceAssignment
	slis            []agentprotocol.SLICollection
	lease           agentprotocol.Lease
	leaseDeadline   time.Time
	clockSafe       bool
	paused          bool
	nextIntervalEnd map[string]time.Time
	active          map[string]activeCollection
	activeSources   map[string]int

	pending          map[string]*pendingLiveResult
	pendingOrder     []*pendingLiveResult
	resultBytes      int
	dropped          uint64
	losses           map[string]uint64
	lossEarliest     time.Time
	lossLatest       time.Time
	lossLastReport   time.Time
	lossLogger       *slog.Logger
	lastAttempt      *time.Time
	lastSuccess      *time.Time
	notify           chan struct{}
	root             context.Context
	workers          *sync.WaitGroup
	launchCollection func(context.Context, collectionDefinition)
	nextReservation  uint64
}

type activeCollection struct {
	reservation uint64
	cancel      context.CancelFunc
	datasource  string
}

type pendingLiveResult struct {
	deliveryID    string
	raw           []byte
	size          int
	inFlight      bool
	evicted       bool
	attempts      int
	nextRetry     time.Time
	intervalStart time.Time
}

type collectionDefinition struct {
	sli           agentprotocol.SLICollection
	assignment    agentprotocol.DatasourceAssignment
	lease         agentprotocol.Lease
	agentID       string
	intervalEnd   time.Time
	key           string
	reservation   uint64
	ctx           context.Context
	leaseDeadline time.Time
}

func newCollectionRunner(agent *client) *collectionRunner {
	return &collectionRunner{client: agent, assignments: make(map[string]agentprotocol.DatasourceAssignment),
		nextIntervalEnd: make(map[string]time.Time), active: make(map[string]activeCollection), activeSources: make(map[string]int),
		pending: make(map[string]*pendingLiveResult), losses: make(map[string]uint64), lossLogger: slog.Default(), notify: make(chan struct{}, 1), clockSafe: true}
}

func (r *collectionRunner) start(ctx context.Context, workers *sync.WaitGroup) {
	r.mu.Lock()
	r.root = ctx
	r.workers = workers
	r.mu.Unlock()
	workers.Add(1)
	go func() {
		defer workers.Done()
		r.schedule(ctx)
	}()
	for range maxLiveSubmissions {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.upload(ctx)
		}()
	}
}

func (r *collectionRunner) SetLease(lease agentprotocol.Lease) {
	r.mu.Lock()
	deadline, clockSafe := r.leaseDeadline, r.clockSafe
	r.mu.Unlock()
	r.SetLeaseAuthority(lease, deadline, clockSafe)
}

func (r *collectionRunner) SetLeaseAuthority(lease agentprotocol.Lease, deadline time.Time, clockSafe bool) {
	r.mu.Lock()
	r.lease = lease
	r.leaseDeadline = deadline
	r.clockSafe = clockSafe
	if !clockSafe {
		for _, active := range r.active {
			active.cancel()
		}
	}
	r.mu.Unlock()
	r.signal()
}

func (r *collectionRunner) SetConfiguration(snapshot agentprotocol.ConfigurationSnapshot) {
	r.mu.Lock()
	assignments := make(map[string]agentprotocol.DatasourceAssignment, len(snapshot.Assignments))
	for _, assignment := range snapshot.Assignments {
		assignments[assignment.DatasourceID] = assignment
	}
	allowedNext := make(map[string]struct{}, len(snapshot.SLIs))
	for _, definition := range snapshot.SLIs {
		allowedNext[collectionKey(definition)] = struct{}{}
	}
	for key, active := range r.active {
		if _, exists := allowedNext[key]; !exists {
			active.cancel()
		}
	}
	for key := range r.nextIntervalEnd {
		if _, exists := allowedNext[key]; !exists {
			delete(r.nextIntervalEnd, key)
		}
	}
	r.assignments = assignments
	r.slis = append([]agentprotocol.SLICollection(nil), snapshot.SLIs...)
	r.paused = false
	r.mu.Unlock()
	r.signal()
}

func (r *collectionRunner) SetPaused(paused bool) {
	r.mu.Lock()
	r.paused = paused
	r.mu.Unlock()
	r.signal()
}

func (r *collectionRunner) Heartbeat() (progress string, lastAttempt, lastSuccess, nextExpected *time.Time) {
	if r == nil {
		return "not_started", nil, nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	progress = "idle"
	if len(r.active) > 0 || len(r.pending) > 0 {
		progress = "running"
	}
	if len(r.slis) == 0 {
		progress = "not_started"
	}
	if r.lastAttempt != nil {
		value := *r.lastAttempt
		lastAttempt = &value
	}
	if r.lastSuccess != nil {
		value := *r.lastSuccess
		lastSuccess = &value
	}
	for _, definition := range r.slis {
		if next := r.nextIntervalEnd[collectionKey(definition)]; !next.IsZero() {
			assignment := r.assignments[definition.DatasourceID]
			expected := next.Add(time.Duration(assignment.SettlingDelaySeconds) * time.Second)
			if nextExpected == nil || expected.Before(*nextExpected) {
				nextExpected = &expected
			}
		}
	}
	return progress, lastAttempt, lastSuccess, nextExpected
}

func (r *collectionRunner) schedule(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer r.reportLosses(true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.dispatchDue(ctx)
			r.reportLosses(false)
		}
	}
}

func (r *collectionRunner) dispatchDue(ctx context.Context) {
	now := r.client.currentServerTime().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return
	}
	r.mu.Lock()
	if r.paused || !r.clockSafe || len(r.slis) == 0 ||
		!r.lease.ValidUntil.After(now) || !r.lease.ValidFrom.Before(now.Add(time.Minute)) {
		r.mu.Unlock()
		return
	}
	definitions := make([]collectionDefinition, 0)
	for _, current := range r.slis {
		if len(r.active) >= maxCollectionQueries {
			break
		}
		key := collectionKey(current)
		if _, alreadyActive := r.active[key]; alreadyActive {
			continue
		}
		if !hasGrant(r.lease, current.SLIID, current.MeasurementRevision) {
			continue
		}
		assignment, exists := r.assignments[current.DatasourceID]
		if !exists || assignment.DatasourceVersion != current.DatasourceVersion {
			continue
		}
		end := r.nextIntervalEnd[key]
		if end.IsZero() {
			end = nextIntervalEnd(now, current.EffectiveFrom, assignment.PollingIntervalSeconds)
			r.nextIntervalEnd[key] = end
		}
		readyAt := end.Add(time.Duration(assignment.SettlingDelaySeconds) * time.Second)
		if now.Before(readyAt) {
			continue
		}
		cadence := time.Duration(assignment.PollingIntervalSeconds) * time.Second
		if now.Sub(readyAt) >= cadence {
			end = nextIntervalEnd(now, current.EffectiveFrom, assignment.PollingIntervalSeconds)
			r.nextIntervalEnd[key] = end
			continue
		}
		if r.activeSources[current.DatasourceID] >= maxCollectionPerSource {
			continue
		}
		lease := r.lease
		collectionCtx, cancel := context.WithCancel(ctx)
		r.nextReservation++
		reservation := r.nextReservation
		r.active[key] = activeCollection{reservation: reservation, cancel: cancel, datasource: current.DatasourceID}
		r.activeSources[current.DatasourceID]++
		r.nextIntervalEnd[key] = end.Add(cadence)
		definitions = append(definitions, collectionDefinition{sli: current, assignment: assignment, lease: lease,
			agentID: lease.AgentID, intervalEnd: end, key: key, reservation: reservation, ctx: collectionCtx,
			leaseDeadline: r.leaseDeadline})
		if r.workers != nil {
			r.workers.Add(1)
		}
	}
	launch := r.launchCollection
	workers := r.workers
	r.mu.Unlock()
	for _, definition := range definitions {
		go func(definition collectionDefinition) {
			if workers != nil {
				defer workers.Done()
			}
			if launch != nil {
				launch(definition.ctx, definition)
				return
			}
			r.collect(definition.ctx, definition)
		}(definition)
	}
}

func (r *collectionRunner) collect(parent context.Context, definition collectionDefinition) {
	finished := false
	complete := func(outcome *sli.ExecutionOutcome, successfulQueryAt *time.Time) {
		r.finishCollection(definition, outcome, successfulQueryAt)
		finished = true
	}
	defer func() {
		if !finished {
			complete(nil, nil)
		}
	}()
	serverNow := r.client.currentServerTime().UTC()
	remaining := definition.lease.ValidUntil.Sub(serverNow)
	deadline := definition.leaseDeadline
	if !deadline.IsZero() {
		remaining = min(remaining, time.Until(deadline))
	}
	if remaining <= 0 {
		return
	}
	queryTimeout := min(maxCollectionQueryTime, remaining)
	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return
	}
	r.mu.Lock()
	if active, exists := r.active[definition.key]; !exists || active.reservation != definition.reservation {
		r.mu.Unlock()
		return
	}
	attemptedAt := r.client.currentServerTime().UTC().Truncate(time.Microsecond)
	r.lastAttempt = &attemptedAt
	r.mu.Unlock()

	settings := datasource.Settings{Name: definition.assignment.Name, DisplayName: definition.assignment.Name,
		Provider: definition.assignment.Provider, ConfigurationFormat: definition.assignment.ConfigurationFormat,
		Endpoint:           definition.assignment.Endpoint,
		TLS:                datasource.TLSSettings{InsecureSkipVerify: definition.assignment.TLS.InsecureSkipVerify, CAPEM: definition.assignment.TLS.CAPEM},
		AuthenticationMode: definition.assignment.AuthenticationMode, AuthenticationUser: definition.assignment.Username,
		PollingIntervalSeconds: definition.assignment.PollingIntervalSeconds,
		SettlingDelaySeconds:   definition.assignment.SettlingDelaySeconds, AgentID: definition.agentID}
	tlsConfig, err := settings.ClientTLSConfig()
	if err != nil {
		complete(failedCollectionOutcome("unsupported"), nil)
		return
	}
	endpoint, _ := url.Parse(definition.assignment.Endpoint)
	source, err := prometheus.NewClient(prometheus.Config{Endpoint: definition.assignment.Endpoint,
		AuthMode: definition.assignment.AuthenticationMode, Username: definition.assignment.Username,
		Password: definition.assignment.Password, BearerToken: definition.assignment.BearerToken,
		TLSConfig: tlsConfig, Timeout: queryTimeout, AllowedDestinations: []string{endpoint.Hostname()}})
	if err != nil {
		complete(failedCollectionOutcome("unsupported"), nil)
		return
	}
	good, goodErr := source.QueryAt(ctx, definition.sli.GoodQuery, definition.intervalEnd)
	if goodErr != nil {
		complete(failedCollectionOutcome(collectionErrorCode(goodErr)), nil)
		return
	}
	total, totalErr := source.QueryAt(ctx, definition.sli.TotalQuery, definition.intervalEnd)
	if totalErr != nil {
		complete(failedCollectionOutcome(collectionErrorCode(totalErr)), nil)
		return
	}
	successAt := r.client.currentServerTime().UTC().Truncate(time.Microsecond)
	outcome := collectedOutcomeAt(good, true, good.Class == "incomplete", total, true, total.Class == "incomplete", definition.intervalEnd)
	complete(outcome, &successAt)
}

func (r *collectionRunner) finishCollection(definition collectionDefinition, outcome *sli.ExecutionOutcome, successfulQueryAt *time.Time) {
	r.mu.Lock()
	if active, exists := r.active[definition.key]; exists && active.reservation == definition.reservation {
		active.cancel()
		delete(r.active, definition.key)
		if r.activeSources[active.datasource] > 1 {
			r.activeSources[active.datasource]--
		} else {
			delete(r.activeSources, active.datasource)
		}
	}
	if successfulQueryAt != nil {
		value := *successfulQueryAt
		r.lastSuccess = &value
	}
	r.mu.Unlock()
	if outcome == nil {
		return
	}
	createdAt := r.client.currentServerTime().UTC().Truncate(time.Microsecond)
	if !createdAt.Before(definition.lease.ValidUntil) || createdAt.Before(definition.lease.ValidFrom) ||
		(!definition.leaseDeadline.IsZero() && !time.Now().Before(definition.leaseDeadline)) {
		return
	}
	intervalEnd := definition.intervalEnd.UTC().Truncate(time.Microsecond)
	intervalStart := intervalEnd.Add(-time.Duration(definition.assignment.PollingIntervalSeconds) * time.Second)
	executionID, err := newMessageIDUUID()
	if err != nil {
		r.recordDrop("encoding", intervalStart)
		return
	}
	deliveryID, err := newMessageIDUUID()
	if err != nil {
		r.recordDrop("encoding", intervalStart)
		return
	}
	var encodedOutcome jsontext.Value
	switch outcome.State {
	case sli.OutcomeObserved:
		encodedOutcome, err = json.Marshal(struct {
			State      string `json:"state"`
			GoodCount  string `json:"good_count"`
			TotalCount string `json:"total_count"`
		}{"observed", outcome.GoodCount, outcome.TotalCount})
	case sli.OutcomeCompleteEmpty:
		encodedOutcome, err = json.Marshal(struct {
			State string `json:"state"`
		}{"complete_empty"})
	case sli.OutcomeIncomplete:
		encodedOutcome, err = json.Marshal(struct {
			State   string   `json:"state"`
			Missing []string `json:"missing_components"`
		}{"incomplete", slices.Clone(outcome.MissingComponents)})
	case sli.OutcomeFailed:
		encodedOutcome, err = json.Marshal(struct {
			State     string `json:"state"`
			ErrorCode string `json:"error_code"`
		}{"failed", outcome.ErrorCode})
	default:
		r.recordDrop("encoding", intervalStart)
		return
	}
	if err != nil {
		r.recordDrop("encoding", intervalStart)
		return
	}
	created := struct {
		DeliveryID            string         `json:"delivery_id"`
		ExecutionID           string         `json:"execution_id"`
		CreatedAt             time.Time      `json:"created_at"`
		LeaseID               string         `json:"lease_id"`
		ConfigurationRevision string         `json:"configuration_revision"`
		SessionGeneration     string         `json:"session_generation"`
		SLIID                 string         `json:"sli_id"`
		MeasurementRevision   string         `json:"measurement_revision"`
		IntervalStart         time.Time      `json:"interval_start"`
		IntervalEnd           time.Time      `json:"interval_end"`
		Outcome               jsontext.Value `json:"outcome"`
	}{deliveryID, executionID, createdAt, definition.lease.LeaseID, definition.lease.ConfigurationRevision,
		definition.lease.SessionGeneration, definition.sli.SLIID, definition.sli.MeasurementRevision,
		intervalStart, intervalEnd, encodedOutcome}
	raw, err := json.Marshal(created)
	if err != nil || len(raw) > 1<<20 {
		r.recordDrop("encoding", intervalStart)
		return
	}
	r.enqueue(deliveryID, raw, intervalStart)
}

func collectedOutcome(good prometheus.QueryResult, goodOK, goodIncomplete bool, total prometheus.QueryResult, totalOK, totalIncomplete bool) *sli.ExecutionOutcome {
	return collectedOutcomeAt(good, goodOK, goodIncomplete, total, totalOK, totalIncomplete, time.Time{})
}

func collectedOutcomeAt(good prometheus.QueryResult, goodOK, goodIncomplete bool, total prometheus.QueryResult, totalOK, totalIncomplete bool, evaluationEnd time.Time) *sli.ExecutionOutcome {
	if !evaluationEnd.IsZero() && (!resultAligned(good, evaluationEnd) || !resultAligned(total, evaluationEnd)) {
		return failedCollectionOutcome("query")
	}
	goodValue, hasGood, goodFailed := singleCount(good, goodOK, goodIncomplete)
	totalValue, hasTotal, totalFailed := singleCount(total, totalOK, totalIncomplete)
	if goodFailed || totalFailed {
		return failedCollectionOutcome("unsupported")
	}
	missing := make([]string, 0, 2)
	if !hasGood {
		missing = append(missing, "good")
	}
	if !hasTotal {
		missing = append(missing, "total")
	}
	if len(missing) == 2 && !goodIncomplete && !totalIncomplete {
		return &sli.ExecutionOutcome{State: sli.OutcomeCompleteEmpty}
	}
	if len(missing) > 0 {
		return &sli.ExecutionOutcome{State: sli.OutcomeIncomplete, MissingComponents: missing}
	}
	goodValue, totalValue, err := sli.ValidateIntervalCounts(goodValue, totalValue)
	if err != nil {
		return failedCollectionOutcome("query")
	}
	return &sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: goodValue, TotalCount: totalValue}
}

func resultAligned(result prometheus.QueryResult, evaluationEnd time.Time) bool {
	if result.Class != "complete" {
		return true
	}
	if len(result.Series) != 1 {
		return true // singleCount reports the shape as unsupported.
	}
	seconds, err := strconv.ParseFloat(result.Series[0].Time, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return false
	}
	actual := time.UnixMicro(int64(math.Round(seconds * 1_000_000))).UTC()
	return actual.Equal(evaluationEnd.UTC().Truncate(time.Microsecond))
}

func singleCount(result prometheus.QueryResult, success, incomplete bool) (string, bool, bool) {
	if incomplete || result.Class == "incomplete" {
		return "", false, false
	}
	if !success {
		return "", false, true
	}
	switch result.Class {
	case "empty":
		return "", false, false
	case "complete":
		if len(result.Series) != 1 {
			return "", false, true
		}
		return result.Series[0].Value, true, false
	default:
		return "", false, true
	}
}

func failedCollectionOutcome(code string) *sli.ExecutionOutcome {
	return &sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: code}
}

func collectionErrorCode(err error) string {
	var sourceErr *prometheus.SourceError
	if errors.As(err, &sourceErr) {
		switch sourceErr.Code {
		case "authentication", "incomplete", "network", "query", "rate_limited", "tls", "timeout", "unsupported", "deadline":
			return sourceErr.Code
		}
	}
	return "network"
}

func (r *collectionRunner) enqueue(deliveryID string, raw []byte, intervalStart time.Time) {
	if len(raw) > 1<<20 {
		r.recordDrop("encoding", intervalStart)
		return
	}
	entry := &pendingLiveResult{deliveryID: deliveryID, raw: bytes.Clone(raw), size: len(raw)*2 + 512, nextRetry: time.Now(), intervalStart: intervalStart}
	r.mu.Lock()
	if _, exists := r.pending[deliveryID]; exists || entry.size > maxLiveResultBytes {
		r.recordDropLocked("buffer", intervalStart)
		r.mu.Unlock()
		return
	}
	r.pending[deliveryID] = entry
	index := len(r.pendingOrder)
	for i, existing := range r.pendingOrder {
		if intervalStart.Before(existing.intervalStart) {
			index = i
			break
		}
	}
	r.pendingOrder = slices.Insert(r.pendingOrder, index, entry)
	r.resultBytes += entry.size
	for r.resultBytes > maxLiveResultBytes {
		if !r.evictOldestLocked() {
			break
		}
	}
	if r.resultBytes > maxLiveResultBytes && r.pending[deliveryID] == entry {
		delete(r.pending, deliveryID)
		entry.evicted = true
		r.recordDropLocked("buffer", entry.intervalStart)
		r.resultBytes -= entry.size
		entry.raw = nil
	}
	r.compactOrderLocked()
	r.mu.Unlock()
	r.signal()
}

func (r *collectionRunner) recordDrop(reason string, interval time.Time) {
	r.mu.Lock()
	r.recordDropLocked(reason, interval)
	r.mu.Unlock()
}

func (r *collectionRunner) recordDropLocked(reason string, interval time.Time) {
	r.dropped++
	r.losses[reason]++
	if !interval.IsZero() {
		if r.lossEarliest.IsZero() || interval.Before(r.lossEarliest) {
			r.lossEarliest = interval
		}
		if r.lossLatest.IsZero() || interval.After(r.lossLatest) {
			r.lossLatest = interval
		}
	}
}

func (r *collectionRunner) reportLosses(force bool) {
	r.mu.Lock()
	if len(r.losses) == 0 || !force && time.Since(r.lossLastReport) < time.Minute {
		r.mu.Unlock()
		return
	}
	counts := r.losses
	earliest, latest := r.lossEarliest, r.lossLatest
	r.losses = make(map[string]uint64)
	r.lossEarliest, r.lossLatest, r.lossLastReport = time.Time{}, time.Time{}, time.Now()
	r.mu.Unlock()
	r.lossLogger.Warn("live observation loss", "buffer", counts["buffer"], "encoding", counts["encoding"], "terminal", counts["terminal"], "earliest_interval_start", earliest, "latest_interval_start", latest)
}

func (r *collectionRunner) evictOldestLocked() bool {
	for len(r.pendingOrder) > 0 {
		entry := r.pendingOrder[0]
		r.pendingOrder[0] = nil
		r.pendingOrder = r.pendingOrder[1:]
		if r.pending[entry.deliveryID] != entry {
			continue
		}
		delete(r.pending, entry.deliveryID)
		entry.evicted = true
		r.recordDropLocked("buffer", entry.intervalStart)
		if !entry.inFlight {
			r.resultBytes -= entry.size
			entry.raw = nil
		}
		return true
	}
	return false
}

func (r *collectionRunner) compactOrderLocked() {
	if len(r.pendingOrder) <= len(r.pending)*2+64 {
		return
	}
	compacted := make([]*pendingLiveResult, 0, len(r.pending))
	for _, entry := range r.pendingOrder {
		if r.pending[entry.deliveryID] == entry {
			compacted = append(compacted, entry)
		}
	}
	r.pendingOrder = compacted
}

func (r *collectionRunner) upload(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	defer r.reportLosses(true)
	for {
		batch := r.takeBatch()
		if len(batch) > 0 {
			r.submit(ctx, batch)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-r.notify:
		case <-ticker.C:
		}
	}
}

func (r *collectionRunner) takeBatch() []*pendingLiveResult {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	batch := make([]*pendingLiveResult, 0, maxUploadBatchItems)
	bodyBytes := len(`{"items":[]}`)
	for _, entry := range r.pendingOrder {
		if r.pending[entry.deliveryID] != entry || entry.inFlight || entry.nextRetry.After(now) {
			continue
		}
		if bodyBytes+len(entry.raw)+1 > 4<<20 {
			continue
		}
		bodyBytes += len(entry.raw) + 1
		entry.inFlight = true
		batch = append(batch, entry)
		if len(batch) >= maxUploadBatchItems {
			break
		}
	}
	return batch
}

func (r *collectionRunner) submit(ctx context.Context, batch []*pendingLiveResult) {
	size := len(`{"items":[]}`) + len(batch)
	for _, entry := range batch {
		size += len(entry.raw)
	}
	body := make([]byte, 0, size)
	body = append(body, `{"items":[`...)
	for index, entry := range batch {
		if index > 0 {
			body = append(body, ',')
		}
		body = append(body, entry.raw...)
	}
	body = append(body, `]}`...)
	u, err := url.Parse(r.client.opts.backendURL)
	if err == nil {
		u.Path = "/api/agent/v1/observations"
		u.RawQuery = ""
	}
	var response *http.Response
	if err == nil {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if requestErr == nil {
			request.Header.Set("Authorization", "Bearer "+r.client.token)
			request.Header.Set("Content-Type", "application/json")
			response, err = r.client.http.Do(request)
		} else {
			err = requestErr
		}
	}
	if err != nil || response == nil {
		for _, entry := range batch {
			r.retry(entry)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusRequestTimeout {
			for _, entry := range batch {
				r.recordDrop("terminal", entry.intervalStart)
				r.finishUpload(entry, true)
			}
			return
		}
		for _, entry := range batch {
			r.retryAfter(entry, response.Header.Get("Retry-After"))
		}
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	var result api.ObservationBatchResponse
	if readErr != nil || len(data) > 4<<20 || json.Unmarshal(data, &result) != nil || len(result.Items) != len(batch) {
		for _, entry := range batch {
			r.retry(entry)
		}
		return
	}
	for index, item := range result.Items {
		if item.DeliveryId.String() != batch[index].deliveryID || !item.Status.Valid() {
			r.retry(batch[index])
			continue
		}
		if item.Status == api.Retryable {
			r.retry(batch[index])
		} else {
			if item.Status != api.Accepted && item.Status != api.Duplicate {
				r.recordDrop("terminal", batch[index].intervalStart)
			}
			r.finishUpload(batch[index], true)
		}
	}
}

func (r *collectionRunner) retry(entry *pendingLiveResult) {
	r.retryAfter(entry, "")
}

func (r *collectionRunner) retryAfter(entry *pendingLiveResult, guidance string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending[entry.deliveryID] != entry {
		if entry.evicted {
			r.resultBytes -= entry.size
			entry.evicted = false
			entry.raw = nil
		}
		return
	}
	entry.inFlight = false
	entry.attempts++
	delay := min(time.Second<<min(entry.attempts-1, 5), collectionRetryMax)
	delay = delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
	if seconds, err := strconv.Atoi(guidance); err == nil && seconds > 0 {
		delay = max(delay, time.Duration(min(seconds, int(collectionRetryMax/time.Second)))*time.Second)
	} else if deadline, err := http.ParseTime(guidance); err == nil {
		delay = max(delay, min(time.Until(deadline), collectionRetryMax))
	}
	entry.nextRetry = time.Now().Add(delay)
}

func (r *collectionRunner) finishUpload(entry *pendingLiveResult, terminal bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending[entry.deliveryID] == entry {
		entry.inFlight = false
		if terminal {
			delete(r.pending, entry.deliveryID)
			r.resultBytes -= entry.size
			entry.raw = nil
			for i, pending := range r.pendingOrder {
				if pending == entry {
					r.pendingOrder = slices.Delete(r.pendingOrder, i, i+1)
					break
				}
			}
		}
	} else if entry.evicted {
		r.resultBytes -= entry.size
		entry.evicted = false
		entry.raw = nil
	}
}

func (r *collectionRunner) signal() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func hasGrant(lease agentprotocol.Lease, sliID, revision string) bool {
	for _, grant := range lease.AllowedMeasurements {
		if grant.SLIID == sliID && grant.MeasurementRevision == revision {
			return true
		}
	}
	return false
}

func collectionKey(definition agentprotocol.SLICollection) string {
	return definition.SLIID + ":" + definition.MeasurementRevision
}

func nextIntervalEnd(now, effectiveFrom time.Time, cadenceSeconds int) time.Time {
	cadence := int64(cadenceSeconds) * 1_000_000
	nowMicro := now.UTC().UnixMicro()
	next := (nowMicro/cadence + 1) * cadence
	activationEnd := effectiveFrom.UTC().UnixMicro() + cadence
	if next < activationEnd {
		next = activationEnd
	}
	return time.UnixMicro(next).UTC()
}

func newMessageIDUUID() (string, error) {
	value, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return value.String(), nil
}
