//go:build linux

package cliprofile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRoundTripAndAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	ctx := t.Context()
	store, err := Open(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("Load() = found %v, err %v", found, err)
	}
	want := Record{SchemaVersion: 1, BackendURL: "https://example.test", CAPEM: "test CA", Session: "session_cookie=value"}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Load()
	if err != nil || !found || got != want {
		t.Fatalf("Load() = %#v, %v, %v", got, found, err)
	}
	if err := store.Save(Record{SchemaVersion: 1, BackendURL: "https://other.test"}); err != nil {
		t.Fatal(err)
	}
	got, found, err = store.Load()
	if err != nil || !found || got.BackendURL != "https://other.test" {
		t.Fatalf("replacement Load() = %#v, %v, %v", got, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(want); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Save after Close error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Load after Close error = %v", err)
	}
}

func TestStrictRecordFormatAndBounds(t *testing.T) {
	valid := `{"schema_version":1,"backend_url":"https://example.test","ca_pem":"","session":""}`
	tests := []string{
		strings.Replace(valid, `"session":""`, `"session":"","session":"x"`, 1),
		strings.Replace(valid, `"session":""`, `"session":"","\u0073ession":"x"`, 1),
		strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		strings.Replace(valid, `"schema_version":1`, `"schema_version":"1"`, 1),
		strings.Replace(valid, `"backend_url":"https://example.test"`, `"backend_url":""`, 1),
		strings.Replace(valid, `"session":""`, `"session":"contains;separator"`, 1),
		strings.Replace(valid, `"schema_version":1`, `"schema_version":null`, 1),
		strings.Replace(valid, `"backend_url":"https://example.test"`, `"backend_url":null`, 1),
		strings.Replace(valid, `"ca_pem":""`, `"ca_pem":null`, 1),
		strings.Replace(valid, `"session":""`, `"session":null`, 1),
		strings.Replace(valid, `"backend_url":"https://example.test"`, `"backend_url":"https://example.test\ud800"`, 1),
	}
	for _, input := range tests {
		record, err := decodeRecord([]byte(input))
		if err == nil {
			err = validate(record)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("decodeRecord(%s) error = %v", input, err)
		}
	}
	if _, err := decodeRecord([]byte(strings.Repeat(" ", maxProfileBytes+1))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized record error = %v", err)
	}
	if err := validate(Record{SchemaVersion: 1, BackendURL: "https://x", Session: strings.Repeat("a", maxSessionBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized session error = %v", err)
	}
	boundaries := []Record{
		{SchemaVersion: 1, BackendURL: "x" + strings.Repeat("u", maxURLBytes-1), CAPEM: strings.Repeat("c", maxCAPEMBytes), Session: strings.Repeat("s", maxSessionBytes)},
	}
	for _, record := range boundaries {
		if err := validate(record); err != nil {
			t.Errorf("valid field boundary rejected: %v", err)
		}
	}
	for _, record := range []Record{
		{SchemaVersion: 1, BackendURL: strings.Repeat("u", maxURLBytes+1)},
		{SchemaVersion: 1, BackendURL: "https://x", CAPEM: strings.Repeat("c", maxCAPEMBytes+1)},
		{SchemaVersion: 1, BackendURL: "https://x", Session: strings.Repeat("s", maxSessionBytes+1)},
	} {
		if err := validate(record); !errors.Is(err, ErrInvalid) {
			t.Errorf("oversized fields accepted: %v", err)
		}
	}
}

func TestRejectUnsafeFilesAndLinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink Open error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("insecure mode Open error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("FIFO Open error = %v", err)
	}
}

func TestRejectUnsafeParentAndLockFiles(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "unsafe-parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), filepath.Join(parent, "profile"), true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe parent error = %v", err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "symlink-lock-profile")
	lockPath := path + ".lock"
	if err := os.Symlink(filepath.Join(base, "target"), lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink lock error = %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("insecure lock mode error = %v", err)
	}
}

func TestMissingProfileRequiresCreateAndCanBeSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	if _, err := Open(t.Context(), path, false); !errors.Is(err, syscall.ENOENT) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open missing profile without create error = %v", err)
	}
	store, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("missing Load = found %v, err %v", found, err)
	}
	if err := store.Save(Record{SchemaVersion: 1, BackendURL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLockContentionHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	first, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	second, err := Open(ctx, path, true)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended Open error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("lock cancellation was not bounded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatalf("Open after release: %v", err)
	}
	_ = third.Close()
}

func TestProfileNameCannotCollideWithLockOrTemporaryNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	first, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for _, reserved := range []string{path + ".lock", filepath.Join(filepath.Dir(path), ".almanac-profile-tmp-test")} {
		if _, err := Open(t.Context(), reserved, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Open reserved name %q error = %v", filepath.Base(reserved), err)
		}
	}
	if _, err := os.Stat(path + ".lock.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collision created nested lock artifact: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if second, err := Open(ctx, path, true); second != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("original profile lock was displaced: handle %v, error %v", second, err)
	}
}

func TestLoadAndGCDoNotReleaseOwnedDescriptorOrLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	first, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Save(Record{SchemaVersion: 1, BackendURL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if _, found, err := first.Load(); err != nil || !found {
			t.Fatalf("Load() = found %v, err %v", found, err)
		}
		runtime.GC()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if second, err := Open(ctx, path, true); second != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Load/GC released live lock: handle %v, error %v", second, err)
	}
	if err := first.Save(Record{SchemaVersion: 1, BackendURL: "https://still-locked.test"}); err != nil {
		t.Fatalf("live handle unusable after GC: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatalf("reacquire after Close: %v", err)
	}
	_ = second.Close()
}

func TestLoadRejectsValidOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	validLarge := `{"schema_version":1,"backend_url":"https://x","ca_pem":"` + strings.Repeat("c", maxProfileBytes) + `","session":""}`
	if err := os.WriteFile(path, []byte(validLarge), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Load(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load oversized valid JSON error = %v", err)
	}
}

func TestCancelledOpenHandleRejectsLoadAndSave(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store, err := Open(ctx, filepath.Join(t.TempDir(), "profile"), true)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := store.Load(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Load error = %v", err)
	}
	if err := store.Save(Record{SchemaVersion: 1, BackendURL: "https://example.test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Save error = %v", err)
	}
	_ = store.Close()
}

func TestFailedSaveKeepsExistingProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	store, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	want := Record{SchemaVersion: 1, BackendURL: "https://example.test", Session: "still-here"}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Record{SchemaVersion: 2, BackendURL: "https://invalid.test"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid Save error = %v", err)
	}
	got, found, err := store.Load()
	if err != nil || !found || got != want {
		t.Fatalf("after failed save: %#v, %v, %v", got, found, err)
	}
	_ = store.Close()
}

func TestPreRenameFailurePreservesOldRecordAndCleansTemp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	store, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := Record{SchemaVersion: 1, BackendURL: "https://old.test", Session: "old-session"}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ops := store.ops
	store.ops.rename = func(int, string, int, string) error { return syscall.EIO }
	err = store.Save(Record{SchemaVersion: 1, BackendURL: "https://new.test", Session: "new-session"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Save with rename I/O failure error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("old profile changed before failed rename: before %s after %s", before, after)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".almanac-profile-tmp-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("owned temporary files remain: %v, error %v", temps, err)
	}
	store.ops = ops
}

func TestPostRenameDirectorySyncFailureIsUncertain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	store, err := Open(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Save(Record{SchemaVersion: 1, BackendURL: "https://old.test"}); err != nil {
		t.Fatal(err)
	}
	store.ops.syncDir = func(int) error { return syscall.EIO }
	want := Record{SchemaVersion: 1, BackendURL: "https://new.test", Session: "new-session"}
	if err := store.Save(want); !errors.Is(err, ErrCommitUncertain) {
		t.Fatalf("Save with directory-sync failure error = %v", err)
	}
	got, found, err := store.Load()
	if err != nil || !found || got != want {
		t.Fatalf("visible record after uncertain commit = %#v, %v, %v", got, found, err)
	}
}
