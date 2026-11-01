package command

import (
	"errors"
	"testing"
	"time"
)

type performanceCollectionReport struct {
	Start           time.Time               `json:"start"`
	End             time.Time               `json:"end"`
	Expected        int64                   `json:"expected_intervals"`
	Observed        int64                   `json:"observed_intervals"`
	Missing         int64                   `json:"missing_source_intervals"`
	AttemptFailures int64                   `json:"failed_or_incomplete_attempts"`
	CollectionDelay performanceDistribution `json:"collection_delay_after_settling"`
	DeliveryAge     performanceDistribution `json:"delivery_age_before_commit"`
}

// Aggregate latency can stay fast when an entire agent disappears. Require the
// complete comparison interval set independently of ingestion percentiles.
func checkPerformanceCollection(report performanceCollectionReport) error {
	if report.Expected <= 0 || report.Observed != report.Expected || report.Missing != 0 ||
		report.AttemptFailures != 0 || int64(report.CollectionDelay.Count) != report.Observed {
		return errors.New("collection phase has missing intervals, failed attempts or inconsistent accounting")
	}
	return nil
}

func TestPerformanceCollectionRejectsLossDespiteEnoughFastSamples(t *testing.T) {
	complete := performanceCollectionReport{Expected: 1300, Observed: 1300, CollectionDelay: performanceDistribution{Count: 1300}}
	if err := checkPerformanceCollection(complete); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing agent", "failed attempts", "inconsistent count", "negative missing", "empty phase"} {
		t.Run(name, func(t *testing.T) {
			report := complete
			switch name {
			case "missing agent":
				report.Observed, report.Missing, report.CollectionDelay.Count = 1200, 100, 1200
			case "failed attempts":
				report.AttemptFailures = 1
			case "inconsistent count":
				report.CollectionDelay.Count = 1200
			case "negative missing":
				report.Missing = -1
			case "empty phase":
				report = performanceCollectionReport{}
			}
			if err := checkPerformanceCollection(report); err == nil {
				t.Fatal("invalid collection phase passed")
			}
		})
	}
}
