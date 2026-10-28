package agentcmd

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/prometheus"
	"github.com/dsolsona/almanac/internal/sli"
)

func TestNextIntervalEndSkipsMissedAndHonorsActivation(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		now       time.Time
		effective time.Time
		want      time.Time
	}{
		{name: "next canonical boundary", now: base.Add(3 * time.Second), effective: base, want: base.Add(15 * time.Second)},
		{name: "skip elapsed interval", now: base.Add(16 * time.Second), effective: base, want: base.Add(30 * time.Second)},
		{name: "activation interval starts at effective bound", now: base, effective: base.Add(30 * time.Second), want: base.Add(45 * time.Second)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextIntervalEnd(test.now, test.effective, 15); !got.Equal(test.want) {
				t.Fatalf("nextIntervalEnd() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestExpiredCursorKeepsCurrentSettledInterval(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
	agent := testClient(now, testNonce(71))
	runner := newCollectionRunner(agent)
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
	definition := agentprotocol.SLICollection{SLIID: "sli", MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: now.Add(-10 * time.Minute)}
	lease := testLease(now.Add(-time.Minute), "lease", now.Add(time.Hour))
	lease.ConfigurationRevision = "1"
	lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: "sli", MeasurementRevision: "1"}}
	runner.SetLease(lease)
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{definition}})
	runner.nextIntervalEnd[collectionKey(definition)] = now.Add(-2 * time.Minute)
	runner.initialPending[collectionKey(definition)] = true
	launched := make(chan collectionDefinition, 1)
	var workers sync.WaitGroup
	runner.workers = &workers
	runner.launchCollection = func(_ context.Context, d collectionDefinition) { launched <- d }
	runner.dispatchDue(t.Context())
	workers.Wait()
	select {
	case d := <-launched:
		if !d.intervalEnd.Equal(now.Add(-time.Minute)) {
			t.Fatalf("endpoint %v, want %v", d.intervalEnd, now.Add(-time.Minute))
		}
	default:
		t.Fatal("eligible interval was skipped")
	}
	if runner.losses["missed"] != 1 {
		t.Fatalf("missed count = %d, want 1", runner.losses["missed"])
	}
}

func TestFreshCursorDoesNotReportPreviousOwnersIntervalsLost(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
	runner := newCollectionRunner(testClient(now, testNonce(74)))
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
	d := agentprotocol.SLICollection{SLIID: "sli", MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: now.Add(-10 * time.Minute)}
	lease := testLease(now.Add(-time.Minute), "lease", now.Add(time.Hour))
	lease.ConfigurationRevision = "1"
	lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: "sli", MeasurementRevision: "1"}}
	runner.SetLease(lease)
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{d}})
	launched := make(chan collectionDefinition, 1)
	var workers sync.WaitGroup
	runner.workers = &workers
	runner.launchCollection = func(_ context.Context, d collectionDefinition) { launched <- d }
	runner.dispatchDue(t.Context())
	workers.Wait()
	select {
	case result := <-launched:
		if !result.intervalEnd.Equal(now.Add(-time.Minute)) {
			t.Fatalf("endpoint %v, want %v", result.intervalEnd, now.Add(-time.Minute))
		}
	default:
		t.Fatal("latest settled interval was not dispatched")
	}
	if runner.losses["missed"] != 0 {
		t.Fatalf("fresh cursor reported %d historical losses", runner.losses["missed"])
	}
	if len(runner.active) != 0 {
		t.Fatal("worker completion returned before reservation release")
	}
}

func TestFreshCursorUsesUnixCanonicalBoundaryForArbitraryCadence(t *testing.T) {
	now := time.Unix(250, 0).UTC()
	runner := newCollectionRunner(testClient(now, testNonce(76)))
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 17, SettlingDelaySeconds: 60}
	d := agentprotocol.SLICollection{SLIID: "sli", MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: time.Unix(85, 0).UTC()}
	lease := testLease(now.Add(-time.Minute), "lease", now.Add(time.Hour))
	lease.ConfigurationRevision = "1"
	lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: "sli", MeasurementRevision: "1"}}
	runner.SetLease(lease)
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{d}})
	launched := make(chan collectionDefinition, 1)
	var workers sync.WaitGroup
	runner.workers = &workers
	runner.launchCollection = func(_ context.Context, result collectionDefinition) { launched <- result }
	runner.dispatchDue(t.Context())
	workers.Wait()
	select {
	case result := <-launched:
		if result.intervalEnd.Unix() != 187 || result.intervalEnd.Unix()%17 != 0 || result.intervalEnd.Add(-17*time.Second).Before(d.EffectiveFrom) {
			t.Fatalf("invalid endpoint %v for effective bound %v", result.intervalEnd, d.EffectiveFrom)
		}
	default:
		t.Fatal("latest settled canonical interval was not dispatched")
	}
}

func TestFirstCollectionIsImmediateAndRestartUsesPersistedCursorAndPhase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
		const id = "00000000-0000-4000-8000-000000000011"
		phase := sli.DispatchPhase(id, 60)
		if phase == 0 {
			t.Fatal("test requires a nonzero phase")
		}
		agent := testClient(base, testNonce(77))
		assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
		definition := agentprotocol.SLICollection{SLIID: id, MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: base.Add(-3 * time.Minute)}
		lease := testLease(base.Add(-time.Minute), "lease", base.Add(time.Hour))
		lease.ConfigurationRevision = "1"
		lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: id, MeasurementRevision: "1"}}
		snapshot := agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{definition}}
		runner := newCollectionRunner(agent)
		var workers sync.WaitGroup
		runner.workers = &workers
		launched := make(chan collectionDefinition, 2)
		runner.launchCollection = func(_ context.Context, d collectionDefinition) { launched <- d }
		runner.SetLease(lease)
		runner.SetConfiguration(snapshot)
		runner.dispatchDue(t.Context())
		workers.Wait()
		first := <-launched
		if !first.intervalEnd.Equal(base.Add(-time.Minute)) {
			t.Fatalf("initial endpoint %v", first.intervalEnd)
		}

		// A new process receives a backend-persisted cursor for that first result.
		last := first.intervalEnd
		lease.AllowedMeasurements[0].LastAttemptedIntervalEnd = &last
		restarted := newCollectionRunner(agent)
		restarted.workers = &workers
		restarted.launchCollection = func(_ context.Context, d collectionDefinition) { launched <- d }
		restarted.SetLease(lease)
		restarted.SetConfiguration(snapshot)
		restarted.dispatchDue(t.Context())
		workers.Wait()
		select {
		case d := <-launched:
			t.Fatalf("restart repeated interval %v", d.intervalEnd)
		default:
		}
		// The next canonical endpoint becomes due only after its stable phase.
		agent.serverClock.mu.Lock()
		agent.serverClock.serverTime = base.Add(time.Minute + phase - time.Microsecond)
		agent.serverClock.serverAt = time.Now()
		agent.serverClock.mu.Unlock()
		restarted.dispatchDue(t.Context())
		workers.Wait()
		select {
		case d := <-launched:
			t.Fatalf("recurring interval ran before phase: %v", d.intervalEnd)
		default:
		}
		agent.serverClock.mu.Lock()
		agent.serverClock.serverTime = base.Add(time.Minute + phase)
		agent.serverClock.serverAt = time.Now()
		agent.serverClock.mu.Unlock()
		restarted.dispatchDue(t.Context())
		workers.Wait()
		select {
		case d := <-launched:
			if !d.intervalEnd.Equal(base) {
				t.Fatalf("recurring endpoint %v", d.intervalEnd)
			}
		default:
			t.Fatal("phased recurring interval did not run")
		}
	})
}

func TestRecurringDispatchSpreadsAcrossCadence(t *testing.T) {
	for _, count := range []int{100, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
				agent := testClient(base, testNonce(78))
				runner := newCollectionRunner(agent)
				lease := testLease(base.Add(-time.Minute), "lease", base.Add(time.Hour))
				lease.ConfigurationRevision = "1"
				snapshot := agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1"}
				for _, source := range []string{"a", "b", "c"} {
					snapshot.Assignments = append(snapshot.Assignments, agentprotocol.DatasourceAssignment{DatasourceID: source, DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60})
				}
				previous := base.Add(-2 * time.Minute)
				for i := range count {
					id := fmt.Sprintf("sli-%04d", i)
					d := agentprotocol.SLICollection{SLIID: id, MeasurementRevision: "1", DatasourceID: snapshot.Assignments[i%3].DatasourceID, DatasourceVersion: "1", EffectiveFrom: base.Add(-10 * time.Minute)}
					snapshot.SLIs = append(snapshot.SLIs, d)
					lease.AllowedMeasurements = append(lease.AllowedMeasurements, agentprotocol.MeasurementGrant{SLIID: id, MeasurementRevision: "1", LastAttemptedIntervalEnd: &previous})
				}
				runner.SetLease(lease)
				runner.SetConfiguration(snapshot)
				seen := make(map[string]bool)
				buckets := 0
				for second := range 60 {
					agent.serverClock.mu.Lock()
					agent.serverClock.serverTime = base.Add(time.Duration(second+1)*time.Second - time.Microsecond)
					agent.serverClock.serverAt = time.Now()
					agent.serverClock.mu.Unlock()
					before := len(seen)
					for {
						started := make(chan collectionDefinition, maxCollectionQueries)
						release := make(chan struct{})
						var workers sync.WaitGroup
						runner.workers = &workers
						runner.launchCollection = func(ctx context.Context, d collectionDefinition) {
							started <- d
							select {
							case <-release:
							case <-ctx.Done():
							}
						}
						runner.dispatchDue(t.Context())
						active := len(runner.active)
						if active > maxCollectionQueries {
							t.Fatalf("active %d", active)
						}
						for _, n := range runner.activeSources {
							if n > maxCollectionPerSource {
								t.Fatalf("source active %d", n)
							}
						}
						for range active {
							d := <-started
							if seen[d.sli.SLIID] || !d.intervalEnd.Equal(base.Add(-time.Minute)) {
								t.Fatalf("duplicate or wrong endpoint: %+v", d)
							}
							seen[d.sli.SLIID] = true
						}
						close(release)
						workers.Wait()
						if active == 0 {
							break
						}
					}
					if len(seen) > before {
						buckets++
					}
				}
				if len(seen) != count || buckets < 30 {
					t.Fatalf("dispatched %d/%d across %d seconds", len(seen), count, buckets)
				}
			})
		})
	}
}

func TestCursorHintRejectsUnsettledOrUnalignedIntervals(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
	d := agentprotocol.SLICollection{SLIID: "sli", MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: base.Add(-3 * time.Minute)}
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
	for _, hint := range []time.Time{base, base.Add(-time.Minute + time.Second)} {
		runner := newCollectionRunner(testClient(base, testNonce(79)))
		lease := testLease(base.Add(-time.Minute), "lease", base.Add(time.Hour))
		lease.ConfigurationRevision = "1"
		lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: d.SLIID, MeasurementRevision: "1", LastAttemptedIntervalEnd: &hint}}
		runner.SetLease(lease)
		runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{d}})
		runner.dispatchDue(t.Context())
		if !runner.nextIntervalEnd[collectionKey(d)].IsZero() || len(runner.active) != 0 {
			t.Fatalf("accepted invalid cursor %v", hint)
		}
	}
}

func TestSchedulerRefillsOnCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Minute)
		agent := testClient(now, testNonce(72))
		runner := newCollectionRunner(agent)
		assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
		lease := testLease(now.Add(-time.Minute), "lease", now.Add(time.Hour))
		lease.ConfigurationRevision = "1"
		snapshot := agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}}
		for i := range 5 {
			id := fmt.Sprintf("sli-%d", i)
			d := agentprotocol.SLICollection{SLIID: id, MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: now.Add(-10 * time.Minute)}
			snapshot.SLIs = append(snapshot.SLIs, d)
			lease.AllowedMeasurements = append(lease.AllowedMeasurements, agentprotocol.MeasurementGrant{SLIID: id, MeasurementRevision: "1"})
			runner.nextIntervalEnd[collectionKey(d)] = now.Add(-time.Minute)
			runner.initialPending[collectionKey(d)] = true
		}
		runner.SetLease(lease)
		runner.SetConfiguration(snapshot)
		started := make(chan string, 5)
		release := make(chan struct{})
		runner.launchCollection = func(ctx context.Context, d collectionDefinition) {
			started <- d.sli.SLIID
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		var workers sync.WaitGroup
		runner.start(ctx, &workers)
		defer func() { cancel(); workers.Wait() }()
		for range 4 {
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("initial work did not start")
			}
		}
		close(release)
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("available slot waited for scheduler tick")
		}
	})
}

func TestFairBoundedAdmissionAcrossLargeConfigurations(t *testing.T) {
	for _, count := range []int{100, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			now := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
			runner := newCollectionRunner(testClient(now, testNonce(73)))
			lease := testLease(now.Add(-time.Minute), "lease", now.Add(time.Hour))
			lease.ConfigurationRevision = "1"
			snapshot := agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1"}
			for _, source := range []string{"a", "b", "c"} {
				snapshot.Assignments = append(snapshot.Assignments, agentprotocol.DatasourceAssignment{DatasourceID: source, DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60})
			}
			for i := range count {
				id := fmt.Sprintf("sli-%04d", i)
				source := snapshot.Assignments[i%3].DatasourceID
				d := agentprotocol.SLICollection{SLIID: id, MeasurementRevision: "1", DatasourceID: source, DatasourceVersion: "1", EffectiveFrom: now.Add(-2 * time.Minute)}
				snapshot.SLIs = append(snapshot.SLIs, d)
				lease.AllowedMeasurements = append(lease.AllowedMeasurements, agentprotocol.MeasurementGrant{SLIID: id, MeasurementRevision: "1"})
				runner.nextIntervalEnd[collectionKey(d)] = now.Add(-time.Minute)
				runner.initialPending[collectionKey(d)] = true
			}
			runner.SetLease(lease)
			runner.SetConfiguration(snapshot)
			seen := make(map[string]bool)
			for len(seen) < count {
				started := make(chan collectionDefinition, maxCollectionQueries)
				release := make(chan struct{})
				var workers sync.WaitGroup
				runner.workers = &workers
				runner.launchCollection = func(ctx context.Context, d collectionDefinition) {
					started <- d
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				runner.dispatchDue(t.Context())
				active := len(runner.active)
				if active == 0 || active > maxCollectionQueries {
					t.Fatalf("active = %d after %d admitted", active, len(seen))
				}
				for source, n := range runner.activeSources {
					if n > maxCollectionPerSource {
						t.Fatalf("source %s active = %d", source, n)
					}
				}
				for range active {
					d := <-started
					if seen[d.sli.SLIID] {
						t.Fatalf("duplicate admission %s", d.sli.SLIID)
					}
					seen[d.sli.SLIID] = true
				}
				close(release)
				workers.Wait()
				if len(seen) >= count/2 {
					slices.Reverse(snapshot.SLIs)
					runner.SetConfiguration(snapshot)
				}
			}
		})
	}
}

func TestSaturatedWindowsRotateAdmissionAcrossReorderedConfigurations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Date(2026, 10, 27, 12, 44, 0, 0, time.UTC)
		agent := testClient(base, testNonce(75))
		runner := newCollectionRunner(agent)
		assignment := agentprotocol.DatasourceAssignment{DatasourceID: "source", DatasourceVersion: "1", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
		lease := testLease(base.Add(-time.Minute), "lease", base.Add(time.Hour))
		lease.ConfigurationRevision = "1"
		snapshot := agentprotocol.ConfigurationSnapshot{ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{assignment}}
		var latestPhase time.Duration
		for i := range 12 {
			id := fmt.Sprintf("sli-%02d", i)
			d := agentprotocol.SLICollection{SLIID: id, MeasurementRevision: "1", DatasourceID: "source", DatasourceVersion: "1", EffectiveFrom: base.Add(-2 * time.Minute)}
			snapshot.SLIs = append(snapshot.SLIs, d)
			lease.AllowedMeasurements = append(lease.AllowedMeasurements, agentprotocol.MeasurementGrant{SLIID: id, MeasurementRevision: "1"})
			latestPhase = max(latestPhase, sli.DispatchPhase(id, 60))
			runner.nextIntervalEnd[collectionKey(d)] = base.Add(-time.Minute)
		}
		runner.SetLease(lease)
		seen := make(map[string]bool)
		for window := range 3 {
			agent.serverClock.mu.Lock()
			agent.serverClock.serverTime = base.Add(time.Duration(window)*time.Minute + latestPhase + time.Microsecond)
			agent.serverClock.serverAt = time.Now()
			agent.serverClock.mu.Unlock()
			slices.Reverse(snapshot.SLIs)
			runner.SetConfiguration(snapshot)
			started := make(chan collectionDefinition, maxCollectionPerSource)
			release := make(chan struct{})
			var workers sync.WaitGroup
			runner.workers = &workers
			runner.launchCollection = func(ctx context.Context, d collectionDefinition) {
				started <- d
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			runner.dispatchDue(t.Context())
			if len(runner.active) != maxCollectionPerSource {
				t.Fatalf("window %d admitted %d", window, len(runner.active))
			}
			for range maxCollectionPerSource {
				d := <-started
				if seen[d.sli.SLIID] {
					t.Fatalf("window %d repeated %s before other definitions ran", window, d.sli.SLIID)
				}
				seen[d.sli.SLIID] = true
			}
			close(release)
			workers.Wait()
		}
		if len(seen) != 12 {
			t.Fatalf("admitted %d of 12 definitions", len(seen))
		}
	})
}

func TestCollectedOutcomePreservesMissingAndExactCountSemantics(t *testing.T) {
	tests := []struct {
		name    string
		good    prometheus.QueryResult
		total   prometheus.QueryResult
		want    sli.OutcomeState
		counts  [2]string
		missing []string
		error   string
	}{
		{name: "observed exact counts", good: oneValue("99"), total: oneValue("100"), want: sli.OutcomeObserved, counts: [2]string{"99", "100"}},
		{name: "explicit complete empty", good: emptyResult(), total: emptyResult(), want: sli.OutcomeCompleteEmpty},
		{name: "missing good component", good: emptyResult(), total: oneValue("100"), want: sli.OutcomeIncomplete, missing: []string{"good"}},
		{name: "partial warning remains unknown", good: incompleteResult(), total: oneValue("100"), want: sli.OutcomeIncomplete, missing: []string{"good"}},
		{name: "multiple series requires aggregation", good: prometheus.QueryResult{Class: "complete", Series: []prometheus.Series{{Value: "1"}, {Value: "2"}}}, total: oneValue("2"), want: sli.OutcomeFailed, error: "unsupported"},
		{name: "good exceeds total", good: oneValue("101"), total: oneValue("100"), want: sli.OutcomeFailed, error: "query"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := collectedOutcome(test.good, true, test.good.Class == "incomplete", test.total, true, test.total.Class == "incomplete")
			if got.State != test.want || got.GoodCount != test.counts[0] || got.TotalCount != test.counts[1] || got.ErrorCode != test.error {
				t.Fatalf("outcome = %+v", got)
			}
			if len(got.MissingComponents) != len(test.missing) {
				t.Fatalf("missing components = %v, want %v", got.MissingComponents, test.missing)
			}
			for index := range test.missing {
				if got.MissingComponents[index] != test.missing[index] {
					t.Fatalf("missing components = %v, want %v", got.MissingComponents, test.missing)
				}
			}
		})
	}
}

func TestCollectedOutcomeRequiresBothComponentTimestampsAtCanonicalEvaluationEnd(t *testing.T) {
	evaluationEnd := time.Date(2026, 10, 27, 12, 5, 0, 0, time.UTC)
	good := oneValue("99")
	total := oneValue("100")
	good.Series[0].Time = "1793102700.000"
	total.Series[0].Time = "1793102700.000"
	if got := collectedOutcomeAt(good, true, false, total, true, false, evaluationEnd); got.State != sli.OutcomeObserved {
		t.Fatalf("aligned component results = %+v, want observed", got)
	}
	total.Series[0].Time = "1793102700.001"
	if got := collectedOutcomeAt(good, true, false, total, true, false, evaluationEnd); got.State != sli.OutcomeFailed || got.ErrorCode != "query" {
		t.Fatalf("misaligned total result = %+v, want failed query", got)
	}
}

func TestLeaseDeadlineAnchorsToSendAndRejectsDelayedOrSkewedResponses(t *testing.T) {
	sent := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	serverAtSend := time.Date(2026, 10, 27, 12, 0, 1, 0, time.UTC)
	lease := testLease(serverAtSend, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", serverAtSend.Add(time.Minute))
	deadline, err := leaseDeadlineFromResponse(lease, serverAtSend, sent, sent.Add(2*time.Second))
	if err != nil || !deadline.Equal(sent.Add(time.Minute)) {
		t.Fatalf("lease deadline = %s, %v; want send time plus one minute", deadline, err)
	}
	if _, err := leaseDeadlineFromResponse(lease, serverAtSend, sent, sent.Add(6*time.Second)); err == nil {
		t.Fatal("accepted lease response after the safe round-trip window")
	}
	shortLease := testLease(serverAtSend, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", serverAtSend.Add(4*time.Second))
	if _, err := leaseDeadlineFromResponse(shortLease, serverAtSend, sent, sent.Add(5*time.Second)); err == nil {
		t.Fatal("accepted delayed response after the conservatively anchored lease deadline")
	}
	if !serverClockSkewed(serverAtSend, serverAtSend.Add(6*time.Second)) || serverClockSkewed(serverAtSend, serverAtSend.Add(5*time.Second)) {
		t.Fatal("server clock skew guard did not enforce the five-second threshold")
	}
}

func TestCollectionHeartbeatReportsBoundedBufferEviction(t *testing.T) {
	runner := newCollectionRunner(&client{})
	for index := range 34 {
		runner.enqueue(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1), make([]byte, 1<<20), time.Time{})
	}
	progress, _, _, _ := runner.Heartbeat()
	if progress != "not_started" || runner.resultBytes > maxLiveResultBytes || runner.dropped == 0 || runner.losses["buffer"] == 0 {
		t.Fatalf("buffer status = progress %q bytes %d drops %d", progress, runner.resultBytes, runner.dropped)
	}
}

func TestEmptyConfigurationReportsCollectionNotStarted(t *testing.T) {
	runner := newCollectionRunner(&client{})
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{Assignments: []agentprotocol.DatasourceAssignment{}, SLIs: []agentprotocol.SLICollection{}})
	progress, attempt, success, next := runner.Heartbeat()
	if progress != "not_started" || attempt != nil || success != nil || next != nil {
		t.Fatalf("empty configuration reported collection activity: progress=%q attempt=%v success=%v next=%v", progress, attempt, success, next)
	}
}

func TestLiveBufferOrdersByIntervalAndReleasesAcknowledgedPayload(t *testing.T) {
	runner := newCollectionRunner(&client{})
	now := time.Now()
	runner.enqueue("newer", []byte("new"), now)
	runner.enqueue("older", []byte("old"), now.Add(-time.Minute))
	batch := runner.takeBatch()
	if len(batch) != 2 || batch[0].deliveryID != "older" {
		t.Fatal("oldest measurement must be selected first")
	}
	runner.finishUpload(batch[0], true)
	if batch[0].raw != nil || len(runner.pendingOrder) != 1 || runner.resultBytes != batch[1].size {
		t.Fatal("acknowledged payload remained retained or accounted")
	}
	runner.retryAfter(batch[1], "30")
	if time.Until(batch[1].nextRetry) < 29*time.Second {
		t.Fatal("bounded server retry guidance was not honored")
	}
}

func TestOversizedResultDoesNotEvictExistingBuffer(t *testing.T) {
	runner := newCollectionRunner(&client{})
	runner.enqueue("kept", []byte("valid"), time.Now())
	before := runner.resultBytes
	runner.enqueue("oversized", make([]byte, (1<<20)+1), time.Now())
	if len(runner.pending) != 1 || runner.pending["kept"] == nil || runner.resultBytes != before || runner.losses["encoding"] != 1 {
		t.Fatal("oversized item changed retained buffer or did not record loss")
	}
}

func TestUploadRetries429WithStableDeliveryAndBoundedGuidance(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000020"
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[{"delivery_id":"%s","status":"accepted"}]}`, id)
	}))
	defer server.Close()
	runner := newCollectionRunner(&client{opts: options{backendURL: server.URL}, http: server.Client()})
	runner.enqueue(id, []byte(`{"delivery_id":"`+id+`"}`), time.Now())
	batch := runner.takeBatch()
	runner.submit(t.Context(), batch)
	if len(runner.pending) != 1 || time.Until(batch[0].nextRetry) < 29*time.Second {
		t.Fatal("429 did not retain delivery and honor guidance")
	}
	batch[0].nextRetry = time.Now().Add(-time.Second)
	runner.submit(t.Context(), runner.takeBatch())
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || len(runner.pending) != 0 || runner.resultBytes != 0 {
		t.Fatal("retry changed delivery or failed to release acknowledged result")
	}
}

func TestUploadBatchRespectsBodyLimitAndLateAckDoesNotReleaseReplacement(t *testing.T) {
	runner := newCollectionRunner(&client{})
	for index := range 5 {
		runner.enqueue(fmt.Sprintf("delivery-%d", index), make([]byte, 1<<20), time.Unix(int64(index+1), 0))
	}
	batch := runner.takeBatch()
	size := len(`{"items":[]}`)
	for _, entry := range batch {
		size += len(entry.raw) + 1
	}
	if len(batch) != 3 || size > 4<<20 {
		t.Fatalf("batch items=%d bytes=%d", len(batch), size)
	}
	old := batch[0]
	runner.mu.Lock()
	if !runner.evictOldestLocked() {
		runner.mu.Unlock()
		t.Fatal("could not evict oldest in-flight result")
	}
	runner.mu.Unlock()
	runner.enqueue(old.deliveryID, []byte("replacement"), time.Now())
	replacement := runner.pending[old.deliveryID]
	before := runner.resultBytes
	runner.finishUpload(old, true)
	if runner.pending[old.deliveryID] != replacement || replacement.raw == nil || runner.resultBytes != before-old.size {
		t.Fatal("late acknowledgment released replacement or corrupted byte accounting")
	}
}

func TestLiveLossReportsBoundedReasonsAndIntervalSummary(t *testing.T) {
	runner := newCollectionRunner(&client{})
	var logs bytes.Buffer
	runner.lossLogger = slog.New(slog.NewJSONHandler(&logs, nil))
	first := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	runner.recordDrop("terminal", first.Add(time.Minute))
	runner.recordDrop("buffer", first)
	runner.reportLosses(false)
	if !strings.Contains(logs.String(), `"terminal":1`) || !strings.Contains(logs.String(), `"buffer":1`) || !strings.Contains(logs.String(), "2026-10-27T12:00:00Z") {
		t.Fatalf("loss summary missing bounded counts or interval: %s", logs.String())
	}
	before := logs.Len()
	runner.recordDrop("buffer", first)
	runner.reportLosses(false)
	if logs.Len() != before {
		t.Fatal("loss reporting was not rate limited")
	}
	runner.reportLosses(true)
	if logs.Len() == before {
		t.Fatal("shutdown did not flush pending losses")
	}
}

func TestCollectionSubmissionUsesStableDeliveryAndExecutionIDs(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 5, 0, 123000, time.UTC)
	agent := &client{agentID: testAgentID, serverClock: &serverClockEstimate{serverTime: now, serverAt: time.Now()}}
	runner := newCollectionRunner(agent)
	definition := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1",
		EffectiveFrom: now.Add(-2 * time.Minute)}
	lease := testLease(now.Add(-time.Minute), "Abcdef0123456789Abcdef0123456789", now.Add(time.Hour))
	job := collectionDefinition{sli: definition, assignment: agentprotocol.DatasourceAssignment{PollingIntervalSeconds: 15}, lease: lease,
		intervalEnd: now.Add(-time.Minute), key: collectionKey(definition)}
	runner.finishCollection(job, &sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}, nil)
	if len(runner.pending) != 1 {
		t.Fatalf("pending result count = %d, want 1", len(runner.pending))
	}
	var item struct {
		DeliveryID    string    `json:"delivery_id"`
		ExecutionID   string    `json:"execution_id"`
		CreatedAt     time.Time `json:"created_at"`
		LeaseID       string    `json:"lease_id"`
		IntervalStart time.Time `json:"interval_start"`
		IntervalEnd   time.Time `json:"interval_end"`
		Outcome       struct {
			State      string `json:"state"`
			GoodCount  string `json:"good_count"`
			TotalCount string `json:"total_count"`
		} `json:"outcome"`
	}
	for _, saved := range runner.pending {
		if err := json.Unmarshal(saved.raw, &item); err != nil {
			t.Fatal(err)
		}
	}
	if item.DeliveryID == "" || item.ExecutionID == "" || item.DeliveryID == item.ExecutionID || item.LeaseID != lease.LeaseID ||
		item.CreatedAt.Location() != time.UTC || item.Outcome.State != "observed" || item.Outcome.GoodCount != "99" || item.Outcome.TotalCount != "100" ||
		!item.IntervalEnd.Equal(job.intervalEnd) || !item.IntervalStart.Equal(job.intervalEnd.Add(-15*time.Second)) {
		t.Fatalf("serialized observation item = %+v", item)
	}
}

func TestDispatchDueReservesGlobalPerSourceAndPerSLIConcurrencyBeforeLaunch(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 5, 0, 0, time.UTC)
	agent := &client{agentID: "mutable-before-welcome", serverClock: &serverClockEstimate{serverTime: now, serverAt: time.Now()}}
	runner := newCollectionRunner(agent)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var workers sync.WaitGroup
	launched := make(chan collectionDefinition, 16)
	canceled := make(chan string, 16)
	runner.mu.Lock()
	runner.root = ctx
	runner.workers = &workers
	runner.launchCollection = func(ctx context.Context, definition collectionDefinition) {
		launched <- definition
		<-ctx.Done()
		canceled <- definition.key
	}
	runner.mu.Unlock()

	assignments := make([]agentprotocol.DatasourceAssignment, 3)
	for index := range assignments {
		assignments[index] = agentprotocol.DatasourceAssignment{DatasourceID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1),
			DatasourceVersion: "1", Name: fmt.Sprintf("source-%d", index), Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1",
			Endpoint: "http://127.0.0.1:8428", PollingIntervalSeconds: 15, SettlingDelaySeconds: 60}
	}
	slis := make([]agentprotocol.SLICollection, 10)
	grants := make([]agentprotocol.MeasurementGrant, 0, len(slis))
	for index := range slis {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+100)
		source := assignments[index%len(assignments)]
		slis[index] = agentprotocol.SLICollection{SLIID: id, MeasurementRevision: "1", DatasourceID: source.DatasourceID,
			DatasourceVersion: source.DatasourceVersion, MeasurementKind: agentprotocol.MeasurementKindEventCounts,
			ComponentMode: agentprotocol.ComponentModeIntervalCount, GoodQuery: "vector(1)", TotalQuery: "vector(2)", EffectiveFrom: now.Add(-2 * time.Minute)}
		grants = append(grants, agentprotocol.MeasurementGrant{SLIID: id, MeasurementRevision: "1"})
	}
	lease := testLease(now.Add(-time.Hour), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", now.Add(time.Hour))
	lease.AgentID = testAgentID
	lease.AllowedMeasurements = grants
	runner.SetLease(lease)
	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{Assignments: assignments, SLIs: slis})
	runner.mu.Lock()
	for _, definition := range slis {
		runner.nextIntervalEnd[collectionKey(definition)] = now.Add(-61 * time.Second)
		runner.initialPending[collectionKey(definition)] = true
	}
	runner.mu.Unlock()

	runner.dispatchDue(ctx)
	for range maxCollectionQueries {
		select {
		case definition := <-launched:
			if definition.agentID != lease.AgentID {
				t.Fatalf("collection read mutable client identity %q instead of lease identity %q", definition.agentID, lease.AgentID)
			}
		case <-time.After(time.Second):
			t.Fatal("dispatch did not reserve the global query limit")
		}
	}
	runner.dispatchDue(ctx)
	runner.mu.Lock()
	active, sourceCounts := len(runner.active), make(map[string]int, len(runner.activeSources))
	for key, count := range runner.activeSources {
		sourceCounts[key] = count
	}
	runner.mu.Unlock()
	if active != maxCollectionQueries || len(launched) != 0 {
		t.Fatalf("second dispatch duplicated active keys or exceeded global limit: active=%d queued_launches=%d", active, len(launched))
	}
	for sourceID, count := range sourceCounts {
		if count > maxCollectionPerSource {
			t.Fatalf("datasource %s has %d active queries", sourceID, count)
		}
	}

	runner.SetConfiguration(agentprotocol.ConfigurationSnapshot{})
	for range maxCollectionQueries {
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("removed configuration did not cancel an already reserved collection")
		}
	}
	cancel()
	workers.Wait()
}

func TestValidateLeaseAllowsScopedMeasurementGrant(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	agent := testClient(now, testNonce(1))
	lease := testLease(now, "lease-current", now.Add(time.Hour))
	lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1"}}
	if err := agent.validateLease(lease, now); err != nil {
		t.Fatalf("valid scoped execution lease rejected: %v", err)
	}
}

func oneValue(value string) prometheus.QueryResult {
	return prometheus.QueryResult{Class: "complete", Series: []prometheus.Series{{Value: value}}}
}

func emptyResult() prometheus.QueryResult {
	return prometheus.QueryResult{Class: "empty", Series: []prometheus.Series{}}
}

func incompleteResult() prometheus.QueryResult {
	return prometheus.QueryResult{Class: "incomplete", Series: []prometheus.Series{}}
}

func TestConfigurationDrainWaitsForRemovedCollectionAndLeavesOthersRunning(t *testing.T) {
	runner := newCollectionRunner(&client{})
	removedCtx, cancelRemoved := context.WithCancel(t.Context())
	otherCtx, cancelOther := context.WithCancel(t.Context())
	defer cancelOther()
	removedDone, otherDone := make(chan struct{}), make(chan struct{})
	removed := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000001", MeasurementRevision: "1"}
	other := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000002", MeasurementRevision: "1"}
	runner.active[collectionKey(removed)] = activeCollection{reservation: 1, cancel: cancelRemoved, done: removedDone}
	runner.active[collectionKey(other)] = activeCollection{reservation: 2, cancel: cancelOther, done: otherDone}
	result := make(chan error, 1)
	go func() {
		result <- runner.SetConfigurationAndDrain(t.Context(), agentprotocol.ConfigurationSnapshot{SLIs: []agentprotocol.SLICollection{other}})
	}()
	select {
	case <-removedCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("removed work was not canceled")
	}
	select {
	case <-otherCtx.Done():
		t.Fatal("unrelated work was canceled")
	default:
	}
	select {
	case <-result:
		t.Fatal("drain completed before removed work exited")
	default:
	}
	close(removedDone)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}
}

func TestConfigurationDrainReturnsCancellationWithoutClaimingCompletion(t *testing.T) {
	runner := newCollectionRunner(&client{})
	ctx, cancel := context.WithCancel(t.Context())
	work, stopWork := context.WithCancel(t.Context())
	defer stopWork()
	sli := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000001", MeasurementRevision: "1"}
	runner.active[collectionKey(sli)] = activeCollection{reservation: 1, cancel: stopWork, done: make(chan struct{})}
	cancel()
	if err := runner.SetConfigurationAndDrain(ctx, agentprotocol.ConfigurationSnapshot{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain error = %v", err)
	}
	select {
	case <-work.Done():
	default:
		t.Fatal("cancellation did not reach removed work")
	}
}

func TestTransitionDrainsVersionChangedPairBeforeRestoringIt(t *testing.T) {
	runner := newCollectionRunner(&client{})
	work, cancelWork := context.WithCancel(t.Context())
	done := make(chan struct{})
	prior := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000001", MeasurementRevision: "1", DatasourceVersion: "1"}
	restored := prior
	restored.DatasourceVersion = "2"
	runner.active[collectionKey(prior)] = activeCollection{reservation: 1, cancel: cancelWork, done: done}
	result := make(chan error, 1)
	go func() {
		result <- runner.SetTransitionConfigurationAndDrain(t.Context(), agentprotocol.ConfigurationSnapshot{SLIs: []agentprotocol.SLICollection{restored}}, map[string]bool{})
	}()
	select {
	case <-work.Done():
	case <-time.After(time.Second):
		t.Fatal("version-changed work was not canceled")
	}
	select {
	case <-result:
		t.Fatal("transition returned before work drained")
	default:
	}
	close(done)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestTransitionKeepsUnchangedCursorAndWithholdsAddedCollection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	agent := testClient(now, testNonce(60))
	runner := newCollectionRunner(agent)
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: "00000000-0000-4000-8000-000000000020", DatasourceVersion: "1", Name: "metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "http://127.0.0.1:8428", AuthenticationMode: "none", PollingIntervalSeconds: 15, SettlingDelaySeconds: 60}
	retained := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1", DatasourceID: assignment.DatasourceID, DatasourceVersion: "1", MeasurementKind: agentprotocol.MeasurementKindEventCounts, ComponentMode: agentprotocol.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", EffectiveFrom: now.Add(-2 * time.Minute)}
	added := retained
	added.SLIID = "00000000-0000-4000-8000-000000000011"
	lease := testLease(now.Add(-time.Minute), "lease-transition", now.Add(time.Hour))
	lease.AgentID = testAgentID
	lease.ConfigurationRevision = "1"
	lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: retained.SLIID, MeasurementRevision: "1"}, {SLIID: added.SLIID, MeasurementRevision: "1"}}
	runner.SetLease(lease)
	cursor := now.Add(-61 * time.Second)
	runner.nextIntervalEnd[collectionKey(retained)] = cursor
	runner.initialPending[collectionKey(retained)] = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	launched := make(chan string, 2)
	var workers sync.WaitGroup
	runner.root, runner.workers = ctx, &workers
	runner.launchCollection = func(_ context.Context, definition collectionDefinition) { launched <- definition.sli.SLIID }
	if err := runner.SetTransitionConfigurationAndDrain(ctx, agentprotocol.ConfigurationSnapshot{AgentID: testAgentID, ConfigurationRevision: "2", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{retained, added}}, map[string]bool{collectionKey(retained): true}); err != nil {
		t.Fatal(err)
	}
	if got := runner.nextIntervalEnd[collectionKey(retained)]; !got.Equal(cursor) {
		t.Fatalf("retained cursor changed: %v", got)
	}
	runner.nextIntervalEnd[collectionKey(added)] = cursor
	runner.dispatchDue(ctx)
	workers.Wait()
	select {
	case got := <-launched:
		if got != retained.SLIID {
			t.Fatalf("ungranted added collection executed: %s", got)
		}
	default:
		t.Fatal("unchanged collection did not run")
	}
	select {
	case got := <-launched:
		t.Fatalf("unexpected collection executed: %s", got)
	default:
	}
}

func TestTransitionDefersAddedCursorUntilDelayedGrant(t *testing.T) {
	for _, test := range []struct {
		name  string
		delay time.Duration
		want  time.Duration
	}{
		{name: "first settled interval", delay: 75 * time.Second, want: 15 * time.Second},
		{name: "late takeover", delay: 180 * time.Second, want: 120 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			cutoff := time.Now().UTC().Truncate(15 * time.Second).Add(30 * time.Second)
			agent := testClient(cutoff.Add(-15*time.Second), testNonce(70))
			runner := newCollectionRunner(agent)
			assignment := agentprotocol.DatasourceAssignment{DatasourceID: "00000000-0000-4000-8000-000000000020", DatasourceVersion: "1", Name: "metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "http://127.0.0.1:8428", AuthenticationMode: "none", PollingIntervalSeconds: 15, SettlingDelaySeconds: 60}
			added := agentprotocol.SLICollection{SLIID: "00000000-0000-4000-8000-000000000011", MeasurementRevision: "1", DatasourceID: assignment.DatasourceID, DatasourceVersion: "1", MeasurementKind: agentprotocol.MeasurementKindEventCounts, ComponentMode: agentprotocol.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", EffectiveFrom: cutoff}
			lease := testLease(cutoff.Add(-time.Minute), "lease-old", cutoff.Add(time.Hour))
			runner.SetLeaseAuthority(lease, time.Now().Add(time.Hour), true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			launched := make(chan collectionDefinition, 1)
			var workers sync.WaitGroup
			runner.root, runner.workers = ctx, &workers
			runner.launchCollection = func(_ context.Context, definition collectionDefinition) { launched <- definition }
			if err := runner.SetTransitionConfigurationAndDrain(ctx, agentprotocol.ConfigurationSnapshot{AgentID: testAgentID, ConfigurationRevision: "2", Assignments: []agentprotocol.DatasourceAssignment{assignment}, SLIs: []agentprotocol.SLICollection{added}}, map[string]bool{}); err != nil {
				t.Fatal(err)
			}
			runner.dispatchDue(ctx)
			if got := runner.nextIntervalEnd[collectionKey(added)]; !got.IsZero() {
				t.Fatalf("withheld cursor=%v, want uninitialized", got)
			}
			select {
			case <-launched:
				t.Fatal("added collection executed before its matching grant")
			default:
			}
			agent.serverClock.mu.Lock()
			agent.serverClock.serverTime = cutoff.Add(test.delay)
			agent.serverClock.serverAt = time.Now()
			agent.serverClock.mu.Unlock()
			lease.ConfigurationRevision = "2"
			runner.SetLeaseAuthority(lease, time.Now().Add(time.Hour), true)
			runner.dispatchDue(ctx)
			if got := runner.nextIntervalEnd[collectionKey(added)]; !got.IsZero() || runner.losses["missed"] != 0 {
				t.Fatalf("ungranted cursor=%v missed=%d", got, runner.losses["missed"])
			}
			lease.AllowedMeasurements = []agentprotocol.MeasurementGrant{{SLIID: added.SLIID, MeasurementRevision: "1"}}
			runner.SetLeaseAuthority(lease, time.Now().Add(time.Hour), true)
			runner.dispatchDue(ctx)
			workers.Wait()
			select {
			case definition := <-launched:
				if !definition.intervalEnd.Equal(cutoff.Add(test.want)) {
					t.Fatalf("latest settled interval = %v", definition.intervalEnd)
				}
			default:
				t.Fatal("latest settled interval did not run with matching grant")
			}
			if runner.losses["missed"] != 0 {
				t.Fatalf("reported %d losses before grant", runner.losses["missed"])
			}
		})
	}
}
