package storage

import (
	"context"
	"fmt"
	"net/http"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// Labels describe fixed traffic classes and stages, never users, SQL, URLs or
// objective IDs. Tracing retains no query text or parameters.
type runtimeMetrics struct {
	queries           [3]queryMetrics
	sourceKeys        atomic.Uint64
	historyJobs       atomic.Uint64
	preparationErrors [2]atomic.Uint64
	historyHits       atomic.Uint64
	historyMisses     atomic.Uint64
	diagnostics       atomic.Pointer[derivedMetricsSnapshot]
	ingestion         [3]ingestionMetrics
}

// Fine fixed buckets let the controlled fixture compare conservative percentile
// bounds between ingestion-only and saturated phases. No per-agent labels or
// observation payloads are retained, and scrapes perform no database work.
type ingestionMetrics struct {
	mu    sync.Mutex
	value ingestionHistogram
}

type ingestionHistogram struct {
	count       uint64
	nanoseconds uint64
	buckets     [256]uint64
}

func (m *ingestionMetrics) snapshot() ingestionHistogram {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.value
}

var ingestionDurationBounds = func() [256]time.Duration {
	var bounds [256]time.Duration
	bound := 50 * time.Microsecond
	for index := range bounds {
		bounds[index] = bound
		bound = bound * 105 / 100
	}
	return bounds
}()

// ObserveIngestionBatch measures the whole HTTP handler, including credential
// checks, canonical acceptance and response serialization. A successful HTTP 200
// containing retryable/rejected items is not counted as accepted ingestion.
func (r *Runtime) ObserveIngestionBatch(elapsed time.Duration, accepted, duplicateOnly bool) {
	if r == nil {
		return
	}
	index := 2
	if accepted {
		index = 0
	} else if duplicateOnly {
		index = 1
	}
	sample := &r.metrics.ingestion[index]
	sample.mu.Lock()
	defer sample.mu.Unlock()
	sample.value.count++
	sample.value.nanoseconds += uint64(max(0, elapsed))
	for index, bound := range ingestionDurationBounds {
		if elapsed <= bound {
			sample.value.buckets[index]++
		}
	}
}

type queryMetrics struct {
	count       atomic.Uint64
	failures    atomic.Uint64
	nanoseconds atomic.Uint64
	buckets     [8]atomic.Uint64
}

var queryDurationBounds = [...]time.Duration{time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 500 * time.Millisecond, time.Second, 5 * time.Second}

type queryTimingKey struct{}

type runtimeQueryTracer struct{ metrics *queryMetrics }

func (t runtimeQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryTimingKey{}, time.Now())
}

func (t runtimeQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryTimingKey{}).(time.Time)
	if !ok {
		return
	}
	t.metrics.observe(time.Since(start), data.Err)
}

func (m *queryMetrics) observe(elapsed time.Duration, err error) {
	m.count.Add(1)
	m.nanoseconds.Add(uint64(max(0, elapsed)))
	if err != nil {
		m.failures.Add(1)
	}
	for index, bound := range queryDurationBounds {
		if elapsed <= bound {
			m.buckets[index].Add(1)
		}
	}
}

type derivedMetricsSnapshot struct {
	At time.Time
	dbgen.ReadDerivedMetricsRow
}

// sampleDerivedMetrics runs on the existing source worker. Scrapes never acquire
// a connection, inspect canonical rows, or start a third background DB worker.
func (r *Runtime) sampleDerivedMetrics(ctx context.Context, now time.Time) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	row, err := dbgen.New(conn).ReadDerivedMetrics(bounded, dbgen.ReadDerivedMetricsParams{LeaseUntil: timestamp(now), CalculationVersion: operationalCalculationVersion})
	if err != nil {
		return err
	}
	r.metrics.diagnostics.Store(&derivedMetricsSnapshot{At: now.UTC(), ReadDerivedMetricsRow: row})
	return nil
}

// MetricsHandler is mounted only on the separately configured internal listener.
// No profiling or management endpoint is registered here.
func (r *Runtime) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		var body strings.Builder
		body.WriteString("# TYPE almanac_db_query_duration_seconds histogram\n")
		body.WriteString("# TYPE almanac_ingestion_batch_duration_seconds histogram\n")
		for index, result := range [...]string{"accepted", "duplicate", "unsuccessful"} {
			labels := fmt.Sprintf(`result=%q`, result)
			sample := r.metrics.ingestion[index].snapshot()
			metric(&body, "almanac_ingestion_batch_duration_seconds_count", labels, sample.count)
			metric(&body, "almanac_ingestion_batch_duration_seconds_sum", labels, float64(sample.nanoseconds)/float64(time.Second))
			for index, bound := range ingestionDurationBounds {
				metric(&body, "almanac_ingestion_batch_duration_seconds_bucket", labels+fmt.Sprintf(`,le="%g"`, bound.Seconds()), sample.buckets[index])
			}
			metric(&body, "almanac_ingestion_batch_duration_seconds_bucket", labels+`,le="+Inf"`, sample.count)
		}
		for class, name := range [...]string{"live", "interactive", "derived"} {
			labels := fmt.Sprintf(`class=%q`, name)
			if pool := r.pools[class]; pool != nil {
				stat := pool.Stat()
				metric(&body, "almanac_db_pool_connections", labels, stat.TotalConns())
				metric(&body, "almanac_db_pool_acquired", labels, stat.AcquiredConns())
				metric(&body, "almanac_db_pool_capacity", labels, stat.MaxConns())
				metric(&body, "almanac_db_pool_acquires_total", labels, stat.AcquireCount())
				metric(&body, "almanac_db_pool_acquire_seconds_total", labels, stat.AcquireDuration().Seconds())
				metric(&body, "almanac_db_pool_waits_total", labels, stat.EmptyAcquireCount())
				metric(&body, "almanac_db_pool_canceled_total", labels, stat.CanceledAcquireCount())
			}
			q := &r.metrics.queries[class]
			metric(&body, "almanac_db_query_duration_seconds_count", labels, q.count.Load())
			metric(&body, "almanac_db_query_duration_seconds_sum", labels, float64(q.nanoseconds.Load())/float64(time.Second))
			metric(&body, "almanac_db_query_errors_total", labels, q.failures.Load())
			for index, bound := range queryDurationBounds {
				metric(&body, "almanac_db_query_duration_seconds_bucket", labels+fmt.Sprintf(`,le="%g"`, bound.Seconds()), q.buckets[index].Load())
			}
			metric(&body, "almanac_db_query_duration_seconds_bucket", labels+`,le="+Inf"`, q.count.Load())
		}
		metric(&body, "almanac_derived_source_keys_total", "", r.metrics.sourceKeys.Load())
		metric(&body, "almanac_derived_history_jobs_total", "", r.metrics.historyJobs.Load())
		for index, stage := range [...]string{"aggregate", "history"} {
			metric(&body, "almanac_derived_preparation_errors_total", fmt.Sprintf(`stage=%q`, stage), r.metrics.preparationErrors[index].Load())
		}
		metric(&body, "almanac_derived_history_cache_hits_total", "", r.metrics.historyHits.Load())
		metric(&body, "almanac_derived_history_cache_misses_total", "", r.metrics.historyMisses.Load())
		samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/gc/heap/objects:objects"}}
		metrics.Read(samples)
		metric(&body, "almanac_go_heap_bytes", "", samples[0].Value.Uint64())
		metric(&body, "almanac_go_heap_objects", "", samples[1].Value.Uint64())
		if snapshot := r.metrics.diagnostics.Load(); snapshot != nil {
			metric(&body, "almanac_derived_metrics_ready", "", 1)
			metric(&body, "almanac_derived_metrics_timestamp_seconds", "", float64(snapshot.At.UnixNano())/float64(time.Second))
			metric(&body, "almanac_derived_dirty_keys", "", snapshot.DirtyKeys)
			metric(&body, "almanac_derived_failed_keys", "", snapshot.FailedKeys)
			metric(&body, "almanac_derived_oldest_dirty_seconds", "", snapshot.OldestDirtySeconds)
			metric(&body, "almanac_derived_history_jobs", "", snapshot.HistoryJobs)
			metric(&body, "almanac_derived_failed_history_jobs", "", snapshot.FailedHistoryJobs)
			metric(&body, "almanac_derived_oldest_history_seconds", "", snapshot.OldestHistorySeconds)
			metric(&body, "almanac_derived_current_lag_seconds", "", snapshot.CurrentLagSeconds)
			metric(&body, "almanac_derived_pending_current", "", snapshot.PendingCurrent)
			metric(&body, "almanac_derived_cache_payload_bytes", "", snapshot.CacheBytes)
			metric(&body, "almanac_derived_cache_limit_bytes", "", snapshot.CacheLimitBytes)
			metric(&body, "almanac_storage_relation_bytes", `kind="canonical"`, snapshot.CanonicalBytes)
			metric(&body, "almanac_storage_relation_bytes", `kind="derived"`, snapshot.DerivedBytes)
		} else {
			metric(&body, "almanac_derived_metrics_ready", "", 0)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body.String()))
	})
	return mux
}

func metric(body *strings.Builder, name, labels string, value any) {
	if labels != "" {
		fmt.Fprintf(body, "%s{%s} %v\n", name, labels, value)
	} else {
		fmt.Fprintf(body, "%s %v\n", name, value)
	}
}

var _ pgx.QueryTracer = runtimeQueryTracer{}
