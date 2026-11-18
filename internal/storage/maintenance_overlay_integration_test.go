//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationMaintenanceOverlayCountsOverflow(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	ctx, database := t.Context(), f.f.databaseURL
	start := f.input("first").Start
	for index, key := range []string{"first", "second", "third", "other-slo", "outside"} {
		input := f.input(key)
		input.Start = start.Add(time.Duration(index) * time.Minute)
		if key == "other-slo" {
			input.Start = start
			input.SloIDs = []string{f.ids[1]}
		}
		if _, err := CreateMaintenance(ctx, database, input); err != nil {
			t.Fatal(err)
		}
	}
	end := start.Add(4 * time.Minute)
	list := ListSloMaintenanceInput{
		ListMaintenanceInput: ListMaintenanceInput{Proof: f.proof, Limit: 2, Now: f.clock},
		SloID:                f.ids[0], Start: start, End: end, IncludeOverflow: true,
	}
	page, err := ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Overflow == nil ||
		page.Overflow.Count != 2 || !page.Overflow.Start.Equal(start.Add(time.Minute)) || !page.Overflow.End.Equal(end) {
		t.Fatalf("counted overflow page %+v, cluster %+v: %v", page, page.Overflow, err)
	}
	list.Cursor = page.NextCursor
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.NextCursor != "" || page.Overflow != nil {
		t.Fatalf("later list page %+v: %v", page, err)
	}
	list.Cursor = ""
	list.Limit = 3
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 3 || page.NextCursor != "" || page.Overflow != nil {
		t.Fatalf("exact-limit page %+v: %v", page, err)
	}
}

func TestIntegrationMaintenanceOverlayCursorAndCurrentScope(t *testing.T) {
	f := newMaintenanceFixture(t)
	input := f.input("first")
	ctx, database := t.Context(), f.f.databaseURL
	first, err := CreateMaintenance(ctx, database, input)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := f.input("second")
	secondInput.Start = input.Start.Add(time.Minute)
	second, err := CreateMaintenance(ctx, database, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	list := ListSloMaintenanceInput{
		ListMaintenanceInput: ListMaintenanceInput{Proof: f.proof, Limit: 1, Now: f.clock},
		SloID:                f.ids[0], Start: first.Start, End: first.End,
	}
	page, err := ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" {
		t.Fatalf("initial page %+v: %v", page, err)
	}
	list.Cursor = page.NextCursor
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor != "" {
		t.Fatalf("next page %+v: %v", page, err)
	}
	fractional := list
	fractional.Cursor = ""
	fractional.Limit = 2
	fractional.Start = first.Start.Add(time.Microsecond)
	fractional.End = second.Start.Add(time.Microsecond)
	fractionalPage, err := ListSloMaintenance(ctx, database, fractional)
	if err != nil || len(fractionalPage.Items) != 2 || fractionalPage.Items[0].ID != first.ID ||
		fractionalPage.Items[1].ID != second.ID || fractionalPage.NextCursor != "" {
		t.Fatalf("fractional overlap page %+v: %v", fractionalPage, err)
	}
	wrongRange := list
	wrongRange.End = list.End.Add(time.Microsecond)
	if _, err = ListSloMaintenance(ctx, database, wrongRange); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("range cursor accepted: %v", err)
	}
	wrongSlo := list
	wrongSlo.SloID = f.ids[1]
	if _, err = ListSloMaintenance(ctx, database, wrongSlo); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("SLO cursor accepted: %v", err)
	}

	edit := input
	edit.ID, edit.Revision, edit.IdempotencyKey = first.ID, first.Revision, "change-scope"
	edit.SloIDs = []string{f.ids[1]}
	if _, err := EditMaintenance(ctx, database, edit); err != nil {
		t.Fatal(err)
	}
	list.Cursor = ""
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("old scope visible after edit: %+v %v", page, err)
	}
	list.SloID = f.ids[1]
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.Items[0].Revision != 2 {
		t.Fatalf("new scope omitted: %+v %v", page, err)
	}
}
