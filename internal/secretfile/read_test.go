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

func TestReadAcceptsExactMaximumAndRejectsOneByteOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maximum")
	if err := os.WriteFile(path, make([]byte, 16384), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path, 16384); err != nil || len(got) != 16384 {
		t.Fatalf("exact private-file limit length=%d err=%v", len(got), err)
	}
	if err := os.WriteFile(path, make([]byte, 16385), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 16384); err == nil {
		t.Fatal("private file one byte over the limit was accepted")
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

func TestReadCertificateAllowsPublicModeAndChecksOpenedTarget(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "chain.pem"), filepath.Join(dir, "link.pem")
	content := []byte("public certificate chain")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCertificate(link)
	if err != nil || string(got) != string(content) {
		t.Fatalf("ReadCertificate matched_expected=%t err=%v", string(got) == string(content), err)
	}
	if err := os.WriteFile(target, make([]byte, 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCertificate(target); err != nil || len(got) != 64<<10 {
		t.Fatalf("exact certificate limit length=%d err=%v", len(got), err)
	}
	if err := os.WriteFile(target, make([]byte, (64<<10)+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCertificate(target); err == nil {
		t.Fatal("oversized certificate chain was accepted")
	}
}
