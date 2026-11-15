//go:build integration

package alerthttp

import (
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5/pgtype"
)

func occurrenceJSON(t *testing.T, value any) []byte {
	t.Helper()
	durations := json.MarshalFunc(func(value time.Duration) ([]byte, error) {
		return strconv.AppendInt(nil, int64(value/time.Second), 10), nil
	})
	raw, err := json.Marshal(value, json.Deterministic(true), json.WithMarshalers(durations))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func occurrenceUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatal(err)
	}
	return id
}
func occurrenceTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

// The processor is intentionally package-private and unwired. This fixture runs
// the real deterministic Evaluate/Reduce core and persists its saved journal
// inputs. It tests HTTP/storage authority and evidence, not canonical ingestion
// or worker scheduling. No production hook or immutable-history bypass is used.
func seedHTTPEvaluation(t *testing.T, f alertHTTPFixture, policyID string, cutoff time.Time, quality alert.Quality, good, total string, current *alert.Occurrence, version int64) (*alert.Occurrence, string) {
	t.Helper()
	conn := testpostgres.Connect(t, f.databaseURL)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	q := dbgen.New(tx)
	id := occurrenceUUID(t, policyID)
	meaning, err := q.GetSlo(t.Context(), occurrenceUUID(t, f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := q.ReadAlertBinding(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	identity := alert.SnapshotIdentity{SLOID: f.sloID, SLORevision: meaning.DefinitionRevision, SLIRevision: meaning.MeasurementRevision, CalculationVersion: binding.CalculationVersion, Cutoff: cutoff}
	input := alert.Input{Policy: alert.PolicyRevision{ID: policyID, Revision: 1, Conditions: []alert.Condition{{ID: "fast", Short: time.Minute, Long: 2 * time.Minute, Threshold: "1"}}}, Snapshot: identity, Objective: alert.Objective{Target: meaning.Target, Cadence: time.Duration(meaning.CadenceSeconds) * time.Second, DefinitionValidFrom: meaning.EvaluationValidFrom.Time, MeasurementValidFrom: meaning.SliValidFrom.Time, RetentionStart: f.now.Add(-24 * time.Hour)}}
	for _, duration := range []time.Duration{time.Minute, 2 * time.Minute} {
		input.Windows = append(input.Windows, alert.WindowEvidence{Snapshot: identity, Start: cutoff.Add(-duration), End: cutoff, Quality: quality, Good: good, Total: total})
	}
	evaluation, err := alert.Evaluate(input, alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96})
	if err != nil {
		t.Fatal(err)
	}
	detected := cutoff.Add(time.Second)
	transition, err := alert.Reduce(current, evaluation, detected)
	if err != nil || transition.Next == nil {
		t.Fatalf("fixture lifecycle: %v", err)
	}
	next := transition.Next
	if current == nil {
		next.ID = uuid.New().String()
	}
	evaluationID := occurrenceUUID(t, uuid.New().String())
	occurrenceID := occurrenceUUID(t, next.ID)
	digest := sha256.Sum256(occurrenceJSON(t, input))
	evidence := occurrenceJSON(t, struct {
		FormatVersion int
		Input         alert.Input
		Evaluation    alert.Evaluation
	}{1, input, evaluation})
	result := occurrenceJSON(t, struct {
		FormatVersion int
		EvaluationID  string
		Occurrence    *alert.Occurrence
		Version       int64
	}{1, evaluationID.String(), next, version + 1})
	if err := q.InsertAlertEvaluation(t.Context(), dbgen.InsertAlertEvaluationParams{ID: evaluationID, PolicyID: id, Revision: 1, ScheduledAt: occurrenceTime(cutoff), EffectiveCutoff: occurrenceTime(cutoff), Truth: string(evaluation.Truth), ClaimEpoch: 1, ClaimToken: occurrenceUUID(t, uuid.New().String()), InputDigest: digest[:], Evidence: evidence, Result: result, CommittedAt: occurrenceTime(detected)}); err != nil {
		t.Fatal(err)
	}
	payload := occurrenceJSON(t, next)
	if current == nil {
		metadata := occurrenceJSON(t, storage.AlertOriginalPolicy{Name: "HTTP occurrence", Severity: "high", SLOName: meaning.DisplayName, ServiceName: meaning.ServiceDisplayName, Target: meaning.Target, RollingWindowSeconds: meaning.RollingWindowSeconds})
		if err := q.InsertAlertOccurrence(t.Context(), dbgen.InsertAlertOccurrenceParams{ID: occurrenceID, PolicyID: id, Revision: 1, State: string(next.State), Version: 1, TransitionSequence: 1, FiringEvaluationID: evaluationID, LatestEvaluationID: evaluationID, Payload: payload, PolicySnapshot: metadata, OpenedAt: occurrenceTime(cutoff), UpdatedAt: occurrenceTime(detected)}); err != nil {
			t.Fatal(err)
		}
		if err := q.InsertAlertTransition(t.Context(), dbgen.InsertAlertTransitionParams{ID: occurrenceUUID(t, uuid.New().String()), PolicyID: id, Revision: 1, OccurrenceID: occurrenceID, Sequence: 1, ToState: string(next.State), Cause: string(transition.Cause), EffectiveAt: occurrenceTime(cutoff), DetectedAt: occurrenceTime(detected), EvaluationID: evaluationID}); err != nil {
			t.Fatal(err)
		}
		if n, err := q.UpsertAlertBreachPin(t.Context(), dbgen.UpsertAlertBreachPinParams{OccurrenceID: occurrenceID, PolicyID: id, Revision: 1, EvaluationID: evaluationID}); err != nil || n != 1 {
			t.Fatalf("breach pin: %d %v", n, err)
		}
	} else {
		if n, err := q.UpdateAlertOccurrence(t.Context(), dbgen.UpdateAlertOccurrenceParams{ID: occurrenceID, State: string(next.State), TransitionSequence: 1, LatestEvaluationID: evaluationID, Payload: payload, UpdatedAt: occurrenceTime(detected), Version: version}); err != nil || n != 1 {
			t.Fatalf("advance fixture evidence: %d %v", n, err)
		}
	}
	if _, err := tx.Exec(t.Context(), `UPDATE almanac_alert_cursors SET last_cutoff=$2,last_evaluation_id=$3,open_occurrence_id=$4,next_scheduled_at=$5,available_at=$5 WHERE policy_id=$1 AND revision=1`, id, cutoff, evaluationID, occurrenceID, cutoff.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return next, evaluationID.String()
}

func TestIntegrationAlertHTTPOccurrenceJourney(t *testing.T) {
	t.Parallel()
	f := newAlertHTTPFixture(t)
	body, _ := f.call(t, http.MethodPost, "/api/v1/alert-policies", fmt.Sprintf(`{"name":"HTTP occurrence","slo_id":%q,"severity":"high","conditions":[{"id":"fast","short_window_seconds":60,"long_window_seconds":120,"threshold":"1"}]}`, f.sloID), "occurrence-policy", "", 201)
	policy := decodeAlertHTTPCommand(t, body).Policy
	// Move only this server's explicit clock a few minutes forward, allowing full
	// windows after the real SLI/SLO activation while the real session stays valid.
	cutoff := f.now.Truncate(time.Minute).Add(3 * time.Minute)
	now := cutoff.Add(3 * time.Minute)
	clock := func() time.Time { return now }
	authService, err := authentication.New(t.Context(), f.databaseURL, clock)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authhttp.New(authService, authhttp.Options{PublicOrigin: "https://almanac.example", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := storage.OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	handler, err := New(f.databaseURL, auth, Options{Runtime: runtime, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/context", auth)
	mux.Handle("/api/v1/", handler)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	f.baseURL, f.client = server.URL, server.Client()
	f.client.Timeout = 10 * time.Second
	t.Cleanup(f.client.CloseIdleConnections)
	body, _ = f.call(t, http.MethodPost, "/api/v1/auth/context", "{}", "", "", 200)
	var authContext api.AuthResponse
	if err := json.Unmarshal(body, &authContext); err != nil || authContext.CsrfToken == "" {
		t.Fatal("new server authenticated CSRF context unavailable")
	}
	f.csrf = authContext.CsrfToken
	original, firingID := seedHTTPEvaluation(t, f, policy.Id.String(), cutoff, alert.Complete, "9856", "10000", nil, 0)
	path := "/api/v1/alerts/" + original.ID
	body, header := f.call(t, http.MethodGet, path, "", "", "", 200)
	var record api.AlertRecord
	if err := json.Unmarshal(body, &record); err != nil || record.State != "firing" || record.Version != "1" || header.Get("ETag") != fmt.Sprintf(`"alert-occurrence:%s:1"`, original.ID) {
		t.Fatalf("initial occurrence/etag: %v", err)
	}
	readEvidence := func(selection string) api.AlertEvidence {
		t.Helper()
		data, _ := f.call(t, http.MethodGet, path+"/evidence?selection="+selection, "", "", "", 200)
		var value api.AlertEvidence
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	firing := readEvidence("firing")
	checkRate := func(value *string, want string) {
		t.Helper()
		if value == nil || *value != want {
			t.Fatalf("saved exact rate: got=%v want=%s", value, want)
		}
	}
	if firing.Id.String() != firingID || firing.Evaluation.Truth != "breaching" || firing.Evaluation.Conditions[0].Outcome != "matching" {
		t.Fatal("original evidence identity or outcome lost")
	}
	first := firing.Evaluation.Conditions[0].Short
	checkRate(first.Good, "9856")
	checkRate(first.Total, "10000")
	checkRate(first.Bad, "144")
	checkRate(first.ObservedErrorRate, "0.0144")
	checkRate(first.AllowedErrorRate, "0.001")
	checkRate(first.Burn, "14.4")
	missing, missingID := seedHTTPEvaluation(t, f, policy.Id.String(), cutoff.Add(time.Minute), alert.Missing, "", "", original, 1)
	latest := readEvidence("latest")
	missingWindow := latest.Evaluation.Conditions[0].Short
	if latest.Id.String() != missingID || latest.Evaluation.Truth != "indeterminate" || missingWindow.Quality != "missing" || missingWindow.Good != nil || missingWindow.Total != nil || missingWindow.Bad != nil || missingWindow.Burn != nil || missingWindow.ObservedErrorRate != nil {
		t.Fatal("missing evidence fabricated numeric success")
	}
	_, zeroID := seedHTTPEvaluation(t, f, policy.Id.String(), cutoff.Add(2*time.Minute), alert.ZeroTraffic, "0", "0", missing, 2)
	latest = readEvidence("latest")
	zeroWindow := latest.Evaluation.Conditions[0].Short
	if latest.Id.String() != zeroID || latest.Evaluation.Truth != "indeterminate" || zeroWindow.Quality != "zero_traffic" || zeroWindow.Burn != nil || zeroWindow.ObservedErrorRate != nil {
		t.Fatal("zero traffic falsely supplied a burn rate")
	}
	checkRate(zeroWindow.Good, "0")
	checkRate(zeroWindow.Total, "0")
	checkRate(zeroWindow.Bad, "0")
	body, _ = f.call(t, http.MethodGet, path+"/series?condition_id=fast&limit=120", "", "", "", 200)
	var series api.AlertSeries
	if err := json.Unmarshal(body, &series); err != nil || len(series.Points) != 3 || series.Sampling.Total != 3 || series.Sampling.Reduced || series.Condition.Id != "fast" || series.PolicyId != policy.Id {
		t.Fatalf("immutable series mapping: %v %+v", err, series.Sampling)
	}
	checkRate(series.Points[0].Condition.Short.Burn, "14.4")
	if series.Points[1].Condition.Short.Good != nil || series.Points[2].Condition.Short.Burn != nil {
		t.Fatal("series fabricated missing or zero-traffic rates")
	}
	f.call(t, http.MethodGet, path+"/series?condition_id=unknown", "", "", "", 400)
	f.call(t, http.MethodGet, path+"/series?condition_id=fast&limit=1", "", "", "", 400)

	if again := readEvidence("firing"); again.Id != firing.Id || again.Evaluation.Conditions[0].Short.Good == nil || *again.Evaluation.Conditions[0].Short.Good != "9856" {
		t.Fatal("latest evidence rewrote original firing population")
	}
	body, header = f.call(t, http.MethodGet, path, "", "", "", 200)
	if err := json.Unmarshal(body, &record); err != nil || record.State != "firing" || record.Version != "3" {
		t.Fatal("unavailable evidence recovered the occurrence")
	}
	preAck := header.Get("ETag")
	denied := f
	denied.csrf = ""
	denied.call(t, http.MethodPost, path+"/acknowledge", "{}", "no-proof", preAck, 403)
	body, header = f.call(t, http.MethodPost, path+"/acknowledge", "{}", "global-ack", preAck, 200)
	var ack api.AlertCommandResult
	if err := json.Unmarshal(body, &ack); err != nil || ack.Alert == nil || ack.Alert.State != "acknowledged" || ack.Alert.Version != "4" || ack.Alert.AcknowledgedBy == nil || ack.Alert.AcknowledgedAt == nil || !ack.Alert.AcknowledgedAt.Equal(now) || header.Get("ETag") != fmt.Sprintf(`"alert-occurrence:%s:4"`, original.ID) {
		t.Fatalf("ack command/provenance: %v", err)
	}
	replay, _ := f.call(t, http.MethodPost, path+"/acknowledge", "{}", "global-ack", preAck, 200)
	var replayed api.AlertCommandResult
	if err := json.Unmarshal(replay, &replayed); err != nil || replayed.RequestId != ack.RequestId || replayed.Alert == nil || replayed.Alert.Version != "4" {
		t.Fatal("ack replay did not retain original receipt")
	}
	f.call(t, http.MethodPost, path+"/acknowledge", "{}", "stale-ack", preAck, 409)
	f.call(t, http.MethodPost, path+"/acknowledge", "{}", "already-ack", header.Get("ETag"), 200)
	// A separately constructed HTTP request observes the same durable global ack.
	body, _ = f.call(t, http.MethodGet, path, "", "", "", 200)
	if err := json.Unmarshal(body, &record); err != nil || record.State != "acknowledged" || record.Version != "4" || record.AcknowledgedBy == nil || *record.AcknowledgedBy != *ack.Alert.AcknowledgedBy {
		t.Fatal("ack was not globally persisted")
	}
	body, _ = f.call(t, http.MethodGet, path+"/transitions?limit=1", "", "", "", 200)
	var transitions api.AlertTransitionPage
	if err := json.Unmarshal(body, &transitions); err != nil || len(transitions.Items) != 1 || transitions.Items[0].Cause != "condition_matched" || transitions.NextCursor == nil {
		t.Fatal("firing transition page missing")
	}
	body, _ = f.call(t, http.MethodGet, path+"/transitions?limit=1&cursor="+*transitions.NextCursor, "", "", "", 200)
	transitions = api.AlertTransitionPage{}
	if err := json.Unmarshal(body, &transitions); err != nil || len(transitions.Items) != 1 {
		t.Fatal("ack transition page missing")
	}
	transition := transitions.Items[0]
	if transition.Cause != "acknowledged" || transition.Sequence != "2" || transition.ActorId == nil || *transition.ActorId != *ack.Alert.AcknowledgedBy || transition.RequestId != ack.RequestId || !transition.EffectiveAt.Equal(now) || !transition.DetectedAt.Equal(now) || transitions.NextCursor != nil {
		t.Fatal("ack transition lost original actor/request/time or replay added a transition")
	}
	conn := testpostgres.Connect(t, f.databaseURL)
	var auditCount, receiptCount int
	if err := conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_audit_events WHERE subject_id=$1 AND action='alert.acknowledged'),(SELECT count(*) FROM almanac_management_receipts WHERE operation='alert.acknowledge')`, original.ID).Scan(&auditCount, &receiptCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || receiptCount != 2 {
		t.Fatalf("ack idempotency audit/receipt counts=%d/%d", auditCount, receiptCount)
	}
}
