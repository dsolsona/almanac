package storage

import (
	"strings"
	"testing"
)

func TestServiceCatalogMetadataValidation(t *testing.T) {
	valid := ServiceMetadata{Description: "Processes customer orders.\nSee the runbook.", Owner: "Store experience", Contact: "#store-support", Labels: map[string][]string{"team": {"store", "commerce"}}, Links: []ServiceLink{{Name: "Runbook", URL: "https://docs.example.test/checkout"}}}
	if !validServiceMetadata(valid) {
		t.Fatal("ordinary optional service metadata rejected")
	}
	if !validServiceMetadata(ServiceMetadata{}) {
		t.Fatal("legacy two-field service create must remain valid")
	}
	for name, value := range map[string]ServiceMetadata{
		"credential-bearing URL": {Links: []ServiceLink{{Name: "Runbook", URL: "https://user:password@example.test"}}},
		"script URL":             {Links: []ServiceLink{{Name: "Runbook", URL: "javascript:alert(1)"}}},
		"relative URL":           {Links: []ServiceLink{{Name: "Runbook", URL: "/runbook"}}},
		"empty link name":        {Links: []ServiceLink{{URL: "https://example.test"}}},
		"duplicate label value":  {Labels: map[string][]string{"team": {"store", "store"}}},
		"invalid label key":      {Labels: map[string][]string{"team name": {"store"}}},
		"empty label values":     {Labels: map[string][]string{"team": {}}},
		"empty label value":      {Labels: map[string][]string{"team": {""}}},
		"oversized owner":        {Owner: strings.Repeat("x", 201)},
		"oversized description":  {Description: strings.Repeat("x", 4001)},
		"control character":      {Contact: "store\x00@example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			if validServiceMetadata(value) {
				t.Fatal("unsafe or invalid metadata accepted")
			}
		})
	}
}
