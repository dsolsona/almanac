//go:build integration

package storage

import (
	"errors"
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
