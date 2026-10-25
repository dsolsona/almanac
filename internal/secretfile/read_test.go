package secretfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRequiresProtectedRegularFileWithinLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("  password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, 12)
	if err != nil || string(got) != "  password\n" {
		t.Fatalf("Read() matched_expected=%t err=%v", string(got) == "  password\n", err)
	}
	if _, err := Read(path, 10); err == nil {
		t.Fatal("oversized file was accepted")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 12); err == nil {
		t.Fatal("group-readable file was accepted")
	}
}

func TestReadInspectsSymlinkTargetAndRejectsInvalidLimit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link, 6); err == nil {
		t.Fatal("unsafe symlink target was accepted")
	}
	for _, limit := range []int64{0, 16385} {
		if _, err := Read(target, limit); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
	}
}
