package storage

import (
	"bytes"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestRetentionPreviewUsesActualCompleteUTCBounds(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	september := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	coverage := dbgen.ListCanonicalRetentionPartitionsRow{RelationID: 1, ParentName: "almanac_sli_coverage",
		PartitionName: "almanac_sli_coverage_202509", RangeStart: timestamp(september), RangeEnd: timestamp(september.AddDate(0, 1, 0)), Bytes: 1024, EstimatedRows: -1}
	values := coverage
	values.RelationID, values.ParentName, values.PartitionName = 2, "almanac_sli_observation_values", "almanac_sli_observation_values_202509"
	values.EstimatedRows = 8
	partial := coverage
	partial.PartitionName, partial.RangeStart, partial.RangeEnd = "almanac_sli_coverage_202510", timestamp(september.AddDate(0, 1, 0)), timestamp(september.AddDate(0, 2, 0))
	misnamed := coverage
	misnamed.PartitionName = "almanac_sli_coverage_202408"
	wrongBounds := coverage
	wrongBounds.RangeEnd = timestamp(september.Add(24 * time.Hour))
	impact, err := canonicalRetentionImpact([]dbgen.ListCanonicalRetentionPartitionsRow{coverage, values, partial, misnamed, wrongBounds}, now)
	if err != nil || !impact.Cutoff.Equal(time.Date(2025, 10, 31, 12, 30, 0, 0, time.UTC)) || len(impact.Partitions) != 2 || len(impact.UnsupportedPartitions) != 2 || impact.Bytes != 2048 {
		t.Fatalf("preview proposed partial, misaddressed or non-monthly data: %+v, %v", impact, err)
	}
	if impact.Partitions[0].Parent != "almanac_sli_observation_values" || impact.Partitions[0].EstimatedRows == nil || *impact.Partitions[0].EstimatedRows != 8 || impact.Partitions[1].EstimatedRows != nil {
		t.Fatal("preview lost FK removal order or invented exact row counts")
	}
	boundary := september.AddDate(1, 1, 0)
	impact, err = canonicalRetentionImpact([]dbgen.ListCanonicalRetentionPartitionsRow{coverage}, boundary)
	if err != nil || len(impact.Partitions) != 1 {
		t.Fatal("partition ending exactly at the 365-day cutoff was excluded")
	}
}

func TestRetentionScopePinsRelationsAndBoundsButNotEstimates(t *testing.T) {
	start := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	parts := []retentionPartition{{RelationID: 42, Parent: "almanac_sli_coverage", Name: "almanac_sli_coverage_202509", Start: start, End: start.AddDate(0, 1, 0), Bytes: 1024}}
	before, err := retentionScopeDigest(parts)
	if err != nil {
		t.Fatal(err)
	}
	parts[0].Bytes, parts[0].EstimatedRows = 2048, new(int64(30))
	after, err := retentionScopeDigest(parts)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("changing size estimates invalidated an otherwise identical preview")
	}
	for _, changed := range []retentionPartition{
		{RelationID: 43, Parent: parts[0].Parent, Name: parts[0].Name, Start: start, End: parts[0].End},
		{RelationID: 42, Parent: parts[0].Parent, Name: parts[0].Name + "_replacement", Start: start, End: parts[0].End},
		{RelationID: 42, Parent: parts[0].Parent, Name: parts[0].Name, Start: start, End: parts[0].End.Add(time.Hour)},
	} {
		digest, err := retentionScopeDigest([]retentionPartition{changed})
		if err != nil || bytes.Equal(before, digest) {
			t.Fatal("a replaced or redefined partition reused its previous deletion preview")
		}
	}
}
