package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/agentclient"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

type agentsDoc struct {
	SchemaVersion int            `json:"schema_version"`
	Operation     string         `json:"operation"`
	Result        jsontext.Value `json:"result"`
	Token         string         `json:"token,omitempty"`
}
type agentsErrorDoc struct {
	SchemaVersion int        `json:"schema_version"`
	Operation     string     `json:"operation"`
	Error         setupError `json:"error"`
}

func agentsCommand(parent context.Context, args []string, stdout, stderr io.Writer) int {
	operation, rest := agentOperation(args)
	if operation == "" {
		return writeAgentFailure(stdout, stderr, "agents", requestedJSON(args), agentInvalid("choose a supported agents command"))
	}
	jsonOut := requestedJSON(rest)
	op := operation
	var profile, id, credentialID, name, display, ifMatch, passwordFile, key, tokenFile, cursor, output string
	var limit, lifetime, overlap int
	var timeout time.Duration
	var show, confirm bool
	fs := flag.NewFlagSet(op, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&profile, "profile-file", "", "protected profile")
	fs.StringVar(&id, "agent-id", "", "canonical agent UUID")
	fs.StringVar(&credentialID, "credential-id", "", "canonical credential UUID")
	fs.StringVar(&name, "name", "", "immutable agent name")
	fs.StringVar(&display, "display-name", "", "agent display name")
	fs.StringVar(&ifMatch, "if-match", "", "exact strong agent ETag")
	fs.StringVar(&passwordFile, "current-password-file", "", "protected current administrator password")
	fs.StringVar(&key, "idempotency-key", "", "caller-owned operation key")
	fs.StringVar(&tokenFile, "token-file", "", "new protected token file")
	fs.BoolVar(&show, "show-token", false, "display a fresh token once in result output")
	fs.StringVar(&cursor, "cursor", "", "opaque page cursor")
	fs.IntVar(&limit, "limit", 50, "page size from 1 to 200")
	fs.IntVar(&lifetime, "lifetime-seconds", 0, "optional credential lifetime")
	fs.IntVar(&overlap, "overlap-seconds", 0, "optional rotation overlap")
	fs.BoolVar(&confirm, "confirm", false, "confirm this operation")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "whole operation deadline")
	fs.StringVar(&output, "output", "text", "text or json")
	if fs.Parse(rest) != nil || fs.NArg() != 0 {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("invalid agent command flags"))
	}
	jsonOut = jsonOut || output == "json"
	if output != "text" && output != "json" {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("output must be text or json"))
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	allowed := map[string]bool{"profile-file": true, "timeout": true, "output": true}
	switch operation {
	case "agents list":
		allowed["limit"], allowed["cursor"] = true, true
	case "agents get":
		allowed["agent-id"] = true
	case "agents credentials list":
		allowed["agent-id"], allowed["limit"], allowed["cursor"] = true, true, true
	case "agents create":
		allowed["name"], allowed["display-name"], allowed["idempotency-key"], allowed["lifetime-seconds"], allowed["token-file"], allowed["show-token"] = true, true, true, true, true, true
	case "agents credentials rotate":
		allowed["agent-id"], allowed["if-match"], allowed["current-password-file"], allowed["idempotency-key"], allowed["confirm"], allowed["lifetime-seconds"], allowed["overlap-seconds"], allowed["token-file"], allowed["show-token"] = true, true, true, true, true, true, true, true, true
	case "agents credentials revoke":
		allowed["agent-id"], allowed["credential-id"], allowed["if-match"], allowed["current-password-file"], allowed["idempotency-key"], allowed["confirm"] = true, true, true, true, true, true
	case "agents disable":
		allowed["agent-id"], allowed["if-match"], allowed["current-password-file"], allowed["idempotency-key"], allowed["confirm"] = true, true, true, true, true
	}
	for flag := range seen {
		if !allowed[flag] {
			return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("a flag is not valid for this agent operation"))
		}
	}
	if !seen["profile-file"] || profile == "" || timeout <= 0 || timeout > 2*time.Minute {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("profile-file is required and timeout must be positive and at most 2m"))
	}
	if (operation == "agents list" || operation == "agents credentials list") && (limit < 1 || limit > 200 || len(cursor) > 512 || !printable(cursor)) {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("pagination options are invalid"))
	}
	if strings.Contains(operation, "get") || strings.Contains(operation, "credentials list") || strings.Contains(operation, "rotate") || strings.Contains(operation, "revoke") || operation == "agents disable" {
		if !seen["agent-id"] || !validCLIUUID(id) {
			return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("agent-id must be a canonical UUID"))
		}
	}
	if operation == "agents credentials revoke" && (!seen["credential-id"] || !validCLIUUID(credentialID)) {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("credential-id must be a canonical UUID"))
	}
	isIssue := operation == "agents create" || operation == "agents credentials rotate"
	if isIssue && ((tokenFile != "") == show) {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("select exactly one of token-file or show-token"))
	}
	if operation == "agents credentials rotate" || operation == "agents credentials revoke" || operation == "agents disable" {
		if !confirm {
			return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("--confirm is required"))
		}
	}
	if operation == "agents create" {
		if name == "" || display == "" || !seen["name"] || !seen["display-name"] {
			return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("name and display-name are required"))
		}
	}
	mutating := operation == "agents create" || operation == "agents credentials rotate" || operation == "agents credentials revoke" || operation == "agents disable"
	if mutating && (!seen["idempotency-key"] || !validKey(key)) {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("a valid idempotency-key is required"))
	}
	if operation != "agents create" && mutating && (!seen["current-password-file"] || passwordFile == "") {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("current-password-file and a valid idempotency-key are required"))
	}
	if (operation == "agents credentials rotate" || operation == "agents credentials revoke" || operation == "agents disable") && (!seen["if-match"] || !validETag(ifMatch, id)) {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("if-match must be the exact strong ETag for agent-id"))
	}
	if seen["lifetime-seconds"] && (lifetime < 0 || lifetime > 31_536_000) || seen["overlap-seconds"] && (overlap < 0 || overlap > 600) {
		return writeAgentFailure(stdout, stderr, op, jsonOut, agentInvalid("credential timing values are invalid"))
	}
	signalCtx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, timeout)
	defer cancel()
	result, token, replayed, err := performAgent(ctx, operation, profile, id, credentialID, name, display, ifMatch, passwordFile, key, tokenFile, show, limit, cursor, lifetime, seen["lifetime-seconds"], overlap, seen["overlap-seconds"])
	if err != nil {
		return writeAgentFailure(stdout, stderr, op, jsonOut, err)
	}
	doc := agentsDoc{SchemaVersion: 1, Operation: op, Result: result}
	if show && !replayed {
		doc.Token = token
	}
	return writeAgentSuccess(stdout, stderr, jsonOut, doc, op, result, token, show && !replayed)
}

func writeAgentSuccess(stdout, stderr io.Writer, jsonOut bool, doc agentsDoc, operation string, result jsontext.Value, token string, showToken bool) int {
	if jsonOut {
		if json.MarshalWrite(stdout, doc) != nil || writeNewline(stdout) != nil {
			return writeAgentOutputFailure(stderr)
		}
		return 0
	}
	if _, err := io.WriteString(stdout, renderAgentText(operation, result, token, showToken)); err != nil {
		return writeAgentOutputFailure(stderr)
	}
	return 0
}

func agentOperation(a []string) (string, []string) {
	if len(a) < 1 {
		return "", a
	}
	switch a[0] {
	case "list", "get", "create", "disable":
		return "agents " + a[0], a[1:]
	case "credentials":
		if len(a) < 2 {
			return "", a[1:]
		}
		if a[1] == "list" || a[1] == "rotate" || a[1] == "revoke" {
			return "agents credentials " + a[1], a[2:]
		}
	}
	return "", a[1:]
}

func optionalSeconds(value int, provided bool) *int32 {
	if !provided {
		return nil
	}
	n := int32(value)
	return &n
}

func renderAgentText(operation string, result jsontext.Value, token string, showToken bool) string {
	var out strings.Builder
	switch operation {
	case "agents list":
		var page api.AgentPage
		if json.Unmarshal(result, &page) != nil {
			return "Agent list completed.\n"
		}
		fmt.Fprintf(&out, "Agents (%d)\n", len(page.Items))
		for _, a := range page.Items {
			fmt.Fprintf(&out, "%s\t%s\t%s\t%t\t%s\n", cliAgentID(a.Id), strconv.Quote(a.Name), strconv.Quote(a.DisplayName), a.Enabled, a.Version)
		}
		if page.NextCursor != "" {
			fmt.Fprintf(&out, "Next cursor: %s\n", strconv.Quote(page.NextCursor))
		}
	case "agents get":
		var detail struct {
			Agent api.Agent `json:"agent"`
			ETag  string    `json:"etag"`
		}
		if json.Unmarshal(result, &detail) != nil {
			return "Agent details retrieved.\n"
		}
		fmt.Fprintf(&out, "Agent %s (%s)\nName: %s\nEnabled: %t\nVersion: %s\nETag: %s\n", strconv.Quote(detail.Agent.DisplayName), cliAgentID(detail.Agent.Id), strconv.Quote(detail.Agent.Name), detail.Agent.Enabled, detail.Agent.Version, detail.ETag)
		renderAgentRuntime(&out, detail.Agent)
	case "agents credentials list":
		var page api.CredentialPage
		if json.Unmarshal(result, &page) != nil {
			return "Credential history retrieved.\n"
		}
		fmt.Fprintf(&out, "Credentials (%d)\n", len(page.Items))
		for _, c := range page.Items {
			fmt.Fprintf(&out, "%s\tissued %s\texpires %s\trevoked %s\tretire %s\n", cliAgentID(c.Id), c.IssuedAt.Format(time.RFC3339Nano), c.ExpiresAt.Format(time.RFC3339Nano), cliOptionalTime(c.RevokedAt), cliOptionalTime(c.RetireAt))
		}
		if page.NextCursor != "" {
			fmt.Fprintf(&out, "Next cursor: %s\n", strconv.Quote(page.NextCursor))
		}
	case "agents create", "agents credentials rotate":
		var issue struct {
			Agent      api.Agent           `json:"agent"`
			Credential api.AgentCredential `json:"credential"`
			Replayed   bool                `json:"replayed"`
		}
		if json.Unmarshal(result, &issue) != nil {
			return "Agent credential operation completed.\n"
		}
		fmt.Fprintf(&out, "Agent: %s (%s)\nCredential: %s\nReplayed: %t\n", strconv.Quote(issue.Agent.DisplayName), cliAgentID(issue.Agent.Id), cliAgentID(issue.Credential.Id), issue.Replayed)
		if showToken {
			fmt.Fprintf(&out, "Token: %s\n", strconv.Quote(token))
		}
	case "agents credentials revoke", "agents disable":
		var lifecycle api.LifecycleResult
		if json.Unmarshal(result, &lifecycle) != nil {
			return "Agent lifecycle operation completed.\n"
		}
		fmt.Fprintf(&out, "Agent: %s (%s)\nEnabled: %t\nVersion: %s\nChanged: %t\n", strconv.Quote(lifecycle.Agent.DisplayName), cliAgentID(lifecycle.Agent.Id), lifecycle.Agent.Enabled, lifecycle.Agent.Version, lifecycle.Changed)
	}
	return out.String()
}

func renderAgentRuntime(out *strings.Builder, agent api.Agent) {
	runtime := agent.Runtime
	if !agent.Enabled {
		fmt.Fprintln(out, "Connection state: Disabled by administrator")
	} else if runtime == nil {
		fmt.Fprintln(out, "Connection state: Never connected")
	} else {
		fmt.Fprintf(out, "Connection state: %s\n", runtimeReachabilityLabel(runtime.Reachability))
	}
	if runtime == nil {
		fmt.Fprintln(out, "No heartbeat has been received.")
		fmt.Fprintln(out, "Observation acceptance freshness: Not available.")
		return
	}
	fmt.Fprintf(out, "Reported reachability: %s", runtime.Reachability)
	if !agent.Enabled {
		fmt.Fprint(out, " (not operational while disabled)")
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Protocol: %s\nSoftware version: %s\n", strconv.Quote(runtime.Protocol), strconv.Quote(runtime.SoftwareVersion))
	fmt.Fprintf(out, "Last heartbeat: %s\n", cliRuntimeTime(runtime.LastHeartbeatAt))
	fmt.Fprintf(out, "Desired configuration revision: %s\n", runtime.DesiredConfigurationRevision)
	appliedRevision := runtime.AppliedConfigurationRevision
	if appliedRevision == "0" {
		appliedRevision = "Not applied"
	}
	fmt.Fprintf(out, "Applied configuration revision: %s\n", appliedRevision)
	fmt.Fprintf(out, "Scheduler health: %s\nCollection progress: %s\n", runtime.SchedulerHealth, runtime.CollectionProgress)
	fmt.Fprintf(out, "Last attempt: %s\nLast successful query: %s\nNext expected execution: %s\n",
		cliRuntimeTime(runtime.LastAttemptAt), cliRuntimeTime(runtime.LastSuccessfulQueryAt), cliRuntimeTime(runtime.NextExpectedExecutionAt))
	if runtime.CollectionExplanation == api.NoAssignment {
		fmt.Fprintln(out, "No datasources are assigned to this agent.")
	}
	if len(runtime.Capabilities) == 0 {
		fmt.Fprintln(out, "No provider capabilities were reported.")
	} else {
		fmt.Fprintln(out, "Provider capabilities:")
		for _, capability := range runtime.Capabilities {
			fmt.Fprintf(out, "  Provider: %s\n    Configuration formats: %s\n    Operations: %s\n",
				strconv.Quote(capability.Provider), quotedRuntimeList(capability.ConfigurationFormats), quotedRuntimeList(capability.Operations))
		}
	}
	fmt.Fprintln(out, "Observation acceptance freshness: Not available.")
}

func runtimeReachabilityLabel(reachability api.AgentRuntimeStatusReachability) string {
	switch reachability {
	case api.Connected:
		return "Connected"
	case api.Unreachable:
		return "Unreachable"
	case api.Incompatible:
		return "Incompatible"
	default:
		return "Unknown"
	}
}

func cliRuntimeTime(value *time.Time) string {
	if value == nil {
		return "Not reported"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func quotedRuntimeList(values []string) string {
	if len(values) == 0 {
		return "None reported"
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(value)
	}
	return strings.Join(quoted, ", ")
}
func cliAgentID(id api.AgentId) string {
	b, _ := json.Marshal(id)
	var s string
	_ = json.Unmarshal(b, &s)
	return s
}
func cliOptionalTime(t *time.Time) string {
	if t == nil {
		return "none"
	}
	return t.Format(time.RFC3339Nano)
}
func performAgent(ctx context.Context, op, profile, id, credentialID, name, display, etag, passwordFile, key, tokenFile string, show bool, limit int, cursor string, lifetime int, lifetimeSet bool, overlap int, overlapSet bool) (jsontext.Value, string, bool, error) {
	var password []byte
	mutating := op == "agents create" || op == "agents credentials rotate" || op == "agents credentials revoke" || op == "agents disable"
	if mutating && op != "agents create" {
		var e error
		password, e = secretfile.Read(passwordFile, 512)
		if e != nil || len(password) == 0 || !utf8.Valid(password) {
			return nil, "", false, agentInvalid("current-password file is invalid")
		}
	}
	locked, e := cliprofile.Open(ctx, profile, false)
	if e != nil {
		return nil, "", false, agentProfileFailure(e, false)
	}
	defer locked.Close()
	record, exists, e := locked.Load()
	if e != nil {
		return nil, "", false, agentProfileFailure(e, false)
	}
	if !exists {
		return nil, "", false, agentProfileFailure(cliprofile.ErrUnavailable, false)
	}
	session, e := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if e != nil {
		return nil, "", false, agentSetupFailure(e)
	}
	defer session.Close()
	client, e := agentclient.New(ctx, session)
	if session.Session() != record.Session {
		record.Session = session.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return nil, "", false, agentProfileFailure(saveErr, false)
		}
	}
	if e != nil {
		return nil, "", false, agentClientFailure(e)
	}
	var tokenSink *secretfile.TokenFile
	tokenSinkClosed := false
	if mutating && (op == "agents create" || op == "agents credentials rotate") && !show {
		tokenSink, e = secretfile.Reserve(ctx, tokenFile)
		if e != nil {
			return nil, "", false, agentInvalid("token-file must be a new private file in a protected directory")
		}
		defer func() {
			if !tokenSinkClosed {
				_ = tokenSink.Close()
			}
		}()
	}
	var data []byte
	var token string
	var replay bool
	switch op {
	case "agents list":
		data, e = client.List(ctx, limit, cursor)
	case "agents get":
		var idv api.AgentId
		_ = json.Unmarshal([]byte(strconv.Quote(id)), &idv)
		var tag string
		data, tag, e = client.Get(ctx, idv)
		if e == nil {
			data, _ = json.Marshal(struct {
				Agent jsontext.Value `json:"agent"`
				ETag  string         `json:"etag"`
			}{jsontext.Value(data), tag})
		}
	case "agents credentials list":
		var idv api.AgentId
		_ = json.Unmarshal([]byte(strconv.Quote(id)), &idv)
		data, e = client.Credentials(ctx, idv, limit, cursor)
	case "agents create":
		life := optionalSeconds(lifetime, lifetimeSet)
		var b []byte
		b, token, replay, e = client.Create(ctx, api.CreateAgentRequest{Name: name, DisplayName: display, LifetimeSeconds: life}, key)
		data = b
	case "agents credentials rotate":
		var idv api.AgentId
		_ = json.Unmarshal([]byte(strconv.Quote(id)), &idv)
		life := optionalSeconds(lifetime, lifetimeSet)
		over := optionalSeconds(overlap, overlapSet)
		var b []byte
		b, token, replay, e = client.Rotate(ctx, idv, api.RotateAgentRequest{CurrentPassword: string(password), LifetimeSeconds: life, OverlapSeconds: over}, etag, key)
		data = b
	case "agents credentials revoke":
		var idv, cid api.AgentId
		_ = json.Unmarshal([]byte(strconv.Quote(id)), &idv)
		_ = json.Unmarshal([]byte(strconv.Quote(credentialID)), &cid)
		data, e = client.Revoke(ctx, idv, cid, api.PasswordProofRequest{CurrentPassword: string(password)}, etag, key)
	case "agents disable":
		var idv api.AgentId
		_ = json.Unmarshal([]byte(strconv.Quote(id)), &idv)
		data, e = client.Disable(ctx, idv, api.PasswordProofRequest{CurrentPassword: string(password)}, etag, key)
	}
	if record.Session != session.Session() {
		record.Session = session.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return nil, "", false, agentProfileFailure(saveErr, mutationMayHaveCommitted(e))
		}
	}
	if e != nil {
		if tokenSink != nil {
			if closeErr := tokenSink.Close(); closeErr != nil {
				tokenSinkClosed = true
				return nil, "", false, &agentclient.Failure{Kind: setupclient.KindRemote, Code: "token_output_cleanup_failed", Message: "reserved token output could not be cleaned up; inspect the destination before continuing", Unknown: mutationMayHaveCommitted(e)}
			}
			tokenSinkClosed = true
		}
		return nil, "", false, agentClientFailure(e)
	}
	if token != "" && !show {
		if tokenSink == nil {
			return nil, "", false, &agentclient.Failure{Kind: setupclient.KindRemote, Code: "token_output_failed", Message: "agent credential was issued but protected token output failed; the one-time token cannot be recovered", Unknown: true}
		}
		if e = tokenSink.Complete([]byte(token)); e != nil {
			return nil, "", false, agentTokenCompleteFailure(e)
		}
		if e = tokenSink.Close(); e != nil {
			tokenSinkClosed = true
			return nil, "", false, &agentclient.Failure{Kind: setupclient.KindRemote, Code: "token_output_failed", Message: "agent credential was issued but protected token output could not be confirmed; inspect the token file and agent state", Unknown: true}
		}
		tokenSinkClosed = true
	}
	if tokenSink != nil && !tokenSinkClosed {
		if e = tokenSink.Close(); e != nil {
			tokenSinkClosed = true
			return nil, "", false, &agentclient.Failure{Kind: setupclient.KindRemote, Code: "token_output_cleanup_failed", Message: "reserved token output cleanup could not be confirmed; inspect the destination before continuing", Unknown: mutating}
		}
		tokenSinkClosed = true
	}
	if e = locked.Close(); e != nil {
		return nil, "", false, agentProfileFailure(e, mutating)
	}
	return jsontext.Value(data), token, replay, nil
}

func agentTokenCompleteFailure(err error) *agentclient.Failure {
	failure := &agentclient.Failure{
		Kind:    setupclient.KindRemote,
		Code:    "token_output_failed",
		Message: "agent credential was issued but protected token output failed; the one-time token cannot be recovered",
		Unknown: true,
	}
	if errors.Is(err, secretfile.ErrTokenCancelled) {
		failure.Kind = setupclient.KindCancelled
		failure.Code = "cancelled"
		failure.Message = "agent credential was issued but token output was cancelled; the one-time token cannot be recovered; inspect agent state before continuing"
	} else if errors.Is(err, secretfile.ErrTokenCommitUncertain) {
		failure.Message = "agent credential was issued; token file completion could not be confirmed; inspect the destination and agent state before continuing"
	}
	return failure
}

func validCLIUUID(s string) bool { return cliUUID.MatchString(s) }

var cliUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validKey(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
func validETag(s, id string) bool {
	if !strings.HasPrefix(s, "\"agent:"+id+":") || !strings.HasSuffix(s, "\"") {
		return false
	}
	n := strings.TrimSuffix(strings.TrimPrefix(s, "\"agent:"+id+":"), "\"")
	if n == "" || n[0] == '0' {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	v, e := strconv.ParseInt(n, 10, 64)
	return e == nil && v > 0
}
func printable(s string) bool {
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
func agentInvalid(m string) error {
	return &agentclient.Failure{Kind: setupclient.KindInvalid, Code: "invalid_request", Message: m}
}
func agentSetupFailure(e error) error {
	var f *setupclient.Failure
	if errors.As(e, &f) {
		return f
	}
	return &agentclient.Failure{Kind: setupclient.KindRemote, Code: "client_initialization_failed", Message: "agent client initialization failed"}
}
func agentClientFailure(e error) error {
	var f *agentclient.Failure
	if errors.As(e, &f) {
		return f
	}
	return agentSetupFailure(e)
}
func mutationMayHaveCommitted(err error) bool {
	if err == nil {
		return true
	}
	if f, ok := errors.AsType[*agentclient.Failure](err); ok {
		return f.Unknown
	}
	if f, ok := errors.AsType[*setupclient.Failure](err); ok {
		return f.OutcomeUnknown
	}
	return false
}
func agentProfileFailure(e error, possibleMutation bool) error {
	f := &agentclient.Failure{Kind: setupclient.KindRemote, Code: "profile_unavailable", Message: "protected profile could not be read or saved"}
	if errors.Is(e, cliprofile.ErrInvalid) {
		f.Kind = setupclient.KindInvalid
		f.Code = "invalid_profile"
		f.Message = "profile file or its directory is invalid"
	}
	if errors.Is(e, cliprofile.ErrCommitUncertain) {
		f.Unknown = true
		f.Message = "profile persistence is uncertain; inspect the profile before continuing"
	}
	if errors.Is(e, cliprofile.ErrUnsupported) {
		f.Kind = setupclient.KindInvalid
		f.Code = "profile_unsupported"
		f.Message = "protected profiles are unsupported on this platform"
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		f.Kind = setupclient.KindCancelled
		f.Code = "cancelled"
		f.Message = "agent operation was cancelled or timed out"
	}
	if possibleMutation {
		f.Unknown = true
		f.Message = "profile persistence or lock release could not be confirmed; the agent operation may have completed; inspect agent state before deciding what to do next"
	}
	return f
}
func writeAgentFailure(stdout, stderr io.Writer, op string, jsonOut bool, e error) int {
	var a *agentclient.Failure
	if !errors.As(e, &a) {
		var s *setupclient.Failure
		if errors.As(e, &s) {
			a = &agentclient.Failure{Kind: s.Kind, Code: s.Code, Message: s.Message, RequestID: s.RequestID, RetryAfter: s.RetryAfterSeconds, Unknown: s.OutcomeUnknown}
		} else {
			a = &agentclient.Failure{Kind: setupclient.KindRemote, Code: "agent_operation_failed", Message: "agent operation failed"}
		}
	}
	exit := 1
	switch a.Kind {
	case setupclient.KindInvalid:
		exit = 2
	case setupclient.KindAuthentication:
		exit = 3
	case setupclient.KindNotFound:
		exit = 4
	case setupclient.KindConflict:
		exit = 5
	case setupclient.KindCancelled:
		exit = 6
	}
	if jsonOut {
		doc := agentsErrorDoc{SchemaVersion: 1, Operation: op, Error: setupError{Code: a.Code, Message: a.Message, OutcomeUnknown: a.Unknown, SessionState: "unchecked", RequestID: a.RequestID, RetryAfterSeconds: a.RetryAfter}}
		if json.MarshalWrite(stdout, doc) != nil || writeNewline(stdout) != nil {
			return writeAgentOutputFailure(stderr)
		}
	} else {
		if _, err := fmt.Fprintln(stderr, op+":", a.Message); err != nil {
			return 1
		}
	}
	return exit
}
func writeAgentOutputFailure(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "agents: output failed")
	return 1
}
func writeNewline(w io.Writer) error { _, e := fmt.Fprintln(w); return e }
