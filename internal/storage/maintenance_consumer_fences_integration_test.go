//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceConsumerPublicationAuthorityFence(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	row, err := q.GetSlo(t.Context(), h.SloID)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := q.GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	session := &maintenanceConsumerSession{tx: tx, requestedRevision: row.ExclusionRevision, scope: maintenancePreparationScope{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision}, sourceGeneration: generation, sourceCaptured: true}
	change := f.input("concurrent-consumer-authority")
	change.Start, change.End = f.at.Add(3*time.Hour), f.at.Add(4*time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
		t.Fatal(err)
	}
	if err := fenceMaintenanceConsumerSnapshot(t.Context(), tx, session); err == nil {
		t.Fatal("RR snapshot published after committed exclusion authority changed")
	}
}
