//go:build integration

package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationAgentLifecycleReadsRevokesDisablesAndReplays(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("lifecycle-agent", "lifecycle-create"))
	if err != nil {
		t.Fatal(err)
	}
	clock := func(at time.Time) func() time.Time { return func() time.Time { return at } }
	readAt := f.base.Add(4 * time.Second)
	readConn := integrationConnect(t, f.databaseURL)
	var sessionBefore, sessionAfter time.Time
	if err := readConn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&sessionBefore); err != nil {
		t.Fatal(err)
	}
	got, err := GetAgent(t.Context(), f.databaseURL, AgentReadInput{SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: clock(readAt)})
	if err != nil || got != created.Agent {
		t.Fatalf("GetAgent = %+v, %v", got, err)
	}
	page, err := ListAgents(t.Context(), f.databaseURL, ListAgentsInput{SessionVerifier: f.verifier, Now: clock(readAt)})
	if err != nil || len(page.Items) != 1 || page.Items[0] != created.Agent {
		t.Fatalf("ListAgents = %+v, %v", page, err)
	}
	history, err := ListAgentCredentials(t.Context(), f.databaseURL, ListAgentCredentialsInput{SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: clock(readAt)})
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != created.Credential.ID || history.Items[0].RevokedAt != nil {
		t.Fatalf("credential history = %+v, %v", history, err)
	}
	if err := readConn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&sessionAfter); err != nil {
		t.Fatal(err)
	}
	if !sessionAfter.Equal(sessionBefore) {
		t.Fatalf("successful agent reads changed session last_seen_at: %v -> %v", sessionBefore, sessionAfter)
	}
	closeIntegrationConn(readConn)
	proof := f.input("unused", "unused").Proof
	revokeInput := RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "lifecycle-revoke", RequestID: "lifecycle-revoke", Now: clock(f.base.Add(5 * time.Second))}
	revoked, err := RevokeAgentCredential(t.Context(), f.databaseURL, revokeInput)
	if err != nil || !revoked.Changed || revoked.RevokedCredentialCount != 1 || revoked.Agent.Version != 2 || revoked.Credential == nil || revoked.Credential.RevokedAt == nil {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	revokeInput.Now = clock(f.base.Add(6 * time.Second))
	replayed, err := RevokeAgentCredential(t.Context(), f.databaseURL, revokeInput)
	if err != nil || !replayed.Replayed || replayed.Agent != revoked.Agent || replayed.Credential == nil {
		t.Fatalf("revoke replay = %+v, %v", replayed, err)
	}
	disableInput := DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 2, IdempotencyKey: "lifecycle-disable", RequestID: "lifecycle-disable", Now: clock(f.base.Add(7 * time.Second))}
	disabled, err := DisableAgent(t.Context(), f.databaseURL, disableInput)
	if err != nil || !disabled.Changed || disabled.Agent.Enabled || disabled.Agent.Version != 3 || disabled.RevokedCredentialCount != 0 {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
	if _, err := GetAgent(t.Context(), f.databaseURL, AgentReadInput{SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: clock(f.base.Add(8 * time.Second))}); err != nil {
		t.Fatalf("disabled agent is not readable: %v", err)
	}
}

func TestIntegrationMalformedAgentLifecycleReceiptFailsClosed(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("bad-receipt-agent", "bad-receipt-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	input := RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "bad-lifecycle-receipt", RequestID: "bad-lifecycle-receipt", Now: func() time.Time { return f.base.Add(5 * time.Second) }}
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_management_receipts SET result='{"version":1}'::jsonb WHERE actor_id=$1 AND operation='agent.credential.revoke' AND idempotency_key=$2`, f.userID, input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	input.Now = func() time.Time { return f.base.Add(6 * time.Second) }
	got, err := RevokeAgentCredential(t.Context(), f.databaseURL, input)
	if err == nil || err.Error() != "storage: invalid agent lifecycle receipt" {
		t.Fatalf("malformed receipt error=%v", err)
	}
	if got != (AgentLifecycleResult{}) {
		t.Fatalf("malformed receipt returned partial result: %+v", got)
	}
}

func TestIntegrationMalformedAgentCreationReceiptFailsClosed(t *testing.T) {
	f := newAgentFixture(t)
	input := f.input("bad-create-receipt-agent", "bad-create-receipt")
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_management_receipts SET result='{"version":1}'::jsonb WHERE actor_id=$1 AND operation='agent.create' AND idempotency_key=$2`, f.userID, input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	input.Now = func() time.Time { return f.base.Add(5 * time.Second) }
	got, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err == nil || err.Error() != "storage: invalid agent receipt" || got != (AgentCreation{}) || created.Agent.ID == "" {
		t.Fatalf("malformed creation receipt result=%+v err=%v", got, err)
	}
}

func TestIntegrationMalformedNoopDisableReceiptMissingRequiredScalarsFailsClosed(t *testing.T) {
	for _, field := range []string{"changed", "revoked_credential_count", "agent.enabled"} {
		t.Run(field, func(t *testing.T) {
			f := newAgentFixture(t)
			name := "missing-field-" + strings.NewReplacer(".", "-", "_", "-").Replace(field)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input(name, "missing-field-create-"+field))
			if err != nil {
				t.Fatal(err)
			}
			proof := f.input("unused", "unused").Proof
			at := f.base.Add(5 * time.Second)
			if _, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "missing-field-first-" + field, RequestID: "missing-field-first-" + field, Now: func() time.Time { return at }}); err != nil {
				t.Fatal(err)
			}
			input := DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 2, IdempotencyKey: "missing-field-noop-" + field, RequestID: "missing-field-noop-" + field, Now: func() time.Time { return f.base.Add(6 * time.Second) }}
			if _, err := DisableAgent(t.Context(), f.databaseURL, input); err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.databaseURL)
			mutation := `UPDATE public.almanac_management_receipts SET result=result - $3 WHERE actor_id=$1 AND operation='agent.disable' AND idempotency_key=$2`
			if field == "agent.enabled" {
				mutation = `UPDATE public.almanac_management_receipts SET result=jsonb_set(result,'{agent}',(result->'agent') - 'enabled') WHERE actor_id=$1 AND operation='agent.disable' AND idempotency_key=$2`
				if _, err := conn.Exec(t.Context(), mutation, f.userID, input.IdempotencyKey); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := conn.Exec(t.Context(), mutation, f.userID, input.IdempotencyKey, field); err != nil {
					t.Fatal(err)
				}
			}
			closeIntegrationConn(conn)
			result, err := DisableAgent(t.Context(), f.databaseURL, input)
			if err == nil || err.Error() != "storage: invalid agent lifecycle receipt" || result != (AgentLifecycleResult{}) {
				t.Fatalf("missing %s receipt result=%+v err=%v", field, result, err)
			}
		})
	}
}

func TestIntegrationAgentReadAndLifecycleAuthorizationPreservesState(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      error
	}{
		{"viewer", "UPDATE public.almanac_users SET role='viewer' WHERE id=$1", ErrAgentForbidden},
		{"editor", "UPDATE public.almanac_users SET role='editor' WHERE id=$1", ErrAgentForbidden},
		{"restricted", "UPDATE public.almanac_sessions SET restricted=true WHERE verifier=$1", ErrAgentForbidden},
		{"disabled-user", "UPDATE public.almanac_users SET enabled=false WHERE id=$1", ErrInvalidSession},
		{"expired-session", "", ErrInvalidSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFixture(t)
			in := f.input("read-auth-agent", "read-auth-create")
			created, err := CreateAgent(t.Context(), f.databaseURL, in)
			if err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.databaseURL)
			if tc.name == "expired-session" {
				// The issued session expires at base+30m+1s; preserve last_seen_at >= created_at.
			} else if tc.name == "restricted" {
				if _, err := conn.Exec(t.Context(), tc.sql, f.verifier[:]); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := conn.Exec(t.Context(), tc.sql, f.userID); err != nil {
					t.Fatal(err)
				}
			}
			var lastSeen time.Time
			if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&lastSeen); err != nil {
				t.Fatal(err)
			}
			targetID := created.Agent.ID
			if tc.name == "viewer" {
				targetID = "abcdefab-cdef-4abc-8def-abcdefabcdf0"
			}
			readAt := f.base.Add(5 * time.Second)
			if tc.name == "expired-session" {
				readAt = f.base.Add(30*time.Minute + time.Second)
			}
			_, err = GetAgent(t.Context(), f.databaseURL, AgentReadInput{SessionVerifier: f.verifier, AgentID: targetID, Now: func() time.Time { return readAt }})
			if !errors.Is(err, tc.want) {
				t.Fatalf("denied read error=%v, want %v", err, tc.want)
			}
			proof := in.Proof
			proof.VerifiedAt = readAt // Isolate current authority from proof expiry.
			revoked, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{
				Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID,
				ExpectedVersion: 1, IdempotencyKey: "denied-matrix-revoke", RequestID: "denied-matrix-revoke",
				Now: func() time.Time { return readAt },
			})
			if !errors.Is(err, tc.want) || revoked != (AgentLifecycleResult{}) {
				t.Fatalf("denied revoke error=%v, want %v; zero result=%t", err, tc.want, revoked == (AgentLifecycleResult{}))
			}
			disabled, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{
				Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1,
				IdempotencyKey: "denied-matrix-disable", RequestID: "denied-matrix-disable",
				Now: func() time.Time { return readAt },
			})
			if !errors.Is(err, tc.want) || disabled != (AgentLifecycleResult{}) {
				t.Fatalf("denied disable error=%v, want %v; zero result=%t", err, tc.want, disabled == (AgentLifecycleResult{}))
			}
			var enabled bool
			var version int64
			var revokedAt pgtype.Timestamptz
			if err := conn.QueryRow(t.Context(), `SELECT a.enabled,a.version,c.revoked_at
FROM public.almanac_agents a JOIN public.almanac_agent_credentials c ON c.agent_id=a.id
WHERE a.id=$1 AND c.id=$2`, created.Agent.ID, created.Credential.ID).Scan(&enabled, &version, &revokedAt); err != nil {
				t.Fatal(err)
			}
			var receipts, audits int
			if err := conn.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key IN ('denied-matrix-revoke','denied-matrix-disable')),
(SELECT count(*) FROM public.almanac_audit_events WHERE request_id IN ('denied-matrix-revoke','denied-matrix-disable'))`).Scan(&receipts, &audits); err != nil {
				t.Fatal(err)
			}
			if !enabled || version != 1 || revokedAt.Valid || receipts != 0 || audits != 0 {
				t.Fatal("denied lifecycle command changed protected state, audit or receipts")
			}
			var after time.Time
			if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !after.Equal(lastSeen) {
				t.Fatalf("read changed session last_seen_at: %v -> %v", lastSeen, after)
			}
			closeIntegrationConn(conn)
		})
	}
}

func TestIntegrationAgentReadRejectsRevokedSessionWithoutTargetDisclosure(t *testing.T) {
	f := newAgentFixture(t)
	if _, err := RevokeSession(t.Context(), f.databaseURL, f.verifier, f.base.Add(3*time.Second), "agent-read-session-revoke"); err != nil {
		t.Fatal(err)
	}
	_, err := GetAgent(t.Context(), f.databaseURL, AgentReadInput{SessionVerifier: f.verifier, AgentID: "abcdefab-cdef-4abc-8def-abcdefabcdf0", Now: func() time.Time { return f.base.Add(4 * time.Second) }})
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session read error=%v", err)
	}
}

func TestIntegrationAgentReadRechecksSessionExpiryAfterTargetLock(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("read-expiry-wait-agent", "read-expiry-wait-create"))
	if err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, f.databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	initial := f.base.Add(30 * time.Minute)
	var nanos atomic.Int64
	nanos.Store(initial.UnixNano())
	result := make(chan struct {
		agent AgentRecord
		err   error
	}, 1)
	go func() {
		agent, err := GetAgent(t.Context(), f.databaseURL, AgentReadInput{SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: func() time.Time { return time.Unix(0, nanos.Load()).UTC() }})
		result <- struct {
			agent AgentRecord
			err   error
		}{agent: agent, err: err}
	}()
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
	nanos.Store(f.base.Add(30*time.Minute + 2*time.Second).UnixNano())
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if !errors.Is(got.err, ErrInvalidSession) || got.agent != (AgentRecord{}) {
		t.Fatalf("expired read result=%+v", got)
	}
	var lastSeen time.Time
	if err := observer.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if !lastSeen.Equal(f.base.Add(time.Second)) {
		t.Fatalf("read changed last_seen_at to %v", lastSeen)
	}
	closeIntegrationConn(blocker)
}

func TestIntegrationAgentLifecycleRejectsPriorLogoutPasswordRoleAndEpochChanges(t *testing.T) {
	for _, kind := range []string{"logout", "password-change", "role-change", "epoch-change"} {
		t.Run(kind, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("invalidated-life-"+kind, "invalidated-life-create-"+kind))
			if err != nil {
				t.Fatal(err)
			}
			want := ErrInvalidSession
			switch kind {
			case "logout":
				if _, err := RevokeSession(t.Context(), f.databaseURL, f.verifier, f.base.Add(3*time.Second), "ordered-lifecycle-logout"); err != nil {
					t.Fatal(err)
				}
			case "password-change":
				var next [32]byte
				next[0] = 201
				if _, err := RotateLocalPassword(t.Context(), f.databaseURL, RotatePasswordInput{CurrentSessionVerifier: f.verifier, ExpectedCredentialRevision: f.revision, ExpectedCredentialEpoch: f.epoch, NewPasswordVerifier: testPasswordVerifier, NewSessionVerifier: next, Now: f.base.Add(3 * time.Second), RequestID: "ordered-lifecycle-password"}); err != nil {
					t.Fatal(err)
				}
			case "role-change":
				conn := integrationConnect(t, f.databaseURL)
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET role='viewer' WHERE id=$1", f.userID); err != nil {
					t.Fatal(err)
				}
				closeIntegrationConn(conn)
				want = ErrAgentForbidden
			case "epoch-change":
				conn := integrationConnect(t, f.databaseURL)
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET credential_epoch=gen_random_uuid() WHERE singleton"); err != nil {
					t.Fatal(err)
				}
				closeIntegrationConn(conn)
			}
			proof := f.input("unused", "unused").Proof
			requestID := "denied-lifecycle-" + kind
			result, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "ordered-lifecycle-" + kind, RequestID: requestID, Now: func() time.Time { return f.base.Add(5 * time.Second) }})
			if !errors.Is(err, want) || result != (AgentLifecycleResult{}) {
				t.Fatalf("post-%s lifecycle result=%+v err=%v; want %v", kind, result, err, want)
			}
			conn := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(conn)
			var enabled bool
			var version int64
			var receipts, audits int
			if err := conn.QueryRow(t.Context(), "SELECT enabled,version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&enabled, &version); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key=$1", "ordered-lifecycle-"+kind).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", requestID).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if !enabled || version != 1 || receipts != 0 || audits != 0 {
				t.Fatalf("%s invalidation changed agent enabled=%v version=%d receipt=%d audit=%d", kind, enabled, version, receipts, audits)
			}
		})
	}
}

func TestIntegrationAgentPaginationEqualTimestampAndCursorScope(t *testing.T) {
	f := newAgentFixture(t)
	ids := []string{"abcdefab-cdef-4abc-8def-abcdefabcdea", "abcdefab-cdef-4abc-8def-abcdefabcdeb", "abcdefab-cdef-4abc-8def-abcdefabcdec"}
	var owners []string
	for i, id := range ids {
		in := f.input(fmt.Sprintf("page-agent-%d", i), fmt.Sprintf("page-create-%d", i))
		in.CredentialID = id
		created, err := CreateAgent(t.Context(), f.databaseURL, in)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, created.Agent.ID)
	}
	conn := integrationConnect(t, f.databaseURL)
	credentialIDs := []string{"77777777-7777-4777-8777-777777777771", "77777777-7777-4777-8777-777777777772"}
	issued := f.base.Add(3 * time.Second)
	for _, id := range credentialIDs {
		if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at) VALUES($1,$2,decode(repeat('03',32),'hex'),$3,$4,$5)`, id, owners[0], f.epoch, issued, issued.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	closeIntegrationConn(conn)
	now := f.base.Add(4 * time.Second)
	seen := map[string]bool{}
	cursor := ""
	for range 3 {
		page, err := ListAgents(t.Context(), f.databaseURL, ListAgentsInput{SessionVerifier: f.verifier, Limit: 1, Cursor: cursor, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatalf("bad/duplicate page: %+v", page)
		}
		seen[page.Items[0].ID] = true
		cursor = page.NextCursor
	}
	if len(seen) != 3 || cursor != "" {
		t.Fatalf("stable traversal seen=%d next=%q", len(seen), cursor)
	}
	credentialSeen := map[string]bool{}
	credentialCursor := ""
	for range 3 {
		page, err := ListAgentCredentials(t.Context(), f.databaseURL, ListAgentCredentialsInput{SessionVerifier: f.verifier, AgentID: owners[0], Limit: 1, Cursor: credentialCursor, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || credentialSeen[page.Items[0].ID] {
			t.Fatalf("bad credential history page: %+v", page)
		}
		credentialSeen[page.Items[0].ID] = true
		credentialCursor = page.NextCursor
	}
	if len(credentialSeen) != 3 || credentialCursor != "" {
		t.Fatalf("credential traversal seen=%d next=%q", len(credentialSeen), credentialCursor)
	}
	foreignCursor, err := encodeAgentCursor(agentCursor{Version: 1, Kind: "credentials", Time: now, ID: ids[0], AgentID: owners[0]})
	if err != nil {
		t.Fatal(err)
	}
	bad := ListAgentCredentialsInput{SessionVerifier: f.verifier, AgentID: owners[1], Limit: 1, Cursor: foreignCursor, Now: func() time.Time { return now }}
	if _, err := ListAgentCredentials(t.Context(), f.databaseURL, bad); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("empty wrong-scope cursor error=%v", err)
	}
}

func TestIntegrationDisableBulkRevokesExpiredOldEpochAndPreservesHistory(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("bulk-disable-agent", "bulk-disable-create"))
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	issued := f.base.Add(4 * time.Second)
	prior := f.base.Add(5 * time.Second)
	oldEpoch := "abcdefab-cdef-4abc-8def-abcdefabcdef"
	for i, row := range []struct {
		id, epoch string
		revoked   *time.Time
	}{{"abcdefab-cdef-4abc-8def-abcdefabcdeb", oldEpoch, nil}, {"abcdefab-cdef-4abc-8def-abcdefabcdec", f.epoch, nil}, {"abcdefab-cdef-4abc-8def-abcdefabcdee", f.epoch, &prior}} {
		expires := issued.Add(time.Second)
		var revoked any
		if row.revoked != nil {
			revoked = *row.revoked
		}
		if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at,revoked_at) VALUES($1,$2,decode(repeat('01',32),'hex'),$3,$4,$5,$6)`, row.id, created.Agent.ID, row.epoch, issued, expires, revoked); err != nil {
			t.Fatalf("insert history %d: %v", i, err)
		}
	}
	input := DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "bulk-disable", RequestID: "bulk-disable", Now: func() time.Time { return f.base.Add(6 * time.Second) }}
	result, err := DisableAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.RevokedCredentialCount != 3 || result.Agent.Version != 2 {
		t.Fatalf("bulk disable result=%+v", result)
	}
	var revokedCount int
	var preserved pgtype.Timestamptz
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE revoked_at IS NOT NULL), max(revoked_at) FILTER(WHERE id=$1) FROM public.almanac_agent_credentials WHERE agent_id=$2", "abcdefab-cdef-4abc-8def-abcdefabcdee", created.Agent.ID).Scan(&revokedCount, &preserved); err != nil {
		t.Fatal(err)
	}
	if revokedCount != 4 || !preserved.Valid || !preserved.Time.Equal(prior) {
		t.Fatalf("revoked count=%d preserved timestamp=%v", revokedCount, preserved)
	}
	var revocationAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action='agent.credential.revoked' AND occurred_at=$1", f.base.Add(6*time.Second)).Scan(&revocationAudits); err != nil {
		t.Fatal(err)
	}
	if revocationAudits != 3 {
		t.Fatalf("revocation audits=%d; want 3", revocationAudits)
	}
	closeIntegrationConn(conn)
}

func TestIntegrationAgentLifecycleNoOpsVersionAndHistoricalReplay(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("lifecycle-noop-agent", "lifecycle-noop-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	at := func(sec int) func() time.Time {
		return func() time.Time { return f.base.Add(time.Duration(sec) * time.Second) }
	}
	revoke := RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "revoke-original", RequestID: "revoke-original", Now: at(5)}
	first, err := RevokeAgentCredential(t.Context(), f.databaseURL, revoke)
	if err != nil {
		t.Fatal(err)
	}
	revokeNoop, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 2, IdempotencyKey: "revoke-noop", RequestID: "revoke-noop", Now: at(5)})
	if err != nil || revokeNoop.Changed || revokeNoop.RevokedCredentialCount != 0 || revokeNoop.Agent.Version != 2 {
		t.Fatalf("revoke no-op=%+v err=%v", revokeNoop, err)
	}
	if _, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 2, IdempotencyKey: "revoke-original", RequestID: "disable-after-revoke", Now: at(6)}); err != nil {
		t.Fatal(err)
	}
	changedBody := revoke
	changedBody.ExpectedVersion = 2
	changedBody.Now = at(6)
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, changedBody); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("changed receipt body/version error=%v", err)
	}
	revoke.Now = at(7)
	historical, err := RevokeAgentCredential(t.Context(), f.databaseURL, revoke)
	if err != nil || !historical.Replayed || historical.Agent != first.Agent || historical.Agent.Enabled != first.Agent.Enabled {
		t.Fatalf("historical replay=%+v err=%v", historical, err)
	}
	noop, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 3, IdempotencyKey: "disable-noop", RequestID: "disable-noop", Now: at(8)})
	if err != nil || noop.Changed || noop.Agent.Version != 3 || noop.RevokedCredentialCount != 0 {
		t.Fatalf("disable no-op=%+v err=%v", noop, err)
	}
	if _, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 2, IdempotencyKey: "stale-noop", RequestID: "stale-noop", Now: at(9)}); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("stale no-op error=%v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var revokeAudits, revokeNoopAudits, disableAudits, disableNoopAudits int
	for _, query := range []struct {
		sql     string
		dest    *int
		subject string
	}{
		{"SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action='agent.credential.revoked'", &revokeAudits, created.Credential.ID},
		{"SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action='agent.credential.revoke.noop'", &revokeNoopAudits, created.Credential.ID},
		{"SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action='agent.disabled'", &disableAudits, created.Agent.ID},
		{"SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action='agent.disable.noop'", &disableNoopAudits, created.Agent.ID},
	} {
		if err := conn.QueryRow(t.Context(), query.sql, query.subject).Scan(query.dest); err != nil {
			t.Fatal(err)
		}
	}
	if revokeAudits != 1 || revokeNoopAudits != 1 || disableAudits != 1 || disableNoopAudits != 1 {
		t.Fatalf("lifecycle audit counts revoke=%d revoke-noop=%d disable=%d disable-noop=%d", revokeAudits, revokeNoopAudits, disableAudits, disableNoopAudits)
	}
	var staleReceipts int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key='stale-noop'").Scan(&staleReceipts); err != nil {
		t.Fatal(err)
	}
	if staleReceipts != 0 {
		t.Fatalf("stale no-op inserted %d receipt(s)", staleReceipts)
	}
	closeIntegrationConn(conn)
	var fresh [32]byte
	fresh[0] = 151
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: fresh, Now: f.base.Add(10 * time.Second), RequestID: "fresh-replay-session"}); err != nil {
		t.Fatal(err)
	}
	freshProof := proof
	freshProof.SessionVerifier = fresh
	freshProof.VerifiedAt = f.base.Add(11 * time.Second)
	freshReplay, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: freshProof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "revoke-original", RequestID: "fresh-replay", Now: at(12)})
	if err != nil || !freshReplay.Replayed || freshReplay.Agent != first.Agent {
		t.Fatalf("fresh-session historical replay=%+v err=%v", freshReplay, err)
	}
	if _, err := RevokeSession(t.Context(), f.databaseURL, fresh, f.base.Add(13*time.Second), "revoke-fresh-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: freshProof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "revoke-original", RequestID: "revoked-authority-replay", Now: at(14)}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked authority replay error=%v", err)
	}
}

func TestIntegrationLifecycleMaxVersionNoopAndChangedConflict(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("max-noop-agent", "max-noop-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "max-revoke-first", RequestID: "max-revoke-first", Now: func() time.Time { return f.base.Add(5 * time.Second) }}); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agents SET version=$2 WHERE id=$1", created.Agent.ID, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	noop, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: math.MaxInt64, IdempotencyKey: "max-revoke-noop", RequestID: "max-revoke-noop", Now: func() time.Time { return f.base.Add(6 * time.Second) }})
	if err != nil || noop.Changed || noop.Agent.Version != math.MaxInt64 {
		t.Fatalf("max-version no-op=%+v err=%v", noop, err)
	}
	changedInput := f.input("max-change-agent", "max-change-create")
	changedInput.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdeb"
	changed, err := CreateAgent(t.Context(), f.databaseURL, changedInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: changed.Credential.ID, ExpectedVersion: math.MaxInt64, IdempotencyKey: "wrong-agent-credential", RequestID: "wrong-agent-credential", Now: func() time.Time { return f.base.Add(6 * time.Second) }}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("wrong-agent credential error=%v", err)
	}
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdf1", ExpectedVersion: math.MaxInt64, IdempotencyKey: "missing-agent-credential", RequestID: "missing-agent-credential", Now: func() time.Time { return f.base.Add(6 * time.Second) }}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("missing credential error=%v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agents SET version=$2 WHERE id=$1", changed.Agent.ID, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	_, err = DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: changed.Agent.ID, ExpectedVersion: math.MaxInt64, IdempotencyKey: "max-disable", RequestID: "max-disable", Now: func() time.Time { return f.base.Add(7 * time.Second) }})
	if !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("max-version changed disable error=%v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var rejectedReceipts int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key IN ('wrong-agent-credential','missing-agent-credential','max-disable')").Scan(&rejectedReceipts); err != nil {
		t.Fatal(err)
	}
	if rejectedReceipts != 0 {
		t.Fatalf("not-found/conflict commands left %d receipts", rejectedReceipts)
	}
}

func TestIntegrationAgentLifecycleReceiptExactExpiryCreatesFreshNoop(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("receipt-expiry-agent", "receipt-expiry-create"))
	if err != nil {
		t.Fatal(err)
	}
	firstAt := f.base.Add(5 * time.Second)
	proof := f.input("unused", "unused").Proof
	input := RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "expiry-key", RequestID: "expiry-first", Now: func() time.Time { return firstAt }}
	first, err := RevokeAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || !first.Changed {
		t.Fatalf("initial revoke=%+v err=%v", first, err)
	}
	expiry := firstAt.Add(agentCreateReceiptTTL)
	var verifier [32]byte
	verifier[0] = 152
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: verifier, Now: expiry, RequestID: "expiry-fresh-session"}); err != nil {
		t.Fatal(err)
	}
	input.Proof.SessionVerifier = verifier
	input.Proof.VerifiedAt = expiry
	input.RequestID = "expiry-fresh-request"
	input.Now = func() time.Time { return expiry }
	stale := input
	if result, err := RevokeAgentCredential(t.Context(), f.databaseURL, stale); !errors.Is(err, ErrAgentConflict) || result != (AgentLifecycleResult{}) {
		t.Fatalf("expired-receipt stale version result=%+v err=%v", result, err)
	}
	input.ExpectedVersion = 2
	result, err := RevokeAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Changed || result.Agent.Version != 2 {
		t.Fatalf("exact-expiry result=%+v", result)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var count int
	var createdAt, expiresAt time.Time
	if err := conn.QueryRow(t.Context(), "SELECT count(*), min(created_at), min(expires_at) FROM public.almanac_management_receipts WHERE operation='agent.credential.revoke' AND idempotency_key='expiry-key'").Scan(&count, &createdAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !createdAt.Equal(expiry) || !expiresAt.Equal(expiry.Add(agentCreateReceiptTTL)) {
		t.Fatalf("fresh boundary receipt count=%d created=%v expiry=%v", count, createdAt, expiresAt)
	}
}

func TestIntegrationAgentLifecycleAuditAndReceiptFailuresRollback(t *testing.T) {
	for _, point := range []string{"audit", "receipt"} {
		t.Run(point, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rollback-life-agent-"+point, "rollback-life-create-"+point))
			if err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.databaseURL)
			if point == "audit" {
				_, err = conn.Exec(t.Context(), `CREATE FUNCTION public.fail_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='agent.credential.revoked' THEN RAISE EXCEPTION 'test lifecycle audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_lifecycle_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_lifecycle_audit()`)
			} else {
				_, err = conn.Exec(t.Context(), `CREATE FUNCTION public.fail_lifecycle_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation='agent.disable' THEN RAISE EXCEPTION 'test lifecycle receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_lifecycle_receipt BEFORE INSERT ON public.almanac_management_receipts FOR EACH ROW EXECUTE FUNCTION public.fail_lifecycle_receipt()`)
			}
			if err != nil {
				t.Fatal(err)
			}
			proof := f.input("unused", "unused").Proof
			now := f.base.Add(5 * time.Second)
			var operationErr error
			var operationResult AgentLifecycleResult
			if point == "audit" {
				operationResult, operationErr = RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "rollback-revoke", RequestID: "rollback-revoke", Now: func() time.Time { return now }})
			} else {
				operationResult, operationErr = DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "rollback-disable", RequestID: "rollback-disable", Now: func() time.Time { return now }})
			}
			wantError := "storage: agent revoke audit failed"
			receiptKey := "rollback-revoke"
			requestID := "rollback-revoke"
			if point == "receipt" {
				wantError = "storage: agent lifecycle receipt insert failed"
				receiptKey = "rollback-disable"
				requestID = "rollback-disable"
			}
			if operationErr == nil || operationErr.Error() != wantError || operationResult != (AgentLifecycleResult{}) {
				t.Fatalf("injected %s failure result=%+v err=%v; want safe zero result and %q", point, operationResult, operationErr, wantError)
			}
			var enabled bool
			var version int64
			var revoked pgtype.Timestamptz
			if err := conn.QueryRow(t.Context(), "SELECT enabled,version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&enabled, &version); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT revoked_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if !enabled || version != 1 || revoked.Valid {
				t.Fatalf("%s rollback left enabled=%v version=%d revoked=%v", point, enabled, version, revoked)
			}
			var receipts, audits int
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE operation=$1 AND idempotency_key=$2", map[string]string{"audit": "agent.credential.revoke", "receipt": "agent.disable"}[point], receiptKey).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1 AND action IN ('agent.disabled','agent.credential.revoked')", requestID).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if receipts != 0 || audits != 0 {
				t.Fatalf("%s rollback rows receipts=%d audits=%d", point, receipts, audits)
			}
			closeIntegrationConn(conn)
		})
	}
}

func TestIntegrationAgentNoopAuditFailureRollsBackReceipt(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("noop-audit-agent", "noop-audit-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	priorAt := f.base.Add(4 * time.Second)
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "noop-audit-prime", RequestID: "noop-audit-prime", Now: func() time.Time { return priorAt }}); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_agent_noop_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='agent.credential.revoke.noop' THEN RAISE EXCEPTION 'test no-op audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_agent_noop_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_agent_noop_audit()`); err != nil {
		t.Fatal(err)
	}
	input := RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 2, IdempotencyKey: "noop-audit-fail", RequestID: "noop-audit-fail", Now: func() time.Time { return f.base.Add(5 * time.Second) }}
	result, operationErr := RevokeAgentCredential(t.Context(), f.databaseURL, input)
	if operationErr == nil || operationErr.Error() != "storage: agent revoke no-op audit failed" || result != (AgentLifecycleResult{}) {
		t.Fatalf("no-op audit failure result=%+v err=%v", result, operationErr)
	}
	var version int64
	var revoked pgtype.Timestamptz
	var receipts, noops int
	if err := conn.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT revoked_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key=$1", input.IdempotencyKey).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action='agent.credential.revoke.noop' AND request_id=$1", input.RequestID).Scan(&noops); err != nil {
		t.Fatal(err)
	}
	if version != 2 || !revoked.Valid || !revoked.Time.Equal(priorAt) || receipts != 0 || noops != 0 {
		t.Fatalf("no-op audit rollback version=%d revoked=%+v receipts=%d noops=%d", version, revoked, receipts, noops)
	}
	closeIntegrationConn(conn)
}

func TestIntegrationDisableValidatesCredentialTimeAfterTargetLock(t *testing.T) {
	for _, tc := range []struct {
		name         string
		issuedOffset time.Duration
		wantErr      error
	}{{"between-samples", 5500 * time.Millisecond, nil}, {"after-final-sample", 6500 * time.Millisecond, ErrInvalidAgentInput}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("clock-disable-"+tc.name, "clock-disable-create-"+tc.name))
			if err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.databaseURL)
			issued := f.base.Add(tc.issuedOffset)
			id := "abcdefab-cdef-4abc-8def-abcdefabcdeb"
			if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at) VALUES($1,$2,decode(repeat('02',32),'hex'),$3,$4,$5)`, id, created.Agent.ID, f.epoch, issued, issued.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			closeIntegrationConn(conn)
			times := []time.Time{f.base.Add(5 * time.Second), f.base.Add(6 * time.Second), f.base.Add(7 * time.Second)}
			var index atomic.Int64
			clock := func() time.Time {
				i := int(index.Add(1)) - 1
				if i >= len(times) {
					return times[len(times)-1]
				}
				return times[i]
			}
			result, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "clock-disable", RequestID: "clock-disable", Now: clock})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("disable err=%v want %v result=%+v", err, tc.wantErr, result)
				}
			} else if err != nil || result.RevokedCredentialCount != 2 {
				t.Fatalf("valid post-lock issuance result=%+v err=%v", result, err)
			}
		})
	}
}

func TestIntegrationAgentLifecycleRejectsBackwardsCompletionClock(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("backwards-clock-agent", "backwards-clock-create"))
	if err != nil {
		t.Fatal(err)
	}
	times := []time.Time{f.base.Add(5 * time.Second), f.base.Add(6 * time.Second), f.base.Add(4 * time.Second)}
	var index atomic.Int64
	clock := func() time.Time {
		i := int(index.Add(1)) - 1
		if i >= len(times) {
			return times[len(times)-1]
		}
		return times[i]
	}
	_, err = RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "backwards-clock", RequestID: "backwards-clock", Now: clock})
	if !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("backward completion error=%v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var version int64
	var revoked pgtype.Timestamptz
	var receipts, audits int
	if err := conn.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT revoked_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key='backwards-clock'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id='backwards-clock'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if version != 1 || revoked.Valid || receipts != 0 || audits != 0 {
		t.Fatalf("backwards clock committed version=%d revoked=%v receipts=%d audits=%d", version, revoked, receipts, audits)
	}
}

func TestIntegrationLifecycleTargetLockWaitRechecksProofAndSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lock    string
		pattern string
		expiry  time.Duration
	}{
		{name: "user-proof", lock: "SELECT id FROM public.almanac_users WHERE id=$1 FOR UPDATE", pattern: "%FROM public.almanac_users%", expiry: 35 * time.Second},
		{name: "agent-proof", lock: "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", pattern: "%FROM public.almanac_agents%", expiry: 35 * time.Second},
		{name: "credential-session", lock: "SELECT id FROM public.almanac_agent_credentials WHERE id=$1 FOR UPDATE", pattern: "%FROM public.almanac_agent_credentials%", expiry: 30*time.Minute + 2*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("wait-agent-"+tc.name, "wait-create-"+tc.name))
			if err != nil {
				t.Fatal(err)
			}
			blocker := integrationConnect(t, f.databaseURL)
			tx, err := blocker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			key := created.Agent.ID
			if tc.name == "user-proof" {
				key = f.userID
			}
			if tc.name == "credential-session" {
				key = created.Credential.ID
			}
			if _, err := tx.Exec(t.Context(), tc.lock, key); err != nil {
				t.Fatal(err)
			}
			initial := f.base.Add(5 * time.Second)
			if tc.name == "credential-session" {
				initial = f.base.Add(30 * time.Minute)
			}
			var nanos atomic.Int64
			nanos.Store(initial.UnixNano())
			input := RevokeAgentCredentialInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "wait-" + tc.name, RequestID: "wait-" + tc.name, Now: func() time.Time { return time.Unix(0, nanos.Load()).UTC() }}
			if tc.name == "credential-session" {
				input.Proof.VerifiedAt = initial.Add(-time.Second)
			}
			result := make(chan error, 1)
			go func() { _, err := RevokeAgentCredential(t.Context(), f.databaseURL, input); result <- err }()
			observer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentQueryWait(t, observer, tc.pattern)
			if tc.name == "credential-session" {
				nanos.Store(f.base.Add(tc.expiry).UnixNano())
			} else {
				nanos.Store(f.base.Add(tc.expiry).UnixNano())
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("post-lock error=%v", err)
			}
			var version int64
			if err := observer.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 1 {
				t.Fatalf("post-lock denial changed version to %d", version)
			}
			closeIntegrationConn(blocker)
		})
	}
}

func TestIntegrationLifecycleCancellationDuringTargetLockReleasesTransaction(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("cancel-lifecycle-agent", "cancel-lifecycle-create"))
	if err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, f.databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "cancel-lifecycle", RequestID: "cancel-lifecycle", Now: func() time.Time { return f.base.Add(5 * time.Second) }}
	result := make(chan error, 1)
	go func() { _, err := DisableAgent(ctx, f.databaseURL, input); result <- err }()
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
	cancel()
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrCancelled) {
		t.Fatalf("canceled lifecycle error=%v", err)
	}
	var enabled bool
	var version int64
	if err := observer.QueryRow(t.Context(), "SELECT enabled,version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&enabled, &version); err != nil {
		t.Fatal(err)
	}
	if !enabled || version != 1 {
		t.Fatalf("cancellation changed agent enabled=%v version=%d", enabled, version)
	}
	var receipts, audits int
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key='cancel-lifecycle'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id='cancel-lifecycle'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || audits != 0 {
		t.Fatalf("cancellation left receipt=%d audit=%d", receipts, audits)
	}
	closeIntegrationConn(blocker)
}

func TestIntegrationConcurrentSameLifecycleReceiptHasOneMutation(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("lifecycle-race-agent", "lifecycle-race-create"))
	if err != nil {
		t.Fatal(err)
	}
	at := f.base.Add(5 * time.Second)
	input := RevokeAgentCredentialInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "lifecycle-race", RequestID: "lifecycle-race", Now: func() time.Time { return at }}
	start := make(chan struct{})
	results := make(chan struct {
		value AgentLifecycleResult
		err   error
	}, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			value, err := RevokeAgentCredential(t.Context(), f.databaseURL, input)
			results <- struct {
				value AgentLifecycleResult
				err   error
			}{value, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	replays, freshChanges := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.value.Replayed {
			replays++
		}
		if result.value.Changed && !result.value.Replayed {
			freshChanges++
		}
	}
	if replays != 1 || freshChanges != 1 {
		t.Fatalf("same receipt outcomes: replays=%d fresh_changes=%d", replays, freshChanges)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var version int64
	var audits, receipts int
	if err := conn.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action='agent.credential.revoked'", created.Credential.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.credential.revoke' AND idempotency_key='lifecycle-race'", f.userID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if version != 2 || audits != 1 || receipts != 1 {
		t.Fatalf("same receipt protected state version=%d audits=%d receipts=%d", version, audits, receipts)
	}
}

func TestIntegrationDifferentAdministratorsRaceOnSharedAgentVersion(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("admin-race-agent", "admin-race-create"))
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var otherID string
	if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users(login,password_verifier,role,enabled,must_change_password,created_at,updated_at) VALUES('lifecycle-second-admin',$1,'administrator',true,false,$2,$2) RETURNING id::text`, testPasswordVerifier, f.base).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	var verifier [32]byte
	verifier[0] = 199
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: otherID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: 1, Verifier: verifier, Now: f.base.Add(2 * time.Second), RequestID: "second-lifecycle-session"}); err != nil {
		t.Fatal(err)
	}
	first := DisableAgentInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "admin-one-disable", RequestID: "admin-one-disable", Now: func() time.Time { return f.base.Add(5 * time.Second) }}
	secondProof := first.Proof
	secondProof.UserID = otherID
	secondProof.SessionVerifier = verifier
	secondProof.CredentialRevision = 1
	second := DisableAgentInput{Proof: secondProof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "admin-two-disable", RequestID: "admin-two-disable", Now: func() time.Time { return f.base.Add(5 * time.Second) }}
	start := make(chan struct{})
	type result struct {
		err     error
		changed bool
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, input := range []DisableAgentInput{first, second} {
		wg.Go(func() {
			<-start
			value, err := DisableAgent(t.Context(), f.databaseURL, input)
			results <- result{err: err, changed: value.Changed}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for item := range results {
		if item.err == nil && item.changed {
			success++
		} else if errors.Is(item.err, ErrAgentConflict) {
			conflict++
		} else {
			t.Fatalf("race result=%+v", item)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("cross-actor race success=%d conflict=%d", success, conflict)
	}
}
