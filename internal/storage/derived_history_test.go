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
		if got := derivedFailureCode(test.err); got != test.want {
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
