package storage

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestOverviewCursorAcceptsBoundedKeysetAndRejectsMalformedKeys(t *testing.T) {
	valid := `{"r":1,"n":"alpha","i":"a842b928-2799-47d2-992c-ed7836b80111","f":"` + strings.Repeat("a", 64) + `","b":false}`
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	if _, ok := decodeOverviewCursor(encode(valid)); !ok {
		t.Fatal("valid keyset cursor was rejected")
	}
	longest := encode(strings.Replace(valid, "alpha", strings.Repeat("x", 63), 1))
	if _, ok := decodeOverviewCursor(longest); !ok || len(longest) > 512 {
		t.Fatal("maximum-length native SLO name broke the v1 overview cursor limit")
	}
	for _, raw := range []string{
		strings.Replace(valid, `"r":1`, `"r":-1`, 1),
		strings.Replace(valid, `"r":1`, `"r":3`, 1),
		strings.Replace(valid, "alpha", strings.Repeat("x", 65), 1),
		strings.Replace(valid, "a842b928-2799-47d2-992c-ed7836b80111", "invalid", 1),
		strings.Replace(valid, strings.Repeat("a", 64), "digest", 1),
		valid + `{}`,
		`{"s":"a842b928-2799-47d2-992c-ed7836b80111","o":10,"m":"legacy"}`,
	} {
		if _, ok := decodeOverviewCursor(encode(raw)); ok {
			t.Fatalf("invalid keyset cursor was accepted: %s", raw)
		}
	}
}
