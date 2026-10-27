package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceKeysetProvisioningWritesProtectedFileAndRedactedResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-keyset.json")
	keysetID := "00000000-0000-4000-8000-000000000001"
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), []string{"secrets", "provision-source-keyset", "--file=" + path, "--keyset-id=" + keysetID, "--output=json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 || stderr.Len() != 0 || stdout.String() != "{\"schema_version\":1,\"status\":\"provisioned\",\"keyset_id\":\""+keysetID+"\"}\n" {
		t.Fatalf("provision result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("provisioned file info=%v, error=%v", info, err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(t.Context(), []string{"secrets", "provision-source-keyset", "--file=" + path, "--keyset-id=" + keysetID}, &stdout, &stderr, BuildInfo{})
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "provisioning failed") || strings.Contains(stderr.String(), path) {
		t.Fatalf("repeat provisioning result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSourceKeysetProvisioningRejectsMissingArgumentsWithoutLeaking(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"secrets", "provision-source-keyset", "--file=/private/path"}, &stdout, &stderr, BuildInfo{}); code != 2 {
		t.Fatalf("usage exit = %d, stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "/private/path") {
		t.Fatal("usage error disclosed output path")
	}
}
