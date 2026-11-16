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
	if _, err := conn.Exec(t.Context(), `DROP VIEW IF EXISTS public.almanac_alert_catalog;
DROP TABLE IF EXISTS
public.almanac_alert_breach_pins, public.almanac_alert_retention_work,
public.almanac_alert_transitions, public.almanac_alert_ranges,
public.almanac_alert_cursors, public.almanac_alert_occurrences,
public.almanac_alert_evaluations, public.almanac_alert_policy_revisions,
public.almanac_alert_policies;
DROP FUNCTION IF EXISTS public.almanac_immutable_alert_history()`); err != nil {
		t.Fatalf("remove alert journal fixture objects: %v", err)
	}
}

// Remove the exact unwired 0062 suffix before replaying an older populated
// schema. Do not use CASCADE to hide an unmodeled dependency.
func dropMaintenanceCompilerFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	dropMaintenanceCompatibilityFixture(t, conn)
	if _, err := conn.Exec(t.Context(), `DROP TABLE public.almanac_slo_maintenance_hours`); err != nil {
		t.Fatalf("reconstruct pre-0063 scoped-hour schema: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `
 DROP TRIGGER almanac_maintenance_change_pin ON public.almanac_slo_exclusion_changes;
 DROP TRIGGER almanac_maintenance_scope_pin ON public.almanac_maintenance_exclusion_scopes;
 DROP INDEX public.almanac_maintenance_change_provenance_idx;
 DROP TABLE public.almanac_maintenance_coverage,public.almanac_maintenance_members,
 public.almanac_maintenance_builds,public.almanac_maintenance_input_guards;
 DROP FUNCTION public.almanac_guard_maintenance_input_delete(),
 public.almanac_guard_maintenance_compiler_stage(),public.almanac_guard_maintenance_build_identity();`); err != nil {
		t.Fatalf("reconstruct pre-0062 compiler schema: %v", err)
	}
}

// This private schema rewind removes the exact 0065 indexes and 0064 objects before replay.
func dropMaintenanceCompatibilityFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	dropMaintenanceRetainedFixture(t, conn)
	if _, err := conn.Exec(t.Context(), `DROP TABLE public.almanac_maintenance_preparation;
DROP FUNCTION public.guard_maintenance_preparation_identity()`); err != nil {
		t.Fatalf("reconstruct pre-0066 preparation schema: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `DROP INDEX public.almanac_maintenance_complete_candidate_idx,public.almanac_maintenance_hour_candidate_idx`); err != nil {
		t.Fatalf("reconstruct pre-0065 candidate indexes: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `
 DROP TRIGGER almanac_maintenance_change_analyze ON public.almanac_slo_exclusion_changes;
 DROP TRIGGER almanac_maintenance_complete_coverage_pin ON public.almanac_maintenance_coverage;
 DROP TRIGGER almanac_maintenance_comparison_identity ON public.almanac_maintenance_comparisons;
 DROP TRIGGER almanac_maintenance_compatibility_identity ON public.almanac_maintenance_compatibility;
 DROP FUNCTION public.almanac_analyze_inserted_maintenance_change(),public.almanac_analyze_maintenance_change(uuid,bigint),
 public.almanac_guard_maintenance_comparison(),public.almanac_guard_complete_maintenance_coverage(),public.almanac_guard_maintenance_compatibility();
 DROP TABLE public.almanac_maintenance_comparisons,public.almanac_maintenance_changed_inputs,
 public.almanac_maintenance_change_sets,public.almanac_maintenance_compatibility;`); err != nil {
		t.Fatalf("reconstruct pre-0064 compatibility schema: %v", err)
	}
}

// Rewind only the additive 0067 objects before replaying a populated old schema.
func dropMaintenanceRetainedFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	dropMaintenanceAuthorityFixture(t, conn)
	if _, err := conn.Exec(t.Context(), `ALTER TABLE public.almanac_maintenance_preparation
DROP CONSTRAINT almanac_maintenance_preparation_extrema_bucket_check,
DROP CONSTRAINT almanac_maintenance_preparation_kind_check,
DROP CONSTRAINT almanac_maintenance_preparation_check2,
ADD CONSTRAINT almanac_maintenance_preparation_kind_check CHECK(kind IN ('mask','compatibility','scoped_hour','comparison')),
ADD CONSTRAINT almanac_maintenance_preparation_check2 CHECK((kind='scoped_hour' AND sli_id IS NOT NULL AND definition_revision IS NOT NULL AND measurement_revision IS NOT NULL AND hour_start IS NOT NULL AND source_generation IS NOT NULL AND second_build IS NULL)
 OR (kind IN ('mask','compatibility','comparison') AND sli_id IS NULL AND definition_revision IS NULL AND measurement_revision IS NULL AND hour_start IS NULL AND source_generation IS NULL AND (second_build IS NOT NULL)=(kind='comparison')));
DROP INDEX public.almanac_slo_extrema_authority_candidate_idx;
ALTER TABLE public.almanac_slo_extrema_cursor DROP COLUMN exclusion_revision;
ALTER TABLE public.almanac_slo_extrema DROP CONSTRAINT almanac_slo_extrema_exclusion_revision_check,
ADD CONSTRAINT almanac_slo_extrema_exclusion_revision_check CHECK (exclusion_revision=0)`); err != nil {
		t.Fatalf("reconstruct pre-0067 retained authority schema: %v", err)
	}
}

// Model history created before 0064: every ordinary authority/audit/grid guard
// remains enabled, and only the newly added derived analyzer is absent.
func setLegacyMaintenanceAnalysis(t *testing.T, conn *pgx.Conn, legacy bool) {
	t.Helper()
	statement := `ALTER TABLE public.almanac_slo_exclusion_changes ENABLE TRIGGER almanac_maintenance_change_analyze`
	if legacy {
		statement = `ALTER TABLE public.almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`
	}
	if _, err := conn.Exec(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
}

// Rewind the additive 0068 queue checkpoints without altering older work identity.
func dropMaintenanceAuthorityFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), `DROP INDEX public.almanac_authority_due_idx,
public.almanac_maintenance_input_start_idx,public.almanac_maintenance_input_end_idx,
public.almanac_maintenance_retarget_idx;
ALTER TABLE public.almanac_maintenance_preparation DROP COLUMN authority_retargeted_revision;
ALTER TABLE public.almanac_slo_exclusion_work
DROP CONSTRAINT almanac_authority_progress_revision,DROP CONSTRAINT almanac_authority_lease,
DROP COLUMN processed_revision,DROP COLUMN progress_part,DROP COLUMN progress_hour,
DROP COLUMN progress_until,DROP COLUMN scheduled_through,DROP COLUMN current_revision,
DROP COLUMN boundary_through,DROP COLUMN attempts,DROP COLUMN failure_version,
DROP COLUMN failure_reason,DROP COLUMN lease_token,DROP COLUMN lease_epoch,
DROP COLUMN lease_until,DROP COLUMN claimed_version,DROP COLUMN claimed_revision,DROP COLUMN last_tick`); err != nil {
		t.Fatalf("reconstruct pre-0068 authority queue: %v", err)
	}
}
