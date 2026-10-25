package storage

import (
	"crypto/sha256"
	"testing"
	"testing/fstest"
)

func TestLoadMigrationsRequiresContiguousPositiveVersions(t *testing.T) {
	_, err := LoadMigrations(fstest.MapFS{
		"migrations/0001_installation.sql": {Data: []byte("SELECT 1")},
		"migrations/0003_later.sql":        {Data: []byte("SELECT 3")},
	})
	if err == nil {
		t.Fatal("LoadMigrations accepted a gap in migration versions")
	}
}

func TestLoadMigrationsRejectsMalformedAndDuplicateVersions(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"malformed": {"migrations/latest.sql": {Data: []byte("SELECT 1")}},
		"duplicate": {
			"migrations/0001_installation.sql": {Data: []byte("SELECT 1")},
			"migrations/1_duplicate.sql":       {Data: []byte("SELECT 2")},
		},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadMigrations(files); err == nil {
				t.Fatal("LoadMigrations accepted invalid migration names")
			}
		})
	}
}

func TestLoadMigrationsReturnsOrderedChecksums(t *testing.T) {
	first := []byte("SELECT 1")
	second := []byte("SELECT 2")
	got, err := LoadMigrations(fstest.MapFS{
		"migrations/0002_second.sql": {Data: second},
		"migrations/0001_first.sql":  {Data: first},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("migration versions = %v", versions(got))
	}
	if got[0].Checksum != sha256.Sum256(first) || got[1].Checksum != sha256.Sum256(second) {
		t.Fatal("migration checksum does not match embedded SQL")
	}
}

func versions(migrations []Migration) []int {
	got := make([]int, len(migrations))
	for i, migration := range migrations {
		got[i] = migration.Version
	}
	return got
}
