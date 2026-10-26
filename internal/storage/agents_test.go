package storage

import (
	"errors"
	"testing"
	"time"
)

func TestValidateCreateAgentInput(t *testing.T) {
	base := validCreateAgentInput()
	cases := []struct {
		name string
		edit func(*CreateAgentInput)
		want error
	}{
		{"valid", func(*CreateAgentInput) {}, nil},
		{"default lifetime", func(input *CreateAgentInput) { input.Lifetime = 0 }, nil},
		{"uppercase name", func(input *CreateAgentInput) { input.Name = "Agent" }, ErrInvalidAgentInput},
		{"invalid display utf8", func(input *CreateAgentInput) { input.DisplayName = string([]byte{0xff}) }, ErrInvalidAgentInput},
		{"control display", func(input *CreateAgentInput) { input.DisplayName = "bad\nname" }, ErrInvalidAgentInput},
		{"too long display", func(input *CreateAgentInput) { input.DisplayName = string(make([]byte, 257)) }, ErrInvalidAgentInput},
		{"space idempotency key", func(input *CreateAgentInput) { input.IdempotencyKey = "bad key" }, ErrInvalidAgentInput},
		{"fractional lifetime", func(input *CreateAgentInput) { input.Lifetime = time.Second + time.Nanosecond }, ErrInvalidAgentInput},
		{"excess lifetime", func(input *CreateAgentInput) { input.Lifetime = agentMaxLifetime + time.Second }, ErrInvalidAgentInput},
		{"noncanonical user uuid", func(input *CreateAgentInput) { input.Proof.UserID = "ABCDEFAB-CDEF-4ABC-8DEF-ABCDEFABCDEE" }, ErrInvalidAgentInput},
		{"missing clock", func(input *CreateAgentInput) { input.Now = nil }, ErrInvalidAgentInput},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			_, _, _, err := validateCreateAgentInput(input)
			if test.want == nil && err != nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("validate error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestCreateAgentAcceptsAndNormalizesRealisticClockPrecision(t *testing.T) {
	input := validCreateAgentInput()
	zone := time.FixedZone("local", 2*60*60)
	input.Proof.VerifiedAt = time.Date(2026, 10, 26, 12, 0, 0, 123456789, zone)
	input.Now = func() time.Time { return input.Proof.VerifiedAt.Add(3 * time.Second) }
	if _, _, _, err := validateCreateAgentInput(input); err != nil {
		t.Fatalf("validation rejected a nonzero zoned, nanosecond precision clock: %v", err)
	}
	got := input.Now().UTC().Truncate(time.Microsecond)
	want := input.Proof.VerifiedAt.UTC().Add(3 * time.Second).Truncate(time.Microsecond)
	if !got.Equal(want) || got.Location() != time.UTC || got.Nanosecond()%1000 != 0 {
		t.Fatalf("normalized clock = %v; want UTC microsecond time %v", got, want)
	}
}

func TestAgentRequestDigestNormalizesDefaultLifetime(t *testing.T) {
	input := validCreateAgentInput()
	defaulted, _, _, err := validateCreateAgentInput(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Lifetime = agentDefaultLifetime
	explicit, _, _, err := validateCreateAgentInput(input)
	if err != nil {
		t.Fatal(err)
	}
	left := agentRequestDigest{Version: 1, Name: input.Name, DisplayName: input.DisplayName, LifetimeSec: int64(defaulted / time.Second)}
	right := agentRequestDigest{Version: 1, Name: input.Name, DisplayName: input.DisplayName, LifetimeSec: int64(explicit / time.Second)}
	if left != right {
		t.Fatalf("default digest tuple %#v differs from explicit tuple %#v", left, right)
	}
}

func validCreateAgentInput() CreateAgentInput {
	now := time.Date(2026, 10, 26, 10, 0, 0, 123456000, time.UTC)
	return CreateAgentInput{
		Proof: AgentAdminProof{
			SessionVerifier: [32]byte{1}, UserID: "abcdefab-cdef-4abc-8def-abcdefabcdee",
			CredentialEpoch: "abcdefab-cdef-4abc-8def-abcdefabcdef", CredentialRevision: 2, VerifiedAt: now,
		},
		Name: "collector-1", DisplayName: "Collector 1", CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdea",
		CredentialVerifier: [32]byte{2}, IdempotencyKey: "key-1", RequestID: "request-1",
		Now: func() time.Time { return now.Add(time.Second) },
	}
}
