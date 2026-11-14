//go:build integration

package storage

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type alertTestFixture struct {
	*selfHealFixture
	sloID  string
	limits alert.Limits
}

func newAlertTestFixture(t *testing.T) alertTestFixture {
	t.Helper()
	s := newSelfHealFixture(t, "alert-journal", true)
	return alertTestFixture{s, s.createSLO(t, "alert-journal"), alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}}
}

func alertTestTx(t *testing.T, conn *pgx.Conn, work func(pgx.Tx) error) {
	t.Helper()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := work(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f alertTestFixture) policy(t *testing.T, name string) string {
	t.Helper()
	return f.policyThreshold(t, name, "1")
}

func (f alertTestFixture) policyThreshold(t *testing.T, name, threshold string) string {
	t.Helper()
	id := uuid.New().String()
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		return registerAlertPolicy(t.Context(), tx, alertRegistration{
			Policy: alert.PolicyRevision{ID: id, Revision: 1, Conditions: []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: threshold}}},
			SLOID:  f.sloID, Name: name, Severity: "high", FirstBoundary: f.at, Now: f.now, Limits: f.limits,
		})
	})
	return id
}

func (f alertTestFixture) input(t *testing.T, claim alertClaim, quality alert.Quality, breaching bool) alert.Input {
	t.Helper()
	row, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	generation, err := dbgen.New(f.conn).GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		t.Fatal(err)
	}
	identity := alert.SnapshotIdentity{SLOID: f.sloID, SLORevision: row.DefinitionRevision, SLIRevision: row.MeasurementRevision, SourceGeneration: generation, CalculationVersion: operationalCalculationVersion, Cutoff: claim.ScheduledAt}
	binding, err := dbgen.New(f.conn).ReadAlertBinding(t.Context(), mustPGUUID(claim.PolicyID))
	if err != nil {
		t.Fatal(err)
	}
	var conditions []alert.Condition
	if err := unmarshalAlertData(binding.Conditions, &conditions); err != nil {
		t.Fatal(err)
	}
	input := alert.Input{Policy: alert.PolicyRevision{ID: claim.PolicyID, Revision: claim.Revision, Conditions: conditions},
		Objective: alert.Objective{Target: row.Target, Cadence: time.Duration(row.CadenceSeconds) * time.Second, DefinitionValidFrom: row.EvaluationValidFrom.Time, MeasurementValidFrom: row.SliValidFrom.Time, RetentionStart: f.at.Add(-400 * 24 * time.Hour)}, Snapshot: identity}
	for _, duration := range []time.Duration{5 * time.Minute, time.Hour} {
		good := "10000"
		total := "10000"
		if breaching {
			good = "9800"
		}
		if generation == 0 && quality != alert.Complete {
			good, total = "", ""
		}
		input.Windows = append(input.Windows, alert.WindowEvidence{Snapshot: identity, Start: identity.Cutoff.Add(-duration), End: identity.Cutoff, Quality: quality, Good: good, Total: total})
	}
	return input
}

func (f alertTestFixture) claim(t *testing.T, at time.Time) alertClaim {
	t.Helper()
	claims, err := f.runtime.claimAlertWork(t.Context(), at, at, time.Minute, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim=%+v %v", claims, err)
	}
	return claims[0]
}

func TestIntegrationAlertJournal(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Checkout burn")
	claim := f.claim(t, f.at)
	first, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil || first.Occurrence == nil || first.Occurrence.State != alert.Firing {
		t.Fatalf("open: %+v %v", first, err)
	}
	var ack alertStoredOccurrence
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		var err error
		ack, err = acknowledgeAlertTx(t.Context(), tx, alertAcknowledgement{ID: first.Occurrence.ID, ExpectedVersion: first.Version, ActorID: f.f.userID, RequestID: "ack", At: f.at.Add(2 * time.Second)})
		return err
	})
	if ack.Occurrence.AcknowledgedBy != f.f.userID || ack.Occurrence.State != alert.Acknowledged {
		t.Fatalf("global ack: %+v", ack)
	}
	var requestID string
	if err := f.conn.QueryRow(t.Context(), `SELECT request_id FROM almanac_alert_transitions WHERE occurrence_id=$1 AND cause='acknowledged'`, ack.Occurrence.ID).Scan(&requestID); err != nil || requestID != "ack" {
		t.Fatalf("acknowledgement trace provenance: %q %v", requestID, err)
	}
	for i, quality := range []alert.Quality{alert.Missing, alert.Complete, alert.Complete} {
		at := f.at.Add(time.Duration(i+1) * time.Minute)
		claim = f.claim(t, at)
		got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, quality, i == 1), f.limits, func() time.Time { return at.Add(time.Second) })
		if err != nil || got.Occurrence == nil || got.Occurrence.ID != first.Occurrence.ID || got.Occurrence.AcknowledgedBy != f.f.userID {
			t.Fatalf("continuity: %+v %v", got, err)
		}
		want := alert.Acknowledged
		if i == 2 {
			want = alert.Resolved
		}
		if got.Occurrence.State != want {
			t.Fatalf("state=%s want %s", got.Occurrence.State, want)
		}
	}
	claim = f.claim(t, f.at.Add(4*time.Minute))
	next, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(4*time.Minute + time.Second) })
	if err != nil || next.Occurrence.ID == first.Occurrence.ID || next.Occurrence.AcknowledgedBy != "" {
		t.Fatalf("new occurrence: %+v %v", next, err)
	}
	var evaluations, occurrences, transitions int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1),(SELECT count(*) FROM almanac_alert_occurrences WHERE policy_id=$1),(SELECT count(*) FROM almanac_alert_transitions WHERE policy_id=$1)`, policy).Scan(&evaluations, &occurrences, &transitions); err != nil {
		t.Fatal(err)
	}
	if evaluations != 5 || occurrences != 2 || transitions != 4 {
		t.Fatalf("journal counts=%d/%d/%d want5/2/4", evaluations, occurrences, transitions)
	}
}

func TestIntegrationAlertClaims(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "A")
	f.policy(t, "B")
	var group sync.WaitGroup
	results := make(chan []alertClaim, 2)
	failures := make(chan error, 2)
	for range 2 {
		group.Go(func() {
			claims, err := f.runtime.claimAlertWork(t.Context(), f.at, f.at, 5*time.Second, 1)
			results <- claims
			failures <- err
		})
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	var old alertClaim
	for claims := range results {
		if len(claims) != 1 || seen[claims[0].PolicyID] {
			t.Fatalf("overlapping claims: %+v", claims)
		}
		old = claims[0]
		seen[old.PolicyID] = true
	}
	if claims, err := f.runtime.claimAlertWork(t.Context(), f.at, f.at, time.Minute, 2); err != nil || len(claims) != 0 {
		t.Fatalf("same tick claims: %+v %v", claims, err)
	}
	reclaimed, err := f.runtime.claimAlertWork(t.Context(), f.at.Add(6*time.Second), f.at.Add(6*time.Second), time.Minute, 2)
	if err != nil || len(reclaimed) != 2 {
		t.Fatalf("reclaim: %+v %v", reclaimed, err)
	}
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), old, f.input(t, old, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(7 * time.Second) }); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("old worker committed: %v", err)
	}
}

func TestIntegrationAlertDeferral(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	first := f.policy(t, "Pending")
	f.policy(t, "Independent")
	claims, err := f.runtime.claimAlertWork(t.Context(), f.at, f.at, time.Minute, 2)
	if err != nil || len(claims) != 2 {
		t.Fatalf("claim: %+v %v", claims, err)
	}
	for _, claim := range claims {
		if claim.PolicyID == first {
			if err := f.runtime.deferAlertWork(t.Context(), claim, func() time.Time { return f.at }, time.Second, "aggregation_pending"); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) }); err != nil {
				t.Fatal(err)
			}
		}
	}
	next := f.claim(t, f.at.Add(time.Second))
	if next.PolicyID != first || !next.ScheduledAt.Equal(f.at) {
		t.Fatalf("pending cursor advanced: %+v", next)
	}
	if err := f.runtime.deferAlertWork(t.Context(), next, func() time.Time { return f.at.Add(time.Second) }, time.Second, "aggregation_pending"); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	due, err := reopened.claimAlertWork(t.Context(), f.at.Add(2*time.Second), f.at.Add(2*time.Second), time.Minute, 1)
	if err != nil || len(due) != 1 || due[0].PolicyID != first || !due[0].ScheduledAt.Equal(f.at) {
		t.Fatalf("restart lost work: %+v %v", due, err)
	}
}

func TestIntegrationAlertAtomicity(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Atomic")
	claim := f.claim(t, f.at)
	input := f.input(t, claim, alert.Complete, true)
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_alert_transition() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected journal failure'; END$$; CREATE TRIGGER reject_alert_transition BEFORE INSERT ON almanac_alert_transitions FOR EACH ROW EXECUTE FUNCTION reject_alert_transition()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(time.Second) }); err == nil {
		t.Fatal("injected failure accepted")
	}
	var rows int
	var boundary time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1)+(SELECT count(*) FROM almanac_alert_occurrences WHERE policy_id=$1),next_scheduled_at FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&rows, &boundary); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || !boundary.Equal(f.at) {
		t.Fatalf("partial journal commit: rows=%d boundary=%s", rows, boundary)
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_alert_transition ON almanac_alert_transitions; DROP FUNCTION reject_alert_transition()`); err != nil {
		t.Fatal(err)
	}
	committed, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(3 * time.Second) })
	if err != nil || replay.EvaluationID != committed.EvaluationID || replay.Occurrence.ID != committed.Occurrence.ID {
		t.Fatalf("committed replay: %+v %v", replay, err)
	}
	input.Windows[0].Good = "9700"
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(4 * time.Second) }); !errors.Is(err, ErrAlertStale) {
		t.Fatal("changed evidence reused committed receipt")
	}
}

func TestIntegrationAlertFences(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source", "exclusion", "policy", "definition"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			policy := f.policy(t, "Fenced")
			claim := f.claim(t, f.at)
			input := f.input(t, claim, alert.Complete, true)
			var err error
			switch mode {
			case "source":
				_, err = f.conn.Exec(t.Context(), `UPDATE almanac_sli_source_generations SET generation=generation+1 WHERE sli_id=$1`, f.sliID)
			case "exclusion":
				_, err = f.conn.Exec(t.Context(), `UPDATE almanac_slo_exclusion_state SET revision=revision+1 WHERE slo_id=$1`, f.sloID)
			case "policy":
				_, err = f.conn.Exec(t.Context(), `UPDATE almanac_alert_policies SET enabled=false WHERE id=$1`, policy)
			case "definition":
				alertTestTx(t, f.conn, func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at)
SELECT slo_id,2,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=1`, f.sloID); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=2 WHERE id=$1`, f.sloID)
					return err
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(time.Second) }); !errors.Is(err, ErrAlertStale) {
				t.Fatalf("%s fence: %v", mode, err)
			}
			var rows int
			var due time.Time
			if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1),next_scheduled_at FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&rows, &due); err != nil {
				t.Fatal(err)
			}
			if rows != 0 || !due.Equal(f.at) {
				t.Fatal("stale result wrote or advanced work")
			}
		})
	}
}

func TestIntegrationAlertLeaseExpiresWhileWaiting(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"evaluation", "gap", "repeat"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			policy := f.policy(t, "Expiry")
			at := f.at
			if mode == "repeat" {
				first := f.claim(t, f.at)
				if _, err := f.runtime.commitAlertEvaluation(t.Context(), first, f.input(t, first, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) }); err != nil {
					t.Fatal(err)
				}
				at = at.Add(time.Minute)
			}
			if mode == "gap" {
				at = at.Add(25 * time.Hour)
			}
			claim := f.claim(t, at)
			input := f.input(t, claim, alert.Complete, true)
			locker := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(locker)
			tx, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			if _, err := dbgen.New(tx).LockAlertPolicy(t.Context(), mustPGUUID(policy)); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(at.Add(time.Second).UnixNano())
			readClock := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			done := make(chan error, 1)
			go func() {
				var err error
				switch mode {
				case "evaluation":
					_, err = f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, readClock)
				case "gap":
					_, err = f.runtime.recordAlertGap(t.Context(), claim, readClock, 24*time.Hour)
				case "repeat":
					err = f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, f.at, readClock)
				}
				done <- err
			}()
			waitForAgentQueryWait(t, f.conn, "%FROM public.almanac_alert_policies WHERE id=%FOR UPDATE%")
			clock.Store(at.Add(time.Minute + time.Second).UnixNano())
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, ErrAlertStale) {
				t.Fatalf("expired lease published after lock wait: %v", err)
			}
		})
	}
}

func TestIntegrationAlertAbsentGenerationFirstWriter(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	old, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := dbgen.New(f.conn).GetSli(t.Context(), mustPGUUID(f.sliID))
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	empty, err := CreateSli(t.Context(), f.f.databaseURL, CreateSliInput{Proof: proof, Name: "empty-alert-source", DisplayName: "Empty source", DatasourceID: measurement.DatasourceID.String(), MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "empty-sli", RequestID: "empty-sli", Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	_, objective, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "empty-alert-slo", DisplayName: "Empty SLO", ServiceID: old.ServiceID.String(), SliID: empty.ID, Target: "0.99", RollingWindowSeconds: 3600, IdempotencyKey: "empty-slo", RequestID: "empty-slo", Now: f.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	f.sliID, f.sloID = empty.ID, objective.ID
	f.policy(t, "Empty policy")
	claim := f.claim(t, f.at)
	input := f.input(t, claim, alert.Missing, false)
	// Hold a fixture-owned gate only after publication has taken its real
	// authority locks, then let the real ingestion lock query contend with it.
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION hold_alert_publication() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(7301); RETURN NEW; END$$; CREATE TRIGGER hold_alert_publication BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION hold_alert_publication()`); err != nil {
		t.Fatal(err)
	}
	gate := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(gate)
	gateTx, err := gate.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(gateTx)
	if _, err := gateTx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(7301)`); err != nil {
		t.Fatal(err)
	}
	publication := make(chan error, 1)
	go func() {
		_, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(time.Second) })
		publication <- err
	}()
	waitForAgentQueryWait(t, f.conn, "%INSERT INTO public.almanac_alert_evaluations%")
	writer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(writer)
	first := make(chan error, 1)
	go func() {
		tx, err := writer.Begin(t.Context())
		if err != nil {
			first <- err
			return
		}
		defer rollbackTransaction(tx)
		q := dbgen.New(tx)
		if _, err := q.LockSliMeasurementForObservation(t.Context(), dbgen.LockSliMeasurementForObservationParams{SliID: mustPGUUID(f.sliID), Revision: 1, IntervalStart: timestamp(empty.EffectiveFrom)}); err != nil {
			first <- err
			return
		}
		generation, err := q.AdvanceSliSourceGeneration(t.Context(), dbgen.AdvanceSliSourceGenerationParams{SliID: mustPGUUID(f.sliID), MeasurementRevision: 1, RecordedAt: timestamp(f.at.Add(2 * time.Second))})
		if err != nil {
			first <- err
			return
		}
		if generation != 1 {
			first <- errors.New("first generation must remain one")
			return
		}
		first <- tx.Commit(t.Context())
	}()
	waitForAgentQueryWait(t, f.conn, "%LockSliMeasurementForObservation%")
	if err := gateTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-publication; err != nil {
		t.Fatalf("generation-zero publication: %v", err)
	}
	if err := <-first; err != nil {
		t.Fatalf("first writer: %v", err)
	}
	// The receipt keeps the original unknown result, despite a later source.
	if replay, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(3 * time.Second) }); err != nil || replay.Occurrence != nil {
		t.Fatalf("empty receipt replay: %+v %v", replay, err)
	}
}

func TestIntegrationAlertGapAndRepeatedBoundaries(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Continuity")
	first := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), first, f.input(t, first, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	repeat := f.claim(t, f.at.Add(time.Minute))
	if err := f.runtime.recordAlertRepeatedCutoff(t.Context(), repeat, f.at, func() time.Time { return f.at.Add(time.Minute + time.Second) }); err != nil {
		t.Fatal(err)
	}
	// A full outage keeps the exact horizon boundary and records every omitted
	// scheduled boundary; the open occurrence is preserved without fake recovery.
	now := f.at.Add(25 * time.Hour)
	late := f.claim(t, now)
	recorded, err := f.runtime.recordAlertGap(t.Context(), late, func() time.Time { return now.Add(time.Second) }, 24*time.Hour)
	if err != nil || !recorded {
		t.Fatalf("catch-up gap: %v %v", recorded, err)
	}
	var due time.Time
	var occurrenceID string
	if err := f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at,open_occurrence_id::text FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&due, &occurrenceID); err != nil {
		t.Fatal(err)
	}
	// now includes one processing second: the first retained grid point is1h1m.
	if !due.Equal(f.at.Add(time.Hour+time.Minute)) || occurrenceID != opened.Occurrence.ID {
		t.Fatalf("lost continuity/state: due=%s occurrence=%s", due, occurrenceID)
	}
	rows, err := f.conn.Query(t.Context(), `SELECT kind,starts_at,ends_at FROM almanac_alert_ranges WHERE policy_id=$1 ORDER BY starts_at`, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	var starts, ends []time.Time
	for rows.Next() {
		var kind string
		var start, end time.Time
		if err := rows.Scan(&kind, &start, &end); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
		starts = append(starts, start)
		ends = append(ends, end)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] != "repeated_cutoff" || kinds[1] != "unreconstructed_gap" || !starts[0].Equal(f.at.Add(time.Minute)) || !ends[0].Equal(f.at.Add(2*time.Minute)) || !starts[1].Equal(ends[0]) || !ends[1].Equal(due) {
		t.Fatalf("range coverage: kinds=%v starts=%v ends=%v", kinds, starts, ends)
	}
}

func TestIntegrationAlertAcknowledgementRecoveryRace(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Ack race")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	claim = f.claim(t, f.at.Add(time.Minute))
	input := f.input(t, claim, alert.Complete, false)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := acknowledgeAlertTx(t.Context(), tx, alertAcknowledgement{ID: opened.Occurrence.ID, ExpectedVersion: opened.Version, ActorID: f.f.userID, RequestID: "race-ack", At: f.at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result alertCommitResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, func() time.Time { return f.at.Add(time.Minute + time.Second) })
		done <- outcome{result, err}
	}()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_alert_occurrences%FOR UPDATE%")
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.result.Occurrence.State != alert.Resolved || got.result.Occurrence.AcknowledgedBy != f.f.userID {
		t.Fatalf("recovery overwrote acknowledgement: %+v %v", got.result, got.err)
	}
	rejected, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(rejected)
	if _, err := acknowledgeAlertTx(t.Context(), rejected, alertAcknowledgement{ID: opened.Occurrence.ID, ExpectedVersion: opened.Version, ActorID: f.f.userID, RequestID: "old-ack", At: f.at.Add(2 * time.Minute)}); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("terminal stale acknowledgement accepted: %v", err)
	}
}

func TestIntegrationAlertImmutableHistory(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Immutable")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.conn.Exec(t.Context(), `UPDATE almanac_alert_occurrences SET payload=jsonb_set(payload,'{FiringSnapshot,SourceGeneration}','999') WHERE id=$1`, opened.Occurrence.ID)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
		t.Fatalf("original firing evidence rewritable: %v", err)
	}
	claim = f.claim(t, f.at.Add(time.Minute))
	closed, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, func() time.Time { return f.at.Add(time.Minute + time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.conn.Exec(t.Context(), `UPDATE almanac_alert_occurrences SET updated_at=updated_at+interval '1 second' WHERE id=$1`, closed.Occurrence.ID)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
		t.Fatalf("terminal history rewritable: %v", err)
	}
}

func TestIntegrationAlertDisableCancelsAndFences(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Disable")
	first := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), first, f.input(t, first, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.runtime.claimAlertWork(t.Context(), f.at.Add(3*time.Minute), f.at.Add(3*time.Minute), 10*time.Minute, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("pending claim: %+v %v", claims, err)
	}
	stale := claims[0]
	evidence := f.input(t, stale, alert.Complete, true)
	var closed alertStoredOccurrence
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		var err error
		closed, err = disableAlertPolicyTx(t.Context(), tx, alertDisableInput{PolicyID: policy, ExpectedVersion: 1, ActorID: f.f.userID, Reason: "Maintenance of policy", RequestID: "disable", EffectiveAt: f.at.Add(3 * time.Minute), Now: func() time.Time { return f.at.Add(4 * time.Minute) }})
		return err
	})
	if closed.Occurrence.ID != opened.Occurrence.ID || closed.Occurrence.State != alert.Disabled || !closed.Occurrence.ClosedAt.Equal(f.at.Add(3*time.Minute)) || !closed.Occurrence.UpdatedAt.Equal(f.at.Add(4*time.Minute)) {
		t.Fatalf("disabled lifecycle/clocks: %+v", closed)
	}
	var enabled, schedulable, hasOpen bool
	var lastCutoff time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT p.enabled,c.schedulable,c.open_occurrence_id IS NOT NULL,c.last_cutoff FROM almanac_alert_policies p JOIN almanac_alert_cursors c ON c.policy_id=p.id AND c.revision=p.evaluation_revision WHERE p.id=$1`, policy).Scan(&enabled, &schedulable, &hasOpen, &lastCutoff); err != nil {
		t.Fatal(err)
	}
	if enabled || schedulable || hasOpen || !lastCutoff.Equal(f.at) {
		t.Fatalf("disabled cursor misrepresents state: enabled=%v scheduled=%v open=%v cutoff=%s", enabled, schedulable, hasOpen, lastCutoff)
	}
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), stale, evidence, f.limits, func() time.Time { return f.at.Add(5 * time.Minute) }); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("disabled worker committed: %v", err)
	}
	if claims, err := f.runtime.claimAlertWork(t.Context(), f.at.Add(5*time.Minute), f.at.Add(5*time.Minute), time.Minute, 1); err != nil || len(claims) != 0 {
		t.Fatalf("disabled work reclaimed: %+v %v", claims, err)
	}
	var start, end time.Time
	var actor, reason, requestID string
	if err := f.conn.QueryRow(t.Context(), `SELECT starts_at,ends_at,actor_id::text,reason,request_id FROM almanac_alert_ranges WHERE policy_id=$1 AND kind='administrative_cancellation'`, policy).Scan(&start, &end, &actor, &reason, &requestID); err != nil {
		t.Fatal(err)
	}
	if !start.Equal(f.at.Add(time.Minute)) || !end.Equal(f.at.Add(4*time.Minute)) || actor != f.f.userID || reason != "Maintenance of policy" || requestID != "disable" {
		t.Fatalf("cancelled range=%s..%s actor=%s reason=%s", start, end, actor, reason)
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1`, policy).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fabricated recovery evaluation: %d %v", count, err)
	}
}

func TestIntegrationAlertSharedSloIndependence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	a := f.policy(t, "Team A")
	b := f.policy(t, "Team B")
	c := f.policyThreshold(t, "Higher threshold", "3")
	claims, err := f.runtime.claimAlertWork(t.Context(), f.at, f.at, time.Minute, 3)
	if err != nil || len(claims) != 3 {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	results := map[string]alertCommitResult{}
	for _, claim := range claims {
		result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
		if err != nil {
			t.Fatal(err)
		}
		results[claim.PolicyID] = result
	}
	if results[a].Occurrence == nil || results[b].Occurrence == nil || results[a].Occurrence.ID == results[b].Occurrence.ID || results[c].Occurrence != nil {
		t.Fatalf("policies sharing SLO conflated: %+v", results)
	}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		_, err := acknowledgeAlertTx(t.Context(), tx, alertAcknowledgement{ID: results[a].Occurrence.ID, ExpectedVersion: results[a].Version, ActorID: f.f.userID, RequestID: "team-a-ack", At: f.at.Add(2 * time.Second)})
		return err
	})
	row, err := dbgen.New(f.conn).LockAlertOccurrence(t.Context(), mustPGUUID(results[b].Occurrence.ID))
	if err != nil {
		t.Fatal(err)
	}
	other, err := decodeAlertOccurrence(row)
	if err != nil || other.Occurrence.State != alert.Firing || other.Occurrence.AcknowledgedBy != "" || other.Version != 1 {
		t.Fatalf("ack leaked across policies: %+v %v", other, err)
	}
}
