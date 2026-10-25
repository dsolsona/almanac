package storage

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum [sha256.Size]byte
}

func LoadMigrations(source fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(source, "migrations")
	if err != nil {
		return nil, fmt.Errorf("storage: read embedded migrations")
	}
	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			return nil, fmt.Errorf("storage: malformed migration filename")
		}
		prefix, description, ok := strings.Cut(name, "_")
		if !ok || description == "" || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("storage: malformed migration filename")
		}
		digits := strings.TrimSuffix(description, ".sql")
		if digits == "" {
			return nil, fmt.Errorf("storage: malformed migration filename")
		}
		for _, digit := range prefix {
			if digit < '0' || digit > '9' {
				return nil, fmt.Errorf("storage: malformed migration filename")
			}
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("storage: malformed migration filename")
		}
		if _, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("storage: duplicate migration version")
		}
		seen[version] = struct{}{}
		data, err := fs.ReadFile(source, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("storage: read embedded migration")
		}
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(data), Checksum: sha256.Sum256(data)})
	}
	slices.SortFunc(migrations, func(a, b Migration) int { return a.Version - b.Version })
	for i, migration := range migrations {
		if migration.Version != i+1 {
			return nil, fmt.Errorf("storage: migration versions must be contiguous from 1")
		}
	}
	return migrations, nil
}
