package api

import "testing"

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
