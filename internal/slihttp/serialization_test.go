package slihttp

import (
	"encoding/json/v2"
	"testing"

	"github.com/dsolsona/almanac/internal/storage"
)

// A newly created SLI can have no eligible intervals. Its list still carries a
// valid empty projection; omitempty must not erase the empty array in JSON v2.
func TestSliEmptyRecentMeasurementsSerialization(t *testing.T) {
	t.Parallel()
	const id = "00000000-0000-4000-8000-000000000001"
	record := storage.SliRecord{ID: id, DatasourceID: id, AgentID: id}
	for _, list := range []bool{false, true} {
		name := "detail"
		if list {
			name = "list"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := record
			if list {
				item.ListProjection = &storage.SliListProjection{}
			}
			encoded, err := json.Marshal(sliDTO(item))
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			recent, present := decoded["recent_measurements"]
			if !list {
				if present {
					t.Fatalf("detail unexpectedly contains a list projection: %v", recent)
				}
				return
			}
			measurements, valid := recent.([]any)
			if !present || !valid || len(measurements) != 0 {
				t.Fatalf("empty list projection must contain [], got present=%v value=%v", present, recent)
			}
		})
	}
}
