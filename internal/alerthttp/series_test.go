package alerthttp

import (
	"net/url"
	"testing"
)

func TestAlertSeriesQuery(t *testing.T) {
	for _, raw := range []string{"", "condition_id=fast&start=0001-01-01T00:00:00Z", "condition_id=fast&end=0001-01-01T00:00:00Z", "condition_id=fast&limit=1", "condition_id=fast&limit=121", "condition_id=fast&limit=02", "condition_id=fast&cursor=x", "condition_id=fast&start=2026-11-15", "condition_id=fast&start=2026-11-15T00:00:00%2B00:00", "condition_id=fast&start=2026-11-15T00:00:00Z&end=2026-11-17T00:00:00Z", "condition_id=fast&start=2026-11-15T00:00:00Z&end=2026-11-14T00:00:00Z", "condition_id=fast&condition_id=other", "condition_id=%00"} {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := parseSeriesQuery(q); ok {
			t.Errorf("accepted %q", raw)
		}
	}
	value, ok := parseSeriesQuery(url.Values{"condition_id": {"fast"}})
	if !ok || value.Limit != 120 || !value.Start.IsZero() || !value.End.IsZero() {
		t.Fatalf("bad defaults: %+v %t", value, ok)
	}
}
