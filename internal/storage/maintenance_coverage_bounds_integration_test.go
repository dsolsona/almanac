//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type coverageQuery struct {
	sql  string
	args []any
}
type coverageTraceTx struct {
	pgx.Tx
	calls []coverageQuery
}

func (tx *coverageTraceTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.calls = append(tx.calls, coverageQuery{sql, args})
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func (tx *coverageTraceTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.calls = append(tx.calls, coverageQuery{sql, args})
	return tx.Tx.Query(ctx, sql, args...)
}

func TestIntegrationMaintenanceCoverageBoundsAndIdentity(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	b := compilerCapture(t, f, nil)
	window := slo.WindowInput{At: f.at, Duration: time.Hour, Cadence: time.Minute}
	invoke := func(t *testing.T, iso pgx.TxIsoLevel, sid, bid pgtype.UUID, revision int64, inputs []slo.WindowInput, totals []slo.WindowTotals) error {
		t.Helper()
		tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: iso})
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackTransaction(tx)
		_, err = readMaintenanceWindowCoverage(t.Context(), tx, sid, bid, revision, inputs, totals)
		return err
	}
	if err := invoke(t, pgx.RepeatableRead, b.SloID, b.ID, 0, []slo.WindowInput{window}, []slo.WindowTotals{{}}); !errors.Is(err, ErrMaintenanceCompilePending) {
		t.Fatalf("pending: %v", err)
	}
	compilerFinish(t, f, b.ID, 1)
	for _, test := range []struct {
		name     string
		iso      pgx.TxIsoLevel
		sid, bid pgtype.UUID
		revision int64
		inputs   []slo.WindowInput
		totals   []slo.WindowTotals
		want     error
	}{
		{"scope", pgx.RepeatableRead, mustPGUUID(f.ids[1]), b.ID, 0, []slo.WindowInput{window}, []slo.WindowTotals{{}}, ErrMaintenanceSnapshotChanged},
		{"revision", pgx.RepeatableRead, b.SloID, b.ID, 1, []slo.WindowInput{window}, []slo.WindowTotals{{}}, ErrMaintenanceSnapshotChanged},
		{"snapshot", pgx.ReadCommitted, b.SloID, b.ID, 0, []slo.WindowInput{window}, []slo.WindowTotals{{}}, ErrMaintenanceSnapshotChanged},
		{"missing", pgx.RepeatableRead, b.SloID, maintenanceCompilerID(), 0, []slo.WindowInput{window}, []slo.WindowTotals{{}}, ErrMaintenanceNotFound},
		{"length", pgx.RepeatableRead, b.SloID, b.ID, 0, []slo.WindowInput{window}, nil, ErrMaintenanceInvalid},
		{"cap", pgx.RepeatableRead, b.SloID, b.ID, 0, make([]slo.WindowInput, derivedWindowBatchLimit+1), make([]slo.WindowTotals, derivedWindowBatchLimit+1), ErrMaintenanceInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := invoke(t, test.iso, test.sid, test.bid, test.revision, test.inputs, test.totals); !errors.Is(err, test.want) {
				t.Fatalf("got%v want%v", err, test.want)
			}
		})
	}
	badGrid := window
	badGrid.Cadence = 30 * time.Second
	if err := invoke(t, pgx.RepeatableRead, b.SloID, b.ID, 0, []slo.WindowInput{badGrid}, []slo.WindowTotals{{}}); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("grid: %v", err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	traced := &coverageTraceTx{Tx: tx}
	empty, err := readMaintenanceWindowCoverage(t.Context(), traced, b.SloID, b.ID, 0, nil, nil)
	if err != nil || len(empty) != 0 || len(traced.calls) != 0 {
		t.Fatalf("empty batch: %v %v %d", empty, err, len(traced.calls))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readMaintenanceWindowCoverage(ctx, traced, b.SloID, b.ID, 0, []slo.WindowInput{window}, []slo.WindowTotals{{}}); !errors.Is(err, context.Canceled) || len(traced.calls) != 0 {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestIntegrationMaintenanceCoverageBoundedSeeks(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("seek")); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 1)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	traced := &coverageTraceTx{Tx: tx}
	inputs := make([]slo.WindowInput, derivedWindowBatchLimit)
	totals := make([]slo.WindowTotals, len(inputs))
	for i := range inputs {
		inputs[i] = slo.WindowInput{At: f.at.Add(3 * time.Hour), Duration: time.Hour, Cadence: time.Minute}
	}
	certs, err := readMaintenanceWindowCoverage(t.Context(), traced, b.SloID, b.ID, b.TargetRevision, inputs, totals)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 960 || len(traced.calls) != 3 {
		t.Fatalf("outputs%d queries%d", len(certs), len(traced.calls))
	}
	for _, call := range traced.calls[1:] {
		if len(call.args[0].([]pgtype.Timestamptz)) > 2 {
			t.Fatal("equal windows not deduplicated")
		}
	}
	// Inspect the actual generated query, not a separately handwritten equivalent.
	// Small fixture proves the index path exists; it does not claim fleet costing.
	if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	call := traced.calls[2]
	rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) "+call.sql, call.args...)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	t.Log(plan)
	if !strings.Contains(plan, "almanac_maintenance_coverage_pkey") || !strings.Contains(plan, "Limit") || strings.Contains(plan, "Seq Scan on almanac_maintenance_coverage") {
		t.Fatalf("unbounded plan: %s", plan)
	}
}

func TestIntegrationMaintenanceCoverageGenesis(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("genesis")
	input.Start = time.Unix(0, 0).UTC()
	input.End = input.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 1)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	window := slo.WindowInput{At: input.End, Duration: 30 * time.Minute, Cadence: time.Minute}
	got, err := readMaintenanceWindowCoverage(t.Context(), tx, b.SloID, b.ID, b.TargetRevision, []slo.WindowInput{window}, []slo.WindowTotals{{}})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].LatestEligibleEnd.Equal(input.Start) || !got[0].Points[0].At.Equal(input.Start.Add(-time.Minute)) || got[0].Points[0].ExcludedBefore != 0 {
		t.Fatalf("pre-genesis: %+v", got)
	}
}

func TestIntegrationMaintenanceCoveragePublicationSnapshot(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	b := compilerCapture(t, f, nil)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	window := slo.WindowInput{At: f.at, Duration: time.Hour, Cadence: time.Minute}
	read := func() error {
		_, err := readMaintenanceWindowCoverage(t.Context(), tx, b.SloID, b.ID, 0, []slo.WindowInput{window}, []slo.WindowTotals{{}})
		return err
	}
	if err := read(); !errors.Is(err, ErrMaintenanceCompilePending) {
		t.Fatal(err)
	}
	publisher := f
	copySource := *f.selfHealFixture
	publisher.selfHealFixture = &copySource
	publisher.conn = integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(publisher.conn)
	compilerFinish(t, publisher, b.ID, 1)
	if err := read(); !errors.Is(err, ErrMaintenanceCompilePending) {
		t.Fatalf("mixed publication snapshot: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := read(); err != nil {
		t.Fatalf("fresh published snapshot: %v", err)
	}
}

func TestIntegrationMaintenanceCoverageUnavailableHistory(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	setLegacyMaintenanceAnalysis(t, f.conn, true)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("missing-history")); err != nil {
		t.Fatal(err)
	}
	setLegacyMaintenanceAnalysis(t, f.conn, false)
	// Legitimate prior retention loss before any build pin cannot become an empty mask.
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=1`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	if p := compilerStep(t, f, b.ID, 1); p.Phase != "unavailable" {
		t.Fatalf("phase: %+v", p)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	_, err = readMaintenanceWindowCoverage(t.Context(), tx, b.SloID, b.ID, b.TargetRevision, []slo.WindowInput{{At: f.at, Duration: time.Hour, Cadence: time.Minute}}, []slo.WindowTotals{{}})
	if !errors.Is(err, ErrMaintenanceCompileUnavailable) {
		t.Fatalf("lost history accepted: %v", err)
	}
}
