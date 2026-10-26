//go:build !linux

package secretfile

import "testing"

import "context"

func TestTokenOutputFailsExplicitlyOutsideLinux(t *testing.T) {
	if ErrTokenCancelled == nil || ErrTokenCommitUncertain == nil {
		t.Fatal("shared token output outcomes are unavailable on this platform")
	}
	if _, err := Reserve(context.Background(), "token"); err != ErrUnsupported {
		t.Fatal("token reservation did not report unsupported platform")
	}
	if err := WriteNew("token", []byte("secret")); err != ErrUnsupported {
		t.Fatal("token output did not report unsupported platform")
	}
}
