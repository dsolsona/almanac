//go:build integration && !linux

package command

import "testing"

func runRestrictedAgentCLI(t *testing.T, _, _, _, _, _ string) {
	t.Helper()
}

func runAgentManagementCLIAcceptance(t *testing.T, _, _, _, _, _, _ string) bool {
	t.Helper()
	return false
}
