//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntegrationBackendOwnerExcludesDuplicatesAndReleases(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	owner, err := AcquireBackendOwner(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("acquire owner failed: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	admin := integrationConnect(t, databaseURL)
	var pid int32
	if err := admin.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE application_name = 'almanac-backend-owner' AND datname = current_database()").Scan(&pid); err != nil || pid <= 0 {
		_ = owner.Close()
		t.Fatal("observe owner connection failed")
	}
	var lockCount int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND granted", pid).Scan(&lockCount); err != nil || lockCount != 1 {
		_ = owner.Close()
		t.Fatal("observe granted backend lock failed")
	}
	if duplicate, err := AcquireBackendOwner(t.Context(), databaseURL); !errors.Is(err, ErrBackendAlreadyRunning) || duplicate != nil {
		_ = owner.Close()
		t.Fatal("duplicate owner was not rejected")
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner failed: %v", err)
	}
	reacquired, err := AcquireBackendOwner(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("reacquire after close failed: %v", err)
	}
	t.Cleanup(func() { _ = reacquired.Close() })
	if err := reacquired.Close(); err != nil {
		t.Fatalf("close reacquired owner failed: %v", err)
	}
}

func TestIntegrationBackendOwnerLossIsRecorded(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	owner, err := AcquireBackendOwner(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("acquire owner failed: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	admin := integrationConnect(t, databaseURL)
	var pid int32
	if err := admin.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE application_name = 'almanac-backend-owner' AND datname = current_database()").Scan(&pid); err != nil || pid <= 0 {
		t.Fatal("observe owner connection failed")
	}
	var terminated bool
	if err := admin.QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate observed owner connection failed")
	}
	select {
	case <-owner.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("owner loss was not detected within probe bounds")
	}
	if !errors.Is(owner.Err(), ErrBackendOwnershipLost) {
		t.Fatal("ownership loss cause was not preserved")
	}
	if err := owner.Close(); !errors.Is(err, ErrBackendOwnershipLost) {
		t.Fatal("close hid recorded ownership loss")
	}
}

func TestIntegrationBackendOwnerCloseDuringDrainDetectsConcurrentTermination(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade disposable database failed")
	}
	owner, err := AcquireBackendOwner(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("acquire owner failed: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	admin := integrationConnect(t, databaseURL)
	var pid int32
	if err := admin.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE application_name='almanac-backend-owner' AND datname=current_database()").Scan(&pid); err != nil || pid <= 0 {
		t.Fatal("observe owner connection failed")
	}
	var terminated bool
	if err := admin.QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate observed owner connection failed")
	}
	if err := owner.Close(); !errors.Is(err, ErrBackendOwnershipLost) {
		t.Fatal("normal drain concealed owner termination concurrent with final verification")
	}
	select {
	case <-owner.Done():
	default:
		t.Fatal("close did not signal recorded ownership loss")
	}
}

func TestIntegrationBackendOwnerRejectsIncompatibleSchema(t *testing.T) {
	databaseURL := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if owner, err := AcquireBackendOwner(ctx, databaseURL); err == nil || owner != nil {
		t.Fatal("incompatible database schema was accepted")
	}
}
