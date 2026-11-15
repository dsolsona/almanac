package storage

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestAlertCompactionSummaryValidation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	valid := func() (dbgen.AlmanacAlertRange, AlertCompactionSummary) {
		first := alert.SnapshotIdentity{SLOID: "00000000-0000-4000-8000-000000000001", SLORevision: 1, SLIRevision: 2, SourceGeneration: 7, ExclusionRevision: 3, CalculationVersion: "canonical-v1", Cutoff: at}
		last := first
		last.Cutoff = at.Add(time.Minute)
		last.SourceGeneration = 8
		return dbgen.AlmanacAlertRange{Kind: "compacted_evaluations", CadenceSeconds: 60, StartsAt: timestamp(at), EndsAt: timestamp(at.Add(2 * time.Minute))}, AlertCompactionSummary{FormatVersion: 1, First: first, Last: last, Truth: alert.Clear, Conditions: []alert.ConditionCompactionSummary{{ID: "fast", Threshold: "14.4", Outcome: alert.NotMatching, ShortDuration: 5 * time.Minute, LongDuration: time.Hour, ShortQuality: alert.Complete, LongQuality: alert.Complete}}}
	}
	for _, test := range []struct {
		name   string
		change func(*dbgen.AlmanacAlertRange, *AlertCompactionSummary)
	}{
		{"format", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.FormatVersion = 2 }},
		{"no conditions", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Conditions = nil }},
		{"duplicate conditions", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) {
			s.Conditions = append(s.Conditions, s.Conditions[0])
		}},
		{"truth mismatch", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Truth = alert.Breaching }},
		{"unknown outcome", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Conditions[0].Outcome = "pending" }},
		{"unavailable complete", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) {
			s.Truth = alert.Indeterminate
			s.Conditions[0].Outcome = alert.Unavailable
		}},
		{"matching missing", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) {
			s.Truth = alert.Breaching
			s.Conditions[0].Outcome = alert.Matching
			s.Conditions[0].LongQuality = alert.Missing
		}},
		{"unknown quality", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Conditions[0].ShortQuality = "pending" }},
		{"negative threshold", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Conditions[0].Threshold = "-1" }},
		{"zero threshold", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Conditions[0].Threshold = "0.00" }},
		{"rational threshold", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Conditions[0].Threshold = "1/2" }},
		{"fractional window", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) {
			s.Conditions[0].ShortDuration = time.Second
		}},
		{"reversed windows", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) {
			s.Conditions[0].ShortDuration = 2 * time.Hour
		}},
		{"excessive window", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) {
			s.Conditions[0].LongDuration = 31 * 24 * time.Hour
		}},
		{"source regression", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Last.SourceGeneration = 6 }},
		{"changed meaning", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Last.ExclusionRevision++ }},
		{"changed calculation", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Last.CalculationVersion = "other" }},
		{"missing identity", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.First.SLOID = "" }},
		{"negative revision", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.First.SLIRevision = -1 }},
		{"repeated cutoff", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Last.Cutoff = s.First.Cutoff }},
		{"cutoff after boundary", func(_ *dbgen.AlmanacAlertRange, s *AlertCompactionSummary) { s.Last.Cutoff = at.Add(2 * time.Minute) }},
		{"invalid cadence", func(r *dbgen.AlmanacAlertRange, _ *AlertCompactionSummary) { r.CadenceSeconds = 0 }},
		{"singleton range", func(r *dbgen.AlmanacAlertRange, _ *AlertCompactionSummary) { r.EndsAt = timestamp(at.Add(time.Minute)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, s := valid()
			test.change(&r, &s)
			var err error
			r.Summary, err = marshalAlertData(s)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := decodeAlertCompactionSummary(r); !errors.Is(err, ErrAlertInvalid) || got != nil {
				t.Fatalf("invalid summary accepted: %+v %v", got, err)
			}
		})
	}
	for _, truth := range []alert.Truth{alert.Clear, alert.Breaching, alert.Indeterminate} {
		r, s := valid()
		s.Truth = truth
		if truth == alert.Breaching {
			s.Conditions[0].Outcome = alert.Matching
		}
		if truth == alert.Indeterminate {
			s.Conditions[0].Outcome = alert.Unavailable
			s.Conditions[0].LongQuality = alert.CalculationFailed
			s.Conditions[0].LongReason = "calculation_failed"
		}
		var err error
		r.Summary, err = marshalAlertData(s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeAlertCompactionSummary(r)
		if err != nil || got == nil || got.Truth != truth || got.Last.SourceGeneration != 8 {
			t.Fatalf("valid summary lost: %+v %v", got, err)
		}
	}

	// OR remains breaching when another condition is unavailable, in either order.
	for _, reverse := range []bool{false, true} {
		r, s := valid()
		s.Truth = alert.Breaching
		matching := s.Conditions[0]
		matching.Outcome = alert.Matching
		unavailable := s.Conditions[0]
		unavailable.ID = "sustained"
		unavailable.Outcome = alert.Unavailable
		unavailable.LongQuality = alert.Missing
		s.Conditions = []alert.ConditionCompactionSummary{matching, unavailable}
		if reverse {
			s.Conditions[0], s.Conditions[1] = s.Conditions[1], s.Conditions[0]
		}
		r.Summary, _ = marshalAlertData(s)
		if got, err := decodeAlertCompactionSummary(r); err != nil || got == nil || got.Truth != alert.Breaching {
			t.Fatalf("mixed outcome rejected: %+v %v", got, err)
		}
	}
	for _, body := range []string{`null`, `{}`, `{"FormatVersion":1,"FormatVersion":1}`} {
		r, _ := valid()
		r.Summary = []byte(body)
		if _, err := decodeAlertCompactionSummary(r); !errors.Is(err, ErrAlertInvalid) {
			t.Fatalf("malformed summary accepted: %s %v", body, err)
		}
	}
	r, s := valid()
	r.Summary, _ = marshalAlertData(s)
	r.Summary = []byte(strings.Replace(string(r.Summary), `"Truth":"clear"`, `"Good":"100","Truth":"clear"`, 1))
	if _, err := decodeAlertCompactionSummary(r); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("invented numeric summary accepted: %v", err)
	}
	r.Kind = "unreconstructed_gap"
	if _, err := decodeAlertCompactionSummary(r); !errors.Is(err, ErrAlertInvalid) {
		t.Fatal("summary accepted on non-compacted range")
	}
	r.Summary = nil
	if got, err := decodeAlertCompactionSummary(r); err != nil || got != nil {
		t.Fatalf("ordinary range rejected: %+v %v", got, err)
	}
}
