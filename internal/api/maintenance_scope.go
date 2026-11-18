package api

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

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

// maintenanceScope backs the generated MaintenanceScope alias. Every selector
// variant supplies all of its fields, so merge methods replace the payload.
type maintenanceScope struct {
	union jsontext.Value
}

func (s maintenanceScope) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.union)
}

func (s *maintenanceScope) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &s.union)
}

func (s *maintenanceScope) set(v any) error {
	data, err := json.Marshal(v, json.FormatNilSliceAsNull(true))
	if err == nil {
		s.union = data
	}
	return err
}

func decodeMaintenanceScope[T any](data jsontext.Value) (T, error) {
	var scope T
	err := json.Unmarshal(data, &scope)
	return scope, err
}

func (s maintenanceScope) AsMaintenanceSloScope() (MaintenanceSloScope, error) {
	return decodeMaintenanceScope[MaintenanceSloScope](s.union)
}

func (s *maintenanceScope) FromMaintenanceSloScope(scope MaintenanceSloScope) error {
	scope.Kind = "slos"
	return s.set(scope)
}

func (s *maintenanceScope) MergeMaintenanceSloScope(scope MaintenanceSloScope) error {
	return s.FromMaintenanceSloScope(scope)
}

func (s maintenanceScope) AsMaintenanceServiceScope() (MaintenanceServiceScope, error) {
	return decodeMaintenanceScope[MaintenanceServiceScope](s.union)
}

func (s *maintenanceScope) FromMaintenanceServiceScope(scope MaintenanceServiceScope) error {
	scope.Kind = "services"
	return s.set(scope)
}

func (s *maintenanceScope) MergeMaintenanceServiceScope(scope MaintenanceServiceScope) error {
	return s.FromMaintenanceServiceScope(scope)
}

func (s maintenanceScope) AsMaintenanceAllServicesScope() (MaintenanceAllServicesScope, error) {
	return decodeMaintenanceScope[MaintenanceAllServicesScope](s.union)
}

func (s *maintenanceScope) FromMaintenanceAllServicesScope(scope MaintenanceAllServicesScope) error {
	scope.Kind = "all_services"
	return s.set(scope)
}

func (s *maintenanceScope) MergeMaintenanceAllServicesScope(scope MaintenanceAllServicesScope) error {
	return s.FromMaintenanceAllServicesScope(scope)
}

func (s maintenanceScope) Discriminator() (string, error) {
	var scope struct {
		Kind string `json:"kind"`
	}
	err := json.Unmarshal(s.union, &scope)
	return scope.Kind, err
}

func (s maintenanceScope) ValueByDiscriminator() (any, error) {
	kind, err := s.Discriminator()
	if err != nil {
		return nil, err
	}
	switch kind {
	case "slos":
		return s.AsMaintenanceSloScope()
	case "services":
		return s.AsMaintenanceServiceScope()
	case "all_services":
		return s.AsMaintenanceAllServicesScope()
	default:
		return nil, errors.New("unknown discriminator value: " + kind)
	}
}
