package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestOperationalPeriodsUseServerBoundsAndPreserveExplicitRequests(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 34, 56, 0, time.UTC)
	cadence := 7 * time.Minute
	end := floorSliBoundary(now, cadence)
	for _, test := range []struct {
		period string
		window int64
		days   int
	}{
		{"7d", 0, 7}, {"90d", 0, 90}, {"1y", 0, 365},
		{"window", 60, 1}, {"window", 28*86400 + 43199, 28},
		{"window", 28*86400 + 43200, 29}, {"window", 400 * 86400, 365},
	} {
		t.Run(test.period+"/"+time.Duration(test.window*int64(time.Second)).String(), func(t *testing.T) {
			input := OperationalDetailInput{Period: test.period, Now: func() time.Time { return now }}
			got, err := resolveOperationalBounds(input, cadence, test.window)
			start := ceilSliBoundary(end.Add(-time.Duration(test.days)*24*time.Hour), cadence)
			if err != nil || !got.requestedAt.Equal(now) || !got.end.Equal(end) || !got.start.Equal(start) || got.end.Sub(got.start) > 365*24*time.Hour {
				t.Fatalf("period %q: %+v, %v", test.period, got, err)
			}
			input.Period, input.Start, input.End = "", &start, &end
			if explicit, err := resolveOperationalBounds(input, cadence, test.window); err != nil || explicit != got {
				t.Fatalf("explicit bounds differ: %+v, %v", explicit, err)
			}
		})
	}
	base := OperationalDetailInput{Now: func() time.Time { return now }}
	legacy, err := resolveOperationalBounds(base, cadence, 0)
	if err != nil || !legacy.start.Equal(floorSliBoundary(end.Add(-7*24*time.Hour), cadence)) {
		t.Fatalf("legacy default changed: %+v, %v", legacy, err)
	}
	for _, invalid := range []OperationalDetailInput{
		{Now: base.Now, Period: "invalid"}, {Now: base.Now, Period: "window"},
		{Now: base.Now, Period: "7d", Start: &end},
		{Now: base.Now, Period: "7d", Start: &legacy.start, End: &end},
		{Now: base.Now, Period: "7d", At: new(now.Add(time.Second))},
	} {
		if _, err := resolveOperationalBounds(invalid, cadence, 0); err == nil {
			t.Fatalf("invalid selector accepted: %+v", invalid)
		}
	}
}

func TestDerivedFailureCodesDoNotExposeDatabaseDiagnostics(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{&pgconn.PgError{Code: "54000", Message: "private SQL and credential text"}, "derived_cache_limit"},
		{ErrOperationalRangeDense, "history_range_too_dense"},
		{ErrOperationalResource, "derived_resource_limit"},
		{errors.New("private connection URL"), "history_preparation_failed"},
	} {
		if got := derivedFailureReason(test.err, "history_preparation_failed"); got != test.want {
			t.Fatalf("failure code=%q want %q", got, test.want)
		}
	}
}

func TestPreparedHistoryRejectsCorruptProvenance(t *testing.T) {
	end := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	start := end.Add(-7 * 24 * time.Hour)
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	generation := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	key := dbgen.FindPreparedSloHistoryParams{SloID: id, DefinitionRevision: 3, MeasurementRevision: 4, SourceGeneration: 5,
		EvaluatedThrough: timestamp(end), RangeStart: timestamp(start), RangeEnd: timestamp(end), BurnWindowSeconds: 3600, Kind: "detail"}
	valid := func() api.SloOperationalDetail {
		return api.SloOperationalDetail{Objective: api.Slo{Id: id.Bytes, DefinitionRevision: "3"},
			EffectiveAt: end, CalculationVersion: operationalCalculationVersion, Urgency: "healthy",
			Range:      api.SloOperationalRange{RequestedStart: start, RequestedEnd: end},
			BurnSeries: api.SloOperationalSeries{BurnWindowSeconds: new(api.SloOperationalSeriesBurnWindowSeconds(3600))},
			Generation: &api.DerivedGeneration{GenerationId: generation.Bytes, DefinitionRevision: "3", MeasurementRevision: "4", SourceGeneration: "5",
				CalculationVersion: operationalCalculationVersion, EvaluatedThrough: end}}
	}
	if !preparedHistoryMatches(valid(), key, generation) {
		t.Fatal("matching prepared history was rejected")
	}
	for name, corrupt := range map[string]func(*api.SloOperationalDetail){
		"missing generation": func(v *api.SloOperationalDetail) { v.Generation = nil },
		"wrong generation":   func(v *api.SloOperationalDetail) { v.Generation.GenerationId[0]++ },
		"wrong objective":    func(v *api.SloOperationalDetail) { v.Objective.Id[0]++ },
		"wrong definition":   func(v *api.SloOperationalDetail) { v.Generation.DefinitionRevision = "2" },
		"wrong measurement":  func(v *api.SloOperationalDetail) { v.Generation.MeasurementRevision = "3" },
		"wrong source":       func(v *api.SloOperationalDetail) { v.Generation.SourceGeneration = "4" },
		"old calculation":    func(v *api.SloOperationalDetail) { v.CalculationVersion = "old" },
		"wrong endpoint":     func(v *api.SloOperationalDetail) { v.EffectiveAt = end.Add(-time.Minute) },
		"wrong range":        func(v *api.SloOperationalDetail) { v.Range.RequestedStart = start.Add(time.Minute) },
		"wrong burn": func(v *api.SloOperationalDetail) {
			v.BurnSeries.BurnWindowSeconds = new(api.SloOperationalSeriesBurnWindowSeconds(21600))
		},
		"empty urgency": func(v *api.SloOperationalDetail) { v.Urgency = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid()
			corrupt(&value)
			if preparedHistoryMatches(value, key, generation) {
				t.Fatal("corrupt history was served as valid")
			}
		})
	}
	if preparedHistoryMatches(api.SloOperationalDetail{}, key, generation) {
		t.Fatal("empty JSON object was accepted as a complete history")
	}
}
