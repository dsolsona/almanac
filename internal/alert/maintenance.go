package alert

import (
	"slices"
	"time"
	"uuid"
)

// MaintenanceEvidence records the calculation's actual immutable population.
// RequestedRevision remains the snapshot authority; ComputedRevision and BuildID
// identify the population actually read. The pure kernel checks structure only:
// storage must authenticate complete builds, compatibility proofs and source keys.
type MaintenanceEvidence struct {
	RequestedRevision, ComputedRevision int64
	BuildID                             string                    `json:",omitempty"`
	Proof                               *MaintenanceProof         `json:",omitempty"`
	ReusedHours                         []MaintenanceHourEvidence `json:",omitempty"`
}

type MaintenanceProof struct {
	Kind                                                string
	ComputedRevision, RequestedRevision, IndexedThrough int64
	Start, End                                          time.Time
	ComparisonID                                        string `json:",omitempty"`
}

// MaintenanceHourEvidence preserves the original key of a reused partial hour;
// its proof targets the enclosing computed mask, not the requested authority.
type MaintenanceHourEvidence struct {
	SLOID, SLIID, BuildID                                        string
	SLORevision, SLIRevision, SourceGeneration, ComputedRevision int64
	HourStart                                                    time.Time
	Proof                                                        MaintenanceProof
}

func maintenanceUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil() && id.String() == s
}
func maintenanceTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 && t.Nanosecond()%1000 == 0
}

func validMaintenanceProof(p MaintenanceProof, c, r int64, start, end time.Time) bool {
	if c < 0 || r < c || p.ComputedRevision != c || p.RequestedRevision != r || p.IndexedThrough < 0 || !maintenanceTime(p.Start) || !maintenanceTime(p.End) || !p.Start.Before(p.End) || !p.Start.Equal(start) || !p.End.Equal(end) {
		return false
	}
	switch p.Kind {
	case "same_revision":
		return c == r && p.ComparisonID == ""
	case "indexed_absence":
		return c < r && p.IndexedThrough >= r && p.ComparisonID == ""
	case "equal_union":
		return c < r && p.IndexedThrough >= r && maintenanceUUID(p.ComparisonID)
	default:
		return false
	}
}

// ValidateMaintenanceEvidence validates optional saved provenance independently
// of numerical truth. It also serves compacted endpoint readers, which must not
// recalculate historical decisions. Nil preserves legacy format-1 evidence.
func ValidateMaintenanceEvidence(w WindowEvidence) error {
	m := w.Maintenance
	if m == nil {
		return nil
	}
	if m.RequestedRevision != w.Snapshot.ExclusionRevision || m.ComputedRevision < 0 || m.ComputedRevision > m.RequestedRevision || (m.BuildID == "" && m.ComputedRevision != 0) || (m.BuildID != "" && !maintenanceUUID(m.BuildID)) {
		return ErrEvidence
	}
	if m.Proof == nil {
		if m.ComputedRevision != m.RequestedRevision {
			return ErrEvidence
		}
	} else if !validMaintenanceProof(*m.Proof, m.ComputedRevision, m.RequestedRevision, w.Start, w.End) {
		return ErrEvidence
	}
	seen := make(map[time.Time]bool, len(m.ReusedHours))
	for _, h := range m.ReusedHours {
		hour := h.HourStart.UTC()
		if !maintenanceTime(hour) || !hour.Equal(hour.Truncate(time.Hour)) || hour.Before(w.Start) || hour.Add(time.Hour).After(w.End) || seen[hour] || h.SLOID != w.Snapshot.SLOID || !maintenanceUUID(h.SLIID) || !maintenanceUUID(h.BuildID) || h.SLORevision != w.Snapshot.SLORevision || h.SLIRevision != w.Snapshot.SLIRevision || h.SourceGeneration < 0 || h.SourceGeneration > w.Snapshot.SourceGeneration || h.ComputedRevision < 0 || h.ComputedRevision >= m.ComputedRevision {
			return ErrEvidence
		}
		seen[hour] = true
		// For nondividing grids, owned starts (and their last interval end) can
		// extend past the physical hour. The producer authenticates exact cadence.
		if h.Proof.Start.Before(hour) || !h.Proof.Start.Before(hour.Add(time.Hour)) || h.Proof.End.Before(hour.Add(time.Hour)) || !h.Proof.End.Before(hour.Add(2*time.Hour)) || h.Proof.Start.Before(w.Start) || h.Proof.End.After(w.End) || !validMaintenanceProof(h.Proof, h.ComputedRevision, m.ComputedRevision, h.Proof.Start, h.Proof.End) {
			return ErrEvidence
		}
	}
	return nil
}

func cloneMaintenance(m *MaintenanceEvidence) *MaintenanceEvidence {
	if m == nil {
		return nil
	}
	c := *m
	if m.Proof != nil {
		p := *m.Proof
		c.Proof = &p
	}
	c.ReusedHours = slices.Clone(m.ReusedHours)
	return &c
}

// Equal compares provenance values rather than pointer identity. Timestamp
// locations do not change immutable snapshot meaning.
func (m *MaintenanceEvidence) Equal(other *MaintenanceEvidence) bool {
	if m == nil || other == nil {
		return m == other
	}
	if m.RequestedRevision != other.RequestedRevision || m.ComputedRevision != other.ComputedRevision || m.BuildID != other.BuildID || len(m.ReusedHours) != len(other.ReusedHours) {
		return false
	}
	if m.Proof == nil || other.Proof == nil {
		if m.Proof != other.Proof {
			return false
		}
	} else if !sameMaintenanceProof(*m.Proof, *other.Proof) {
		return false
	}
	for i, a := range m.ReusedHours {
		b := other.ReusedHours[i]
		if a.SLOID != b.SLOID || a.SLIID != b.SLIID || a.BuildID != b.BuildID || a.SLORevision != b.SLORevision || a.SLIRevision != b.SLIRevision || a.SourceGeneration != b.SourceGeneration || a.ComputedRevision != b.ComputedRevision || !a.HourStart.Equal(b.HourStart) || !sameMaintenanceProof(a.Proof, b.Proof) {
			return false
		}
	}
	return true
}

func sameMaintenanceProof(a, b MaintenanceProof) bool {
	return a.Kind == b.Kind && a.ComputedRevision == b.ComputedRevision && a.RequestedRevision == b.RequestedRevision && a.IndexedThrough == b.IndexedThrough && a.ComparisonID == b.ComparisonID && a.Start.Equal(b.Start) && a.End.Equal(b.End)
}
