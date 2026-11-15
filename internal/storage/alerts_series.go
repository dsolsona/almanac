package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math/big"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type ReadAlertSeriesInput struct {
	ReadAlertInput
	ConditionID string
	Start, End  time.Time
	Limit       int
}

type AlertSeriesPoint struct {
	EvaluationID             string
	Snapshot                 alert.SnapshotIdentity
	ScheduledAt, CommittedAt time.Time
	Condition                alert.ConditionResult
}

type AlertSeriesGap struct {
	Start, End                   time.Time
	StartInclusive, EndInclusive bool
	Basis, Kind, Reason, RangeID string
}

type AlertSeriesSampling struct {
	Total, Returned         int
	Reduced, OmittedExtrema bool
	Method                  string
}

type AlertSeriesRecord struct {
	PolicyID   string
	Revision   int64
	Objective  alert.Objective
	Condition  alert.Condition
	Start, End time.Time
	Points     []AlertSeriesPoint
	Gaps       []AlertSeriesGap
	Sampling   AlertSeriesSampling
}

// A normal minute-grid 24-hour request contains at most 1,441 distinct cutoffs.
// Internal/custom schedules and range histories are not assumed to satisfy that
// bound: one extra row yields resource refusal, never a partial success. Both
// bounded reads and all identity checks occur in the same authorized snapshot.
const alertSeriesSourceLimit = 1441

func ReadAlertSeries(ctx context.Context, databaseURL string, input ReadAlertSeriesInput) (AlertSeriesRecord, error) {
	var result AlertSeriesRecord
	if input.Limit < 2 || input.Limit > 120 || input.ConditionID == "" || !canonicalUUIDCheck(input.ID) {
		return result, ErrAlertInvalid
	}
	err := alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		id, _ := canonicalUUID(input.ID)
		occurrence, err := q.ReadAlertOccurrence(ctx, id)
		if err != nil {
			return err
		}
		revision, err := q.ReadAlertSeriesRevision(ctx, dbgen.ReadAlertSeriesRevisionParams{PolicyID: occurrence.PolicyID, Revision: occurrence.Revision})
		if err != nil {
			return err
		}
		latest, err := q.ReadAlertEvaluation(ctx, occurrence.LatestEvaluationID)
		if err != nil {
			return err
		}
		saved, err := decodeAlertSeriesEvidence(latest, revision)
		if err != nil {
			return err
		}
		var conditions []alert.Condition
		if err := unmarshalAlertData(revision.Conditions, &conditions); err != nil {
			return err
		}
		index := slices.IndexFunc(conditions, func(c alert.Condition) bool { return c.ID == input.ConditionID })
		if index < 0 {
			return ErrAlertInvalid
		}
		result = AlertSeriesRecord{PolicyID: revision.PolicyID.String(), Revision: revision.Revision, Objective: saved.Input.Objective, Condition: conditions[index], Start: input.Start.UTC(), End: input.End.UTC(), Points: []AlertSeriesPoint{}, Gaps: []AlertSeriesGap{}}
		if result.End.IsZero() {
			result.End = saved.Input.Snapshot.Cutoff.UTC()
		}
		if result.Start.IsZero() {
			result.Start = result.End.Add(-2 * time.Hour)
		}
		if result.Start.After(result.End) || result.End.Sub(result.Start) > 24*time.Hour || result.Start.Year() < 1 || result.End.Year() > 9999 {
			return ErrAlertInvalid
		}
		// PostgreSQL stores microseconds. Round the lower bound inward (up),
		// and the upper bound inward (down), so sub-microsecond requests cannot
		// accidentally include a sample outside their inclusive interval.
		queryStart := result.Start.Truncate(time.Microsecond)
		if queryStart.Before(result.Start) {
			queryStart = queryStart.Add(time.Microsecond)
		}
		queryEnd := result.End.Truncate(time.Microsecond)
		rows, err := q.ListAlertSeriesEvaluations(ctx, dbgen.ListAlertSeriesEvaluationsParams{PolicyID: occurrence.PolicyID, Revision: occurrence.Revision, EffectiveCutoff: timestamp(queryStart), EffectiveCutoff_2: timestamp(queryEnd), Limit: alertSeriesSourceLimit + 1})
		if err != nil {
			return err
		}
		if len(rows) > alertSeriesSourceLimit {
			return ErrOperationalResource
		}
		for _, row := range rows {
			evidence, err := decodeAlertSeriesEvidence(row, revision)
			if err != nil {
				return err
			}
			if evidence.Input.Objective.Target != result.Objective.Target || evidence.Input.Objective.Cadence != result.Objective.Cadence {
				return ErrAlertInvalid
			}
			condition := evidence.Evaluation.Conditions[index]
			result.Points = append(result.Points, AlertSeriesPoint{EvaluationID: row.ID.String(), Snapshot: evidence.Evaluation.Snapshot, ScheduledAt: row.ScheduledAt.Time.UTC(), CommittedAt: row.CommittedAt.Time.UTC(), Condition: condition})
			if condition.Short.Quality != alert.Complete || condition.Long.Quality != alert.Complete {
				result.Gaps = append(result.Gaps, AlertSeriesGap{Start: row.EffectiveCutoff.Time.UTC(), End: row.EffectiveCutoff.Time.UTC(), StartInclusive: true, EndInclusive: true, Basis: "effective_cutoff", Kind: "unavailable", Reason: string(condition.Short.Quality) + "/" + string(condition.Long.Quality)})
			}
		}
		// Requested wall-time bounds locate scheduled provenance on that axis.
		// Actual evaluation receipts provide a second, independently bounded
		// scheduled envelope: their cutoffs can lag those receipts substantially.
		// Do not scan the unrelated interval between disjoint envelopes.
		bounds := [][2]time.Time{{result.Start.Truncate(time.Microsecond), queryEnd}}
		if len(result.Points) > 0 {
			first, last := result.Points[0].ScheduledAt, result.Points[0].ScheduledAt
			for _, point := range result.Points[1:] {
				first, last = minSeriesTime(first, point.ScheduledAt), maxSeriesTime(last, point.ScheduledAt)
			}
			if !first.After(bounds[0][1]) && !last.Before(bounds[0][0]) {
				bounds[0] = [2]time.Time{minSeriesTime(bounds[0][0], first), maxSeriesTime(bounds[0][1], last)}
			} else {
				bounds = append(bounds, [2]time.Time{first, last})
			}
		}
		var ranges []dbgen.AlmanacAlertRange
		seenRanges := make(map[string]bool)
		for _, interval := range bounds {
			rangeRows, err := q.ListAlertSeriesRanges(ctx, dbgen.ListAlertSeriesRangesParams{PolicyID: occurrence.PolicyID, Revision: occurrence.Revision, StartAt: timestamp(interval[0]), EndAt: timestamp(interval[1]), RowLimit: alertSeriesSourceLimit + 1})
			if err != nil {
				return err
			}
			if len(rangeRows) > alertSeriesSourceLimit {
				return ErrOperationalResource
			}
			for _, row := range rangeRows {
				if !seenRanges[row.ID.String()] {
					seenRanges[row.ID.String()] = true
					ranges = append(ranges, dbgen.AlmanacAlertRange(row))
				}
			}
			if len(ranges) > alertSeriesSourceLimit {
				return ErrOperationalResource
			}
		}
		slices.SortFunc(ranges, func(a, b dbgen.AlmanacAlertRange) int { return a.StartsAt.Time.Compare(b.StartsAt.Time) })
		cursor, err := q.ReadAlertCursor(ctx, dbgen.ReadAlertCursorParams{PolicyID: occurrence.PolicyID, Revision: occurrence.Revision})
		if err != nil {
			return err
		}
		if err := addAlertSeriesGaps(&result, ranges, cursor); err != nil {
			return err
		}
		result.Points, result.Sampling, err = sampleAlertSeries(result.Points, result.Objective.Target, result.Start, result.End, input.Limit)
		return err
	})
	if err != nil {
		return AlertSeriesRecord{}, err
	}
	return result, nil
}

// Decode without recalculating a past outcome. Verify its immutable input digest,
// row identities, and the correspondence between saved input/result windows.
func decodeAlertSeriesEvidence(row dbgen.AlmanacAlertEvaluation, revision dbgen.AlmanacAlertPolicyRevision) (AlertEvidenceRecord, error) {
	var saved struct {
		FormatVersion int
		Input         alert.Input
		Evaluation    alert.Evaluation
	}
	if err := unmarshalAlertData(row.Evidence, &saved); err != nil {
		return AlertEvidenceRecord{}, err
	}
	input, evaluation := saved.Input, saved.Evaluation
	if saved.FormatVersion != 1 || row.PolicyID != revision.PolicyID || row.Revision != revision.Revision || input.Policy.ID != row.PolicyID.String() || input.Policy.Revision != row.Revision || evaluation.PolicyID != input.Policy.ID || evaluation.Revision != row.Revision || !sameAlertSeriesSnapshot(input.Snapshot, evaluation.Snapshot) || !input.Snapshot.Cutoff.Equal(row.EffectiveCutoff.Time) || string(evaluation.Truth) != row.Truth || input.Snapshot.SourceGeneration != row.SourceGeneration || input.Snapshot.ExclusionRevision != row.ExclusionRevision || input.Snapshot.SLOID != revision.SloID.String() || input.Snapshot.SLORevision != revision.DefinitionRevision || input.Snapshot.CalculationVersion != revision.CalculationVersion {
		return AlertEvidenceRecord{}, ErrAlertInvalid
	}
	var conditions []alert.Condition
	if err := unmarshalAlertData(revision.Conditions, &conditions); err != nil {
		return AlertEvidenceRecord{}, err
	}
	if !slices.Equal(input.Policy.Conditions, conditions) || len(evaluation.Conditions) != len(conditions) {
		return AlertEvidenceRecord{}, ErrAlertInvalid
	}
	encoded, err := marshalAlertData(input)
	if err != nil {
		return AlertEvidenceRecord{}, err
	}
	digest := sha256.Sum256(encoded)
	if !bytes.Equal(digest[:], row.InputDigest) {
		return AlertEvidenceRecord{}, ErrAlertInvalid
	}
	for i, c := range conditions {
		actual := evaluation.Conditions[i]
		if actual.ID != c.ID || actual.Threshold != c.Threshold {
			return AlertEvidenceRecord{}, ErrAlertInvalid
		}
		for j, w := range []alert.WindowEvidence{actual.Short, actual.Long} {
			duration := c.Short
			if j == 1 {
				duration = c.Long
			}
			if !sameAlertSeriesSnapshot(w.Snapshot, input.Snapshot) || !w.End.Equal(input.Snapshot.Cutoff) || !w.Start.Equal(w.End.Add(-duration)) || !slices.ContainsFunc(input.Windows, func(raw alert.WindowEvidence) bool { return alertSeriesWindowMatches(input.Objective, raw, w) }) {
				return AlertEvidenceRecord{}, ErrAlertInvalid
			}
		}
	}
	return AlertEvidenceRecord{ID: row.ID.String(), ScheduledAt: row.ScheduledAt.Time.UTC(), CommittedAt: row.CommittedAt.Time.UTC(), Input: input, Evaluation: evaluation}, nil
}

func sameAlertSeriesSnapshot(a, b alert.SnapshotIdentity) bool {
	a.Cutoff, b.Cutoff = a.Cutoff.UTC(), b.Cutoff.UTC()
	return a == b
}

// Verify the reducer's documented quality/reason normalization without
// recomputing a historical condition outcome or policy truth. Counts and all
// identity/window fields must still match the digest-protected raw evidence.
func alertSeriesWindowMatches(objective alert.Objective, raw, saved alert.WindowEvidence) bool {
	if raw.Quality == alert.Complete {
		if total, ok := new(big.Rat).SetString(raw.Total); ok && total.Sign() == 0 {
			raw.Quality, raw.Reason = alert.ZeroTraffic, "zero_traffic"
		}
	}
	if raw.Start.Before(objective.DefinitionValidFrom) || raw.Start.Before(objective.MeasurementValidFrom) {
		raw.Quality, raw.Reason = alert.Warmup, "full_window_warmup"
	}
	if raw.Start.Before(objective.RetentionStart) {
		raw.Quality, raw.Reason = alert.Expired, "full_window_not_retained"
	}
	raw.Start, saved.Start = raw.Start.UTC(), saved.Start.UTC()
	raw.End, saved.End = raw.End.UTC(), saved.End.UTC()
	raw.Snapshot.Cutoff, saved.Snapshot.Cutoff = raw.Snapshot.Cutoff.UTC(), saved.Snapshot.Cutoff.UTC()
	return raw == saved
}

func addAlertSeriesGaps(result *AlertSeriesRecord, ranges []dbgen.AlmanacAlertRange, cursor dbgen.AlmanacAlertCursor) error {
	// Missing effective evidence is explicit even when scheduled-axis provenance
	// cannot prove an exact mapping. Named work ranges remain on their own axis.
	points := result.Points
	if len(points) == 0 {
		result.Gaps = append(result.Gaps, AlertSeriesGap{Start: result.Start, End: result.End, StartInclusive: true, EndInclusive: true, Basis: "effective_cutoff", Kind: "not_retained", Reason: "no_retained_evaluation"})
	} else {
		if result.Start.Before(points[0].Snapshot.Cutoff) {
			result.Gaps = append(result.Gaps, AlertSeriesGap{Start: result.Start, End: points[0].Snapshot.Cutoff, StartInclusive: true, Basis: "effective_cutoff", Kind: "not_retained", Reason: "no_retained_evaluation"})
		}
		if result.End.After(points[len(points)-1].Snapshot.Cutoff) {
			result.Gaps = append(result.Gaps, AlertSeriesGap{Start: points[len(points)-1].Snapshot.Cutoff, End: result.End, EndInclusive: true, Basis: "effective_cutoff", Kind: "not_retained", Reason: "no_retained_evaluation"})
		}
	}
	// A gap in scheduled receipts does not establish an exact cutoff mapping.
	// Unless every omitted boundary is proven to repeat its predecessor, disclose
	// the open effective interval as missing, alongside the named scheduled range.
	for i := 1; i < len(points); i++ {
		left, right := points[i-1], points[i]
		next := left.ScheduledAt.Add(time.Minute)
		if !next.Before(right.ScheduledAt) {
			continue
		}
		for _, r := range ranges {
			if r.Kind != "repeated_cutoff" || r.StartsAt.Time.After(next) || !r.EndsAt.Time.After(next) {
				continue
			}
			next = r.EndsAt.Time
			if !next.Before(right.ScheduledAt) {
				break
			}
		}
		if next.Before(right.ScheduledAt) {
			result.Gaps = append(result.Gaps, AlertSeriesGap{Start: left.Snapshot.Cutoff, End: right.Snapshot.Cutoff, Basis: "effective_cutoff", Kind: "not_retained", Reason: "no_retained_evaluation"})
		}
	}
	for _, row := range ranges {
		summary, err := decodeAlertCompactionSummary(row)
		if err != nil {
			return err
		}
		gap := AlertSeriesGap{Start: row.StartsAt.Time.UTC(), End: row.EndsAt.Time.UTC(), StartInclusive: true, Basis: "scheduled_boundary", Kind: row.Kind, Reason: row.Reason, RangeID: row.ID.String()}
		if summary != nil {
			gap.Start, gap.End, gap.EndInclusive, gap.Basis = summary.First.Cutoff.UTC(), summary.Last.Cutoff.UTC(), true, "effective_cutoff"
		}
		if gap.Basis == "effective_cutoff" && (gap.Start.After(result.End) || gap.End.Before(result.Start) || gap.End.Equal(result.Start) && !gap.EndInclusive) {
			continue
		}
		result.Gaps = append(result.Gaps, gap)
	}
	// Activation and due work are scheduled facts, not estimates of cutoff time.
	if result.Start.Before(cursor.OriginAt.Time) {
		result.Gaps = append(result.Gaps, AlertSeriesGap{Start: result.Start, End: minSeriesTime(result.End, cursor.OriginAt.Time), StartInclusive: true, Basis: "scheduled_boundary", Kind: "outside_revision", Reason: "before_activation", EndInclusive: result.End.Before(cursor.OriginAt.Time)})
	}
	if cursor.Schedulable && !cursor.NextScheduledAt.Time.After(result.End) {
		result.Gaps = append(result.Gaps, AlertSeriesGap{Start: maxSeriesTime(result.Start, cursor.NextScheduledAt.Time), End: result.End, StartInclusive: true, EndInclusive: true, Basis: "scheduled_boundary", Kind: "pending", Reason: cursor.WaitingReason})
	}
	if !cursor.Schedulable && !cursor.NextScheduledAt.Time.After(result.End) {
		result.Gaps = append(result.Gaps, AlertSeriesGap{Start: maxSeriesTime(result.Start, cursor.NextScheduledAt.Time), End: result.End, StartInclusive: true, EndInclusive: true, Basis: "scheduled_boundary", Kind: "outside_revision", Reason: "after_inactivation"})
	}
	return nil
}
func minSeriesTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b.UTC()
}
func maxSeriesTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b.UTC()
}

func sampleAlertSeries(points []AlertSeriesPoint, target string, start, end time.Time, limit int) ([]AlertSeriesPoint, AlertSeriesSampling, error) {
	metadata := AlertSeriesSampling{Total: len(points), Returned: min(len(points), limit), Reduced: len(points) > limit, Method: "exact"}
	if len(points) <= limit {
		return points, metadata, nil
	}
	metadata.Method = "extrema_priority"
	burns := make([][2]*big.Rat, len(points))
	for i, p := range points {
		for j, w := range []alert.WindowEvidence{p.Condition.Short, p.Condition.Long} {
			if w.Good == "" || w.Total == "" {
				continue
			}
			value, err := slo.CalculateOccurrences(w.Good, w.Total, target, w.Quality != alert.Complete)
			if errors.Is(err, slo.ErrNoTraffic) {
				continue
			}
			if err != nil {
				return nil, metadata, ErrAlertInvalid
			}
			burns[i][j] = value.ExactConsumed
		}
	}
	extrema := func(first, last int) []int {
		indices := make([]int, 4)
		for i := range indices {
			indices[i] = -1
		}
		for i := first; i < last; i++ {
			for lane := range 2 {
				if burns[i][lane] == nil {
					continue
				}
				for direction := range 2 {
					k := direction*2 + lane
					old := indices[k]
					if old < 0 || direction == 0 && burns[i][lane].Cmp(burns[old][lane]) > 0 || direction == 1 && burns[i][lane].Cmp(burns[old][lane]) < 0 {
						indices[k] = i
					}
				}
			}
		}
		return indices
	}
	globals := extrema(0, len(points))
	allExtrema := map[int]bool{}
	for _, index := range globals {
		if index >= 0 {
			allExtrema[index] = true
		}
	}
	selected := map[int]bool{0: true, len(points) - 1: true}
	add := func(index int) {
		if index >= 0 && len(selected) < limit {
			selected[index] = true
		}
	}
	for _, index := range globals {
		add(index)
	}
	// Fixed equal-time buckets provide actual first/last/extrema candidates. The
	// remaining priority is chronological, independent of map iteration order.
	candidates := map[int]bool{}
	buckets := limit
	width := end.Sub(start)
	for bucket := range buckets {
		left, right := -1, -1
		for i, p := range points {
			b := 0
			if width > 0 {
				b = min(int(p.Snapshot.Cutoff.Sub(start)*time.Duration(buckets)/width), buckets-1)
			}
			if b == bucket {
				if left < 0 {
					left = i
				}
				right = i + 1
			}
		}
		if left < 0 {
			continue
		}
		candidates[left] = true
		candidates[right-1] = true
		for _, index := range extrema(left, right) {
			if index >= 0 {
				candidates[index] = true
				allExtrema[index] = true
			}
		}
	}
	for i := range points {
		if candidates[i] {
			add(i)
		}
	}
	for index := range allExtrema {
		if !selected[index] {
			metadata.OmittedExtrema = true
		}
	}
	output := make([]AlertSeriesPoint, 0, len(selected))
	for i, p := range points {
		if selected[i] {
			output = append(output, p)
		}
	}
	metadata.Returned = len(output)
	return output, metadata, nil
}
