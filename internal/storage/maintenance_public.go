package storage

import (
	"encoding/json/jsontext"
	"strconv"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func maintenancePublicQuality(result slo.WindowResult, quality api.SloOperationalQuality) api.SloOperationalQuality {
	if result.State == "unavailable" || result.State == "not_yet_eligible" || result.State == "not_applicable" {
		return quality
	}
	quality.ExcludedIntervals = new(result.Excluded)
	quality.ExcludedGood, quality.ExcludedTotal = jsontext.Value("null"), jsontext.Value("null")
	if result.Excluded == 0 {
		quality.ExcludedGood, quality.ExcludedTotal = jsontext.Value(`"0"`), jsontext.Value(`"0"`)
	} else if result.ExcludedGood != nil && result.ExcludedTotal != nil {
		quality.ExcludedGood = jsontext.Value(strconv.Quote(*result.ExcludedGood))
		quality.ExcludedTotal = jsontext.Value(strconv.Quote(*result.ExcludedTotal))
	}
	return quality
}

// This projection follows calculator or retained-row authentication. It does not
// establish proof truth; it preserves the original identities already verified.
func (m *retainedMaintenanceInputs) publicProvenance() (*api.SloCalculationProvenance, error) {
	if m == nil || m.UnavailableReason != "" {
		return nil, nil
	}
	if m.SourceGeneration < 0 || m.ComputedRevision < 0 || m.RequestedRevision < m.ComputedRevision || m.ComputedRevision != 0 && m.BuildID == "" || m.ComputedRevision != m.RequestedRevision && m.Proof == nil {
		return nil, ErrDerivedFailed
	}
	out := &api.SloCalculationProvenance{SourceGeneration: strconv.FormatInt(m.SourceGeneration, 10), ComputedRevision: strconv.FormatInt(m.ComputedRevision, 10), RequestedRevision: strconv.FormatInt(m.RequestedRevision, 10)}
	if m.BuildID != "" {
		id, valid := canonicalUUID(m.BuildID)
		if !valid {
			return nil, ErrDerivedFailed
		}
		out.BuildId = new(openapi_types.UUID(id.Bytes))
	}
	if p := m.Proof; p != nil {
		if !validRetainedMaintenanceProof(*p, m.Scope, m.ComputedRevision, m.RequestedRevision, p.Start, p.End) {
			return nil, ErrDerivedFailed
		}
		out.Compatibility = &api.SloMaintenanceCompatibility{Kind: api.SloMaintenanceCompatibilityKind(p.Kind), ComputedRevision: strconv.FormatInt(p.ComputedRevision, 10), RequestedRevision: strconv.FormatInt(p.RequestedRevision, 10), IndexedThrough: strconv.FormatInt(p.IndexedThrough, 10), Start: p.Start, End: p.End}
		if p.ComparisonID != "" {
			id, valid := canonicalUUID(p.ComparisonID)
			if !valid {
				return nil, ErrDerivedFailed
			}
			out.Compatibility.ComparisonId = new(openapi_types.UUID(id.Bytes))
		}
	}
	return out, nil
}

func (batch maintenanceConsumerBatch) provenance(index int) (*api.SloCalculationProvenance, error) {
	if len(batch.input.Requests) != 1 || len(batch.result.Masks) != 1 || index < 0 || index >= len(batch.input.Requests[0].Windows) {
		return nil, ErrDerivedFailed
	}
	mask := batch.result.Masks[0]
	if len(mask.Unavailable) != len(batch.input.Requests[0].Windows) {
		return nil, ErrDerivedFailed
	}
	if mask.Unavailable[index] != "" {
		return nil, nil
	}
	w := batch.input.Requests[0].Windows[index]
	if !ceilSliBoundary(maxTime(w.At.Add(-w.Duration), w.ValidFrom, w.SliValidFrom), w.Cadence).Before(w.At) {
		return nil, nil
	}
	if mask.Computed.Revision != mask.RequestedRevision && len(mask.Proofs) == 0 {
		return nil, ErrDerivedFailed
	}
	m := &retainedMaintenanceInputs{Scope: maintenancePreparationScope{SloID: batch.input.SloID, SliID: batch.input.SliID, DefinitionRevision: batch.input.DefinitionRevision, MeasurementRevision: batch.input.MeasurementRevision}, SourceGeneration: batch.result.SourceGeneration, ComputedRevision: mask.Computed.Revision, RequestedRevision: mask.RequestedRevision}
	if mask.Computed.BuildID.Valid {
		m.BuildID = mask.Computed.BuildID.String()
	}
	if len(mask.Proofs) > 0 {
		if len(mask.Proofs) != len(batch.input.Requests[0].Windows) {
			return nil, ErrDerivedFailed
		}
		m.Proof = new(mask.Proofs[index])
		if !validRetainedMaintenanceProof(*m.Proof, m.Scope, m.ComputedRevision, m.RequestedRevision, w.At.Add(-w.Duration), w.At) {
			return nil, ErrDerivedFailed
		}
	}
	return m.publicProvenance()
}
