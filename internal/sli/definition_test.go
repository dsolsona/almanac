package sli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSettledIntervalAndStablePhase(t *testing.T) {
	now := time.Date(2026, 10, 28, 13, 45, 20, 0, time.UTC)
	end := LatestSettledEnd(now, 60, 60)
	if !end.Equal(time.Date(2026, 10, 28, 13, 44, 0, 0, time.UTC)) {
		t.Fatalf("settled endpoint %s", end)
	}
	end = LatestSettledEnd(time.Unix(250, 0).UTC(), 17, 60)
	if end.Unix() != 187 {
		t.Fatalf("17-second endpoint %v", end)
	}
	seen := make(map[time.Duration]bool)
	for i := range 1000 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		phase := DispatchPhase(id, 60)
		if phase < 0 || phase >= time.Minute || phase != DispatchPhase(id, 60) {
			t.Fatalf("invalid phase %s", phase)
		}
		seen[phase] = true
	}
	if len(seen) < 900 {
		t.Fatalf("only %d distinct phases", len(seen))
	}
}

func TestDefinitionRequiresSupportedMeasurementAndConversion(t *testing.T) {
	definition := Definition{
		Name: "request-success", DisplayName: "Request success",
		DatasourceID:    "00000000-0000-4000-8000-000000000002",
		MeasurementKind: MeasurementKindEventCounts, ComponentMode: ComponentModeIntervalCount,
		GoodQuery:  "sum(increase(request_success_total[1m]))",
		TotalQuery: "sum(increase(request_total[1m]))",
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid interval-count SLI rejected: %v", err)
	}
	definition.MeasurementKind = "availability_ratio"
	if err := definition.Validate(); !errors.Is(err, ErrUnsupportedMeasurementKind) {
		t.Fatalf("unsupported measurement kind error = %v", err)
	}
	definition.MeasurementKind = MeasurementKindEventCounts
	definition.ComponentMode = "counter_increase"
	if err := definition.Validate(); !errors.Is(err, ErrUnsupportedComponentMode) {
		t.Fatalf("unsupported component mode error = %v", err)
	}
}

func TestSLINameUsesRFC1123LabelBoundaries(t *testing.T) {
	for _, name := range []string{"9ready", "request-success", "a", "a" + strings.Repeat("x", 61) + "9"} {
		if !ValidName(name) {
			t.Errorf("valid RFC1123 SLI name %q rejected", name)
		}
	}
	for _, name := range []string{"", "-leading", "trailing-", "Upper", "a" + strings.Repeat("x", 63)} {
		if ValidName(name) {
			t.Errorf("invalid RFC1123 SLI name %q accepted", name)
		}
	}
}

func TestObservationOutcomeRequiresExplicitCoverageAndValues(t *testing.T) {
	item := validObservationItem()
	item.Outcome = ExecutionOutcome{State: OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	item.Outcome = ExecutionOutcome{State: OutcomeCompleteEmpty}
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("explicit complete-empty coverage rejected: %v", err)
	}
	item.Outcome = ExecutionOutcome{State: OutcomeIncomplete, MissingComponents: []string{"good"}}
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("unknown component coverage rejected: %v", err)
	}
	item.Outcome = ExecutionOutcome{State: OutcomeFailed, ErrorCode: "query"}
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("bounded failed outcome rejected: %v", err)
	}
	for _, outcome := range []ExecutionOutcome{
		{State: OutcomeCompleteEmpty, GoodCount: "0", TotalCount: "0"},
		{State: OutcomeObserved, GoodCount: "101", TotalCount: "100"},
		{State: OutcomeFailed, ErrorCode: "query", GoodCount: "0", TotalCount: "0"},
		{State: OutcomeFailed, ErrorCode: "source_password: secret"},
		{State: OutcomeIncomplete},
	} {
		item.Outcome = outcome
		if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)); err == nil {
			t.Fatalf("invalid execution outcome accepted: %+v", outcome)
		}
	}
}

func TestObservationIntervalAndRetainedExecutionBoundsAreIndependent(t *testing.T) {
	item := validObservationItem()
	item.Outcome = ExecutionOutcome{State: OutcomeObserved, GoodCount: "1", TotalCount: "1"}
	item.CreatedAt = time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)
	item.IntervalStart = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 1, 0, time.UTC)); err != nil {
		t.Fatalf("valid prior interval under later execution grant rejected: %v", err)
	}
	item.IntervalStart = item.IntervalStart.Add(time.Second)
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 1, 0, time.UTC)); err == nil {
		t.Fatal("misaligned interval was accepted")
	}
	item = validObservationItem()
	item.Outcome = ExecutionOutcome{State: OutcomeObserved, GoodCount: "1", TotalCount: "1"}
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 15, 0, time.UTC), time.Minute, time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC)); err == nil {
		t.Fatal("interval before activation boundary was accepted")
	}
	item = validObservationItem()
	item.Outcome = ExecutionOutcome{State: OutcomeObserved, GoodCount: "1", TotalCount: "1"}
	if err := item.Validate(15, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Minute, item.CreatedAt.Add(8*24*time.Hour)); err == nil {
		t.Fatal("expired replay was accepted")
	}
}

func TestObservationRetentionUsesIntervalStartWithoutProration(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	item := validObservationItem()
	item.Outcome = ExecutionOutcome{State: OutcomeObserved, GoodCount: "1", TotalCount: "1"}
	item.CreatedAt = now
	item.IntervalStart = now.Add(-ObservationRetention)
	item.IntervalEnd = item.IntervalStart.Add(15 * time.Second)
	if err := item.Validate(15, item.IntervalStart, time.Minute, now); err != nil {
		t.Fatalf("boundary interval rejected: %v", err)
	}
	item.IntervalStart = item.IntervalStart.Add(-15 * time.Second)
	item.IntervalEnd = item.IntervalEnd.Add(-15 * time.Second)
	if err := item.Validate(15, item.IntervalStart, time.Minute, now); err == nil {
		t.Fatal("interval before retention cutoff accepted")
	}
}

func validObservationItem() ObservationItem {
	return ObservationItem{
		DeliveryID: "00000000-0000-4000-8000-000000000020", ExecutionID: "00000000-0000-4000-8000-000000000021",
		CreatedAt: time.Date(2026, 10, 27, 12, 2, 0, 0, time.UTC), LeaseID: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ConfigurationRevision: "2", SessionGeneration: "1", SLIID: "00000000-0000-4000-8000-000000000010",
		MeasurementRevision: "1", IntervalStart: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		IntervalEnd: time.Date(2026, 10, 27, 12, 0, 15, 0, time.UTC),
	}
}

func TestValidateCountsPreservesExactDecimalSemantics(t *testing.T) {
	for _, values := range [][2]string{{"99", "100"}, {"0", "0"}, {"0.000000000001", "0.000000000002"}, {"99999999999999999999999999.999999999999", "99999999999999999999999999.999999999999"}} {
		good, total, err := ValidateCounts(values[0], values[1])
		if err != nil {
			t.Fatalf("ValidateCounts(%q,%q): %v", values[0], values[1], err)
		}
		if good != values[0] || total != values[1] {
			t.Fatalf("ValidateCounts(%q,%q) = %q,%q; exact decimal text changed", values[0], values[1], good, total)
		}
	}
	for _, values := range [][2]string{{"101", "100"}, {"-1", "0"}, {"NaN", "100"}, {"1.0000000000001", "2"}, {"100000000000000000000000000", "100000000000000000000000000"}, {"1e2", "100"}, {"1", "0.9"}} {
		if _, _, err := ValidateCounts(values[0], values[1]); err == nil {
			t.Fatalf("ValidateCounts(%q,%q) unexpectedly succeeded", values[0], values[1])
		}
	}
}

func TestIntervalCountModeRejectsFractionalEstimates(t *testing.T) {
	for _, values := range [][2]string{{"1.5", "2"}, {"0", "0.0001"}} {
		if _, _, err := ValidateIntervalCounts(values[0], values[1]); err == nil {
			t.Errorf("ValidateIntervalCounts(%q,%q) accepted an undeclared estimate", values[0], values[1])
		}
	}
	if good, total, err := ValidateIntervalCounts("1.000", "2"); err != nil || good != "1.000" || total != "2" {
		t.Fatalf("integral decimal interval counts = %q,%q,%v", good, total, err)
	}
}
