package api

import (
	"net/http"
	"slices"
	"testing"
	"uuid"
)

func TestMaintenanceScopeRejectsAmbiguousSelectors(t *testing.T) {
	t.Parallel()
	const id = "aaf71d39-d21c-4e8c-bb0e-736a86647677"
	for _, tc := range []struct {
		body, kind string
	}{
		{`{"kind":"all_services"}`, "all_services"},
		{`{"kind":"services","service_ids":["` + id + `"]}`, "services"},
		{`{"kind":"slos","slo_ids":["` + id + `"]}`, "slos"},
		{`{}`, ""},
		{`null`, ""},
		{`{"kind":"future"}`, ""},
		{`{"kind":"slos","slo_ids":[]}`, ""},
		{`{"kind":"slos","slo_ids":["00000000-0000-0000-0000-000000000000"]}`, ""},
		{`{"kind":"services","service_ids":["AAF71D39-D21C-4E8C-BB0E-736A86647677"]}`, ""},
		{`{"kind":"services","service_ids":["` + id + `","` + id + `"]}`, ""},
		{`{"kind":"all_services","slo_ids":null}`, ""},
		{`{"kind":"slos","service_ids":["` + id + `"],"slo_ids":["` + id + `"]}`, ""},
		{`{"kind":"slos","kind":"all_services"}`, ""},
	} {
		scope, valid := ParseMaintenanceScope([]byte(tc.body))
		if valid != (tc.kind != "") || scope.Kind != tc.kind {
			t.Errorf("ParseMaintenanceScope(%s) = %+v, %t, want kind %q", tc.body, scope, valid, tc.kind)
		}
		if (tc.kind == "slos" || tc.kind == "services") && (len(scope.IDs) != 1 || scope.IDs[0] != id) {
			t.Errorf("parsed IDs = %q", scope.IDs)
		}
	}
}

func TestMaintenanceScopeMergePreservesExclusiveSelector(t *testing.T) {
	t.Parallel()
	const first = "aaf71d39-d21c-4e8c-bb0e-736a86647677"
	const second = "d5c7322d-bbc3-4917-98bd-42c6f390fb8a"
	firstID, err := uuid.Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := uuid.Parse(second)
	if err != nil {
		t.Fatal(err)
	}
	var scope MaintenanceScope
	for _, step := range []struct {
		merge func() error
		kind  string
		ids   []string
	}{
		{func() error {
			return scope.MergeMaintenanceServiceScope(MaintenanceServiceScope{ServiceIds: []ServiceId{ServiceId(firstID)}})
		}, "services", []string{first}},
		{func() error {
			return scope.MergeMaintenanceServiceScope(MaintenanceServiceScope{ServiceIds: []ServiceId{ServiceId(secondID)}})
		}, "services", []string{second}},
		{func() error { return scope.MergeMaintenanceAllServicesScope(MaintenanceAllServicesScope{}) }, "all_services", nil},
		{func() error {
			return scope.MergeMaintenanceSloScope(MaintenanceSloScope{SloIds: []SloId{SloId(firstID)}})
		}, "slos", []string{first}},
		{func() error {
			return scope.MergeMaintenanceServiceScope(MaintenanceServiceScope{ServiceIds: []ServiceId{ServiceId(secondID)}})
		}, "services", []string{second}},
		{func() error {
			return scope.MergeMaintenanceSloScope(MaintenanceSloScope{SloIds: []SloId{SloId(secondID)}})
		}, "slos", []string{second}},
	} {
		if err := step.merge(); err != nil {
			t.Fatal(err)
		}
		data, err := scope.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		got, valid := ParseMaintenanceScope(data)
		if !valid || got.Kind != step.kind || !slices.Equal(got.IDs, step.ids) {
			t.Errorf("merge to %s = %s, want exact selector with IDs %v", step.kind, data, step.ids)
		}
	}
}

func TestScopedMaintenanceRequestsExposeRequiredAuthHeaders(t *testing.T) {
	t.Parallel()
	const origin = "https://example.test"
	const token = "session-csrf-token"
	create, err := NewCreateScopedMaintenanceRequestWithBody(origin, &CreateScopedMaintenanceParams{
		Origin: origin, XCSRFToken: token, IdempotencyKey: "request-key",
	}, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := NewPreviewMaintenanceScopeRequestWithBody(origin, &PreviewMaintenanceScopeParams{
		Origin: origin, XCSRFToken: token,
	}, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*http.Request{create, preview} {
		if !slices.Equal(req.Header.Values("Origin"), []string{origin}) ||
			!slices.Equal(req.Header.Values("X-CSRF-Token"), []string{token}) {
			t.Errorf("%s has wrong auth headers: %v", req.URL.Path, req.Header)
		}
	}
}
