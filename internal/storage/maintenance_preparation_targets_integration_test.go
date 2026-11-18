//go:build integration

package storage

import (
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func targetedPreparation(f maintenanceFixture, kind string, at time.Time, generation int64) maintenanceEvaluationPreparation {
	s := preparationScope(f)
	return maintenanceEvaluationPreparation{Kind: kind, RequestedRevision: 0, Hour: maintenanceHourKey{SloID: s.SloID, SliID: s.SliID, DefinitionRevision: s.DefinitionRevision, MeasurementRevision: s.MeasurementRevision, HourStart: at, SourceGeneration: generation}}
}

func TestIntegrationMaintenancePreparationTargetBuckets(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	baseline := targetQueueSourceSnapshot(t, f)
	first := f.historyStart.UTC().Truncate(24 * time.Hour)
	requests := []maintenanceEvaluationPreparation{targetedPreparation(f, "extrema_hour", first, 7), targetedPreparation(f, "extrema_hour", first.Add(90*24*time.Hour), 7), targetedPreparation(f, "extrema_day", first, 7)}
	requests = append(requests, requests[0])
	for repetition := range 2 {
		if repetition == 1 {
			var build pgtype.UUID
			if err := f.conn.QueryRow(t.Context(), `SELECT id FROM almanac_maintenance_builds WHERE target_revision=0`).Scan(&build); err != nil {
				t.Fatal(err)
			}
			for i := range requests {
				requests[i].BuildID = build
				requests[i].Hour.BuildID = build
			}
		}
		compilerTx(t, f, func(tx pgx.Tx) error {
			p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), requests, maintenancePreparationLimits{MaxRequests: 4, MaxSourceHours: 1}, f.now)
			if err != nil {
				return err
			}
			if len(p.jobs) != 3 || len(p.hours) != 0 {
				t.Fatalf("targeted jobs coalesced into canonical hours: %+v", p)
			}
			for _, job := range p.jobs {
				if !job.BuildID.Valid || job.SecondBuild.Valid || job.SourceGeneration.Int64 != 7 {
					t.Fatalf("lost target identity: %+v", job)
				}
			}
			return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationHistorical, f.now)
		})
	}
	var count, builds int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT build_id) FROM almanac_maintenance_preparation`).Scan(&count, &builds); err != nil || count != 3 || builds != 1 {
		t.Fatalf("natural dedup count=%d builds=%d: %v", count, builds, err)
	}
	if targetQueueSourceSnapshot(t, f) != baseline {
		t.Fatal("target work changed source queue or history cursor")
	}
	// A new source generation has its own correction identity, never resets old work.
	requests = requests[:1]
	requests[0].Hour.SourceGeneration++
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, e := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), requests, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
		if e != nil {
			return e
		}
		return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationHistorical, f.now)
	})
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("source correction lost: %d %v", count, err)
	}
	var id pgtype.UUID
	if err := f.conn.QueryRow(t.Context(), `SELECT id FROM almanac_maintenance_preparation LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET hour_start=hour_start+interval '1 day' WHERE id=$1`, id); err == nil {
		t.Fatal("queue identity mutated")
	}
}

func targetQueueSourceSnapshot(t *testing.T, f maintenanceFixture) string {
	t.Helper()
	var out string
	if err := f.conn.QueryRow(t.Context(), `SELECT jsonb_build_object('dirty',(SELECT jsonb_agg(to_jsonb(d) ORDER BY range_start) FROM almanac_derived_dirty_ranges d),'cursor',(SELECT jsonb_agg(to_jsonb(c) ORDER BY slo_id) FROM almanac_slo_extrema_cursor c))::text`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIntegrationMaintenancePreparationTargetValidation(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	at := f.historyStart.UTC().Truncate(24 * time.Hour)
	valid := targetedPreparation(f, "extrema_day", at, 0)
	cases := map[string]func(*maintenanceEvaluationPreparation){
		"hour alignment": func(r *maintenanceEvaluationPreparation) {
			r.Kind = "extrema_hour"
			r.Hour.HourStart = at.Add(time.Minute)
		},
		"day alignment":       func(r *maintenanceEvaluationPreparation) { r.Hour.HourStart = at.Add(time.Hour) },
		"negative generation": func(r *maintenanceEvaluationPreparation) { r.Hour.SourceGeneration = -1 },
		"invalid start":       func(r *maintenanceEvaluationPreparation) { r.Hour.HourStart = time.Time{} },
		"upper endpoint": func(r *maintenanceEvaluationPreparation) {
			r.Hour.HourStart = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
		},
		"other slo":       func(r *maintenanceEvaluationPreparation) { r.Hour.SloID = maintenanceCompilerID() },
		"other sli":       func(r *maintenanceEvaluationPreparation) { r.Hour.SliID = maintenanceCompilerID() },
		"definition":      func(r *maintenanceEvaluationPreparation) { r.Hour.DefinitionRevision++ },
		"measurement":     func(r *maintenanceEvaluationPreparation) { r.Hour.MeasurementRevision++ },
		"comparison tail": func(r *maintenanceEvaluationPreparation) { r.Comparison = &maintenanceEvaluationBuildPair{} },
		"range tail":      func(r *maintenanceEvaluationPreparation) { r.Start = at },
		"conflicting builds": func(r *maintenanceEvaluationPreparation) {
			r.BuildID = maintenanceCompilerID()
			r.Hour.BuildID = maintenanceCompilerID()
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			bad := valid
			change(&bad)
			tx, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			// Invalid suffix must not be silently discarded by prefix admission.
			_, err = prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{valid, bad}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
			if !errors.Is(err, ErrMaintenanceInvalid) {
				t.Fatalf("invalid tail accepted: %v", err)
			}
			var builds, jobs int
			if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_builds),(SELECT count(*) FROM almanac_maintenance_preparation)`).Scan(&builds, &jobs); err != nil || builds != 0 || jobs != 0 {
				t.Fatalf("invalid request wrote pins/jobs: %d/%d %v", builds, jobs, err)
			}
		})
	}
	// A structurally valid but wrong build ID is rejected after capture; rolling
	// back the mandatory command transaction removes the captured resource pin.
	bad := valid
	bad.BuildID = maintenanceCompilerID()
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{bad}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
	rollbackTransaction(tx)
	if !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("wrong captured build accepted: %v", err)
	}
	var builds int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_builds`).Scan(&builds); err != nil || builds != 0 {
		t.Fatalf("rollback leaked build pin: %d %v", builds, err)
	}
}

func TestIntegrationMaintenancePreparationTargetPrefix(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	m := f.input("future-target-mask")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	at := f.historyStart.UTC().Truncate(time.Hour)
	first := targetedPreparation(f, "extrema_hour", at, 0)
	second := first
	second.RequestedRevision = 1
	second.Hour.HourStart = at.Add(90 * 24 * time.Hour)
	requests := []maintenanceEvaluationPreparation{first, second}
	before := append([]maintenanceEvaluationPreparation(nil), requests...)
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), requests, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
		if err != nil {
			return err
		}
		if len(p.jobs) != 1 || p.jobs[0].TargetRevision != 0 {
			t.Fatalf("prefix discarded or expanded: %+v", p.jobs)
		}
		return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationHistorical, f.now)
	})
	var builds int
	var revision int64
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*),max(target_revision) FROM almanac_maintenance_builds`).Scan(&builds, &revision); err != nil || builds != 1 || revision != 0 {
		t.Fatalf("tail build pinned before admission: %d/%d %v", builds, revision, err)
	}
	if !reflect.DeepEqual(requests, before) {
		t.Fatal("caller requests mutated")
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, e := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), requests[1:], maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
		if e != nil {
			return e
		}
		return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationHistorical, f.now)
	})
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("remaining distant bucket lost: %d %v", count, err)
	}
}

func TestIntegrationMaintenancePreparationTargetConstraints(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	at := f.historyStart.UTC().Truncate(24 * time.Hour)
	var job dbgen.EnqueueMaintenancePreparationParams
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, e := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{targetedPreparation(f, "extrema_day", at, 0)}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
		if e != nil {
			return e
		}
		job = p.jobs[0]
		return nil
	})
	job.Priority = "historical"
	cases := map[string]func(*dbgen.EnqueueMaintenancePreparationParams){
		"missing sli":        func(p *dbgen.EnqueueMaintenancePreparationParams) { p.SliID = pgtype.UUID{} },
		"missing definition": func(p *dbgen.EnqueueMaintenancePreparationParams) { p.DefinitionRevision = pgtype.Int8{} },
		"missing source":     func(p *dbgen.EnqueueMaintenancePreparationParams) { p.SourceGeneration = pgtype.Int8{} },
		"negative source":    func(p *dbgen.EnqueueMaintenancePreparationParams) { p.SourceGeneration.Int64 = -1 },
		"comparison tail":    func(p *dbgen.EnqueueMaintenancePreparationParams) { p.SecondBuild = p.BuildID },
		"day alignment":      func(p *dbgen.EnqueueMaintenancePreparationParams) { p.HourStart = timestamp(at.Add(time.Hour)) },
		"hour alignment": func(p *dbgen.EnqueueMaintenancePreparationParams) {
			p.Kind = "extrema_hour"
			p.HourStart = timestamp(at.Add(time.Minute))
		},
		"pre domain": func(p *dbgen.EnqueueMaintenancePreparationParams) {
			p.HourStart = timestamp(time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC))
		},
		"end overflow": func(p *dbgen.EnqueueMaintenancePreparationParams) {
			p.HourStart = timestamp(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := job
			p.ID = maintenanceCompilerID()
			change(&p)
			err := dbgen.New(f.conn).EnqueueMaintenancePreparation(t.Context(), p)
			pg, ok := errors.AsType[*pgconn.PgError](err)
			if !ok || pg.Code != "23514" {
				t.Fatalf("invalid key bypassed CHECK: %v", err)
			}
		})
	}
	if err := dbgen.New(f.conn).EnqueueMaintenancePreparation(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',attempts=3,available_at=$2 WHERE id=$1`, job.ID, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	job.ID = maintenanceCompilerID()
	job.Priority = "live"
	if err := dbgen.New(f.conn).EnqueueMaintenancePreparation(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	var next time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT state,attempts,available_at FROM almanac_maintenance_preparation`).Scan(&state, &attempts, &next); err != nil || state != "exhausted" || attempts != 3 || !next.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("reenqueue reset terminal/backoff: %s/%d/%v %v", state, attempts, next, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='pending'`); err == nil {
		t.Fatal("terminal job resurrected")
	}
}

func TestIntegrationMaintenancePreparationTargetPopulatedUpgrade(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 0}}, maintenancePreparationHistorical)
	dropMaintenanceRetainedFixture(t, f.conn)
	if _, err := f.conn.Exec(t.Context(), `DROP INDEX public.almanac_maintenance_revision_range_idx; DELETE FROM almanac_schema_migrations WHERE version>=67`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var out string
		if err := f.conn.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(p)-'authority_retargeted_revision' ORDER BY id)::text FROM almanac_maintenance_preparation p`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	var build pgtype.UUID
	if err := f.conn.QueryRow(t.Context(), `SELECT id FROM almanac_maintenance_builds WHERE target_revision=0`).Scan(&build); err != nil {
		t.Fatal(err)
	}
	scope := preparationScope(f)
	p := dbgen.EnqueueMaintenancePreparationParams{ID: maintenanceCompilerID(), Kind: "extrema_hour", SloID: scope.SloID, TargetRevision: 0, BuildID: build, SliID: scope.SliID, DefinitionRevision: pgtype.Int8{Int64: 1, Valid: true}, MeasurementRevision: pgtype.Int8{Int64: 1, Valid: true}, HourStart: timestamp(f.historyStart.UTC().Truncate(time.Hour)), SourceGeneration: pgtype.Int8{Int64: 0, Valid: true}, Priority: "historical", Now: timestamp(f.now)}
	err := dbgen.New(f.conn).EnqueueMaintenancePreparation(t.Context(), p)
	pg, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pg.Code != "23514" {
		t.Fatalf("rewind did not restore old queue constraints: %v", err)
	}
	latest := latestTestSchemaVersion(t)
	upgraded, err := Upgrade(t.Context(), f.f.databaseURL)
	if err != nil || upgraded.Applied != latest-66 || upgraded.DatabaseSchemaVersion != latest {
		t.Fatalf("upgrade %+v %v", upgraded, err)
	}
	if snapshot() != before {
		t.Fatal("upgrade modified existing queued work")
	}
	var assignments, current int
	if err := f.conn.QueryRow(t.Context(), `
		SELECT count(*), count(*) FILTER (WHERE a.definition_revision = s.definition_revision
		  AND a.service_id = d.service_id AND a.effective_at = greatest(s.created_at, d.activated_at))
		FROM almanac_slo_service_assignments a
		JOIN almanac_slos s ON s.id = a.slo_id
		JOIN almanac_slo_definitions d ON d.slo_id = s.id AND d.revision = s.definition_revision
		WHERE a.slo_id=$1
	`, scope.SloID).Scan(&assignments, &current); err != nil || assignments != 1 || current != 1 {
		t.Fatalf("populated upgrade seeded %d assignments (%d current): %v", assignments, current, err)
	}
	if err := dbgen.New(f.conn).EnqueueMaintenancePreparation(t.Context(), p); err != nil {
		t.Fatalf("upgraded queue rejected target: %v", err)
	}
}

func TestIntegrationMaintenanceUpgradeFromPublishedScopeChecksums(t *testing.T) {
	t.Parallel()
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	// The bytes published at 7f5bec5 must remain acceptable to the upgrader.
	checksums := []string{
		"2fee5962fd29b7a140b9f7862ce66bb44b3f5b9ab2e09ec6eda3b42f3aba034c",
		"c8dc3f2b8a6a58cb383b3f17e74fb6ad4799c4f9f00ab44afe36cf9fc067f518",
		"3520b72123b54e47aa0f43c7b6594a23d22f5f55f4ed8a1ab2446932b9a0e110",
		"68e4d7d62f9a90dc30ed0ab9581badf9a930b06635e599ab2e6401ee0d59aeca",
		"0f99f3c5653da67d43c02d9b4dc1777cea63ea4901592d5bb8f4a54b99cb2b70",
	}
	const lastPublished = 74
	for offset, want := range checksums {
		m := migrations[69+offset]
		if got := hex.EncodeToString(m.Checksum[:]); got != want {
			t.Fatalf("published migration %d changed: got %s want %s", m.Version, got, want)
		}
	}
	databaseURL := integrationDatabase(t)
	installed, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:lastPublished])
	if err != nil || installed.DatabaseSchemaVersion != lastPublished {
		t.Fatalf("install prior published scope schema: %+v %v", installed, err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	nodes := func() [3]int64 {
		t.Helper()
		var result [3]int64
		if err := conn.QueryRow(t.Context(), `
			SELECT pg_relation_filenode('public.almanac_maintenance_change_sets')::bigint,
			       pg_relation_filenode('public.almanac_maintenance_changed_inputs')::bigint,
			       pg_relation_filenode('public.almanac_slo_exclusion_work')::bigint
		`).Scan(&result[0], &result[1], &result[2]); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := nodes()
	upgraded, err := Upgrade(t.Context(), databaseURL)
	if err != nil || upgraded.DatabaseSchemaVersion != len(migrations) || upgraded.Applied != len(migrations)-lastPublished {
		t.Fatalf("upgrade rejected the published ledger: %+v %v", upgraded, err)
	}
	if after := nodes(); after != before {
		t.Fatalf("upgrade rewrote authority relations: before %v after %v", before, after)
	}
}

func TestIntegrationMaintenanceCompatibilityOrdinalOverflowRetainsExactCoverage(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("compatibility-overflow")
	input.ScopeKind, input.SloIDs = MaintenanceScopeAllServices, nil
	input.End = input.Start.Add(24 * time.Hour)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	id := mustPGUUID(f.ids[0])
	const parts = 32768
	if _, err := f.conn.Exec(t.Context(), `
		WITH state AS (
			UPDATE public.almanac_slo_exclusion_state SET revision=1 WHERE slo_id=$1 RETURNING slo_id
		), pieces AS (
			SELECT $3::timestamptz + n * interval '2 seconds' AS start_at
			FROM generate_series(0, $4::integer - 1) n
		)
		INSERT INTO almanac_slo_exclusion_changes(slo_id,revision,exclusion_id,exclusion_revision,included,coverage_changed,recorded_at,effective_coverage)
		SELECT state.slo_id,1,$2,1,true,true,$3,
		       range_agg(tstzrange(pieces.start_at,pieces.start_at+interval '1 second','[)'))
		FROM state CROSS JOIN pieces GROUP BY state.slo_id
	`, id, window.ID, window.Start, parts); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(f.conn)
	state, err := q.ReadMaintenanceCompatibilityAuthority(t.Context(), id)
	if err != nil || state.Unavailable || state.IndexedThrough != 1 {
		t.Fatalf("overflow disabled exact recomputation: %+v %v", state, err)
	}
	gapStart, gapEnd := window.Start.Add(time.Second), window.Start.Add(2*time.Second)
	requiresRebuild, err := q.HasMaintenanceAuthorityInput(t.Context(), dbgen.HasMaintenanceAuthorityInputParams{
		SloID: id, ScopeOrdinal: state.ScopeOrdinal, Revision: 1, StartAt: timestamp(gapStart), EndAt: timestamp(gapEnd),
	})
	if err != nil || !requiresRebuild {
		t.Fatalf("overflow allowed reuse within the conservative envelope: changed=%t %v", requiresRebuild, err)
	}
	snapshot, err := readSloExclusionSnapshotAt(t.Context(), q, id, 1, window.Start, gapEnd)
	if err != nil || len(snapshot.Intervals) != 1 || !snapshot.Intervals[0].Start.Equal(window.Start) || !snapshot.Intervals[0].End.Equal(gapStart) {
		t.Fatalf("exact coverage lost its gap: %+v %v", snapshot, err)
	}
}
