package storage

import (
	"strings"
	"testing"
)

func TestReadInitialOwnerStateFailsClosedAndRedactsUnavailableDatabase(t *testing.T) {
	databaseURL := "postgres://almanac:synthetic-db-secret@127.0.0.1:1/almanac?sslmode=disable"
	state, err := ReadInitialOwnerState(t.Context(), databaseURL)
	if err == nil || state != (InitialOwnerState{}) {
		t.Fatalf("unavailable database read = %+v, %v; want zero state and error", state, err)
	}
	if strings.Contains(err.Error(), "synthetic-db-secret") || strings.Contains(err.Error(), databaseURL) {
		t.Fatalf("database read error disclosed connection secret: %v", err)
	}
}
