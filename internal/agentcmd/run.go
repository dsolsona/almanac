// Package agentcmd implements the outbound-only almanac-agent executable.
package agentcmd

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/secretfile"
)

const maxTokenBytes = 2048

type options struct {
	backendURL string
	tokenFile  string
	caFile     string
}

type client struct {
	opts                 options
	token                string
	resume               string
	generation           string
	lease                agentprotocol.Lease
	protocol             string
	leaseBaseline        *agentprotocol.ConfigurationSnapshot
	revision             string
	sequence             int64
	pending              *agentprotocol.Heartbeat
	pendingSentAt        time.Time
	pendingServerAtSend  time.Time
	pendingLatestSentAt  time.Time
	leaseDeadline        time.Time
	clockSafe            bool
	agentID              string
	assignments          []agentprotocol.DatasourceAssignment
	slis                 []agentprotocol.SLICollection
	diagnostics          *diagnosticRunner
	collections          *collectionRunner
	serverClock          *serverClockEstimate
	established          bool
	desiredRevision      string
	configurationPending bool
	http                 *http.Client
	diagnosticUploads    chan diagnosticUpload
}

type serverClockEstimate struct {
	mu              sync.RWMutex
	serverTime      time.Time
	leaseServerTime time.Time
	serverAt        time.Time
}

func Run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("invalid command")
	}
	flags := flag.NewFlagSet("almanac-agent run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var opts options
	flags.StringVar(&opts.backendURL, "backend-url", "", "HTTPS backend origin")
	flags.StringVar(&opts.tokenFile, "token-file", "", "protected agent credential file")
	flags.StringVar(&opts.caFile, "ca-file", "", "optional additional CA certificate file")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || opts.backendURL == "" || opts.tokenFile == "" {
		return errors.New("invalid command options")
	}
	u, err := url.Parse(opts.backendURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
		return errors.New("backend URL must be an HTTPS origin")
	}
	tokenBytes, err := secretfile.Read(opts.tokenFile, maxTokenBytes)
	if err != nil {
		return errors.New("credential file is unavailable")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("credential file is invalid")
	}
	client, err := newClient(opts, token)
	if err != nil {
		return err
	}
	client.diagnostics = newDiagnosticRunner(client)
	defer client.diagnostics.close()
	uploadCtx, cancelUploads := context.WithCancel(ctx)
	var uploadWorkers sync.WaitGroup
	client.startDiagnosticUploads(uploadCtx, &uploadWorkers)
	client.collections = newCollectionRunner(client)
	client.collections.start(uploadCtx, &uploadWorkers)
	defer func() {
		cancelUploads()
		uploadWorkers.Wait()
	}()
	return client.run(ctx)
}

func newClient(opts options, token string) (*client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.caFile != "" {
		pem, err := secretfile.ReadCertificate(opts.caFile)
		if err != nil {
			return nil, errors.New("custom CA file is unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("custom CA file is invalid")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	var proof [32]byte
	if _, err := rand.Read(proof[:]); err != nil {
		return nil, errors.New("secure random source unavailable")
	}
	return &client{opts: opts, token: token, resume: base64.RawURLEncoding.EncodeToString(proof[:]), revision: "0", serverClock: &serverClockEstimate{}, clockSafe: true, diagnosticUploads: make(chan diagnosticUpload, maxActiveDiagnostics),
		http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

func (c *client) run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		c.established = false
		err := c.runSession(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrOwnerConflict) || errors.Is(err, ErrRevoked) || errors.Is(err, ErrCredential) || errors.Is(err, ErrIncompatible) || errors.Is(err, ErrSessionRejected) {
			return err
		}
		backoff = retryDelay(backoff, c.established)
		if !waitRetry(ctx, backoff) {
			return nil
		}
		backoff = nextBackoff(backoff)
	}
	return nil
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current >= 16*time.Second {
		return 30 * time.Second
	}
	if current < time.Second {
		return time.Second
	}
	return current * 2
}

func retryDelay(current time.Duration, established bool) time.Duration {
	if established {
		return time.Second
	}
	return current
}

func (c *client) runSession(ctx context.Context) error {
	if c.diagnostics == nil {
		c.diagnostics = newDiagnosticRunner(c)
	}
	u, err := url.Parse(c.opts.backendURL)
	if err != nil {
		return err
	}
	u.Scheme = "wss"
	u.Path = "/api/agent/v1/connect"
	header := http.Header{"Authorization": []string{"Bearer " + c.token}}
	conn, response, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: header, Subprotocols: []string{agentprotocol.ProtocolV2, agentprotocol.ProtocolV1}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return ErrCredential
		}
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(agentprotocol.MaxMessageBytes)
	nonce, err := newNonce()
	if err != nil {
		return err
	}
	if c.generation == "" {
		c.generation = agentprotocol.InitialGeneration
	}
	messageID, err := newMessageID()
	if err != nil {
		return err
	}
	hello := agentprotocol.Envelope[agentprotocol.Hello]{Kind: agentprotocol.KindHello, MessageID: messageID, SessionGeneration: c.generation,
		Payload: agentprotocol.Hello{ProtocolVersions: []string{agentprotocol.ProtocolV2, agentprotocol.ProtocolV1}, SoftwareVersion: "1.0.0",
			Capabilities: []agentprotocol.Capability{{Provider: datasource.PrometheusProvider,
				ConfigurationFormats: []string{datasource.PrometheusConfiguration}, Operations: []string{"collect", "connection_test", "query_preview"}}}, ResumeToken: c.resume, RequestNonce: nonce}}
	if err := hello.Validate(); err != nil {
		return err
	}
	helloSentAt := time.Now()
	writeCtx, cancelWrite := context.WithTimeout(ctx, 5*time.Second)
	err = writeJSON(writeCtx, conn, hello)
	cancelWrite()
	if err != nil {
		return err
	}
	helloCtx, cancelHello := context.WithTimeout(ctx, 5*time.Second)
	var welcome agentprotocol.Envelope[agentprotocol.Welcome]
	err = readJSON(helloCtx, conn, &welcome)
	cancelHello()
	welcomeReceivedAt := time.Now()
	if err == nil && welcome.Kind == agentprotocol.KindWelcome && welcome.Payload.Protocol != conn.Subprotocol() {
		return ErrIncompatible
	}
	if err != nil || welcome.Kind != agentprotocol.KindWelcome || welcome.Validate() != nil || welcome.Payload.RequestNonce != nonce {
		if err != nil {
			return err
		}
		return errors.New("backend welcome is invalid")
	}
	c.protocol = welcome.Payload.Protocol
	if welcomeReceivedAt.Sub(helloSentAt) > 5*time.Second {
		return errors.New("backend welcome exceeded the safe lease response window")
	}
	if c.agentID != "" && c.agentID != welcome.Payload.AgentID {
		return errors.New("backend agent identity changed")
	}
	if c.generation != agentprotocol.InitialGeneration && c.generation != welcome.SessionGeneration {
		return errors.New("backend ownership generation changed")
	}
	welcomeSequence, err := parseDecimal(welcome.Payload.RenewalSequence)
	if err != nil || validateWelcomeSequence(c.sequence, c.pending, welcomeSequence) != nil {
		return errors.New("backend renewal state is invalid")
	}
	if previousServerTime := c.serverClockTime(); !previousServerTime.IsZero() && welcome.Payload.ServerTime.Before(previousServerTime) {
		return errors.New("backend time moved backwards")
	}
	candidate := *c
	candidate.agentID = welcome.Payload.AgentID
	candidate.generation = welcome.SessionGeneration
	candidate.revision = welcome.Payload.ConfigurationRevision
	if err := candidate.validateLease(welcome.Payload.ExecutionLease, welcome.Payload.ServerTime); err != nil {
		return err
	}
	deadline, err := leaseDeadlineFromResponse(welcome.Payload.ExecutionLease, welcome.Payload.ServerTime, helloSentAt, welcomeReceivedAt)
	if err != nil {
		return errors.New("execution lease has no conservative remaining validity")
	}
	if !c.pendingSentAt.IsZero() && c.pendingSentAt.Before(helloSentAt) {
		serverAtPendingSend := c.pendingServerAtSend
		if serverAtPendingSend.IsZero() {
			return errors.New("lost heartbeat renewal has no safe send-time clock evidence")
		}
		deadline, err = leaseDeadlineFrom(welcome.Payload.ExecutionLease, serverAtPendingSend, c.pendingSentAt)
		if err != nil || !deadline.After(welcomeReceivedAt) {
			return errors.New("retained execution lease has no conservative remaining validity")
		}
	}
	clockSafe := !serverClockSkewed(welcome.Payload.ServerTime, welcomeReceivedAt) && welcomeReceivedAt.Sub(helloSentAt) <= 5*time.Second
	c.established = true
	c.agentID = candidate.agentID
	c.setServerTimeAt(welcome.Payload.ServerTime, helloSentAt, welcomeReceivedAt)
	c.leaseDeadline = deadline
	c.clockSafe = clockSafe
	c.generation = candidate.generation
	c.lease = welcome.Payload.ExecutionLease
	if c.collections != nil {
		c.collections.SetLeaseAuthority(c.lease, deadline, clockSafe)
		if !clockSafe {
			c.collections.SetPaused(true)
		}
		if c.configurationPending || c.pending != nil {
			c.collections.SetPaused(true)
		}
	}
	// Preserve the local expected sequence until replay resolves a pending
	// heartbeat. A welcome at pending+1 means the prior ack was lost.
	if c.pending == nil {
		c.sequence = welcomeSequence
	}
	if c.pending == nil {
		if err := c.reconcile(ctx, conn); err != nil {
			return err
		}
	} else {
		// A reconnect with an unresolved heartbeat must re-fetch configuration
		// after its ACK before any collection can resume, even at the same revision.
		c.configurationPending = true
	}
	interval := time.Duration(welcome.Payload.HeartbeatIntervalMS) * time.Millisecond
	if c.pending == nil {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return closeAgentSession(conn)
		case <-timer.C:
		}
	}
	if c.pending == nil {
		if err := c.prepareHeartbeat(); err != nil {
			return err
		}
	}
	if err := c.writeHeartbeat(ctx, conn); err != nil {
		return err
	}
	type readResult struct {
		raw jsontext.Value
		err error
	}
	reads := make(chan readResult, 1)
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	go func() {
		for {
			var raw jsontext.Value
			err := readRaw(sessionCtx, conn, &raw)
			select {
			case reads <- readResult{raw: raw, err: err}:
			case <-sessionCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	heartbeatTimer := time.NewTimer(interval)
	defer heartbeatTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return closeAgentSession(conn)
		case result := <-reads:
			if result.err != nil {
				return result.err
			}
			wasPending := c.pending != nil
			if err := c.handleMessage(sessionCtx, conn, result.raw); err != nil {
				return err
			}
			if wasPending && c.pending == nil {
				heartbeatTimer.Reset(interval)
			}
		case diagnosticResult := <-c.diagnostics.results:
			canonical, err := diagnosticResult.result.CanonicalJSON()
			if err != nil {
				diagnosticTransferFailure()
				continue
			}
			if len(canonical) > agentprotocol.MaxInlineDiagnosticResultBytes {
				if len(canonical) > agentprotocol.MaxHTTPSDiagnosticResultBytes || !c.queueDiagnosticUpload(ctx, diagnosticResult, canonical) {
					diagnosticTransferFailure()
				}
				continue
			}
			messageID, err := newMessageID()
			if err != nil {
				return err
			}
			message := agentprotocol.Envelope[agentprotocol.DiagnosticResult]{Kind: agentprotocol.KindDiagnosticResult,
				MessageID: messageID, SessionGeneration: diagnosticResult.generation, Payload: diagnosticResult.result}
			if err := message.Validate(); err != nil {
				return errors.New("agent diagnostic result is invalid")
			}
			writeCtx, cancelWrite := context.WithTimeout(ctx, 5*time.Second)
			err = writeJSON(writeCtx, conn, message)
			cancelWrite()
			if err != nil {
				return err
			}
		case <-heartbeatTimer.C:
			if c.pending == nil {
				if err := c.prepareHeartbeat(); err != nil {
					return err
				}
			}
			if err := c.writeHeartbeat(ctx, conn); err != nil {
				return err
			}
			heartbeatTimer.Reset(interval)
		}
	}
}

func (c *client) reconcile(ctx context.Context, conn *websocket.Conn) error {
	u, _ := url.Parse(c.opts.backendURL)
	u.Path = "/api/agent/v1/configuration"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusServiceUnavailable {
		c.configurationPending = true
		if c.collections != nil && c.protocol != agentprotocol.ProtocolV2 {
			c.collections.SetPaused(true)
		}
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("configuration fetch failed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, agentprotocol.MaxConfigurationBytes+1))
	if err != nil || len(body) > agentprotocol.MaxConfigurationBytes {
		return errors.New("configuration snapshot is invalid")
	}
	snapshot, err := agentprotocol.DecodeConfigurationSnapshot(body)
	if err != nil || snapshot.AgentID != c.lease.AgentID {
		return errors.New("configuration snapshot does not match execution lease")
	}
	wantETag := `"agent-config:` + snapshot.AgentID + `:` + snapshot.ConfigurationRevision + `"`
	if response.Header.Get("ETag") != wantETag {
		return errors.New("configuration validator is invalid")
	}
	var transitionAllowed map[string]bool
	transition := snapshot.ConfigurationRevision != c.lease.ConfigurationRevision
	if transition {
		if c.protocol != agentprotocol.ProtocolV2 || c.revision == "" || revisionAfter(c.lease.ConfigurationRevision, snapshot.ConfigurationRevision) {
			return errors.New("configuration snapshot does not match execution lease")
		}
		current := agentprotocol.ConfigurationSnapshot{AgentID: c.lease.AgentID, ConfigurationRevision: c.revision, Assignments: c.assignments, SLIs: c.slis}
		if c.leaseBaseline == nil {
			if c.revision != c.lease.ConfigurationRevision {
				return errors.New("previous configuration is not authorized")
			}
			c.leaseBaseline = &current
		}
		if agentprotocol.ValidateSnapshotLease(*c.leaseBaseline, c.lease) != nil {
			return errors.New("previous configuration is not authorized")
		}
		transitionAllowed, err = unchangedTransitionPairs(current, snapshot)
		if err != nil {
			return err
		}
		retainBaselineAuthorized(transitionAllowed, *c.leaseBaseline, snapshot)
	} else if agentprotocol.ValidateSnapshotLease(snapshot, c.lease) != nil {
		return errors.New("configuration snapshot does not match execution lease")
	} else {
		c.leaseBaseline = nil
	}
	drainCtx, cancelDrain := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDrain()
	if c.diagnostics != nil {
		if err := c.diagnostics.reconcileAssignmentsAndDrain(drainCtx, snapshot.Assignments); err != nil {
			return err
		}
	}
	if c.collections != nil {
		var err error
		if transition {
			err = c.collections.SetTransitionConfigurationAndDrain(drainCtx, snapshot, transitionAllowed)
		} else {
			err = c.collections.SetConfigurationAndDrain(drainCtx, snapshot)
		}
		if err != nil {
			return err
		}
		c.collections.SetLease(c.lease)
	}
	messageID, err := newMessageID()
	if err != nil {
		return err
	}
	applied := agentprotocol.Envelope[agentprotocol.ConfigurationApplied]{Kind: agentprotocol.KindConfigurationApplied, MessageID: messageID, SessionGeneration: c.generation,
		Payload: agentprotocol.ConfigurationApplied{ConfigurationRevision: snapshot.ConfigurationRevision}}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := writeJSON(writeCtx, conn, applied); err != nil {
		return err
	}
	c.revision = snapshot.ConfigurationRevision
	c.desiredRevision = ""
	c.configurationPending = false
	c.assignments = append([]agentprotocol.DatasourceAssignment{}, snapshot.Assignments...)
	c.slis = append([]agentprotocol.SLICollection{}, snapshot.SLIs...)
	if c.collections != nil {
		c.collections.SetLease(c.lease)
	}
	return nil
}

func unchangedTransitionPairs(previous, next agentprotocol.ConfigurationSnapshot) (map[string]bool, error) {
	if previous.AgentID != next.AgentID || next.Validate() != nil {
		return nil, errors.New("invalid transition configuration")
	}
	assignments := make(map[string]agentprotocol.DatasourceAssignment, len(previous.Assignments))
	for _, a := range previous.Assignments {
		assignments[a.DatasourceID] = a
	}
	definitions := make(map[string]agentprotocol.SLICollection, len(previous.SLIs))
	for _, def := range previous.SLIs {
		definitions[def.SLIID] = def
	}
	nextAssignments := make(map[string]agentprotocol.DatasourceAssignment, len(next.Assignments))
	for _, a := range next.Assignments {
		nextAssignments[a.DatasourceID] = a
	}
	allowed := make(map[string]bool)
	for _, def := range next.SLIs {
		old, exists := definitions[def.SLIID]
		if !exists {
			continue
		}
		previousAssignment, nextAssignment := assignments[old.DatasourceID], nextAssignments[def.DatasourceID]
		if old == def && previousAssignment == nextAssignment {
			allowed[collectionKey(def)] = true
			continue
		}
		// A cancellation can change only the datasource resource version. Treat
		// it as a removal and withheld addition until a matching grant arrives.
		previousAssignment.DatasourceVersion = nextAssignment.DatasourceVersion
		old.DatasourceVersion = def.DatasourceVersion
		if old != def || previousAssignment != nextAssignment {
			return nil, errors.New("transition changed an existing collection")
		}
	}
	return allowed, nil
}

func retainBaselineAuthorized(allowed map[string]bool, baseline, next agentprotocol.ConfigurationSnapshot) {
	oldSLIs := make(map[string]agentprotocol.SLICollection, len(baseline.SLIs))
	oldAssignments := make(map[string]agentprotocol.DatasourceAssignment, len(baseline.Assignments))
	newAssignments := make(map[string]agentprotocol.DatasourceAssignment, len(next.Assignments))
	for _, value := range baseline.SLIs {
		oldSLIs[value.SLIID] = value
	}
	for _, value := range baseline.Assignments {
		oldAssignments[value.DatasourceID] = value
	}
	for _, value := range next.Assignments {
		newAssignments[value.DatasourceID] = value
	}
	for _, value := range next.SLIs {
		old, exists := oldSLIs[value.SLIID]
		if !exists || old != value || oldAssignments[value.DatasourceID] != newAssignments[value.DatasourceID] {
			delete(allowed, collectionKey(value))
		}
	}
}

func (c *client) prepareHeartbeat() error {
	nonce, err := newNonce()
	if err != nil {
		return err
	}
	progress, lastAttempt, lastSuccess, nextExpected := "not_started", (*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil)
	if c.collections != nil {
		progress, lastAttempt, lastSuccess, nextExpected = c.collections.Heartbeat()
	}
	heartbeat := agentprotocol.Heartbeat{RequestNonce: nonce, AppliedConfigurationRevision: c.revision,
		Scheduler: c.schedulerState(), CollectionProgress: progress, LastAttemptAt: lastAttempt,
		LastSuccessfulQueryAt: lastSuccess, NextExpectedExecutionAt: nextExpected, LeaseID: c.lease.LeaseID,
		RenewalSequence: fmt.Sprint(c.sequence)}
	c.pending = &heartbeat
	c.pendingSentAt = time.Time{}
	c.pendingServerAtSend = time.Time{}
	c.pendingLatestSentAt = time.Time{}
	return nil
}

func (c *client) writeHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	if c.pending == nil {
		return errors.New("heartbeat state is missing")
	}
	messageID, err := newMessageID()
	if err != nil {
		return err
	}
	message := agentprotocol.Envelope[agentprotocol.Heartbeat]{Kind: agentprotocol.KindHeartbeat, MessageID: messageID, SessionGeneration: c.generation, Payload: *c.pending}
	sentAt := time.Now()
	if c.pendingSentAt.IsZero() {
		c.pendingSentAt = sentAt
		c.pendingServerAtSend = c.leaseServerTimeAt(sentAt)
	}
	c.pendingLatestSentAt = sentAt
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return writeJSON(writeCtx, conn, message)
}

func (c *client) handleMessage(ctx context.Context, conn *websocket.Conn, raw jsontext.Value) error {
	var message struct {
		Kind agentprotocol.Kind `json:"kind"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return errors.New("backend message is invalid")
	}
	switch message.Kind {
	case agentprotocol.KindHeartbeatAck:
		var ack agentprotocol.Envelope[agentprotocol.HeartbeatAck]
		if json.Unmarshal(raw, &ack) != nil || ack.Validate() != nil || c.pending == nil || ack.Payload.RequestNonce != c.pending.RequestNonce {
			return errors.New("heartbeat acknowledgement is invalid")
		}
		sequence, err := parseDecimal(ack.Payload.RenewalSequence)
		pendingSequence, parseErr := parseDecimal(c.pending.RenewalSequence)
		lease := ack.Payload.ExecutionLease
		leaseRevision, leaseErr := parseDecimal(lease.ConfigurationRevision)
		currentLeaseRevision, currentLeaseErr := parseDecimal(c.lease.ConfigurationRevision)
		currentAppliedRevision, appliedErr := parseDecimal(c.revision)
		if err != nil || parseErr != nil || leaseErr != nil || currentLeaseErr != nil || appliedErr != nil || sequence != pendingSequence+1 || ack.SessionGeneration != c.generation || lease.AgentID != c.agentID || leaseRevision < currentLeaseRevision {
			return errors.New("heartbeat acknowledgement binding is invalid")
		}
		candidate := *c
		candidate.revision = lease.ConfigurationRevision
		if err := candidate.validateLease(lease, c.currentServerTime()); err != nil {
			return err
		}
		nowLocal := time.Now()
		deadline, deadlineErr := leaseDeadlineFromReplay(lease, c.pendingServerAtSend, c.pendingSentAt, c.pendingLatestSentAt, nowLocal)
		if deadlineErr != nil {
			return errors.New("renewed execution lease has no conservative remaining validity")
		}
		c.lease = ack.Payload.ExecutionLease
		if c.lease.ConfigurationRevision == c.revision {
			c.leaseBaseline = nil
		}
		c.leaseDeadline = deadline
		if c.collections != nil {
			c.collections.SetLeaseAuthority(c.lease, deadline, c.clockSafe)
		}
		c.sequence = sequence
		c.pending = nil
		c.pendingSentAt = time.Time{}
		c.pendingServerAtSend = time.Time{}
		c.pendingLatestSentAt = time.Time{}
		if c.configurationPending || leaseRevision > currentAppliedRevision || revisionAfter(c.desiredRevision, lease.ConfigurationRevision) {
			if c.collections != nil && c.protocol != agentprotocol.ProtocolV2 {
				c.collections.SetPaused(true)
			}
			if err := c.reconcile(ctx, conn); err != nil {
				return err
			}
		}
		if c.desiredRevision != "" && !revisionAfter(c.desiredRevision, c.revision) {
			c.desiredRevision = ""
		}
	case agentprotocol.KindConfigurationChanged:
		var changed agentprotocol.Envelope[agentprotocol.ConfigurationChanged]
		if json.Unmarshal(raw, &changed) != nil || changed.Validate() != nil || changed.SessionGeneration != c.generation {
			return errors.New("configuration notice is invalid")
		}
		if revisionAfter(changed.Payload.DesiredRevision, c.desiredRevision) {
			c.desiredRevision = changed.Payload.DesiredRevision
		}
		c.configurationPending = true
		if c.collections != nil && c.protocol != agentprotocol.ProtocolV2 {
			c.collections.SetPaused(true)
		}
	case agentprotocol.KindDiagnosticRequest:
		var request agentprotocol.Envelope[agentprotocol.DiagnosticRequest]
		if json.Unmarshal(raw, &request) != nil || request.Validate() != nil || request.SessionGeneration != c.generation || c.diagnostics == nil {
			return errors.New("backend diagnostic request is invalid")
		}
		if err := c.diagnostics.start(ctx, request.Payload); err != nil {
			if errors.Is(err, errDiagnosticCapacity) {
				completed := diagnosticCapacityResult(request.Payload)
				completed.generation = c.generation
				c.diagnostics.offer(completed)
				return nil
			}
			return errors.New("backend diagnostic request was rejected")
		}
	case agentprotocol.KindDiagnosticCancel:
		var cancel agentprotocol.Envelope[agentprotocol.DiagnosticCancel]
		if json.Unmarshal(raw, &cancel) != nil || cancel.Validate() != nil || cancel.SessionGeneration != c.generation || c.diagnostics == nil {
			return errors.New("backend diagnostic cancellation is invalid")
		}
		c.diagnostics.cancel(cancel.Payload.DiagnosticID)
	case agentprotocol.KindShutdown:
		var shutdown agentprotocol.Envelope[agentprotocol.Shutdown]
		if json.Unmarshal(raw, &shutdown) != nil || shutdown.Validate() != nil {
			return errors.New("shutdown notice is invalid")
		}
		return errReconnect
	case agentprotocol.KindRevoked:
		var revoked agentprotocol.Envelope[agentprotocol.Revoked]
		if json.Unmarshal(raw, &revoked) != nil || revoked.Validate() != nil {
			return errors.New("revocation notice is invalid")
		}
		return ErrRevoked
	default:
		return errors.New("backend sent unsupported message")
	}
	_ = conn
	return nil
}

var errReconnect = errors.New("backend requested reconnect")
var ErrOwnerConflict = errors.New("agent credential has an active owner")
var ErrRevoked = errors.New("agent credential revoked")
var ErrCredential = errors.New("agent credential rejected")
var ErrIncompatible = errors.New("backend protocol incompatible")
var ErrSessionRejected = errors.New("agent session was rejected; verify the ownership lease and restart only after it expires")

func (c *client) setServerTime(serverTime time.Time) {
	if c.serverClock == nil {
		c.serverClock = &serverClockEstimate{}
	}
	c.serverClock.mu.Lock()
	c.serverClock.serverTime, c.serverClock.leaseServerTime, c.serverClock.serverAt = serverTime, serverTime, time.Now()
	c.serverClock.mu.Unlock()
}

func (c *client) setServerTimeAt(serverTime, sentAt, receivedAt time.Time) {
	if c.serverClock == nil {
		c.serverClock = &serverClockEstimate{}
	}
	// The response timestamp is a lower bound when anchored at local receipt,
	// which keeps interval settling from starting early. A separate upper-bound
	// estimate is used only for lease expiry decisions.
	conservative := serverTime.Add(receivedAt.Sub(sentAt))
	c.serverClock.mu.Lock()
	c.serverClock.serverTime = serverTime
	c.serverClock.leaseServerTime = conservative
	c.serverClock.serverAt = receivedAt
	c.serverClock.mu.Unlock()
}

func (c *client) serverClockTime() time.Time {
	if c.serverClock == nil {
		return time.Time{}
	}
	c.serverClock.mu.RLock()
	defer c.serverClock.mu.RUnlock()
	return c.serverClock.serverTime
}

func (c *client) currentServerTime() time.Time {
	return c.serverTimeAt(time.Now())
}

func (c *client) serverTimeAt(localTime time.Time) time.Time {
	if c.serverClock == nil {
		return time.Time{}
	}
	c.serverClock.mu.RLock()
	defer c.serverClock.mu.RUnlock()
	return c.serverClock.serverTime.Add(localTime.Sub(c.serverClock.serverAt))
}

func (c *client) leaseServerTimeAt(localTime time.Time) time.Time {
	if c.serverClock == nil {
		return time.Time{}
	}
	c.serverClock.mu.RLock()
	defer c.serverClock.mu.RUnlock()
	serverTime := c.serverClock.leaseServerTime
	if serverTime.IsZero() {
		serverTime = c.serverClock.serverTime
	}
	return serverTime.Add(localTime.Sub(c.serverClock.serverAt))
}

func (c *client) schedulerState() string {
	if c.clockSafe {
		return "ready"
	}
	return "degraded"
}

func leaseDeadlineFrom(lease agentprotocol.Lease, serverAtSend, sentAt time.Time) (time.Time, error) {
	if sentAt.IsZero() || serverAtSend.IsZero() || !lease.ValidUntil.After(serverAtSend) {
		return time.Time{}, errors.New("lease bounds do not establish remaining validity")
	}
	return sentAt.Add(lease.ValidUntil.Sub(serverAtSend)), nil
}

func leaseDeadlineFromResponse(lease agentprotocol.Lease, serverAtSend, sentAt, receivedAt time.Time) (time.Time, error) {
	if sentAt.IsZero() || receivedAt.Before(sentAt) || receivedAt.Sub(sentAt) > 5*time.Second {
		return time.Time{}, errors.New("lease response exceeded the safe round-trip window")
	}
	deadline, err := leaseDeadlineFrom(lease, serverAtSend, sentAt)
	if err != nil || !deadline.After(receivedAt) {
		return time.Time{}, errors.New("lease response leaves no conservative validity")
	}
	return deadline, nil
}

// A replay's latest round trip proves response freshness; the original send
// and its then-current server-time upper bound constrain the lease deadline.
func leaseDeadlineFromReplay(lease agentprotocol.Lease, originalServer, originalSend, latestSend, received time.Time) (time.Time, error) {
	if latestSend.IsZero() || latestSend.Before(originalSend) || received.Before(latestSend) || received.Sub(latestSend) > 5*time.Second {
		return time.Time{}, errors.New("lease replay response exceeded the safe round-trip window")
	}
	deadline, err := leaseDeadlineFrom(lease, originalServer, originalSend)
	if err != nil || !deadline.After(received) {
		return time.Time{}, errors.New("lease replay leaves no conservative validity")
	}
	return deadline, nil
}

func serverClockSkewed(serverTime, localTime time.Time) bool {
	delta := serverTime.Sub(localTime)
	if delta < 0 {
		delta = -delta
	}
	return delta > 5*time.Second
}

func revisionAfter(revision, current string) bool {
	revisionValue, revisionErr := parseDecimal(revision)
	currentValue, currentErr := parseDecimal(current)
	return revisionErr == nil && currentErr == nil && revisionValue > currentValue
}

func (c *client) validateLease(lease agentprotocol.Lease, now time.Time) error {
	// The backend preserves the current grant interval across successful renewals;
	// if credential retirement would shorten it, renewal is denied. Update this
	// invariant together with storage semantics if a shorter renewal is introduced.
	if err := lease.Validate(); err != nil || lease.AgentID != c.agentID || lease.SessionGeneration != c.generation || lease.ConfigurationRevision != c.revision || lease.ValidFrom.After(now.Add(time.Minute)) || !lease.ValidUntil.After(now) ||
		(!c.lease.ValidUntil.IsZero() && lease.AgentID == c.lease.AgentID && lease.SessionGeneration == c.lease.SessionGeneration && lease.ValidUntil.Before(c.lease.ValidUntil)) {
		return errors.New("execution lease is invalid")
	}
	return nil
}

// validateWelcomeSequence accepts unchanged renewal state or the one pending
// heartbeat whose acknowledgement may have been lost. It rejects unrelated
// backend progress and local state that cannot be reconciled by nonce replay.
func validateWelcomeSequence(current int64, pending *agentprotocol.Heartbeat, welcome int64) error {
	if current < 0 || welcome < current {
		return errors.New("renewal sequence regressed")
	}
	if pending == nil {
		if welcome != current {
			return errors.New("renewal sequence advanced without a pending heartbeat")
		}
		return nil
	}
	pendingSequence, err := parseDecimal(pending.RenewalSequence)
	if err != nil || pendingSequence != current || current == math.MaxInt64 || (welcome != current && welcome != current+1) {
		return errors.New("welcome does not match pending heartbeat")
	}
	return nil
}

func closeAgentSession(conn *websocket.Conn) error {
	return conn.Close(websocket.StatusNormalClosure, "agent stopped")
}

func newNonce() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func newMessageID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func parseDecimal(value string) (int64, error) {
	var parsed int64
	if _, err := fmt.Sscan(value, &parsed); err != nil || fmt.Sprint(parsed) != value {
		return 0, errors.New("invalid decimal")
	}
	return parsed, nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch envelope := value.(type) {
	case agentprotocol.Envelope[agentprotocol.Hello]:
		if err := envelope.Validate(); err != nil {
			return err
		}
	case agentprotocol.Envelope[agentprotocol.ConfigurationApplied]:
		if err := envelope.Validate(); err != nil {
			return err
		}
	case agentprotocol.Envelope[agentprotocol.Heartbeat]:
		if err := envelope.Validate(); err != nil {
			return err
		}
	}
	if conn == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > agentprotocol.MaxMessageBytes {
		return errors.New("protocol message unavailable")
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func readJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		if closeError, ok := errors.AsType[websocket.CloseError](err); ok {
			if classified := closeReasonError(closeError.Reason); classified != nil {
				return classified
			}
		}
		return errors.New("backend message unavailable")
	}
	if messageType != websocket.MessageText {
		return errors.New("backend message unavailable")
	}
	if _, err := agentprotocol.Decode(data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func closeReasonError(reason string) error {
	switch reason {
	case "active_owner":
		return ErrOwnerConflict
	case "credential_revoked":
		return ErrRevoked
	case "incompatible_protocol":
		return ErrIncompatible
	case "session_rejected":
		return ErrSessionRejected
	default:
		return nil
	}
}

func readRaw(ctx context.Context, conn *websocket.Conn, value *jsontext.Value) error {
	messageType, data, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		return errors.New("backend message unavailable")
	}
	if _, err := agentprotocol.Decode(data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
