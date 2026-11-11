//go:build integration

package storage

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestIntegrationServiceCatalogLifecycle(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	now := f.base.Add(5 * time.Second)
	clock := func() time.Time { return now }
	proof := DatasourceProof{SessionVerifier: f.verifier}
	create := func(name string, metadata *ServiceMetadata, deps []string) ServiceRecord {
		t.Helper()
		record, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name, DisplayName: name, ServiceMetadata: metadata, Dependencies: deps, IdempotencyKey: name, RequestID: name, Now: clock}, false)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	payments := create("payments", nil, nil)
	metadata := ServiceMetadata{Description: "Processes orders", Owner: "Store experience", Contact: "store@example.test", Labels: map[string][]string{"team": {"store", "commerce"}}, Links: []ServiceLink{{Name: "Runbook", URL: "https://docs.example.test/checkout"}}}
	checkout := create("checkout", &metadata, []string{payments.ID})
	if checkout.Owner != "Store experience" || len(checkout.Labels["team"]) != 2 || len(checkout.DependencyServices) != 1 {
		t.Fatalf("metadata/relationship not persisted: %+v", checkout)
	}
	replay := create("checkout", &metadata, []string{payments.ID})
	if replay.ID != checkout.ID {
		t.Fatal("idempotent replay created another service")
	}
	target, _, err := ReadObjective(t.Context(), f.databaseURL, proof, payments.ID, clock, false)
	if err != nil || len(target.UsedBy) != 1 || target.UsedBy[0].ID != checkout.ID {
		t.Fatalf("inverse dependency missing: %+v %v", target, err)
	}
	name := "order-checkout"
	changed, err := UpdateServiceCatalog(t.Context(), f.databaseURL, ServiceUpdateInput{Proof: proof, ID: checkout.ID, Patch: ServicePatch{Name: &name, DisplayName: "Order checkout"}, Version: checkout.Version, RequestID: "rename", Now: clock})
	if err != nil || changed.ID != checkout.ID || changed.Name != name || changed.Owner != metadata.Owner || len(changed.Dependencies) != 1 {
		t.Fatalf("rename lost identity or omitted fields: %+v %v", changed, err)
	}
	if _, err := UpdateServiceCatalog(t.Context(), f.databaseURL, ServiceUpdateInput{Proof: proof, ID: checkout.ID, Patch: ServicePatch{DisplayName: "stale"}, Version: checkout.Version, RequestID: "stale", Now: clock}); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	deps := []string{checkout.ID}
	cycle, err := UpdateServiceCatalog(t.Context(), f.databaseURL, ServiceUpdateInput{Proof: proof, ID: payments.ID, Patch: ServicePatch{DisplayName: "Payments", Dependencies: &deps}, Version: payments.Version, RequestID: "cycle", Now: clock})
	if err != nil {
		t.Fatalf("legitimate cycle rejected: %v", err)
	}
	if err := ArchiveService(t.Context(), f.databaseURL, proof, payments.ID, cycle.Version, "blocked", clock); !errors.Is(err, ErrServiceReferenced) {
		t.Fatalf("referenced service archived: %v", err)
	}
	deps = []string{}
	cycle, err = UpdateServiceCatalog(t.Context(), f.databaseURL, ServiceUpdateInput{Proof: proof, ID: payments.ID, Patch: ServicePatch{DisplayName: "Payments", Dependencies: &deps}, Version: cycle.Version, RequestID: "remove-cycle", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := ArchiveService(t.Context(), f.databaseURL, proof, checkout.ID, changed.Version, "archive", clock); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadObjective(t.Context(), f.databaseURL, proof, checkout.ID, clock, false); !errors.Is(err, ErrObjectiveNotFound) {
		t.Fatalf("archived service remains visible: %v", err)
	}
	if err := ArchiveService(t.Context(), f.databaseURL, proof, payments.ID, cycle.Version, "archive-payments", clock); err != nil {
		t.Fatalf("archived source retained active incoming edge: %v", err)
	}
}

func TestIntegrationServiceCatalogSortAcrossPages(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "catalog-sort", true)
	goodSLO := s.createSLO(t, "sort-met")
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	var goodID string
	if err := s.conn.QueryRow(t.Context(), `SELECT service_id FROM almanac_slo_definitions WHERE slo_id=$1`, goodSLO).Scan(&goodID); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateServiceCatalog(t.Context(), s.f.databaseURL, ServiceUpdateInput{Proof: proof, ID: goodID, Version: 1, Patch: ServicePatch{DisplayName: "Zulu healthy", Owner: new("Bravo")}, RequestID: "owner", Now: s.clock}); err != nil {
		t.Fatal(err)
	}
	create := func(name, display, owner string, dependencies []string) ServiceRecord {
		t.Helper()
		record, _, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name, DisplayName: display, ServiceMetadata: &ServiceMetadata{Owner: owner}, Dependencies: dependencies, IdempotencyKey: name, RequestID: name, Now: s.clock}, false)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	neutral := create("sort-neutral", "Middle neutral", "Delta", nil)
	breached := create("sort-breached", "Alpha breached", "Charlie", []string{goodID, neutral.ID})
	unknown := create("sort-unknown", "Omega unknown", "Alpha", []string{goodID})
	objective := func(name, service, target string) {
		t.Helper()
		start := s.historyStart
		_, _, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name, DisplayName: name, ServiceID: service, SliID: s.sliID, Target: target, RollingWindowSeconds: 3600, EvaluationValidFrom: &start, IdempotencyKey: name, RequestID: name, Now: s.clock}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	objective("sort-breach-one", breached.ID, "0.999")
	objective("sort-breach-two", breached.ID, "0.9999")
	s.drain(t, "sort fixture")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	objective("sort-no-projection", unknown.ID, "0.99")
	zero := []string{goodID, neutral.ID}
	slices.Sort(zero)
	one := []string{goodID, unknown.ID}
	slices.Sort(one)
	wants := map[string][]string{
		"name":             {breached.ID, neutral.ID, unknown.ID, goodID},
		"owner":            {unknown.ID, goodID, breached.ID, neutral.ID},
		"slo_status":       {breached.ID, unknown.ID, goodID, neutral.ID},
		"slo_count":        {neutral.ID, one[0], one[1], breached.ID},
		"dependency_count": {zero[0], zero[1], unknown.ID, breached.ID},
	}
	for key, ascending := range wants {
		for _, order := range []string{"asc", "desc"} {
			input := ServiceCatalogInput{Proof: proof, Sort: key, Order: order, Limit: 1, Now: s.clock}
			var got []string
			for range 5 {
				page, err := ListServiceCatalog(t.Context(), s.f.databaseURL, input)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range page.Services {
					got = append(got, record.ID)
				}
				if page.NextCursor == "" {
					break
				}
				if len(got) == 1 {
					changed := input
					changed.Cursor = page.NextCursor
					changed.Order = map[string]string{"asc": "desc", "desc": "asc"}[order]
					if _, err := ListServiceCatalog(t.Context(), s.f.databaseURL, changed); !errors.Is(err, ErrObjectiveInvalid) {
						t.Fatalf("sort cursor rebound: %v", err)
					}
				}
				input.Cursor = page.NextCursor
			}
			want := slices.Clone(ascending)
			if order == "desc" {
				slices.Reverse(want)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s %s: got %v want %v", key, order, got, want)
			}
		}
	}
}

func TestIntegrationServiceCatalogFilteringAndPermissions(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	clock := func() time.Time { return f.base.Add(5 * time.Second) }
	proof := DatasourceProof{SessionVerifier: f.verifier}
	create := func(name string) ServiceRecord {
		t.Helper()
		v, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: name, DisplayName: name, ServiceMetadata: &ServiceMetadata{Owner: "Commerce", Labels: map[string][]string{"team": {"store"}}}, IdempotencyKey: name, RequestID: name, Now: clock}, false)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	first, second := create("first"), create("second")
	for _, deps := range [][]string{{first.ID}, {second.ID, second.ID}, {"00000000-0000-4000-8000-000000000099"}} {
		if _, err := UpdateServiceCatalog(t.Context(), f.databaseURL, ServiceUpdateInput{Proof: proof, ID: first.ID, Patch: ServicePatch{DisplayName: first.DisplayName, Dependencies: &deps}, Version: first.Version, RequestID: "invalid", Now: clock}); !errors.Is(err, ErrObjectiveInvalid) && !errors.Is(err, ErrObjectiveNotFound) {
			t.Fatalf("invalid dependencies accepted %v: %v", deps, err)
		}
	}
	input := ServiceCatalogInput{Proof: proof, Query: "team:store", Owner: "Commerce", Coverage: "without", Limit: 1, Now: clock}
	page, err := ListServiceCatalog(t.Context(), f.databaseURL, input)
	if err != nil || len(page.Services) != 1 || page.Total != 2 || page.All != 2 || page.WithSlos != 0 || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	input.Cursor = page.NextCursor
	next, err := ListServiceCatalog(t.Context(), f.databaseURL, input)
	if err != nil || len(next.Services) != 1 || next.Services[0].ID == page.Services[0].ID || next.NextCursor != "" {
		t.Fatalf("next page: %+v %v", next, err)
	}
	input.Owner = "Other"
	if _, err := ListServiceCatalog(t.Context(), f.databaseURL, input); !errors.Is(err, ErrObjectiveInvalid) {
		t.Fatalf("cursor filter rebound: %v", err)
	}
	setIntegrationUserRole(t, f, "viewer")
	if _, _, err := ReadObjective(t.Context(), f.databaseURL, proof, first.ID, clock, false); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := UpdateServiceCatalog(t.Context(), f.databaseURL, ServiceUpdateInput{Proof: proof, ID: first.ID, Patch: ServicePatch{DisplayName: "Denied"}, Version: first.Version, RequestID: "denied", Now: clock}); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer update: %v", err)
	}
	if err := ArchiveService(t.Context(), f.databaseURL, proof, first.ID, first.Version, "denied", clock); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer archive: %v", err)
	}
}

func TestIntegrationServiceCatalogArchiveFencesNewReferences(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	clock := func() time.Time { return f.base.Add(5 * time.Second) }
	proof := DatasourceProof{SessionVerifier: f.verifier}
	target, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "target", DisplayName: "Target", IdempotencyKey: "target", RequestID: "target", Now: clock}, false)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	created := make(chan error, 1)
	archived := make(chan error, 1)
	go func() {
		<-start
		_, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "consumer", DisplayName: "Consumer", Dependencies: []string{target.ID}, IdempotencyKey: "consumer", RequestID: "consumer", Now: clock}, false)
		created <- err
	}()
	go func() {
		<-start
		archived <- ArchiveService(t.Context(), f.databaseURL, proof, target.ID, target.Version, "archive", clock)
	}()
	close(start)
	createErr, archiveErr := <-created, <-archived
	if createErr == nil {
		if !errors.Is(archiveErr, ErrServiceReferenced) {
			t.Fatalf("created reference lost: %v", archiveErr)
		}
	} else if !errors.Is(createErr, ErrObjectiveNotFound) || archiveErr != nil {
		t.Fatalf("archive/create: %v / %v", archiveErr, createErr)
	}
}

func TestIntegrationServiceCatalogSummaryTruthAndSloBlocker(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "catalog-summary", true)
	sloID := s.createSLO(t, "catalog-summary")
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	var serviceID string
	if err := s.conn.QueryRow(t.Context(), `SELECT service_id FROM public.almanac_slo_definitions WHERE slo_id=$1 AND revision=1`, sloID).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	read := func() ServiceRecord {
		t.Helper()
		v, _, err := ReadObjective(t.Context(), s.f.databaseURL, proof, serviceID, s.clock, false)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := read(); v.SloSummary.State != "unknown" || v.SloSummary.Unknown != 1 {
		t.Fatalf("missing projection became success: %+v", v.SloSummary)
	}
	s.drain(t, "catalog summary")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	if v := read(); v.SloSummary.State != "on_track" || v.SloSummary.OnTrack != 1 {
		t.Fatalf("complete 99%% projection: %+v", v.SloSummary)
	}
	var refs *ServiceReferencesError
	if err := ArchiveService(t.Context(), s.f.databaseURL, proof, serviceID, 1, "blocked-by-slo", s.clock); !errors.As(err, &refs) || len(refs.Slos) != 1 || refs.Slos[0] != sloID {
		t.Fatalf("SLO archive blocker: %v", err)
	}
	s.now = s.now.Add(time.Minute)
	if v := read(); v.SloSummary.State != "unknown" || v.SloSummary.OnTrack != 0 {
		t.Fatalf("stale projection became success: %+v", v.SloSummary)
	}
}

func TestIntegrationServiceCatalogRetriesConcurrentSessionTouch(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	clock := func() time.Time { return f.base.Add(5 * time.Second) }
	proof := DatasourceProof{SessionVerifier: f.verifier}
	service, _, err := CreateObjective(t.Context(), f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "catalog-touch", DisplayName: "Catalog touch", IdempotencyKey: "catalog-touch", RequestID: "catalog-touch", Now: clock}, false)
	if err != nil {
		t.Fatal(err)
	}
	locker := integrationConnect(t, f.databaseURL)
	observer := integrationConnect(t, f.databaseURL)
	tx, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `SELECT id FROM public.almanac_users WHERE id=(SELECT user_id FROM public.almanac_sessions WHERE verifier=$1) FOR UPDATE`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_sessions SET last_seen_at=last_seen_at + interval '1 microsecond' WHERE verifier=$1`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	type result struct {
		page ServiceCatalogPage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := ListServiceCatalog(t.Context(), f.databaseURL, ServiceCatalogInput{Proof: proof, Sort: "name", Order: "asc", Limit: 1, Now: clock})
		done <- result{page, err}
	}()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_users%")
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || len(got.page.Services) != 1 || got.page.Services[0].ID != service.ID {
		t.Fatalf("catalog after concurrent session touch: %+v, %v", got.page, got.err)
	}
}
