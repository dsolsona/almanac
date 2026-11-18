//go:build integration

package storage

import (
	"errors"
	"slices"
	"testing"
)

func TestIntegrationLegacyMaintenanceRevisionSurvivesScopeChange(t *testing.T) {
	f := newMaintenanceFixture(t)
	original := f.input("legacy-created")
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, original)
	if err != nil {
		t.Fatal(err)
	}
	scoped := f.input("edit-scoped")
	scoped.Scoped, scoped.ID, scoped.Revision = true, window.ID, window.Revision
	scoped.ScopeKind, scoped.SloIDs = MaintenanceScopeAllServices, nil
	newer, err := EditMaintenance(t.Context(), f.f.databaseURL, scoped)
	if err != nil || newer.ScopeKind != MaintenanceScopeAllServices {
		t.Fatalf("scoped edit = %+v, %v", newer, err)
	}

	read := ReadMaintenanceInput{Proof: f.proof, ID: window.ID, Revision: window.Revision, Now: f.clock}
	saved, err := ReadMaintenance(t.Context(), f.f.databaseURL, read)
	if err != nil || saved.Revision != 1 || !slices.Equal(saved.SloIDs, original.SloIDs) {
		t.Fatalf("explicit historical read = %+v, %v", saved, err)
	}
	for _, revision := range []int64{0, newer.Revision} {
		read.Revision = revision
		if _, err := ReadMaintenance(t.Context(), f.f.databaseURL, read); !errors.Is(err, ErrMaintenanceNotFound) {
			t.Fatalf("legacy read revision %d of broad selector: %v", revision, err)
		}
	}
}
