package storage

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
)

func TestAlertStorageDurationUnits(t *testing.T) {
	t.Parallel()
	want := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4"}}
	data, err := marshalAlertData(want)
	if err != nil || !strings.Contains(string(data), `"Short":300`) || !strings.Contains(string(data), `"Long":3600`) {
		t.Fatalf("integer second representation: %s %v", data, err)
	}
	var got []alert.Condition
	if err := unmarshalAlertData(data, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("duration roundtrip: %+v %v", got, err)
	}
	for _, raw := range []string{`[{"Short":0.5}]`, `[{"Short":9223372036854775807}]`, `[{"Short":"5m"}]`} {
		if err := unmarshalAlertData([]byte(raw), &got); err == nil {
			t.Fatalf("invalid seconds accepted: %s", raw)
		}
	}
	if _, err := marshalAlertData(struct{ Duration time.Duration }{time.Millisecond}); err == nil {
		t.Fatal("subsecond duration silently rounded")
	}
}
