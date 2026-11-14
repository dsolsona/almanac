//go:build integration

package storage

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// dropAlertJournalFixture removes only migration 0056's objects when rebuilding
// an older schema in a disposable integration database. Dropping the tables
// removes their triggers, but their shared history function must be removed too.
func dropAlertJournalFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), `DROP TABLE IF EXISTS
public.almanac_alert_transitions, public.almanac_alert_ranges,
public.almanac_alert_cursors, public.almanac_alert_occurrences,
public.almanac_alert_evaluations, public.almanac_alert_policy_revisions,
public.almanac_alert_policies;
DROP FUNCTION IF EXISTS public.almanac_immutable_alert_history()`); err != nil {
		t.Fatalf("remove alert journal fixture objects: %v", err)
	}
}
