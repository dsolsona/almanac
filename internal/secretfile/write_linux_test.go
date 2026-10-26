//go:build linux

package secretfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func tokenTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal("secure temporary directory failed")
	}
	return dir
}

func TestTokenFileCompletesAtomicallyAndPreservesAnInterveningDestination(t *testing.T) {
	dir := tokenTestDir(t)
	path := filepath.Join(dir, "agent-token")
	sink, err := Reserve(t.Context(), path)
	if err != nil {
		t.Fatal("reserve token path failed")
	}
	if err := os.WriteFile(path, []byte("user-owned"), 0600); err != nil {
		t.Fatal("create competing destination failed")
	}
	if err := sink.Complete([]byte("secret-token")); err == nil {
		t.Fatal("completion replaced a destination created after reservation")
	}
	if err := sink.Close(); err != nil {
		t.Fatal("close sink failed")
	}
	if err := sink.Close(); err != nil {
		t.Fatal("repeated close was not idempotent")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "user-owned" {
		t.Fatal("competing user file was changed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal("read token directory failed")
	}
	if len(entries) != 1 || entries[0].Name() != "agent-token" {
		t.Fatalf("owned reservation artifacts remain: %v", entries)
	}
}

func TestTokenFileWritesPrivateCompleteContentAndIsExclusive(t *testing.T) {
	dir := tokenTestDir(t)
	path := filepath.Join(dir, "agent-token")
	sink, err := Reserve(t.Context(), path)
	if err != nil {
		t.Fatal("reserve token path failed")
	}
	if _, err := Reserve(t.Context(), path); err == nil {
		t.Fatal("duplicate reservation accepted")
	}
	if err := sink.Complete([]byte("complete-token")); err != nil {
		t.Fatal("complete atomic token output failed")
	}
	if err := sink.Close(); err != nil {
		t.Fatal("close sink failed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("completed token file is not private")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete-token" {
		t.Fatal("completed token bytes differ")
	}
}

func TestTokenFileWriteAndSyncFailuresLeaveNoPartialFinalFile(t *testing.T) {
	for _, failAt := range []string{"write", "sync"} {
		t.Run(failAt, func(t *testing.T) {
			dir := tokenTestDir(t)
			path := filepath.Join(dir, "agent-token")
			sink, err := Reserve(t.Context(), path)
			if err != nil {
				t.Fatal("reserve failed")
			}
			if failAt == "write" {
				sink.ops.write = func(int, []byte) (int, error) { return 0, unix.EIO }
			} else {
				sink.ops.sync = func(int) error { return unix.EIO }
			}
			if err := sink.Complete([]byte("partial-secret")); err == nil {
				t.Fatal("injected failure was ignored")
			}
			if err := sink.Close(); err != nil {
				t.Fatal("cleanup failed")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("partial token was published")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("reservation/temp artifacts remain: %v err=%v", entries, err)
			}
		})
	}
}

func TestTokenFileCleanupDoesNotUnlinkAReplacedReservation(t *testing.T) {
	dir := tokenTestDir(t)
	sink, err := Reserve(t.Context(), filepath.Join(dir, "agent-token"))
	if err != nil {
		t.Fatal("reserve failed")
	}
	reservePath := filepath.Join(dir, sink.markerName)
	saved := reservePath + ".saved"
	if err := os.Rename(reservePath, saved); err != nil {
		t.Fatal("move owned marker failed")
	}
	if err := os.WriteFile(reservePath, []byte("not-owned"), 0600); err != nil {
		t.Fatal("replace marker failed")
	}
	if err := sink.Close(); err != nil {
		t.Fatal("close failed")
	}
	data, err := os.ReadFile(reservePath)
	if err != nil || string(data) != "not-owned" {
		t.Fatal("cleanup unlinked an artifact it did not own")
	}
	_ = os.Remove(saved)
}

func TestTokenFileHonorsCancelledDeadlineAndReportsPostLinkSyncUncertainty(t *testing.T) {
	dir := tokenTestDir(t)
	path := filepath.Join(dir, "cancelled-token")
	ctx, cancel := context.WithCancel(t.Context())
	sink, err := Reserve(ctx, path)
	if err != nil {
		t.Fatal("reserve failed")
	}
	cancel()
	if err := sink.Complete([]byte("secret")); !errors.Is(err, ErrTokenCancelled) {
		t.Fatalf("cancelled completion error=%v", err)
	}
	_ = sink.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled output was published")
	}

	path = filepath.Join(dir, "uncertain-token")
	sink, err = Reserve(t.Context(), path)
	if err != nil {
		t.Fatal("reserve uncertain sink failed")
	}
	sink.ops.syncDir = func(int) error { return unix.EIO }
	if err := sink.Complete([]byte("complete-secret")); !errors.Is(err, ErrTokenCommitUncertain) {
		t.Fatalf("post-link sync error=%v", err)
	}
	_ = sink.Close()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete-secret" {
		t.Fatal("post-commit file state was not preserved for inspection")
	}
}
