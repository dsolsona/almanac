package storage

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type maintenanceRetainedReuseInput struct {
	Base       slo.WindowInput
	Lanes      extremaLanes
	Row        maintenanceRetainedRowAuthority
	Start, End time.Time
}
type maintenanceRetainedReuseResult struct {
	Lanes       extremaLanes
	Preparation []maintenanceEvaluationPreparation
	// NeedsCalculation means the old selection is not reusable. Preparation
	// lists only unmet optional reuse prerequisites; an empty list routes the
	// caller to fresh bounded calculation, rather than an unschedulable wait.
	NeedsCalculation bool
}
type maintenanceRetainedProofKey struct {
	computed   int64
	start, end time.Time
}
type maintenanceRetainedMarkProof struct {
	mark     *extremaMark
	position int
}
type maintenanceRetainedReusePlan struct {
	owned    extremaLanes
	marks    []maintenanceRetainedMarkProof
	proofs   []int
	changed  []int64
	terminal bool
}

func reconcileMaintenanceRetainedLanes(ctx context.Context, tx pgx.Tx, base slo.WindowInput, bucketStart, bucketEnd time.Time, lanes extremaLanes, row maintenanceRetainedRowAuthority, requestedRevision int64, maxProofs int) (maintenanceRetainedReuseResult, error) {
	results, err := reconcileMaintenanceRetainedBatch(ctx, tx, []maintenanceRetainedReuseInput{{Base: base, Lanes: lanes, Row: row, Start: bucketStart, End: bucketEnd}}, requestedRevision, maxProofs)
	if len(results) == 0 {
		return maintenanceRetainedReuseResult{}, err
	}
	return results[0], err
}

// Reconcile trusted rows of one SLO in one source RR snapshot. Admission covers
// all unique mark windows AND the original endpoint selection input of every
// lane. A change at an unselected endpoint can change an extremum or coverage.
// All proofs share one batch; pending rows return hints without hiding ready
// rows. Invalid input or database failure returns no partial results.
func reconcileMaintenanceRetainedBatch(ctx context.Context, tx pgx.Tx, inputs []maintenanceRetainedReuseInput, requestedRevision int64, maxProofs int) ([]maintenanceRetainedReuseResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tx == nil || maxProofs < 1 || requestedRevision < 0 {
		return nil, ErrMaintenanceInvalid
	}
	if len(inputs) == 0 {
		return []maintenanceRetainedReuseResult{}, nil
	}
	scope := inputs[0].Row.Scope
	if !canonicalUUIDCheck(scope.SloID.String()) || !canonicalUUIDCheck(scope.SliID.String()) || scope.DefinitionRevision < 1 || scope.MeasurementRevision < 1 {
		return nil, ErrMaintenanceInvalid
	}
	plans := make([]maintenanceRetainedReusePlan, len(inputs))
	var proofs []maintenanceCompatibilityProof
	positions := map[maintenanceRetainedProofKey]int{}
	builds := map[int64]string{}
	addProof := func(plan *maintenanceRetainedReusePlan, c int64, start, end time.Time) (int, error) {
		key := maintenanceRetainedProofKey{computed: c, start: start.UTC(), end: end.UTC()}
		if !validMaintenanceTime(key.start) || !validMaintenanceTime(key.end) || !key.start.Before(key.end) {
			return 0, ErrDerivedFailed
		}
		position, ok := positions[key]
		if !ok {
			if len(proofs) >= maxProofs {
				return 0, ErrOperationalResource
			}
			position = len(proofs)
			positions[key] = position
			proofs = append(proofs, maintenanceCompatibilityProof{SloID: scope.SloID.String(), ComputedRevision: c, RequestedRevision: requestedRevision, Start: key.start, End: key.end})
		}
		if !slices.Contains(plan.proofs, position) {
			plan.proofs = append(plan.proofs, position)
		}
		return position, nil
	}
	for i, in := range inputs {
		row, base := in.Row, in.Base
		if row.Scope != scope || row.SourceGeneration < 0 || row.RequestedRevision < 0 || requestedRevision < row.RequestedRevision || base.Cadence <= 0 || base.Cadence%time.Microsecond != 0 || base.Duration <= 0 || !validMaintenanceTime(in.Start) || !validMaintenanceTime(in.End) || !in.Start.Before(in.End) {
			return nil, ErrMaintenanceInvalid
		}
		for _, lane := range in.Lanes.list() {
			if lane != nil && len(lane.Marks) > sloOperationalSeriesLimit {
				return nil, ErrOperationalResource
			}
		}
		plan := &plans[i]
		plan.owned = cloneMaintenanceRetainedLanes(in.Lanes)
		inspect := func(mark *extremaMark, window slo.WindowInput) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !mark.At.After(in.Start) || mark.At.After(in.End) {
				return ErrDerivedFailed
			}
			if err := mark.validateMaintenanceRowAuthority(window, row); err != nil {
				return err
			}
			value, err := mark.result(ctx, window)
			if err != nil {
				return err
			}
			if requestedRevision == row.RequestedRevision {
				return nil
			}
			if mark.Maintenance != nil && mark.Maintenance.UnavailableReason != "" {
				plan.terminal = true
				return nil
			}
			if value.Performance == "not_applicable" && value.Expected == 0 && value.Excluded == 0 {
				mark.Maintenance = nil
				return nil
			}
			computed := int64(0)
			build := ""
			if mark.Maintenance != nil {
				computed, build = mark.Maintenance.ComputedRevision, mark.Maintenance.BuildID
			}
			if old := builds[computed]; old != "" && build != "" && old != build {
				return ErrDerivedFailed
			}
			if build != "" {
				builds[computed] = build
			}
			position, err := addProof(plan, computed, mark.At.Add(-window.Duration), mark.At)
			if err != nil {
				return err
			}
			plan.marks = append(plan.marks, maintenanceRetainedMarkProof{mark: mark, position: position})
			return nil
		}
		for laneIndex, lane := range plan.owned.list() {
			if lane == nil {
				continue
			}
			window := base
			window.Duration = [4]time.Duration{base.Duration, base.Cadence, time.Hour, 6 * time.Hour}[laneIndex]
			first, last := floorSliBoundary(in.Start, base.Cadence).Add(base.Cadence), floorSliBoundary(in.End, base.Cadence)
			if first.After(last) && len(lane.Marks) > 0 {
				return nil, ErrDerivedFailed
			}
			// Every evaluated endpoint has a window of at least one cadence. Their
			// union is contiguous, even for non-hour-dividing grids. Original row R
			// authenticates the selection; selected marks retain their own actual C.
			if requestedRevision != row.RequestedRevision && !first.After(last) && ceilSliBoundary(maxTime(last.Add(-window.Duration), base.ValidFrom, base.SliValidFrom), base.Cadence).Before(last) {
				if _, err := addProof(plan, row.RequestedRevision, first.Add(-window.Duration), last); err != nil {
					return nil, err
				}
			}
			for j := range lane.Marks {
				if j > 0 && !lane.Marks[j].At.After(lane.Marks[j-1].At) {
					return nil, ErrDerivedFailed
				}
				if err := inspect(&lane.Marks[j], window); err != nil {
					return nil, err
				}
			}
		}
		if plan.owned.Compliance != nil {
			c := plan.owned.Compliance
			if !validMaintenanceTime(c.DayEnd) || !c.Mark.At.Equal(floorSliBoundary(c.DayEnd, base.Cadence)) {
				return nil, ErrDerivedFailed
			}
			if err := inspect(&c.Mark, base); err != nil {
				return nil, err
			}
		}
	}
	if len(proofs) == 0 {
		authority, err := dbgen.New(tx).ReadMaintenanceCompatibilityAuthority(ctx, scope.SloID)
		if err != nil {
			return nil, err
		}
		if authority.Isolation != "repeatable read" || requestedRevision > authority.CurrentRevision {
			return nil, ErrMaintenanceSnapshotChanged
		}
	}
	checked, err := readMaintenanceEvaluationProofs(ctx, tx, scope.SloID, proofs)
	pendingCompatibility := errors.Is(err, ErrMaintenanceCompilePending)
	unavailableCompatibility := errors.Is(err, ErrMaintenanceCompileUnavailable)
	if err != nil && !pendingCompatibility && !unavailableCompatibility {
		return nil, err
	}
	results := make([]maintenanceRetainedReuseResult, len(inputs))
	var changedComputed []int64
	anyPending := false
	for i := range plans {
		plan := &plans[i]
		pending := plan.terminal || len(plan.proofs) > 0 && (pendingCompatibility || unavailableCompatibility)
		for _, position := range plan.proofs {
			if len(checked) == 0 {
				break
			}
			proof := checked[position]
			if proof.Kind == "changed" {
				pending = true
				if !slices.Contains(plan.changed, proof.ComputedRevision) {
					plan.changed = append(plan.changed, proof.ComputedRevision)
				}
				if !slices.Contains(changedComputed, proof.ComputedRevision) {
					changedComputed = append(changedComputed, proof.ComputedRevision)
				}
			}
		}
		if pending {
			anyPending = true
			results[i].NeedsCalculation = true
			continue
		}
		for _, entry := range plan.marks {
			m := entry.mark.Maintenance
			if m == nil {
				m = &retainedMaintenanceInputs{Version: 1, Scope: scope, SourceGeneration: inputs[i].Row.SourceGeneration}
				entry.mark.Maintenance = m
			}
			m.RequestedRevision = requestedRevision
			m.Proof = new(checked[entry.position])
		}
		results[i].Lanes = plan.owned
	}
	// Prerequisite point lookups occur only for pending rows. Optional direct-
	// union work is bounded by unique admitted computed revisions; ready history
	// never waits for or requeues an already completed requested build.
	if anyPending {
		q := dbgen.New(tx)
		target, e := q.ReadMaintenanceEvaluationBuild(ctx, dbgen.ReadMaintenanceEvaluationBuildParams{SloID: scope.SloID, TargetRevision: requestedRevision})
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		missingTarget := errors.Is(e, pgx.ErrNoRows)
		needsMask := func(build dbgen.AlmanacMaintenanceBuild, missing bool) (bool, error) {
			if missing {
				return true, nil
			}
			if build.Phase == "complete" || build.Phase == "unavailable" || build.Phase == "cancelled" {
				return false, nil
			}
			health, err := q.ReadMaintenanceEvaluationBuildHealth(ctx, dbgen.ReadMaintenanceEvaluationBuildHealthParams{Kind: "mask", SloID: scope.SloID, TargetRevision: build.TargetRevision, BuildID: build.ID})
			return health == "", err
		}
		needTarget, e := needsMask(target, missingTarget)
		if e != nil {
			return nil, e
		}
		needCompatibility := pendingCompatibility
		if needCompatibility && !missingTarget {
			health, e := q.ReadMaintenanceEvaluationBuildHealth(ctx, dbgen.ReadMaintenanceEvaluationBuildHealthParams{Kind: "compatibility", SloID: scope.SloID, TargetRevision: requestedRevision, BuildID: target.ID})
			if e != nil {
				return nil, e
			}
			needCompatibility = health == ""
		}
		for i, plan := range plans {
			if !results[i].NeedsCalculation {
				continue
			}
			if needTarget {
				results[i].Preparation = append(results[i].Preparation, maintenanceEvaluationPreparation{Kind: "mask", RequestedRevision: requestedRevision})
			}
			if needCompatibility && len(plan.proofs) > 0 {
				results[i].Preparation = append(results[i].Preparation, maintenanceEvaluationPreparation{Kind: "compatibility", RequestedRevision: requestedRevision})
			}
		}
		if !missingTarget && target.Phase == "complete" {
			var pairs []maintenanceEvaluationBuildPair
			revisionByPair := map[maintenanceEvaluationBuildPair]int64{}
			for _, computed := range changedComputed {
				original, e := q.ReadMaintenanceEvaluationBuild(ctx, dbgen.ReadMaintenanceEvaluationBuildParams{SloID: scope.SloID, TargetRevision: computed})
				missingOriginal := errors.Is(e, pgx.ErrNoRows)
				if e != nil && !missingOriginal {
					return nil, e
				}
				// Implicit genesis is the exact empty union. Materialize its build
				// only when a direct comparison is required; saved marks keep their
				// original absent BuildID. Never invent a missing nonzero history.
				if computed == 0 {
					needed, e := needsMask(original, missingOriginal)
					if e != nil {
						return nil, e
					}
					if needed {
						for i, plan := range plans {
							if slices.Contains(plan.changed, computed) {
								results[i].Preparation = append(results[i].Preparation, maintenanceEvaluationPreparation{Kind: "mask", RequestedRevision: 0})
							}
						}
					}
				}
				if missingOriginal {
					continue
				}
				if saved := builds[computed]; saved != "" && saved != original.ID.String() {
					return nil, ErrDerivedFailed
				}
				if original.Phase == "complete" {
					pair := maintenanceEvaluationBuildPair{FirstBuild: original.ID, SecondBuild: target.ID}
					pairs = append(pairs, pair)
					revisionByPair[pair] = computed
				}
			}
			var hints []maintenanceEvaluationPreparation
			if e := prepareEvaluationComparisons(ctx, tx, scope.SloID, requestedRevision, pairs, &hints); e != nil {
				return nil, e
			}
			for i, plan := range plans {
				for _, hint := range hints {
					if hint.Comparison != nil && slices.Contains(plan.changed, revisionByPair[*hint.Comparison]) {
						h := hint
						h.Comparison = new(*hint.Comparison)
						results[i].Preparation = append(results[i].Preparation, h)
					}
				}
			}
		}
	}
	if anyPending {
		return results, errors.Join(ErrDerivedPending, err)
	}
	return results, nil
}

func cloneMaintenanceRetainedLanes(lanes extremaLanes) extremaLanes {
	cloneMark := func(mark extremaMark) extremaMark {
		mark.Labels = slices.Clone(mark.Labels)
		cloneTime := func(at *time.Time) *time.Time {
			if at == nil {
				return nil
			}
			return new(*at)
		}
		mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = cloneTime(mark.FirstObservation), cloneTime(mark.LastObservation), cloneTime(mark.LatestReceipt)
		if mark.Maintenance != nil {
			m := *mark.Maintenance
			if m.Proof != nil {
				m.Proof = new(*m.Proof)
			}
			m.ReusedHours = slices.Clone(m.ReusedHours)
			if m.Coverage != nil {
				c := *m.Coverage
				c.Points = slices.Clone(c.Points)
				m.Coverage = &c
			}
			if m.ExcludedGood != nil {
				m.ExcludedGood = new(*m.ExcludedGood)
			}
			if m.ExcludedTotal != nil {
				m.ExcludedTotal = new(*m.ExcludedTotal)
			}
			mark.Maintenance = &m
		}
		return mark
	}
	cloneLane := func(lane *extremaLane) *extremaLane {
		if lane == nil {
			return nil
		}
		owned := *lane
		owned.States = slices.Clone(lane.States)
		owned.Quality = slices.Clone(lane.Quality)
		owned.Freshness = slices.Clone(lane.Freshness)
		owned.Marks = slices.Clone(lane.Marks)
		for i, mark := range owned.Marks {
			owned.Marks[i] = cloneMark(mark)
		}
		return &owned
	}
	out := extremaLanes{Window: cloneLane(lanes.Window), Attainment: cloneLane(lanes.Attainment), Burn1h: cloneLane(lanes.Burn1h), Burn6h: cloneLane(lanes.Burn6h)}
	if lanes.Compliance != nil {
		c := *lanes.Compliance
		c.Mark = cloneMark(c.Mark)
		out.Compliance = &c
	}
	return out
}
