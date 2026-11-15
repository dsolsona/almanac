package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type maintenanceMaskWindows struct {
	RequestedRevision int64
	Preferred         *maintenanceComputedMask
	Windows           []slo.WindowInput
}
type maintenanceComputedMask struct {
	Revision int64
	BuildID  pgtype.UUID
}
type maintenanceEvaluationInput struct {
	SloID, SliID                            pgtype.UUID
	DefinitionRevision, MeasurementRevision int64
	Requests                                []maintenanceMaskWindows
	Limits                                  maintenanceWindowLimits
}
type maintenanceEvaluationResult struct {
	SourceGeneration int64
	Masks            []maintenanceEvaluatedMask
	Preparation      []maintenanceEvaluationPreparation
}
type maintenanceEvaluatedMask struct {
	RequestedRevision int64
	Computed          maintenanceComputedMask
	Proofs            []maintenanceCompatibilityProof
	Windows           []slo.WindowResult
	Unavailable       []string // Original window order; nonempty entries have no numerical result.
	ReusedHours       []maintenanceReusedHour
}
type maintenanceReusedHour struct {
	Key              maintenanceHourKey
	ComputedRevision int64
	Proof            maintenanceCompatibilityProof
}
type maintenanceEvaluationBuildPair struct{ FirstBuild, SecondBuild pgtype.UUID }

type maintenanceEvaluationPreparation struct {
	Comparison        *maintenanceEvaluationBuildPair
	Kind              string
	RequestedRevision int64
	BuildID           pgtype.UUID
	Hour              maintenanceHourKey
	Start, End        time.Time
}

type maintenanceEvaluationWork struct {
	plan      maintenanceWindowPlan
	mask      maintenanceEvaluatedMask
	windows   []slo.WindowInput
	positions []int
}

// evaluateMaintenanceWindows is unwired. The caller owns RR, resource admission,
// cancellation and final publication fences. Current and last-breaching masks
// belong in one Requests batch. No preparation is executed or registered here.
func evaluateMaintenanceWindows(ctx context.Context, tx pgx.Tx, in maintenanceEvaluationInput) (result maintenanceEvaluationResult, err error) {
	defer func() {
		if err != nil {
			for i := range result.Masks {
				result.Masks[i].Windows = nil
			}
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if tx == nil || !in.SloID.Valid || !in.SliID.Valid || in.DefinitionRevision < 1 || in.MeasurementRevision < 1 {
		return result, ErrMaintenanceInvalid
	}
	var all []slo.WindowInput
	for _, r := range in.Requests {
		if r.RequestedRevision < 0 {
			return result, ErrMaintenanceInvalid
		}
		if len(r.Windows) > derivedWindowBatchLimit-len(all) {
			return result, ErrOperationalResource
		}
		all = append(all, r.Windows...)
	}
	if len(all) == 0 {
		return result, nil
	}
	if _, err = planMaintenanceWindows(ctx, all, in.Limits); err != nil {
		return
	}
	q := dbgen.New(tx)
	identity, e := q.ReadMaintenanceEvaluationIdentity(ctx, in.SloID)
	if e != nil {
		return result, e
	}
	if identity.Isolation != "repeatable read" || identity.SliID != in.SliID || identity.DefinitionRevision != in.DefinitionRevision || identity.MeasurementRevision != in.MeasurementRevision {
		return result, ErrMaintenanceSnapshotChanged
	}
	if identity.MeasurementKind != "event_counts" || identity.ComponentMode != "interval_count" || time.Duration(identity.CadenceSeconds)*time.Second != all[0].Cadence {
		return result, ErrMaintenanceInvalid
	}
	result.SourceGeneration = identity.SourceGeneration
	work := make([]maintenanceEvaluationWork, len(in.Requests))
	result.Masks = make([]maintenanceEvaluatedMask, len(in.Requests))
	var nonzero []slo.WindowInput
	for i, r := range in.Requests {
		if r.RequestedRevision > identity.ExclusionRevision {
			return result, ErrMaintenanceSnapshotChanged
		}
		work[i].mask, err = chooseMaintenanceEvaluationMask(ctx, tx, in.SloID, r)
		result.Masks[i] = work[i].mask
		if err != nil {
			if errors.Is(err, ErrMaintenanceCompilePending) {
				result.Preparation = append(result.Preparation, maintenanceEvaluationPreparation{Kind: "mask", RequestedRevision: r.RequestedRevision})
				return result, errors.Join(ErrDerivedPending, err)
			}
			return
		}
		for j, window := range r.Windows {
			if work[i].mask.Unavailable[j] == "" {
				work[i].positions = append(work[i].positions, j)
				work[i].windows = append(work[i].windows, window)
			}
		}
		work[i].plan, err = planMaintenanceWindows(ctx, work[i].windows, in.Limits)
		if err != nil {
			return
		}
		if work[i].mask.Computed.Revision != 0 {
			nonzero = append(nonzero, work[i].windows...)
		}
	}

	shared, err := planMaintenanceWindows(ctx, nonzero, in.Limits)
	if err != nil {
		return result, err
	}
	if err = admitMaintenanceEvaluation(work, shared, in.Limits); err != nil {
		return result, err
	}
	hours, raw, err := readMaintenanceEvaluationSources(ctx, q, in, shared, all[0])
	if err != nil {
		return result, err
	}
	for i := range work {
		w := &work[i]
		unavailable := make([]string, len(w.windows))
		if len(w.windows) == 0 {
			w.mask.Windows = nil
		} else if w.mask.Computed.Revision == 0 {
			w.mask.Windows, err = evaluateDerivedWindows(ctx, q, in.SliID, in.MeasurementRevision, w.windows)
			if errors.Is(err, ErrDerivedPending) {
				seen := map[maintenanceWindowRange]bool{}
				for _, window := range w.plan.Windows {
					span := maintenanceWindowRange{Start: window.FullStart, End: window.FullEnd}
					if span.Start.Before(span.End) && !seen[span] {
						seen[span] = true
						result.Preparation = append(result.Preparation, maintenanceEvaluationPreparation{Kind: "source_window", RequestedRevision: w.mask.RequestedRevision, Start: span.Start, End: span.End})
					}
				}
			}
		} else {
			w.mask.Windows, w.mask.ReusedHours, err = evaluateMaintenanceMask(ctx, tx, in, w.mask.Computed, w.plan, hours, raw, &result.Preparation, unavailable)
		}
		if err == nil {
			expanded := make([]slo.WindowResult, len(in.Requests[i].Windows))
			if len(w.mask.Windows) != len(w.positions) {
				return result, slo.ErrOperationalFacts
			}
			for j, original := range w.positions {
				expanded[original] = w.mask.Windows[j]
				w.mask.Unavailable[original] = unavailable[j]
			}
			w.mask.Windows = expanded
		}
		result.Masks[i] = w.mask
		if err != nil {
			if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrMaintenanceCompilePending) {
				return result, errors.Join(ErrDerivedPending, err)
			}
			return result, err
		}
	}
	return result, nil
}

func chooseMaintenanceEvaluationMask(ctx context.Context, tx pgx.Tx, id pgtype.UUID, r maintenanceMaskWindows) (maintenanceEvaluatedMask, error) {
	out := maintenanceEvaluatedMask{RequestedRevision: r.RequestedRevision, Unavailable: make([]string, len(r.Windows))}
	if len(r.Windows) == 0 || r.RequestedRevision == 0 {
		return out, nil
	}
	// A window with no eligible interval does not need any exclusion authority
	// preparation. The shared evaluator still supplies not-applicable semantics.
	nonempty := false
	for _, w := range r.Windows {
		start := w.At.Add(-w.Duration)
		for _, b := range []time.Time{w.ValidFrom, w.SliValidFrom} {
			if b.After(start) {
				start = b
			}
		}
		if ceilSliBoundary(start, w.Cadence).Before(w.At) {
			nonempty = true
		}
	}
	if !nonempty {
		return out, nil
	}
	q := dbgen.New(tx)
	build, err := q.ReadMaintenanceEvaluationBuild(ctx, dbgen.ReadMaintenanceEvaluationBuildParams{SloID: id, TargetRevision: r.RequestedRevision})
	if err == nil && build.Phase == "complete" {
		out.Computed = maintenanceComputedMask{Revision: build.TargetRevision, BuildID: build.ID}
		return out, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	terminalReason := ""
	if err == nil {
		if build.Phase == "unavailable" || build.Phase == "cancelled" {
			terminalReason = "maintenance_history_unavailable"
		} else {
			terminalReason, err = q.ReadMaintenanceEvaluationBuildHealth(ctx, dbgen.ReadMaintenanceEvaluationBuildHealthParams{Kind: "mask", SloID: id, TargetRevision: r.RequestedRevision, BuildID: build.ID})
			if err != nil {
				return out, err
			}
		}
	}
	unavailable := func(reason string) (maintenanceEvaluatedMask, error) {
		for i := range out.Unavailable {
			out.Unavailable[i] = reason
		}
		out.Proofs = nil
		return out, nil
	}
	if r.Preferred != nil {
		out.Computed = *r.Preferred
		if out.Computed.Revision < 0 || out.Computed.Revision > r.RequestedRevision || out.Computed.Revision != 0 && !out.Computed.BuildID.Valid {
			return out, ErrMaintenanceInvalid
		}
		if out.Computed.BuildID.Valid {
			build, err = q.ReadMaintenanceBuild(ctx, out.Computed.BuildID)
			if err != nil {
				return out, err
			}
			if build.SloID != id || build.TargetRevision != out.Computed.Revision || build.Phase != "complete" {
				return out, ErrMaintenanceCompilePending
			}
		}
	} else {
		build, err = q.ReadMaintenanceEvaluationCandidate(ctx, dbgen.ReadMaintenanceEvaluationCandidateParams{SloID: id, TargetRevision: r.RequestedRevision})
		if err == nil {
			out.Computed = maintenanceComputedMask{Revision: build.TargetRevision, BuildID: build.ID}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	for _, w := range r.Windows {
		out.Proofs = append(out.Proofs, maintenanceCompatibilityProof{SloID: id.String(), ComputedRevision: out.Computed.Revision, RequestedRevision: r.RequestedRevision, Start: w.At.Add(-w.Duration), End: w.At})
	}
	out.Proofs, err = readMaintenanceEvaluationProofs(ctx, tx, id, out.Proofs)
	if err != nil {
		if errors.Is(err, ErrMaintenanceCompileUnavailable) {
			return unavailable("maintenance_history_unavailable")
		}
		if terminalReason != "" && errors.Is(err, ErrMaintenanceCompilePending) {
			return unavailable(terminalReason)
		}
		return out, err
	}
	for i, p := range out.Proofs {
		if p.Kind == "changed" {
			if terminalReason != "" {
				out.Unavailable[i] = terminalReason
				continue
			}
			return out, ErrMaintenanceCompilePending
		}
	}
	return out, nil
}

func readMaintenanceEvaluationProofs(ctx context.Context, tx pgx.Tx, id pgtype.UUID, proofs []maintenanceCompatibilityProof) ([]maintenanceCompatibilityProof, error) {
	if len(proofs) == 0 {
		return proofs, nil
	}
	q := dbgen.New(tx)
	authority, err := q.ReadMaintenanceCompatibilityAuthority(ctx, id)
	if err != nil {
		return nil, err
	}
	if authority.Isolation != "repeatable read" {
		return nil, ErrMaintenanceSnapshotChanged
	}
	p := dbgen.ReadMaintenanceEvaluationProofsParams{SloScopeID: id, ScopeOrdinal: authority.ScopeOrdinal}
	var positions []int
	for i := range proofs {
		v := &proofs[i]
		if v.ComputedRevision < 0 || v.RequestedRevision < v.ComputedRevision || v.RequestedRevision > authority.CurrentRevision || !validMaintenanceTime(v.Start) || !validMaintenanceTime(v.End) || !v.Start.Before(v.End) {
			return nil, ErrMaintenanceInvalid
		}
		v.IndexedThrough = authority.IndexedThrough
		if v.ComputedRevision == v.RequestedRevision {
			v.Kind = "same_revision"
			continue
		}
		if authority.IndexedThrough < v.RequestedRevision {
			if authority.Unavailable {
				return nil, ErrMaintenanceCompileUnavailable
			}
			return nil, ErrMaintenanceCompilePending
		}
		positions = append(positions, i)
		p.Computed = append(p.Computed, v.ComputedRevision)
		p.Requested = append(p.Requested, v.RequestedRevision)
		p.Starts = append(p.Starts, timestamp(v.Start))
		p.Ends = append(p.Ends, timestamp(v.End))
	}
	if len(positions) == 0 {
		return proofs, nil
	}
	rows, err := q.ReadMaintenanceEvaluationProofs(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(positions) {
		return nil, ErrMaintenanceCompileUnavailable
	}
	for i, r := range rows {
		if r.Ordinal != int32(i+1) {
			return nil, ErrMaintenanceCompileUnavailable
		}
		v := &proofs[positions[i]]
		v.Kind = "indexed_absence"
		if r.Changed {
			v.Kind = "changed"
			if r.ComparisonID.Valid {
				v.Kind = "equal_union"
				v.ComparisonID = r.ComparisonID.String()
			}
		}
	}
	return proofs, nil
}

// The three existing caller budgets cover both fetch and processing. Canonical
// zero uses its original per-window source/edge upper bound, since that query
// performs its own reads. Nonzero source rows are fetched once; each mask's
// hour/edge processing is separately reserved. Sentinels count as read work.
func admitMaintenanceEvaluation(work []maintenanceEvaluationWork, shared maintenanceWindowPlan, limits maintenanceWindowLimits) error {
	hours, raw, fragments := limits.MaxSourceHours, limits.MaxRawRows, limits.MaxFragments
	charge := func(left *int, n uint64) bool {
		if *left < 0 || n > uint64(*left) {
			return false
		}
		*left -= int(n)
		return true
	}
	if !charge(&hours, uint64(len(shared.SourceHours))) {
		return ErrOperationalResource
	}
	slots := func(e maintenanceWindowRange, c time.Duration) uint64 {
		return (uint64(e.End.UnixMicro()) - uint64(e.Start.UnixMicro())) / uint64(c.Microseconds())
	}
	if len(shared.Windows) > 0 {
		for _, e := range shared.Edges {
			if !charge(&raw, slots(e, shared.Windows[0].Input.Cadence)+1) {
				return ErrOperationalResource
			}
		}
	}
	for _, w := range work {
		if w.mask.Computed.Revision == 0 {
			for _, v := range w.plan.Windows {
				if v.FullStart.Before(v.FullEnd) && !charge(&hours, (uint64(v.FullEnd.UnixMicro())-uint64(v.FullStart.UnixMicro()))/uint64(time.Hour.Microseconds())) {
					return ErrOperationalResource
				}
				for _, e := range v.Edges {
					if !charge(&raw, slots(e, v.Input.Cadence)) {
						return ErrOperationalResource
					}
				}
			}
		} else {
			if !charge(&hours, uint64(len(w.plan.SourceHours))) {
				return ErrOperationalResource
			}
			if len(w.plan.Windows) == 0 {
				continue
			}
			for _, e := range w.plan.Edges {
				n := slots(e, w.plan.Windows[0].Input.Cadence)
				if !charge(&raw, n) || !charge(&fragments, n+2) {
					return ErrOperationalResource
				}
			}
		}
	}
	return nil
}
