package sloclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
)

func TestExactValuesRejectAnotherGenerationAndPreservePreparationFailure(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	generation := "22222222-2222-2222-2222-222222222222"
	returnedGeneration := generation
	preparing := false
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v1/slos/"+id+"/history/"+generation+"/values" || r.URL.Query().Get("lane") != "budget" || r.URL.Query().Get("limit") != "50" || r.URL.Query().Get("offset") != "0" {
			t.Errorf("unexpected exact-values request: %s %s", r.Method, r.URL)
		}
		if preparing {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"code":"derived_data_pending"}`)
			return
		}
		fmt.Fprintf(w, `{"generation":{"generation_id":%q,"source_generation":"1","definition_revision":"1","measurement_revision":"1","exclusion_revision":"0","calculation_version":"v1","evaluated_through":"2026-10-30T12:00:00Z","published_at":"2026-10-30T12:00:01Z","calculation_lag_seconds":0},"lane":"budget","offset":0,"next_offset":null,"total_values":0,"samples":[],"days":[]}`, returnedGeneration)
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(ops, func(context.Context, *http.Request) error { return nil })
	input := Input{Kind: "slos", Action: "values", ID: id, GenerationID: generation, Lane: "budget", Limit: 50}
	if _, _, err := client.Do(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	returnedGeneration = id
	if _, _, err := client.Do(t.Context(), input); err == nil {
		t.Fatal("CLI accepted values from another generation")
	}
	preparing = true
	_, _, err = client.Do(t.Context(), input)
	failure, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || failure.Code != "derived_data_pending" || failure.Unknown || calls != 3 {
		t.Fatalf("preparation was hidden or replayed: calls=%d, %v", calls, err)
	}
}
