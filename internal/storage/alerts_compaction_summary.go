package storage

import (
	"encoding/json/v2"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// AlertCompactionSummary retains unchanged condition outcomes and the actual
// endpoint provenance. It deliberately contains no counts or inferred middle
// values: exact numeric evidence at compacted cutoffs is unavailable.
type AlertCompactionSummary struct {
	FormatVersion int
	First, Last   alert.SnapshotIdentity
	Truth         alert.Truth
	Conditions    []alert.ConditionCompactionSummary
}

func decodeAlertCompactionSummary(row dbgen.AlmanacAlertRange) (*AlertCompactionSummary, error) {
	if row.Kind != "compacted_evaluations" {
		if len(row.Summary) != 0 {
			return nil, ErrAlertInvalid
		}
		return nil, nil
	}
	var summary AlertCompactionSummary
	// Reject unknown fields as well as malformed types: a summary must not silently
	// accept numerical evidence that the compaction contract cannot preserve.
	if err := json.Unmarshal(row.Summary, &summary, json.RejectUnknownMembers(true), json.WithUnmarshalers(alertDurationUnmarshalers)); err != nil {
		return nil, ErrAlertInvalid
	}
	if summary.FormatVersion != 1 || len(summary.Conditions) == 0 || row.CadenceSeconds != 60 || !row.StartsAt.Valid || !row.EndsAt.Valid || row.StartsAt.Time.IsZero() || row.EndsAt.Time.Sub(row.StartsAt.Time) < 2*time.Minute || row.StartsAt.Time.Nanosecond() != 0 || row.EndsAt.Time.Nanosecond() != 0 || row.StartsAt.Time.Unix()%60 != 0 || row.EndsAt.Time.Unix()%60 != 0 {
		return nil, ErrAlertInvalid
	}
	for _, identity := range []alert.SnapshotIdentity{summary.First, summary.Last} {
		if !canonicalUUIDCheck(identity.SLOID) || identity.SLORevision <= 0 || identity.SLIRevision <= 0 || identity.SourceGeneration < 0 || identity.ExclusionRevision < 0 || strings.TrimSpace(identity.CalculationVersion) == "" || identity.Cutoff.IsZero() || identity.Cutoff.Nanosecond() != 0 {
			return nil, ErrAlertInvalid
		}
	}
	meaning := summary.Last
	meaning.Cutoff = summary.First.Cutoff
	meaning.SourceGeneration = summary.First.SourceGeneration
	if meaning != summary.First || summary.Last.SourceGeneration < summary.First.SourceGeneration || !summary.Last.Cutoff.After(summary.First.Cutoff) || summary.First.Cutoff.After(row.StartsAt.Time) || summary.Last.Cutoff.After(row.EndsAt.Time.Add(-time.Minute)) {
		return nil, ErrAlertInvalid
	}
	truth := alert.Clear
	seen := make(map[string]struct{}, len(summary.Conditions))
	for _, condition := range summary.Conditions {
		if strings.TrimSpace(condition.ID) == "" {
			return nil, ErrAlertInvalid
		}
		if _, ok := seen[condition.ID]; ok {
			return nil, ErrAlertInvalid
		}
		seen[condition.ID] = struct{}{}
		whole, fraction, point := strings.Cut(condition.Threshold, ".")
		if whole == "" || strings.Trim(whole, "0123456789") != "" || point && (fraction == "" || strings.Trim(fraction, "0123456789") != "") || !strings.ContainsAny(condition.Threshold, "123456789") {
			return nil, ErrAlertInvalid
		}
		if condition.ShortDuration <= 0 || condition.LongDuration < condition.ShortDuration || condition.LongDuration > 30*24*time.Hour || condition.ShortDuration%time.Minute != 0 || condition.LongDuration%time.Minute != 0 {
			return nil, ErrAlertInvalid
		}
		for _, quality := range []alert.Quality{condition.ShortQuality, condition.LongQuality} {
			switch quality {
			case alert.Complete, alert.Missing, alert.Partial, alert.Stale, alert.ZeroTraffic, alert.Warmup, alert.Expired, alert.Excluded, alert.CalculationFailed:
			default:
				return nil, ErrAlertInvalid
			}
		}
		complete := condition.ShortQuality == alert.Complete && condition.LongQuality == alert.Complete
		switch condition.Outcome {
		case alert.Matching:
			if !complete {
				return nil, ErrAlertInvalid
			}
			truth = alert.Breaching
		case alert.NotMatching:
			if !complete {
				return nil, ErrAlertInvalid
			}
		case alert.Unavailable:
			if complete {
				return nil, ErrAlertInvalid
			}
			if truth != alert.Breaching {
				truth = alert.Indeterminate
			}
		default:
			return nil, ErrAlertInvalid
		}
	}
	if truth != summary.Truth {
		return nil, ErrAlertInvalid
	}
	return &summary, nil
}
