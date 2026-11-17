package maintenanceclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func TestTimelinePreservesFractionalRange(t *testing.T) {
	start := time.Date(2026, 11, 18, 2, 0, 0, 123456000, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		if query.Get("start") != start.Format(time.RFC3339Nano) || query.Get("end") != end.Format(time.RFC3339Nano) {
			t.Errorf("wire timeline bounds: %s", req.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer server.Close()
	client, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Do(t.Context(), client, func(context.Context, *http.Request) error { return nil }, Input{Action: "timeline", ID: "89524066-c358-4fce-b1f0-f4e11f614e55", Limit: 20, Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
}
