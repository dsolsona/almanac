package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var activeProvider atomic.Pointer[Provider]
var browserDropped atomic.Uint64
var browserExportFailures atomic.Uint64

func BrowserDroppedBatches() uint64 { return browserDropped.Load() }
func BrowserExportFailures() uint64 { return browserExportFailures.Load() }

type browserExporter struct {
	endpoint string
	queue    chan []byte
	client   *http.Client
	headers  map[string]string
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.RWMutex
	closed   bool
}

func newBrowserExporter(endpoint string, client *http.Client, headers map[string]string) *browserExporter {
	ctx, cancel := context.WithCancel(context.Background())
	b := &browserExporter{endpoint: endpoint, queue: make(chan []byte, 32), ctx: ctx, cancel: cancel, done: make(chan struct{}), client: client, headers: headers}
	go b.run()
	return b
}

func (b *browserExporter) enqueue(body []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed || len(body) > BrowserBodyLimit {
		browserDropped.Add(1)
		return errors.New("telemetry: browser admission unavailable")
	}
	select {
	case b.queue <- body:
		return nil
	default:
		browserDropped.Add(1)
		return errors.New("telemetry: browser queue full")
	}
}

func (b *browserExporter) run() {
	defer close(b.done)
	defer b.client.CloseIdleConnections()
	for body := range b.queue {
		if b.ctx.Err() != nil {
			browserDropped.Add(1)
			continue
		}
		ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
		if err == nil {
			for key, value := range b.headers {
				request.Header.Set(key, value)
			}
			request.Header.Set("Content-Type", "application/json")
			var response *http.Response
			response, err = b.client.Do(request)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || len(body) > 64<<10 || response.StatusCode < 200 || response.StatusCode >= 300 || !browserResponseAccepted(response.Header.Get("Content-Type"), body) {
					err = errors.New("trace export failed")
				}
			}
		}
		cancel()
		if err != nil {
			browserExportFailures.Add(1)
		}
	}
}

// A 2xx OTLP response may still reject spans. Inspect its bounded standard
// response without logging remote error text or treating rejection as delivery.
func browserResponseAccepted(contentType string, body []byte) bool {
	if len(body) == 0 {
		return true
	}
	var result collectortrace.ExportTraceServiceResponse
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch mediaType {
	case "application/json":
		err = protojson.Unmarshal(body, &result)
	case "application/x-protobuf":
		err = proto.Unmarshal(body, &result)
	default:
		return false
	}
	return err == nil && (result.PartialSuccess == nil || result.PartialSuccess.RejectedSpans == 0 && result.PartialSuccess.ErrorMessage == "")
}

func (b *browserExporter) shutdown(ctx context.Context) error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.queue)
	}
	b.mu.Unlock()
	select {
	case <-b.done:
		b.cancel()
		return nil
	case <-ctx.Done():
		b.cancel()
		return ctx.Err()
	}
}
