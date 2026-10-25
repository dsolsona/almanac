package identity

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestValidatePasswordTextBoundariesAndSignificance(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{name: "14 code points", text: strings.Repeat("a", 14)},
		{name: "15 code points", text: strings.Repeat("a", 15), want: true},
		{name: "128 code points", text: strings.Repeat("a", 128), want: true},
		{name: "129 code points", text: strings.Repeat("a", 129)},
		{name: "four byte rune", text: strings.Repeat("😀", 15), want: true},
		{name: "leading and trailing spaces", text: " " + strings.Repeat("a", 13) + " ", want: true},
		{name: "composed unicode", text: strings.Repeat("é", 15), want: true},
		{name: "decomposed unicode", text: strings.Repeat("e\u0301", 15), want: true},
		{name: "invalid utf8", text: strings.Repeat("a", 14) + "\xff"},
		{name: "over 512 bytes", text: strings.Repeat("😀", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordText(tc.text)
			if (err == nil) != tc.want {
				t.Fatalf("ValidatePasswordText() error = %v, want valid %t", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), tc.text) {
				t.Fatalf("validation error disclosed password")
			}
		})
	}
}

func TestPasswordHashVerifyAndEncoding(t *testing.T) {
	h := NewPasswordHasher()
	password := "  " + strings.Repeat("p", 15) + "  "
	encoded, err := h.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected encoding %q", encoded)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || len(parts[4]) != 22 || len(parts[5]) != 43 {
		t.Fatalf("noncanonical encoding shape %q", encoded)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("verifier contains plaintext")
	}
	for _, candidate := range []string{password, "short", ""} {
		ok, err := h.Verify(t.Context(), candidate, encoded)
		if err != nil || ok != (candidate == password) {
			t.Fatalf("Verify(%q) = %t, %v", candidate, ok, err)
		}
	}
	trimmed, err := h.Verify(t.Context(), strings.TrimSpace(password), encoded)
	if err != nil || trimmed {
		t.Fatalf("Verify(trimmed password) = %t, %v", trimmed, err)
	}
	composed := strings.Repeat("é", 15)
	decomposed := strings.Repeat("e\u0301", 15)
	composedVerifier, err := h.Hash(t.Context(), composed)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.Verify(t.Context(), decomposed, composedVerifier); err != nil || ok {
		t.Fatalf("Verify(distinct Unicode form) = %t, %v", ok, err)
	}
	second, err := h.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if second == encoded {
		t.Fatal("repeated hashes reused salt")
	}
}

func TestPasswordVerifierRejectsMalformedBeforeDerivation(t *testing.T) {
	h := NewPasswordHasher()
	for _, verifier := range []string{
		"", strings.Repeat("x", 1025), "$argon2id$v=19$m=19456,t=2,p=1$!$!",
		"$argon2id$v=19$m=19456,t=2,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA=$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA$extra",
	} {
		if _, err := h.Verify(t.Context(), "password", verifier); !errors.Is(err, ErrInvalidVerifier) {
			t.Fatalf("Verify malformed = %v, want ErrInvalidVerifier", err)
		}
	}
	for _, candidate := range []string{strings.Repeat("x", 129), strings.Repeat("😀", 129), "\xff"} {
		valid, err := h.Hash(t.Context(), strings.Repeat("x", 15))
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := h.Verify(t.Context(), candidate, valid); ok || !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("Verify invalid candidate = %t, %v", ok, err)
		}
	}
}

func TestPasswordHasherCancellationAndAdmission(t *testing.T) {
	h := NewPasswordHasher()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.Hash(ctx, strings.Repeat("p", 15)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled Hash = %v", err)
	}

	// Fill the private admission queue directly so the waiter is known to be
	// queued without relying on a sleep or racing Argon2 start notifications.
	h.slots <- struct{}{}
	h.slots <- struct{}{}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer waitCancel()
	if _, err := h.Hash(waitCtx, strings.Repeat("r", 15)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued Hash = %v", err)
	}
	<-h.slots
	<-h.slots
	if _, err := h.Hash(t.Context(), strings.Repeat("s", 15)); err != nil {
		t.Fatalf("Hash after release = %v", err)
	}

	deriveCtx, deriveCancel := context.WithCancel(t.Context())
	defer deriveCancel()
	result := make(chan error, 1)
	h.slots <- struct{}{}
	go func() {
		_, err := h.Hash(deriveCtx, strings.Repeat("c", 15))
		result <- err
	}()
	<-h.slots // Let the call acquire admission before cancellation.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.slots) == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if len(h.slots) != 1 {
		t.Fatal("hash did not acquire a work slot within deadline")
	}
	deriveCancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled derivation = %v", err)
	}
	if len(h.slots) != 0 {
		t.Fatal("canceled derivation leaked a work slot")
	}
	if _, err := h.Hash(t.Context(), strings.Repeat("d", 15)); err != nil {
		t.Fatalf("Hash after canceled derivation = %v", err)
	}
}

func BenchmarkPasswordHash(b *testing.B) {
	h := NewPasswordHasher()
	password := strings.Repeat("p", 15)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.Hash(b.Context(), password); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPasswordVerify(b *testing.B) {
	h := NewPasswordHasher()
	password := strings.Repeat("p", 15)
	encoded, err := h.Hash(b.Context(), password)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if ok, err := h.Verify(b.Context(), password, encoded); err != nil || !ok {
			b.Fatalf("Verify() = %t, %v", ok, err)
		}
	}
}
