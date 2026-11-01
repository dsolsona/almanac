//go:build integration && performance

package command

import "testing"

func TestIntegrationControlledBackendPerformance(t *testing.T) {
	runControlledBackendPerformance(t)
}
