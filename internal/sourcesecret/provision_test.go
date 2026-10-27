package sourcesecret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionCreatesLoadableOwnerOnlyKeysetAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-keyset.json")
	if err := Provision(path, testKeysetID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("provisioned file mode = %v, want regular 0600", info.Mode())
	}
	if _, err := Load(path, testKeysetID); err != nil {
		t.Fatalf("provisioned keyset did not load: %v", err)
	}
	if err := Provision(path, testKeysetID); !errors.Is(err, ErrProvisioning) {
		t.Fatalf("second provisioning error = %v, want already-present failure", err)
	}
}

func TestProvisionRejectsInvalidKeysetIDWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-keyset.json")
	if err := Provision(path, "not-a-uuid"); !errors.Is(err, ErrProvisioning) {
		t.Fatalf("invalid ID error = %v, want provisioning failure", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid ID created a file: stat error = %v", err)
	}
}
