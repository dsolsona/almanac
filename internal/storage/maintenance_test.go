package storage

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestMaintenanceAlignmentRejectsNoRepresentableCommonBounds(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	err := maintenanceAlignment(math.MaxInt64, start, start.Add(time.Hour), []string{"scope"})
	alignment, ok := errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.IsZero() || !alignment.SuggestedEnd.IsZero() {
		t.Fatalf("unrepresentable common bounds must require separate scopes, got %+v %v", alignment, err)
	}
}
