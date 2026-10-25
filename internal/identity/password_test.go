package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync/atomic"
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
	valid, err := h.Hash(t.Context(), strings.Repeat("x", 15))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(valid, "$")
	standardAlphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	noncanonicalSalt := parts[4][:len(parts[4])-1] + string(standardAlphabet[strings.IndexByte(standardAlphabet, parts[4][len(parts[4])-1])^1])
	noncanonicalKey := parts[5][:len(parts[5])-1] + string(standardAlphabet[strings.IndexByte(standardAlphabet, parts[5][len(parts[5])-1])^1])
	wrongSaltSize := base64.RawStdEncoding.EncodeToString(make([]byte, 15))
	wrongKeySize := base64.RawStdEncoding.EncodeToString(make([]byte, 31))
	malformed := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "oversized", text: strings.Repeat("x", 1025)},
		{name: "invalid base64", text: "$argon2id$v=19$m=19456,t=2,p=1$!$!"},
		{name: "changed parallelism", text: strings.Replace(valid, "p=1", "p=2", 1)},
		{name: "changed version", text: strings.Replace(valid, "v=19", "v=20", 1)},
		{name: "changed algorithm", text: strings.Replace(valid, "argon2id", "argon2i", 1)},
		{name: "padded base64", text: strings.Join([]string{parts[0], parts[1], parts[2], parts[3], parts[4] + "=", parts[5]}, "$")},
		{name: "extra field", text: valid + "$extra"},
		{name: "nonzero salt trailing bits", text: strings.Join([]string{parts[0], parts[1], parts[2], parts[3], noncanonicalSalt, parts[5]}, "$")},
		{name: "nonzero key trailing bits", text: strings.Join([]string{parts[0], parts[1], parts[2], parts[3], parts[4], noncanonicalKey}, "$")},
		{name: "embedded newline", text: strings.Join([]string{parts[0], parts[1], parts[2], parts[3], "\n" + parts[4][1:], parts[5]}, "$")},
		{name: "wrong salt length", text: strings.Join([]string{parts[0], parts[1], parts[2], parts[3], wrongSaltSize, parts[5]}, "$")},
		{name: "wrong key length", text: strings.Join([]string{parts[0], parts[1], parts[2], parts[3], parts[4], wrongKeySize}, "$")},
	}
	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.Verify(t.Context(), "password", tc.text); !errors.Is(err, ErrInvalidVerifier) {
				t.Fatalf("Verify malformed = %v, want ErrInvalidVerifier", err)
			}
		})
	}
	for _, candidate := range []string{strings.Repeat("x", 129), strings.Repeat("😀", 129), "\xff"} {
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
	t.Cleanup(func() {
		for len(h.slots) > 0 {
			<-h.slots
		}
	})
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
	if err := h.acquire(t.Context()); err != nil {
		t.Fatalf("acquire after queued cancellation = %v", err)
	}
	h.release()
}

func TestPasswordHasherChecksCancellationAfterDerivation(t *testing.T) {
	h := NewPasswordHasher()
	password := strings.Repeat("c", 15)
	verifier, err := h.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{name: "Hash", call: func(ctx context.Context) error {
			_, err := h.Hash(ctx, password)
			return err
		}},
		{name: "Verify", call: func(ctx context.Context) error {
			_, err := h.Verify(ctx, password, verifier)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			t.Cleanup(cancel)
			ctx := &cancelAtErrContext{Context: baseCtx, cancel: cancel, cancelAt: 3}
			if err := tc.call(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation after derivation = %v, want context cancellation", err)
			}
			if len(h.slots) != 0 {
				t.Fatal("canceled derivation leaked a work slot")
			}
		})
	}
	if _, err := h.Hash(t.Context(), password); err != nil {
		t.Fatalf("Hash after canceled derivations = %v", err)
	}
}

// cancelAtErrContext cancels an underlying bounded context at a selected Err
// check. The third Hash and Verify checks follow synchronous Argon2 work.
type cancelAtErrContext struct {
	context.Context
	cancel   context.CancelFunc
	calls    atomic.Int32
	cancelAt int32
}

func (ctx *cancelAtErrContext) Err() error {
	if ctx.calls.Add(1) == ctx.cancelAt {
		ctx.cancel()
	}
	return ctx.Context.Err()
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
