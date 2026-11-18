package api

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/google/uuid"
)

type MaintenanceSelector struct {
	Kind string
	IDs  []string
}

// ParseMaintenanceScope validates the exact scope variant before applying a selector.
func ParseMaintenanceScope(body []byte) (MaintenanceSelector, bool) {
	var members map[string]jsontext.Value
	if err := json.Unmarshal(body, &members); err != nil {
		return MaintenanceSelector{}, false
	}
	var kind string
	if err := json.Unmarshal(members["kind"], &kind); err != nil {
		return MaintenanceSelector{}, false
	}
	var idsField string
	switch kind {
	case "all_services":
		if len(members) != 1 {
			return MaintenanceSelector{}, false
		}
		return MaintenanceSelector{Kind: kind}, true
	case "slos":
		idsField = "slo_ids"
	case "services":
		idsField = "service_ids"
	default:
		return MaintenanceSelector{}, false
	}
	var ids []string
	if len(members) != 2 || json.Unmarshal(members[idsField], &ids) != nil || len(ids) == 0 || len(ids) > 100 {
		return MaintenanceSelector{}, false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil || id.String() != raw {
			return MaintenanceSelector{}, false
		}
		if _, exists := seen[raw]; exists {
			return MaintenanceSelector{}, false
		}
		seen[raw] = struct{}{}
	}
	return MaintenanceSelector{Kind: kind, IDs: ids}, true
}
