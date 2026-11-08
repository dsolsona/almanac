package storage

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestServiceSortCursorRejectsMalformedAndReboundPositions(t *testing.T) {
	position := serviceSortCursor{Version: 1, Filter: "bound", Number: 3, Text: "commerce", ID: "00000000-0000-4000-8000-000000000001"}
	encoded, err := encodeServiceSortCursor(position)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeServiceSortCursor(encoded, "bound"); err != nil || got != position {
		t.Fatalf("cursor round trip: %v %v", got, err)
	}
	for _, value := range []string{encoded + "=", "!", strings.Repeat("a", 2049), base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"filter":"bound","number":0,"text":"","id":"00000000-0000-4000-8000-000000000001","unexpected":true}`))} {
		if _, err := decodeServiceSortCursor(value, "bound"); err == nil {
			t.Fatal("malformed cursor accepted")
		}
	}
	if _, err := decodeServiceSortCursor(encoded, "other-sort"); err == nil {
		t.Fatal("cursor rebound to another ordering")
	}
}

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
