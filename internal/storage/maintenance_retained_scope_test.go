package storage

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestMaintenanceRetainedRowAuthority(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"same", "other_slo", "other_sli", "definition", "measurement", "source", "exclusion", "legacy_nonzero", "legacy_zero"} {
		t.Run(kind, func(t *testing.T) {
			mark, input, _ := retainedMaintenanceFixture(t)
			authority := maintenanceRetainedRowAuthority{Scope: mark.Maintenance.Scope, SourceGeneration: 7, RequestedRevision: 1}
			switch kind {
			case "other_slo":
				authority.Scope.SloID = pgtype.UUID{Valid: true}
			case "other_sli":
				authority.Scope.SliID = pgtype.UUID{Valid: true}
			case "definition":
				authority.Scope.DefinitionRevision++
			case "measurement":
				authority.Scope.MeasurementRevision++
			case "source":
				authority.SourceGeneration = 6
			case "exclusion":
				authority.RequestedRevision++
			case "legacy_nonzero":
				mark.Maintenance = nil
			case "legacy_zero":
				mark.Maintenance = nil
				authority.RequestedRevision = 0
			}
			err := mark.validateMaintenanceRowAuthority(input, authority)
			if kind == "same" || kind == "legacy_zero" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("foreign/unstamped retained row became current evidence")
			}
		})
	}
}

func TestMaintenanceRetainedRowBeforeValidity(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	row := maintenanceRetainedRowAuthority{Scope: mark.Maintenance.Scope, SourceGeneration: 7, RequestedRevision: 1}
	mark.Maintenance = nil
	mark.Good, mark.Total, mark.Observed = "0", "0", 0
	mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
	input.ValidFrom = input.At
	if err := mark.validateMaintenanceRowAuthority(input, row); err != nil {
		t.Fatalf("no eligible time acquired a false maintenance requirement: %v", err)
	}
	mark.Observed = 1
	if err := mark.validateMaintenanceRowAuthority(input, row); err == nil {
		t.Fatal("pre-validity mark smuggled an observed population")
	}
}
