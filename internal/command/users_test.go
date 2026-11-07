package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUsersRejectUnsafeOrIncompleteCommandsBeforeDispatch(t *testing.T) {
	for _, args := range [][]string{
		{"users", "create", "--profile-file=missing", "--input-file=missing", "--idempotency-key=create"},
		{"users", "disable", "--profile-file=missing", "--user-id=00000000-0000-4000-8000-000000000001", "--if-match=wrong", "--idempotency-key=disable", "--confirm"},
		{"users", "delete", "--profile-file=missing", "--user-id=00000000-0000-4000-8000-000000000001", "--if-match=\"user-00000000-0000-4000-8000-000000000001-1\"", "--idempotency-key=delete"},
		{"users", "list", "--profile-file=missing", "--limit=201"},
	} {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if code := Run(context.Background(), args, &out, &diagnostic, BuildInfo{}); code != 2 {
				t.Fatalf("expected invalid input before profile/network access, got %d: %s", code, diagnostic.String())
			}
			if out.Len() != 0 {
				t.Fatal("invalid command wrote a result")
			}
		})
	}
}

func TestUsersListRequiresProtectedProfile(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if code := Run(t.Context(), []string{"users", "list", "--profile-file=" + t.TempDir() + "/missing"}, &out, &diagnostic, BuildInfo{}); code != 1 || !strings.Contains(diagnostic.String(), "profile") {
		t.Fatalf("expected protected profile error, got %d: %s", code, diagnostic.String())
	}
}
