package storage

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestLoginAttemptInputValidationPrecedesDatabaseWork(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 987654321, time.FixedZone("test", 3600))
	valid := BeginLoginAttemptInput{Login: "owner", Source: netip.MustParseAddr("192.0.2.4"), Now: now}
	tests := map[string]BeginLoginAttemptInput{
		"empty login":     {Source: valid.Source, Now: now},
		"oversized login": {Login: strings.Repeat("x", 257), Source: valid.Source, Now: now},
		"invalid utf8":    {Login: "bad\xff", Source: valid.Source, Now: now},
		"zero time":       {Login: valid.Login, Source: valid.Source},
		"invalid address": {Login: valid.Login, Now: now},
		"zoned address":   {Login: valid.Login, Source: netip.MustParseAddr("fe80::1%eth0"), Now: now},
		"unspecified v4":  {Login: valid.Login, Source: netip.MustParseAddr("0.0.0.0"), Now: now},
		"unspecified v6":  {Login: valid.Login, Source: netip.MustParseAddr("::"), Now: now},
		"multicast v4":    {Login: valid.Login, Source: netip.MustParseAddr("224.0.0.1"), Now: now},
		"multicast v6":    {Login: valid.Login, Source: netip.MustParseAddr("ff02::1"), Now: now},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BeginLoginAttempt(t.Context(), "not a database URL", input); !errors.Is(err, ErrInvalidLoginAttemptInput) {
				t.Fatalf("BeginLoginAttempt error = %v; want invalid-input sentinel before connection", err)
			}
		})
	}
	if _, err := BeginLoginAttempt(t.Context(), "not a database URL", valid); err == nil || errors.Is(err, ErrInvalidLoginAttemptInput) {
		t.Fatalf("valid input error = %v; want database URL error", err)
	}
}

func TestFinishLoginAttemptRequiresCanonicalUUIDAndTime(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"", "A0b1c2d3-e4f5-4678-9012-3456789abcde", "a0b1c2d3-e4f5-4678-9012-3456789abcde ", "not-a-uuid"} {
		if _, err := FinishLoginAttempt(t.Context(), "not a database URL", id, true, now); !errors.Is(err, ErrInvalidLoginAttemptInput) {
			t.Fatalf("FinishLoginAttempt(%q) error = %v; want invalid-input sentinel", id, err)
		}
	}
	if _, err := FinishLoginAttempt(t.Context(), "not a database URL", "a0b1c2d3-e4f5-4678-9012-3456789abcde", true, time.Time{}); !errors.Is(err, ErrInvalidLoginAttemptInput) {
		t.Fatalf("zero timestamp error = %v; want invalid-input sentinel", err)
	}
}
