//go:build integration

package storage

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type retainedReuseFixture struct {
	start, end time.Time
	maintenanceFixture
	hour  maintenanceHourInput
	base  slo.WindowInput
	lanes extremaLanes
	row   maintenanceRetainedRowAuthority
}

func newRetainedReuseFixture(t *testing.T, legacy bool) retainedReuseFixture {
	t.Helper()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	if legacy {
		in.Requests = in.Requests[:1]
		in.Requests[0].Windows[0].At = h.HourStart.Add(2 * time.Hour)
	} else {
		in.Requests = in.Requests[1:]
	}
	in.Summary, in.RetainFacts = true, true
	result, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	base := in.Requests[0].Windows[0]
	lane, err := makeMaintenanceExtremaLane(in, result, 0, []int{0}, base.At.Add(-base.Cadence), base.At, "budget", "1")
	if err != nil || len(lane.Marks) != 1 {
		t.Fatalf("fixture lane %+v %v", lane, err)
	}
	if legacy {
		lane.Marks[0].Maintenance = nil
	}
	mark := lane.Marks[0]
	return retainedReuseFixture{maintenanceFixture: f, hour: h, base: base, lanes: extremaLanes{Window: &lane, Burn1h: &lane, Compliance: &extremaCompliance{DayEnd: base.At, Mark: mark}}, row: maintenanceRetainedRowAuthority{Scope: preparationScope(f), SourceGeneration: result.SourceGeneration, RequestedRevision: in.Requests[0].RequestedRevision}}
}
func (f retainedReuseFixture) reuse(t *testing.T, lanes extremaLanes, requested int64, limit int) (maintenanceRetainedReuseResult, error) {
	t.Helper()
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	recorded := &evaluationQueryTx{Tx: tx}
	start, end := f.start, f.end
	if start.IsZero() {
		start, end = f.base.At.Add(-f.base.Cadence), f.base.At
	}
	got, err := reconcileMaintenanceRetainedLanes(t.Context(), recorded, f.base, start, end, lanes, f.row, requested, limit)
	batches := 0
	for _, call := range recorded.calls {
		if strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationProofs ") {
			batches++
		}
	}
	if batches > 1 {
		t.Fatalf("proof N+1: %d batches", batches)
	}
	return got, err
}
func TestIntegrationMaintenanceRetainedReuseFuture(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, false)
	m := f.input("future-retained")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(f.lanes)
	got, err := f.reuse(t, f.lanes, 2, 1)
	if err != nil || len(got.Preparation) != 0 || got.Lanes.Window == nil {
		t.Fatalf("future edit blocked retained windows: %+v %v", got, err)
	}
	for _, mark := range []extremaMark{got.Lanes.Window.Marks[0], got.Lanes.Burn1h.Marks[0], got.Lanes.Compliance.Mark} {
		m := mark.Maintenance
		if m == nil || m.RequestedRevision != 2 || m.ComputedRevision != 1 || m.BuildID != f.hour.BuildID.String() || m.SourceGeneration != f.row.SourceGeneration || m.Proof == nil || m.Proof.Kind != "indexed_absence" || !m.Proof.Start.Equal(f.base.At.Add(-f.base.Duration)) || !m.Proof.End.Equal(f.base.At) {
			t.Fatalf("relabelled or bucket proof: %+v", m)
		}
		value, e := mark.result(t.Context(), f.base)
		want, e2 := f.lanes.Window.Marks[0].result(t.Context(), f.base)
		if e != nil || e2 != nil || !reflect.DeepEqual(value, want) {
			t.Fatalf("replay changed: %+v %v / %+v %v", value, e, want, e2)
		}
	}
	got.Lanes.Window.Marks[0].Maintenance.Coverage.Points[0].ExcludedBefore = 999
	got.Lanes.Window.Marks[0].Maintenance.Proof.Kind = "changed"
	*got.Lanes.Window.Marks[0].FirstObservation = time.Time{}
	if got.Lanes.Burn1h.Marks[0].Maintenance.Coverage.Points[0].ExcludedBefore == 999 {
		t.Fatal("returned lanes alias each other")
	}
	after, _ := json.Marshal(f.lanes)
	if !bytes.Equal(before, after) {
		t.Fatal("reuse mutated original pinned generation")
	}
}
func TestIntegrationMaintenanceRetainedReuseCanonicalAndSame(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	same, err := f.reuse(t, f.lanes, 0, 1)
	if err != nil || same.Lanes.Window.Marks[0].Maintenance != nil {
		t.Fatalf("legacy same authority: %+v %v", same, err)
	}
	got, err := f.reuse(t, f.lanes, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Lanes.Window.Marks[0].Maintenance
	if m == nil || m.ComputedRevision != 0 || m.RequestedRevision != 1 || m.BuildID != "" || m.Coverage != nil || m.Proof == nil || m.Proof.Kind != "indexed_absence" {
		t.Fatalf("canonical identity lost: %+v", m)
	}
}
func TestIntegrationMaintenanceRetainedReuseChangedAndAuthority(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, false)
	m := f.input("changed-retained")
	m.Start = f.hour.HourStart.Add(30 * time.Minute)
	m.End = m.Start.Add(time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, f.maintenanceFixture, nil)
	compilerFinish(t, f.maintenanceFixture, build.ID, 10)
	got, err := f.reuse(t, f.lanes, 2, 1)
	if !errors.Is(err, ErrDerivedPending) || got.Lanes.Window != nil {
		t.Fatalf("changed population became usable: %+v %v", got, err)
	}
	var mask, comparison bool
	for _, p := range got.Preparation {
		mask = mask || p.Kind == "mask"
		comparison = comparison || p.Kind == "comparison" && p.Comparison.FirstBuild == f.hour.BuildID && p.Comparison.SecondBuild == build.ID
	}
	if mask || !comparison || !got.NeedsCalculation {
		t.Fatalf("missing exact recalculation/comparison request: %+v", got.Preparation)
	}
	var c dbgen.AlmanacMaintenanceComparison
	compilerTx(t, f.maintenanceFixture, func(tx pgx.Tx) error {
		var err error
		c, err = prepareMaintenanceComparison(t.Context(), tx, f.hour.BuildID, build.ID, f.now)
		return err
	})
	compilerTx(t, f.maintenanceFixture, func(tx pgx.Tx) error {
		var err error
		c, err = advanceMaintenanceComparison(t.Context(), tx, c.ID, 10, f.now)
		return err
	})
	if c.State != "different" {
		t.Fatalf("changed union fixture: %s", c.State)
	}
	got, err = f.reuse(t, f.lanes, 2, 1)
	if !errors.Is(err, ErrDerivedPending) || !got.NeedsCalculation || len(got.Preparation) != 0 || got.Lanes.Window != nil {
		t.Fatalf("terminal different union never reaches exact calculation: %+v %v", got, err)
	}
	f.lanes.Window.Marks[0].Maintenance.Scope.DefinitionRevision++
	if got, err = f.reuse(t, f.lanes, 2, 1); err == nil || got.Lanes.Window != nil || len(got.Preparation) != 0 {
		t.Fatalf("forged original scope accepted: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceRetainedReuseNoEligibleAndTerminal(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, false)
	m := f.input("future-empty")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f.maintenanceFixture, f.hour)
	in.Requests = in.Requests[1:]
	in.Requests[0].Windows[0].At = f.historyStart
	in.Summary, in.RetainFacts = true, true
	result, err := evaluateFixtureMaintenance(t, f.maintenanceFixture, in)
	if err != nil {
		t.Fatal(err)
	}
	f.base = in.Requests[0].Windows[0]
	lane, err := makeMaintenanceExtremaLane(in, result, 0, []int{0}, f.base.At.Add(-time.Hour), f.base.At, "budget", "1")
	if err != nil || lane.Marks[0].Maintenance != nil {
		t.Fatalf("actual no-eligible fixture: %+v %v", lane, err)
	}
	got, err := f.reuse(t, extremaLanes{Window: &lane}, 2, 1)
	if err != nil || got.Lanes.Window.Marks[0].Maintenance != nil || len(got.Preparation) != 0 {
		t.Fatalf("no-eligible mark acquired invented provenance: %+v %v", got, err)
	}
	// A saved terminal gap is valid at its original R, not evidence that a later
	// maintenance revision is also terminal.
	in.Requests[0].Windows[0].At = f.hour.HourStart.Add(time.Hour)
	f.base = in.Requests[0].Windows[0]
	result.Masks[0].Unavailable[0] = "maintenance_history_unavailable"
	result.Masks[0].Windows[0] = slo.WindowResult{}
	lane, err = makeMaintenanceExtremaLane(in, result, 0, []int{0}, f.base.At.Add(-time.Hour), f.base.At, "budget", "1")
	if err != nil {
		t.Fatal(err)
	}
	same, err := f.reuse(t, extremaLanes{Window: &lane}, 1, 1)
	if err != nil || same.Lanes.Window.Marks[0].Maintenance.UnavailableReason != "maintenance_history_unavailable" {
		t.Fatalf("same-R gap changed: %+v %v", same, err)
	}
	got, err = f.reuse(t, extremaLanes{Window: &lane}, 2, 1)
	if !errors.Is(err, ErrDerivedPending) || got.Lanes.Window != nil || len(got.Preparation) != 1 || got.Preparation[0].Kind != "mask" || got.Preparation[0].RequestedRevision != 2 {
		t.Fatalf("old gap relabelled terminal: %+v %v", got, err)
	}
	build := compilerCapture(t, f.maintenanceFixture, nil)
	compilerFinish(t, f.maintenanceFixture, build.ID, 10)
	got, err = f.reuse(t, extremaLanes{Window: &lane}, 2, 1)
	if !errors.Is(err, ErrDerivedPending) || !got.NeedsCalculation || len(got.Preparation) != 0 || got.Lanes.Window != nil {
		t.Fatalf("completed target mask blocks exact gap recalculation: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceRetainedReuseBudgetBeforeReads(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	in := evaluationFixtureInput(f.maintenanceFixture, f.hour)
	in.Requests = in.Requests[:1]
	in.Requests[0].Windows[0].At = f.base.At
	in.Requests[0].Windows[0].Duration = 2 * time.Hour
	result, err := evaluateFixtureMaintenance(t, f.maintenanceFixture, in)
	if err != nil {
		t.Fatal(err)
	}
	f.base = in.Requests[0].Windows[0]
	lane, err := makeExtremaSummaryLane(result.Masks[0].Windows, f.base.At.Add(-2*time.Hour), f.base.At, f.base.Target, "budget", "1")
	if err != nil {
		t.Fatal(err)
	}
	lanes := extremaLanes{Window: &lane, Burn1h: f.lanes.Burn1h}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	recorded := &evaluationQueryTx{Tx: tx}
	got, err := reconcileMaintenanceRetainedLanes(t.Context(), recorded, f.base, f.base.At.Add(-f.base.Cadence), f.base.At, lanes, f.row, 1, 1)
	if !errors.Is(err, ErrOperationalResource) || len(recorded.calls) != 0 || got.Lanes.Window != nil || len(got.Preparation) != 0 {
		t.Fatalf("proof budget spent before admission: calls=%d %+v %v", len(recorded.calls), got, err)
	}
}

func TestIntegrationMaintenanceRetainedReuseCompatibilityPending(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, false)
	setLegacyMaintenanceAnalysis(t, f.conn, true)
	m := f.input("legacy-future-reuse")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reuse(t, f.lanes, 1, 1); err != nil {
		t.Fatalf("same-R required index catchup: %v", err)
	}
	got, err := f.reuse(t, f.lanes, 2, 1)
	if !errors.Is(err, ErrDerivedPending) || !errors.Is(err, ErrMaintenanceCompilePending) || got.Lanes.Window != nil {
		t.Fatalf("unproved history became usable: %+v %v", got, err)
	}
	var mask, index bool
	for _, p := range got.Preparation {
		mask = mask || p.Kind == "mask"
		index = index || p.Kind == "compatibility"
	}
	if !mask || !index {
		t.Fatalf("pending proof lost preparation: %+v", got.Preparation)
	}
}

func TestIntegrationMaintenanceRetainedReuseEqualUnion(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, false)
	m := f.input("nested-reuse")
	m.Start = f.hour.HourStart.Add(12 * time.Minute)
	m.End = f.hour.HourStart.Add(15 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f.maintenanceFixture, nil)
	compilerFinish(t, f.maintenanceFixture, b.ID, 10)
	if _, err := f.reuse(t, f.lanes, 2, 1); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("conservative changed input accepted without union proof: %v", err)
	}
	var c dbgen.AlmanacMaintenanceComparison
	compilerTx(t, f.maintenanceFixture, func(tx pgx.Tx) error {
		var e error
		c, e = prepareMaintenanceComparison(t.Context(), tx, f.hour.BuildID, b.ID, f.now)
		return e
	})
	compilerTx(t, f.maintenanceFixture, func(tx pgx.Tx) error {
		var e error
		c, e = advanceMaintenanceComparison(t.Context(), tx, c.ID, 10, f.now)
		return e
	})
	if c.State != "equal" {
		t.Fatalf("comparison: %s", c.State)
	}
	got, err := f.reuse(t, f.lanes, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	m2 := got.Lanes.Window.Marks[0].Maintenance
	if m2.ComputedRevision != 1 || m2.BuildID != f.hour.BuildID.String() || m2.Proof.Kind != "equal_union" || m2.Proof.ComparisonID != c.ID.String() {
		t.Fatalf("direct comparison identity lost: %+v", m2)
	}
}

func TestIntegrationMaintenanceRetainedReuseSelectionEnvelope(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	in := evaluationFixtureInput(f.maintenanceFixture, f.hour)
	in.Requests = in.Requests[:1]
	in.Requests[0].Windows = nil
	var indices []int
	start := f.hour.HourStart.Add(time.Hour)
	for i := range 60 {
		w := f.base
		w.Duration = time.Minute
		w.At = start.Add(time.Duration(i+1) * time.Minute)
		in.Requests[0].Windows = append(in.Requests[0].Windows, w)
		indices = append(indices, i)
	}
	in.Summary = true
	values, err := evaluateFixtureMaintenance(t, f.maintenanceFixture, in)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := makeMaintenanceExtremaLane(in, values, 0, indices, start, start.Add(time.Hour), "attainment", "1")
	if err != nil {
		t.Fatal(err)
	}
	changed := start.Add(20 * time.Minute)
	for _, mark := range lane.Marks {
		if !mark.At.Before(changed) && !mark.At.After(changed.Add(time.Minute)) {
			t.Fatal("fixture retained the interior endpoint intended to be discarded")
		}
	}
	m := f.input("discarded-endpoint")
	m.Start = changed
	m.End = changed.Add(time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	f.start, f.end = start, start.Add(time.Hour)
	got, err := f.reuse(t, extremaLanes{Attainment: &lane}, 2, 8)
	if !errors.Is(err, ErrDerivedPending) || got.Lanes.Attainment != nil {
		t.Fatalf("unchanged selected points hid newly excluded interior endpoint: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceRetainedReuseBatch(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	in := evaluationFixtureInput(f.maintenanceFixture, f.hour)
	in.Requests = in.Requests[:1]
	values, err := evaluateFixtureMaintenance(t, f.maintenanceFixture, in)
	if err != nil {
		t.Fatal(err)
	}
	oldBase := in.Requests[0].Windows[0]
	changedLane, err := makeExtremaSummaryLane(values.Masks[0].Windows, oldBase.At.Add(-oldBase.Cadence), oldBase.At, oldBase.Target, "budget", "1")
	if err != nil {
		t.Fatal(err)
	}
	ready := maintenanceRetainedReuseInput{Base: f.base, Lanes: f.lanes, Row: f.row, Start: f.base.At.Add(-f.base.Cadence), End: f.base.At}
	inputs := []maintenanceRetainedReuseInput{{Base: oldBase, Lanes: extremaLanes{Window: &changedLane}, Row: f.row, Start: oldBase.At.Add(-oldBase.Cadence), End: oldBase.At}, ready, ready}
	before, _ := json.Marshal(inputs)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	recorded := &evaluationQueryTx{Tx: tx}
	got, err := reconcileMaintenanceRetainedBatch(t.Context(), recorded, inputs, 1, 2)
	if !errors.Is(err, ErrDerivedPending) || len(got) != 3 || got[0].Lanes.Window != nil || len(got[0].Preparation) == 0 || got[1].Lanes.Window == nil || got[2].Lanes.Window == nil || len(got[1].Preparation) != 0 || len(got[2].Preparation) != 0 {
		t.Fatalf("changed row hid independent ready rows: %+v %v", got, err)
	}
	queries := 0
	for _, call := range recorded.calls {
		if strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationProofs ") {
			queries++
		}
	}
	if queries != 1 {
		t.Fatalf("retained batch ran %d proof queries", queries)
	}
	after, _ := json.Marshal(inputs)
	if !bytes.Equal(before, after) {
		t.Fatal("batch rewrote pinned rows")
	}
	recorded.calls = nil
	got, err = reconcileMaintenanceRetainedBatch(t.Context(), recorded, inputs, 1, 1)
	if !errors.Is(err, ErrOperationalResource) || got != nil || len(recorded.calls) != 0 {
		t.Fatalf("cohort over budget returned partial rows/read work: %+v %v calls=%d", got, err, len(recorded.calls))
	}
}

func TestIntegrationMaintenanceRetainedReuseCanonicalBuildForms(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	zero := int64(0)
	genesis := compilerCapture(t, f.maintenanceFixture, &zero)
	compilerFinish(t, f.maintenanceFixture, genesis.ID, 10)
	burn := *f.lanes.Burn1h
	burn.Marks = slices.Clone(burn.Marks)
	burn.Marks[0].Maintenance = &retainedMaintenanceInputs{Version: 1, Scope: f.row.Scope, SourceGeneration: f.row.SourceGeneration, BuildID: genesis.ID.String()}
	f.lanes.Burn1h = &burn
	got, err := f.reuse(t, f.lanes, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lanes.Window.Marks[0].Maintenance.BuildID != "" || got.Lanes.Burn1h.Marks[0].Maintenance.BuildID != genesis.ID.String() {
		t.Fatal("canonical implicit/explicit build identity was relabelled")
	}
}

func TestIntegrationMaintenanceRetainedReuseOriginalHourKeys(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, false)
	m := f.input("future-hour-reuse")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, f.maintenanceFixture, nil)
	compilerFinish(t, f.maintenanceFixture, build.ID, 10)
	in := evaluationFixtureInput(f.maintenanceFixture, f.hour)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	in.Summary, in.RetainFacts = true, true
	calculated, err := evaluateFixtureMaintenance(t, f.maintenanceFixture, in)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := makeMaintenanceExtremaLane(in, calculated, 0, []int{0}, f.base.At.Add(-f.base.Cadence), f.base.At, "budget", "1")
	if err != nil || len(lane.Marks[0].Maintenance.ReusedHours) != 1 {
		t.Fatalf("fixture did not reuse original physical hour: %+v %v", lane, err)
	}
	old := lane.Marks[0].Maintenance.ReusedHours[0]
	f.row.RequestedRevision = 2
	m = f.input("later-hour-reuse")
	m.Start = f.at.Add(3 * time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	got, err := f.reuse(t, extremaLanes{Window: &lane}, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	saved := got.Lanes.Window.Marks[0].Maintenance
	if saved.ComputedRevision != 2 || saved.BuildID != build.ID.String() || saved.RequestedRevision != 3 || len(saved.ReusedHours) != 1 || !reflect.DeepEqual(saved.ReusedHours[0], old) {
		t.Fatalf("original scoped hour/proof relabelled: %+v", saved)
	}
	got.Lanes.Window.Marks[0].Maintenance.ReusedHours[0].Proof.Kind = "changed"
	if lane.Marks[0].Maintenance.ReusedHours[0].Proof.Kind == "changed" {
		t.Fatal("reused-hour slice aliases source")
	}
}

func TestIntegrationMaintenanceRetainedReuseSnapshotFailure(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	input := maintenanceRetainedReuseInput{Base: f.base, Lanes: f.lanes, Row: f.row, Start: f.base.At.Add(-f.base.Cadence), End: f.base.At}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	got, err := reconcileMaintenanceRetainedBatch(t.Context(), tx, []maintenanceRetainedReuseInput{input}, 1, 1)
	if !errors.Is(err, ErrMaintenanceSnapshotChanged) || got != nil {
		t.Fatalf("non-repeatable snapshot accepted: %+v %v", got, err)
	}
	rollbackTransaction(tx)
	got, err = reconcileMaintenanceRetainedBatch(t.Context(), tx, []maintenanceRetainedReuseInput{input}, 1, 1)
	if !errors.Is(err, pgx.ErrTxClosed) || got != nil {
		t.Fatalf("database failure leaked usable rows: %+v %v", got, err)
	}
}

func progressRetainedReuseOne(t *testing.T, f *retainedReuseFixture, requested int64) (maintenanceRetainedReuseResult, []string) {
	t.Helper()
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var prefixes []string
	for range 8 {
		got, err := f.reuse(t, f.lanes, requested, 8)
		if err == nil {
			return got, prefixes
		}
		if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) == 0 {
			t.Fatalf("no executable reuse prerequisite: %+v %v", got, err)
		}
		prefixes = append(prefixes, got.Preparation[0].Kind)
		compilerTx(t, f.maintenanceFixture, func(tx pgx.Tx) error {
			prepared, e := prepareMaintenanceEnqueue(t.Context(), tx, f.row.Scope, got.Preparation, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
			if e != nil {
				return e
			}
			if len(prepared.jobs) > 1 || len(prepared.hours) > 1 {
				t.Fatal("one-request bound escaped")
			}
			return enqueueMaintenancePreparation(t.Context(), tx, prepared, maintenancePreparationLive, f.now)
		})
		options := preparationOptions(f.maintenanceFixture)
		options.MaxLive = 1
		options.MaxHistorical = 0
		options.Concurrency = 1
		if _, err := runtime.runMaintenancePreparationTurn(t.Context(), f.now, options, f.clock); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Second)
	}
	t.Fatalf("finite prerequisites never completed with MaxRequests=1: %v", prefixes)
	return maintenanceRetainedReuseResult{}, prefixes
}

func TestIntegrationMaintenanceRetainedReuseBoundedQueueProgress(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"comparison", "compatibility"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newRetainedReuseFixture(t, false)
			m := f.input("bounded-reuse")
			if kind == "comparison" {
				m.Start = f.hour.HourStart.Add(12 * time.Minute)
				m.End = f.hour.HourStart.Add(15 * time.Minute)
			} else {
				setLegacyMaintenanceAnalysis(t, f.conn, true)
			}
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
				t.Fatal(err)
			}
			b := compilerCapture(t, f.maintenanceFixture, nil)
			compilerFinish(t, f.maintenanceFixture, b.ID, 10)
			got, prefixes := progressRetainedReuseOne(t, &f, 2)
			if got.Lanes.Window == nil || got.NeedsCalculation || len(prefixes) == 0 {
				t.Fatal("fixture did not progress through real durable work")
			}
			for _, p := range prefixes {
				if p != kind {
					t.Fatalf("complete mask starved %s: %v", kind, prefixes)
				}
			}
		})
	}
}

func TestIntegrationMaintenanceRetainedReuseMissingGenesisProgress(t *testing.T) {
	t.Parallel()
	f := newRetainedReuseFixture(t, true)
	var id string
	if err := f.conn.QueryRow(t.Context(), `SELECT id::text FROM almanac_maintenance_exclusions`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := WithdrawMaintenance(t.Context(), f.f.databaseURL, MaintenanceCommandInput{Proof: f.proof, ID: id, Revision: 1, Reason: "Historical correction", IdempotencyKey: "withdraw-genesis", RequestID: "withdraw-genesis", Now: f.clock}); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f.maintenanceFixture, nil)
	compilerFinish(t, f.maintenanceFixture, b.ID, 10)
	in := evaluationFixtureInput(f.maintenanceFixture, f.hour)
	in.Requests = in.Requests[:1]
	values, err := evaluateFixtureMaintenance(t, f.maintenanceFixture, in)
	if err != nil {
		t.Fatal(err)
	}
	f.base = in.Requests[0].Windows[0]
	lane, err := makeExtremaSummaryLane(values.Masks[0].Windows, f.base.At.Add(-f.base.Cadence), f.base.At, f.base.Target, "budget", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.lanes = extremaLanes{Window: &lane}
	var genesis int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_builds WHERE target_revision=0`).Scan(&genesis); err != nil || genesis != 0 {
		t.Fatalf("fixture already has genesis: %d %v", genesis, err)
	}
	got, prefixes := progressRetainedReuseOne(t, &f, 2)
	saved := got.Lanes.Window.Marks[0].Maintenance
	if saved == nil || saved.ComputedRevision != 0 || saved.BuildID != "" || saved.RequestedRevision != 2 || saved.Proof.Kind != "equal_union" || got.NeedsCalculation || prefixes[0] != "mask" || !slices.Contains(prefixes, "comparison") {
		t.Fatalf("empty union failed canonical provenance: %+v prefixes=%v", saved, prefixes)
	}
}
