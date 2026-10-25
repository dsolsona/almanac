package storage

import (
	"crypto/sha256"
	"testing"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestCompareHistoryFindsGapsAfterNewerSchemaRows(t *testing.T) {
	sql := "SELECT 1"
	migrations := []Migration{{Version: 1, Name: "0001_initial.sql", SQL: sql, Checksum: sha256.Sum256([]byte(sql))}}
	history := []dbgen.AlmanacSchemaMigration{
		{Version: 1, Filename: migrations[0].Name, Checksum: migrations[0].Checksum[:]},
		{Version: 2, Filename: "0002_future.sql", Checksum: make([]byte, sha256.Size)},
		{Version: 4, Filename: "0004_gap.sql", Checksum: make([]byte, sha256.Size)},
	}
	state, _ := compareHistory(history, migrations)
	if state != CompatibilityInconsistent {
		t.Fatalf("history state = %s, want inconsistent", state)
	}
}
