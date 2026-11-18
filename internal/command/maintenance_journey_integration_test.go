//go:build integration

package command

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func verifyMaintenanceHTTP(t *testing.T, client *http.Client, baseURL, session string, slo api.Slo) {
	t.Helper()
	const path = "/api/v1/maintenance"
	var csrf string
	call := func(method, target, key, tag string, body any, status int) (*http.Response, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(t.Context(), method, baseURL+target, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: session})
		req.Header.Set("Origin", baseURL)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if tag != "" {
			req.Header.Set("If-Match", tag)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: expected %d, got %d (%s), error %v", method, target, status, resp.StatusCode, data, err)
		}
		return resp, data
	}
	decode := func(data []byte) api.MaintenanceWindow {
		t.Helper()
		var value api.MaintenanceWindow
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	const content = "Replace the database replicas"
	cadence := time.Duration(slo.CadenceSeconds) * time.Second
	start := time.Now().UTC().Truncate(cadence).Add(10 * cadence)
	end := start.Add(2 * cadence)
	input := api.MaintenanceWrite{Title: "Database upgrade", Reason: content, Start: start, End: end}
	input.SloIds = append(input.SloIds, slo.Id)
	call(http.MethodPost, path, "maint-create", "", input, 403)
	_, contextBody := call(http.MethodPost, "/api/v1/auth/context", "", "", struct{}{}, 200)
	var auth api.AuthResponse
	if err := json.Unmarshal(contextBody, &auth); err != nil || auth.CsrfToken == "" {
		t.Fatal("missing authenticated CSRF token")
	}
	csrf = auth.CsrfToken
	misaligned := input
	misaligned.Start = start.Add(time.Second)
	_, data := call(http.MethodPost, path, "maint-align", "", misaligned, 400)
	var failure api.Problem
	if err := json.Unmarshal(data, &failure); err != nil || failure.Code != "maintenance_alignment" || failure.BlockingSloIds == nil {
		t.Fatalf("alignment error lost typed evidence: %s", data)
	}

	response, data := call(http.MethodPost, path, "maint-create", "", input, 201)
	created := decode(data)
	detail, tag := response.Header.Get("Location"), response.Header.Get("ETag")
	if detail != path+"/"+created.Id.String() || tag == "" || created.Revision != "1" {
		t.Fatalf("incorrect create identity or headers: %s %s", detail, tag)
	}
	_, data = call(http.MethodPost, path, "maint-create", "", input, 201)
	if replay := decode(data); replay.Id != created.Id || replay.Revision != created.Revision {
		t.Fatal("create replay allocated a new revision")
	}
	overlay := func(from, to time.Time, count int) {
		t.Helper()
		q := url.Values{"start": {from.Format(time.RFC3339)}, "end": {to.Format(time.RFC3339)}}
		_, result := call(http.MethodGet, "/api/v1/slos/"+slo.Id.String()+"/maintenance?"+q.Encode(), "", "", nil, 200)
		var page api.MaintenancePage
		if err := json.Unmarshal(result, &page); err != nil || len(page.Items) != count ||
			(count != 0 && (page.Items[0].Id != created.Id || !page.Items[0].Start.Equal(start) || !page.Items[0].End.Equal(end))) {
			t.Fatalf("wrong overlay results for %s: %s", q.Encode(), result)
		}
	}
	overlay(start.Add(cadence), end.Add(cadence), 1)
	overlay(end, end.Add(cadence), 0)
	input.Reason = "Use the delayed rollout"
	response, data = call(http.MethodPut, detail, "maint-edit", tag, input, 200)
	edited := decode(data)
	if edited.Revision != "2" || edited.Reason != input.Reason {
		t.Fatal("edit did not create the expected revision")
	}
	_, data = call(http.MethodPut, detail, "maint-edit-stale", tag, input, 409)
	failure = api.Problem{}
	if err := json.Unmarshal(data, &failure); err != nil || failure.CurrentRevision == nil || *failure.CurrentRevision != "2" {
		t.Fatalf("stale write omitted current revision: %s", data)
	}
	tag = response.Header.Get("ETag")
	_, data = call(http.MethodPost, detail+"/cancel", "maint-cancel", tag, api.MaintenanceReason{Reason: "Postpone rollout"}, 200)
	if cancelled := decode(data); cancelled.Revision != "3" || !cancelled.Tombstone || cancelled.Operation != "cancel" {
		t.Fatal("cancellation did not retain an audited tombstone")
	}
	overlay(start, end, 0)
	_, data = call(http.MethodGet, detail+"?revision=1", "", "", nil, 200)
	if historic := decode(data); historic.Id != created.Id || historic.Revision != "1" || historic.Reason != content || historic.Tombstone {
		t.Fatal("historical revision changed")
	}
	_, data = call(http.MethodGet, path+"?limit=1", "", "", nil, 200)
	var listing api.MaintenancePage
	if err := json.Unmarshal(data, &listing); err != nil || len(listing.Items) != 1 || listing.Items[0].Id != created.Id || !listing.Items[0].Tombstone {
		t.Fatalf("general list omitted the cancelled window: %s", data)
	}
	active := input
	active.Start = time.Now().UTC().Truncate(cadence)
	active.End = active.Start.Add(10 * cadence)
	response, data = call(http.MethodPost, path, "maint-active", "", active, 201)
	activeRecord := decode(data)
	activePath, activeTag := response.Header.Get("Location"), response.Header.Get("ETag")
	effectiveEnd := active.Start.Add(5 * cadence)
	response, data = call(http.MethodPost, activePath+"/end", "maint-end", activeTag, api.MaintenanceEnd{End: effectiveEnd, Reason: "Work finishing early"}, 200)
	if shortened := decode(data); !shortened.Start.Equal(active.Start) || !shortened.End.Equal(effectiveEnd) || shortened.Revision != "2" {
		t.Fatalf("prospective end did not preserve start: %+v", shortened)
	}
	_, data = call(http.MethodPost, activePath+"/withdraw", "maint-withdraw", response.Header.Get("ETag"), api.MaintenanceReason{Reason: "Maintenance was not required"}, 200)
	if withdrawn := decode(data); !withdrawn.Tombstone || withdrawn.Id != activeRecord.Id || withdrawn.Revision != "3" {
		t.Fatalf("withdrawal omitted tombstone identity or revision: %+v", withdrawn)
	}

	call(http.MethodPost, path, "maint-cluster-first", "", input, 201)
	call(http.MethodPost, path, "maint-cluster-second", "", input, 201)
	clusterStart := start.Add(time.Microsecond)
	clusterEnd := end.Add(-time.Microsecond)
	q := url.Values{"start": {clusterStart.Format(time.RFC3339Nano)}, "end": {clusterEnd.Format(time.RFC3339Nano)},
		"limit": {"1"}, "include_overflow": {"true"}}
	_, data = call(http.MethodGet, "/api/v1/slos/"+slo.Id.String()+"/maintenance?"+q.Encode(), "", "", nil, 200)
	var clustered api.MaintenancePage
	if err := json.Unmarshal(data, &clustered); err != nil || len(clustered.Items) != 1 ||
		clustered.NextCursor == nil || clustered.Overflow == nil || clustered.Overflow.Count != "2" ||
		!clustered.Overflow.Start.Equal(clusterStart) || !clustered.Overflow.End.Equal(clusterEnd) {
		t.Fatalf("counted maintenance overflow missing from authenticated response: %s", data)
	}

	const scopedPath = "/api/v1/scoped-maintenance"
	var scopes [3]api.MaintenanceScope
	if err := scopes[0].FromMaintenanceSloScope(api.MaintenanceSloScope{Kind: "slos", SloIds: input.SloIds}); err != nil {
		t.Fatal(err)
	}
	if err := scopes[1].FromMaintenanceServiceScope(api.MaintenanceServiceScope{Kind: "services", ServiceIds: []api.ServiceId{slo.ServiceId}}); err != nil {
		t.Fatal(err)
	}
	if err := scopes[2].FromMaintenanceAllServicesScope(api.MaintenanceAllServicesScope{Kind: "all_services"}); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"slos", "services", "all_services"} {
		_, data := call(http.MethodPost, scopedPath+"/preview?limit=200", "", "", api.MaintenanceScopePreview{Scope: scopes[i]}, 200)
		var preview api.MaintenanceImpactPage
		if err := json.Unmarshal(data, &preview, json.RejectUnknownMembers(true)); err != nil || !preview.Ready || preview.Total == "0" {
			t.Fatalf("%s preview unavailable: %s (%v)", kind, data, err)
		}
		found := false
		for _, item := range preview.Items {
			if item.SloId == slo.Id && item.ServiceId == slo.ServiceId && item.Start == nil && item.End == nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s preview missing the objective and service: %s", kind, data)
		}

		scopedStart := start.Truncate(time.Hour).Add(24 * time.Hour)
		req := api.ScopedMaintenanceWrite{Title: input.Title, Reason: input.Reason,
			Start: scopedStart, End: scopedStart.Add(time.Hour), Scope: scopes[i]}
		response, data := call(http.MethodPost, scopedPath, "scoped-"+kind, "", req, 201)
		var stored api.ScopedMaintenanceWindow
		if err := json.Unmarshal(data, &stored, json.RejectUnknownMembers(true)); err != nil || stored.Revision != "1" {
			t.Fatalf("%s create did not return the current window: %s (%v)", kind, data, err)
		}
		location := scopedPath + "/" + stored.Id.String()
		if response.Header.Get("Location") != location {
			t.Fatalf("%s create returned incorrect Location: %s", kind, data)
		}
		var wantIDs []string
		switch kind {
		case "slos":
			wantIDs = []string{slo.Id.String()}
		case "services":
			wantIDs = []string{slo.ServiceId.String()}
		}
		_, data = call(http.MethodGet, location+"?revision=1", "", "", nil, 200)
		var saved api.ScopedMaintenanceWindow
		if err := json.Unmarshal(data, &saved, json.RejectUnknownMembers(true)); err != nil {
			t.Fatal(err)
		}
		for _, window := range []api.ScopedMaintenanceWindow{stored, saved} {
			gotScopeJSON, err := window.Scope.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			gotScope, valid := api.ParseMaintenanceScope(gotScopeJSON)
			if !valid || gotScope.Kind != kind || !slices.Equal(gotScope.IDs, wantIDs) {
				t.Fatalf("%s selector = %s, want kind %q and IDs %v", kind, gotScopeJSON, kind, wantIDs)
			}
		}
		if kind != "slos" {
			call(http.MethodGet, path+"/"+stored.Id.String(), "", "", nil, 404)
		}
		call(http.MethodGet, response.Header.Get("Location")+"/impact?revision=1&limit=1", "", "", nil, 200)
		call(http.MethodPost, response.Header.Get("Location")+"/cancel", "scoped-cancel-"+kind,
			response.Header.Get("ETag"), api.MaintenanceReason{Reason: "Reschedule"}, 200)
	}

}
