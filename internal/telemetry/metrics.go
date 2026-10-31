package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type QueueMetrics struct {
	Available bool
	Dropped   int64
	Size      int64
	Capacity  int64
}

// TraceQueueMetrics reads only SDK counters/gauges. It performs no network or
// database work, and never exports application attribute values into labels.
func TraceQueueMetrics(ctx context.Context) QueueMetrics {
	p := activeProvider.Load()
	if p == nil || p.reader == nil {
		return QueueMetrics{}
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	var snapshot metricdata.ResourceMetrics
	if p.reader.Collect(ctx, &snapshot) != nil {
		return QueueMetrics{}
	}
	result := QueueMetrics{Available: true}
	for _, scope := range snapshot.ScopeMetrics {
		for _, metric := range scope.Metrics {
			switch data := metric.Data.(type) {
			case metricdata.Sum[int64]:
				// SDK queue instruments are observable up/down counters: their
				// snapshots use Sum, although their values represent current size.
				for _, point := range data.DataPoints {
					switch metric.Name {
					case "otel.sdk.processor.span.queue.size":
						result.Size += point.Value
					case "otel.sdk.processor.span.queue.capacity":
						result.Capacity += point.Value
					}
				}
				if metric.Name == "otel.sdk.processor.span.processed" {
					for _, point := range data.DataPoints {
						if value, ok := point.Attributes.Value("error.type"); ok && value.AsString() == "queue_full" {
							result.Dropped += point.Value
						}
					}
				}
			case metricdata.Gauge[int64]:
				for _, point := range data.DataPoints {
					switch metric.Name {
					case "otel.sdk.processor.span.queue.size":
						result.Size += point.Value
					case "otel.sdk.processor.span.queue.capacity":
						result.Capacity += point.Value
					}
				}
			}
		}
	}
	return result
}
